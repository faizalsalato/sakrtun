package config

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type Mode string

const (
	ModeDirect     Mode = "direct"
	ModePayload    Mode = "payload"
	ModeSSL        Mode = "ssl"
	ModePayloadSSL Mode = "payload_ssl"
	ModeDNSTT      Mode = "dnstt"
	ModeXray       Mode = "xray"
	ModeOpenVPN    Mode = "openvpn"
)

const (
	ProfileExtension = ".srpc"
	profileMagic     = "SRPC\x01"
)

type Profile struct {
	ID        string
	Name      string
	Mode      Mode
	CreatedAt time.Time
	UpdatedAt time.Time

	SSH       SSHConfig
	Proxy     ProxyConfig
	Payload   PayloadConfig
	TLS       TLSConfig
	DNSTT     DNSTTConfig
	Xray      XrayConfig
	OpenVPN   OpenVPNConfig
	UDPGW     UDPGWConfig
	Reconnect ReconnectConfig
	Local     LocalConfig
	DNS       DNSConfig
	Tun       TunConfig
}

type SSHConfig struct {
	Host               string
	Port               int
	Username           string
	Password           string
	KeepAliveSeconds   int
	HandshakeTimeoutMs int
}

type ProxyConfig struct {
	Host string
	Port int
}

type PayloadConfig struct {
	Text              string
	WaitForResponse   bool
	AcceptAnyStatus   bool
	ResponseTimeoutMs int
	SplitDelayMs      int
}

type TLSConfig struct {
	Enabled            bool
	Host               string
	Port               int
	ServerName         string
	InsecureSkipVerify bool
}

type DNSTTConfig struct {
	Enabled          bool
	UseEmbedded      bool
	ResolverType     string
	ResolverAddress  string
	Domain           string
	PublicKey        string
	UTLSDistribution string
	Executable       string
	Args             []string
	LocalSSHHost     string
	LocalSSHPort     int
	StartupTimeoutMs int
}

type XrayConfig struct {
	Executable       string
	Args             []string
	ConfigPath       string
	LocalSocksHost   string
	LocalSocksPort   int
	StartupTimeoutMs int
	// ConfigJSON is an inline xray configuration in JSON format, written from
	// the manual editor in the UI. When non-empty, it is written to
	// configs/xray-profile-<id>.json at connect time and overrides ConfigPath.
	ConfigJSON string
}

type OpenVPNConfig struct {
	// Executable is the openvpn binary. When relative, it is resolved against
	// the app root and then against PATH (see engine.StartProcess).
	Executable string
	ConfigPath string
	Username   string
	Password   string
	// AuthFile is an optional auth-user-pass file. When Username is set and
	// AuthFile is empty, the app writes a temporary auth file automatically.
	AuthFile string
	// Verbosity maps to OpenVPN --verb (0..11). 0 is silent, 3 is normal.
	Verbosity        int
	StartupTimeoutMs int
}

type UDPGWConfig struct {
	Enabled  bool
	Host     string
	Port     int
	Protocol string
}

type ReconnectConfig struct {
	Enabled              bool
	DelaySeconds         int
	MaxRetries           int
	CheckIntervalSeconds int
}

type LocalConfig struct {
	SocksHost string
	SocksPort int
}

type DNSConfig struct {
	// Servers is the list of custom DNS servers (IPs or hostnames, optionally
	// with ":port"). When set, they override the defaults used by the local
	// SOCKS DNS-over-SSH resolver (1.1.1.1, 8.8.8.8) and by the TUN adapter
	// (the TUN tab values). Empty means "use the defaults".
	Servers []string
}

type TunConfig struct {
	Enabled       bool
	Device        string
	InterfaceName string
	MTU           int
	Gateway       string
	CIDR          string
	DNS           []string
	RouteAll      bool

	// IPv6 is optional because many SSH/DNSTT/UDPGW servers only have IPv4
	// egress. When IPv6Enabled is false, AllowIPv6Leak controls whether the
	// app should leave the normal Windows/Linux IPv6 route untouched.
	IPv6Enabled   bool
	IPv6CIDR      string
	IPv6DNS       []string
	AllowIPv6Leak bool
}

type Store struct {
	Dir string
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

func (s *Store) List() ([]Profile, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	var out []Profile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ProfileExtension) {
			continue
		}
		p, err := s.Load(strings.TrimSuffix(e.Name(), ProfileExtension))
		if err == nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (s *Store) Load(id string) (Profile, error) {
	var p Profile
	b, err := os.ReadFile(filepath.Join(s.Dir, safeID(id)+ProfileExtension))
	if err != nil {
		return p, err
	}
	p, err = DecodeProfileFile(b)
	if err != nil {
		return p, err
	}
	ApplyDefaults(&p)
	return p, nil
}

func (s *Store) Save(p Profile) (Profile, error) {
	if p.ID == "" {
		p.ID = randomID()
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	ApplyDefaults(&p)
	if err := Validate(p); err != nil {
		return p, err
	}
	b, err := EncodeProfileFile(p)
	if err != nil {
		return p, err
	}
	return p, writeFileAtomic(filepath.Join(s.Dir, safeID(p.ID)+ProfileExtension), b, 0o600)
}

func (s *Store) Delete(id string) error {
	err := os.Remove(filepath.Join(s.Dir, safeID(id)+ProfileExtension))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// writeFileAtomic writes data to path via a temporary file in the same
// directory followed by a rename. This prevents profile corruption when the
// app crashes or the disk fills while saving.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".socksrevive-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func EncodeProfileFile(p Profile) ([]byte, error) {
	ApplyDefaults(&p)
	var buf bytes.Buffer
	buf.WriteString(profileMagic)
	gz := gzip.NewWriter(&buf)
	if err := gob.NewEncoder(gz).Encode(p); err != nil {
		_ = gz.Close()
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func DecodeProfileFile(b []byte) (Profile, error) {
	var p Profile
	if !bytes.HasPrefix(b, []byte(profileMagic)) {
		return p, errors.New("invalid SocksRevive PC profile file")
	}
	gz, err := gzip.NewReader(bytes.NewReader(b[len(profileMagic):]))
	if err != nil {
		return p, err
	}
	defer gz.Close()
	payload, err := io.ReadAll(gz)
	if err != nil {
		return p, err
	}
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&p); err != nil {
		return p, err
	}
	ApplyDefaults(&p)
	return p, Validate(p)
}

func Validate(p Profile) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("profile name is required")
	}
	switch p.Mode {
	case ModeDirect, ModePayload, ModeSSL, ModePayloadSSL, ModeDNSTT, ModeXray, ModeOpenVPN:
	default:
		return fmt.Errorf("unknown mode %q", p.Mode)
	}
	if p.Mode != ModeXray && p.Mode != ModeOpenVPN {
		if p.SSH.Username == "" || p.SSH.Password == "" {
			return errors.New("ssh username/password are required")
		}
		if p.Mode != ModeDNSTT && (p.SSH.Host == "" || p.SSH.Port <= 0) {
			return errors.New("ssh host/port are required")
		}
	}
	if (p.Mode == ModePayload || p.Mode == ModePayloadSSL) && strings.TrimSpace(p.Payload.Text) == "" {
		return errors.New("payload text is required for payload modes")
	}
	if p.Mode == ModeXray && strings.TrimSpace(p.Xray.Executable) == "" {
		return errors.New("xray executable path is required")
	}
	if p.Mode == ModeXray && strings.TrimSpace(p.Xray.ConfigJSON) != "" && !json.Valid([]byte(p.Xray.ConfigJSON)) {
		return errors.New("xray config JSON is invalid: check the JSON syntax in the Xray tab")
	}
	if p.Mode == ModeOpenVPN {
		if strings.TrimSpace(p.OpenVPN.ConfigPath) == "" {
			return errors.New("openvpn config file path is required")
		}
		if p.OpenVPN.Verbosity < 0 || p.OpenVPN.Verbosity > 11 {
			return errors.New("openvpn verbosity must be between 0 and 11")
		}
	}
	if p.Mode == ModeDNSTT {
		if p.DNSTT.UseEmbedded {
			if strings.TrimSpace(p.DNSTT.ResolverType) == "" || strings.TrimSpace(p.DNSTT.ResolverAddress) == "" {
				return errors.New("dnstt resolver type/address are required")
			}
			if strings.TrimSpace(p.DNSTT.Domain) == "" {
				return errors.New("dnstt domain is required")
			}
			if strings.TrimSpace(p.DNSTT.PublicKey) == "" {
				return errors.New("dnstt public key is required")
			}
		} else if strings.TrimSpace(p.DNSTT.Executable) == "" {
			return errors.New("dnstt executable path is required when embedded dnstt is disabled")
		}
	}
	if p.UDPGW.Enabled {
		if strings.TrimSpace(p.UDPGW.Host) == "" {
			return errors.New("udpgw host is required when udpgw is enabled")
		}
		if p.UDPGW.Port <= 0 || p.UDPGW.Port > 65535 {
			return errors.New("udpgw port must be between 1 and 65535")
		}
		switch strings.ToLower(strings.TrimSpace(p.UDPGW.Protocol)) {
		case "", "badvpn", "legacy":
		default:
			return errors.New("udpgw protocol must be badvpn or legacy")
		}
	}
	if p.Local.SocksPort <= 0 || p.Local.SocksPort > 65535 {
		return errors.New("local socks port must be between 1 and 65535")
	}
	if p.Tun.Enabled && p.Tun.IPv6Enabled {
		ip, _, err := net.ParseCIDR(strings.TrimSpace(p.Tun.IPv6CIDR))
		if err != nil || ip == nil || ip.To4() != nil {
			return errors.New("IPv6 CIDR must be a valid IPv6 CIDR, for example fd00:534f:434b::1/64")
		}
	}
	for _, server := range p.DNS.Servers {
		server = strings.TrimSpace(server)
		if server == "" {
			continue
		}
		host := server
		if h, _, err := net.SplitHostPort(server); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if host == "" {
			return fmt.Errorf("invalid custom DNS server %q", server)
		}
		if net.ParseIP(host) == nil && strings.ContainsAny(host, " /\\") {
			return fmt.Errorf("invalid custom DNS server %q", server)
		}
	}
	return nil
}

// EffectiveTunDNS returns the DNS servers to apply to the TUN adapter: the
// custom DNS servers when set, otherwise the TUN tab values.
func (p Profile) EffectiveTunDNS() []string {
	if len(p.DNS.Servers) > 0 {
		return p.DNS.Servers
	}
	return p.Tun.DNS
}

func ApplyDefaults(p *Profile) {
	if p.ID == "" {
		p.ID = randomID()
	}
	if p.Mode == "" {
		p.Mode = ModeDirect
	}
	if p.SSH.Port == 0 {
		p.SSH.Port = 22
	}
	if p.SSH.KeepAliveSeconds == 0 {
		p.SSH.KeepAliveSeconds = 20
	}
	if p.SSH.HandshakeTimeoutMs == 0 {
		p.SSH.HandshakeTimeoutMs = 15000
	}
	if p.Payload.Text == "" {
		p.Payload.Text = "CONNECT [host]:[port] HTTP/1.1[crlf]Host: [host][crlf]User-Agent: SocksRevivePC[crlf][crlf]"
	}
	if p.Payload.ResponseTimeoutMs == 0 {
		p.Payload.ResponseTimeoutMs = 8000
	}
	if p.Payload.SplitDelayMs == 0 {
		p.Payload.SplitDelayMs = 120
	}
	if p.TLS.Port == 0 {
		p.TLS.Port = 443
	}
	if p.Reconnect.DelaySeconds == 0 {
		p.Reconnect.DelaySeconds = 3
	}
	if p.Reconnect.CheckIntervalSeconds == 0 {
		p.Reconnect.CheckIntervalSeconds = 10
	}
	if p.Local.SocksHost == "" {
		p.Local.SocksHost = "127.0.0.1"
	}
	if p.Local.SocksPort == 0 {
		p.Local.SocksPort = 10809
	}
	if p.DNSTT.LocalSSHHost == "" {
		p.DNSTT.LocalSSHHost = "127.0.0.1"
	}
	if p.DNSTT.LocalSSHPort == 0 {
		p.DNSTT.LocalSSHPort = 2222
	}
	if p.DNSTT.StartupTimeoutMs == 0 {
		p.DNSTT.StartupTimeoutMs = 5000
	}
	if p.DNSTT.ResolverType == "" {
		p.DNSTT.ResolverType = "doh"
	}
	if p.DNSTT.UTLSDistribution == "" {
		p.DNSTT.UTLSDistribution = "4*random,3*Firefox_120,1*Firefox_105,3*Chrome_120,1*Chrome_102,1*iOS_14,1*iOS_13"
	}
	if !p.DNSTT.UseEmbedded && p.DNSTT.Executable == "" && len(p.DNSTT.Args) == 0 {
		// New profiles use the embedded DNSTT client. Old imported profiles can
		// still disable UseEmbedded and point to an external executable.
		p.DNSTT.UseEmbedded = true
	}
	if p.DNSTT.Executable == "" {
		p.DNSTT.Executable = defaultToolPath("dnstt", "dnstt-client")
	}
	if p.Xray.LocalSocksHost == "" {
		p.Xray.LocalSocksHost = "127.0.0.1"
	}
	if p.Xray.LocalSocksPort == 0 {
		p.Xray.LocalSocksPort = 10808
	}
	if p.Xray.ConfigPath == "" {
		p.Xray.ConfigPath = "configs/xray.json"
	}
	if len(p.Xray.Args) == 0 {
		p.Xray.Args = []string{"run", "-config", p.Xray.ConfigPath}
	}
	if p.Xray.StartupTimeoutMs == 0 {
		p.Xray.StartupTimeoutMs = 2500
	}
	if p.Xray.Executable == "" {
		p.Xray.Executable = defaultToolPath("xray", "xray")
	}
	if p.OpenVPN.Executable == "" {
		// Leave the bare name so engine.StartProcess can resolve it through PATH.
		p.OpenVPN.Executable = "openvpn"
	}
	if p.OpenVPN.Verbosity == 0 {
		p.OpenVPN.Verbosity = 3
	}
	if p.OpenVPN.StartupTimeoutMs == 0 {
		p.OpenVPN.StartupTimeoutMs = 10000
	}
	if p.UDPGW.Host == "" {
		p.UDPGW.Host = "127.0.0.1"
	}
	if p.UDPGW.Port == 0 {
		p.UDPGW.Port = 7400
	}
	if strings.TrimSpace(p.UDPGW.Protocol) == "" {
		// BadVPN is the Android-compatible UDPGW framing. The old PC-only
		// experimental frame is still available as "legacy" for older tests.
		p.UDPGW.Protocol = "badvpn"
	}
	if p.Tun.Device == "" {
		if runtime.GOOS == "windows" {
			p.Tun.Device = "wintun"
			p.Tun.InterfaceName = "wintun"
		} else {
			p.Tun.Device = "tun://socksrevive0"
			p.Tun.InterfaceName = "socksrevive0"
		}
	}
	if runtime.GOOS == "windows" {
		// tun2socks v2 expects the Windows device model to be just "wintun".
		// The old value "wintun://SocksRevive" made the engine treat
		// "SocksRevive" as a network interface and crash with
		// "route ip+net: no such network interface" on many PCs.
		p.Tun.Device = "wintun"
		p.Tun.InterfaceName = "wintun"
	}
	if p.Tun.InterfaceName == "" {
		if strings.HasPrefix(p.Tun.Device, "tun://") {
			p.Tun.InterfaceName = strings.TrimPrefix(p.Tun.Device, "tun://")
		} else if strings.HasPrefix(p.Tun.Device, "wintun://") {
			p.Tun.InterfaceName = strings.TrimPrefix(p.Tun.Device, "wintun://")
		} else {
			p.Tun.InterfaceName = p.Tun.Device
		}
	}
	if p.Tun.MTU == 0 {
		p.Tun.MTU = 1500
	}
	if p.Tun.Gateway == "" {
		p.Tun.Gateway = "198.18.0.1"
	}
	if p.Tun.CIDR == "" {
		p.Tun.CIDR = "198.18.0.1/15"
	}
	if len(p.Tun.DNS) == 0 {
		p.Tun.DNS = []string{"1.1.1.1", "8.8.8.8"}
	}
	if p.Tun.IPv6CIDR == "" {
		p.Tun.IPv6CIDR = "fd00:534f:434b::1/64"
	}
	if len(p.Tun.IPv6DNS) == 0 {
		p.Tun.IPv6DNS = []string{"2606:4700:4700::1111", "2001:4860:4860::8888"}
	}
}

func defaultToolPath(folder, base string) string {
	exe := base
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	return filepath.Join("tools", folder, exe)
}

func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("p%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func safeID(id string) string {
	id = strings.TrimSpace(id)
	if runtime.GOOS == "windows" {
		// These characters are not allowed in Windows file names.
		for _, ch := range []string{":", "*", "?", "\"", "<", ">", "|"} {
			id = strings.ReplaceAll(id, ch, "_")
		}
	}
	id = strings.ReplaceAll(id, "/", "_")
	id = strings.ReplaceAll(id, "\\", "_")
	id = strings.ReplaceAll(id, "..", "_")
	return id
}
