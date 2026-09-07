// Package xraylink parses Xray/V2Ray share links (vless://, trojan://,
// ss:// and vmess://) into complete Xray JSON configurations.
package xraylink

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Config is the result of parsing a share link: a display name and a complete
// Xray JSON configuration ready to run (with a local SOCKS inbound on 10808).
type Config struct {
	Name string
	JSON string
}

// Parse parses a single share link and returns the generated configuration.
func Parse(link string) (Config, error) {
	link = strings.TrimSpace(link)
	if link == "" {
		return Config{}, errors.New("empty link")
	}
	u, err := url.Parse(link)
	if err != nil {
		return Config{}, fmt.Errorf("invalid link: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "vless", "trojan":
		return parseVLESSOrTrojan(strings.ToLower(u.Scheme), u)
	case "ss":
		return parseSS(u)
	case "vmess":
		return parseVMess(u)
	default:
		return Config{}, fmt.Errorf("unsupported link type %q (supported: vless, trojan, ss, vmess)", u.Scheme)
	}
}

// ---- JSON config structure ------------------------------------------------

type logConf struct {
	LogLevel string `json:"loglevel"`
}

type inbound struct {
	Tag      string          `json:"tag"`
	Port     int             `json:"port"`
	Listen   string          `json:"listen"`
	Protocol string          `json:"protocol"`
	Settings inboundSettings `json:"settings"`
}

type inboundSettings struct {
	UDP bool `json:"udp"`
}

type outbound struct {
	Tag            string          `json:"tag"`
	Protocol       string          `json:"protocol"`
	Settings       json.RawMessage `json:"settings,omitempty"`
	StreamSettings json.RawMessage `json:"streamSettings,omitempty"`
}

type fullConfig struct {
	Log       logConf    `json:"log"`
	Inbounds  []inbound  `json:"inbounds"`
	Outbounds []outbound `json:"outbounds"`
}

func buildConfig(protocol, name string, settings any, stream *streamSettings) (Config, error) {
	sb, err := json.Marshal(settings)
	if err != nil {
		return Config{}, err
	}
	var ssb json.RawMessage
	if stream != nil {
		if ssb, err = json.Marshal(stream); err != nil {
			return Config{}, err
		}
	}
	cfg := fullConfig{
		Log: logConf{LogLevel: "warning"},
		Inbounds: []inbound{{
			Tag: "socks-in", Port: 10808, Listen: "127.0.0.1",
			Protocol: "socks", Settings: inboundSettings{UDP: true},
		}},
		Outbounds: []outbound{
			{Tag: "proxy", Protocol: protocol, Settings: sb, StreamSettings: ssb},
			{Tag: "direct", Protocol: "freedom"},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return Config{}, err
	}
	return Config{Name: name, JSON: string(b)}, nil
}

// ---- stream settings --------------------------------------------------------

type streamSettings struct {
	Network         string           `json:"network,omitempty"`
	Security        string           `json:"security,omitempty"`
	PacketEncoding  string           `json:"packetEncoding,omitempty"`
	TLSSettings     *tlsSettings     `json:"tlsSettings,omitempty"`
	RealitySettings *realitySettings `json:"realitySettings,omitempty"`
	WSSettings      *wsSettings      `json:"wsSettings,omitempty"`
	GRPCSettings    *grpcSettings    `json:"grpcSettings,omitempty"`
	KCPSettings     *kcpSettings     `json:"kcpSettings,omitempty"`
}

type tlsSettings struct {
	ServerName    string   `json:"serverName,omitempty"`
	AllowInsecure bool     `json:"allowInsecure,omitempty"`
	ALPN          []string `json:"alpn,omitempty"`
	Fingerprint   string   `json:"fingerprint,omitempty"`
}

type realitySettings struct {
	ServerName  string `json:"serverName,omitempty"`
	PublicKey   string `json:"publicKey,omitempty"`
	ShortID     string `json:"shortId,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type wsSettings struct {
	Path    string            `json:"path,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type grpcSettings struct {
	ServiceName string `json:"serviceName,omitempty"`
}

type kcpSettings struct {
	Header kcpHeader `json:"header,omitempty"`
}

type kcpHeader struct {
	Type string `json:"type,omitempty"`
}

func buildStream(q url.Values, address string) *streamSettings {
	network := strings.ToLower(q.Get("type"))
	if network == "" {
		network = "tcp"
	}
	security := strings.ToLower(q.Get("security"))
	path := q.Get("path")
	wsHost := q.Get("host")
	sni := q.Get("sni")
	fp := q.Get("fp")
	alpn := q.Get("alpn")
	insecure := q.Get("insecure") == "1" || q.Get("allowInsecure") == "1"
	headerType := q.Get("headerType")
	serviceName := q.Get("serviceName")
	packetEncoding := q.Get("packetEncoding")

	if sni == "" {
		sni = wsHost
	}
	if sni == "" {
		sni = address
	}

	ss := &streamSettings{Network: network}
	if security != "" && security != "none" {
		ss.Security = security
	}
	if packetEncoding != "" {
		ss.PacketEncoding = packetEncoding
	}

	switch network {
	case "ws":
		ws := &wsSettings{Path: path}
		if wsHost != "" {
			ws.Headers = map[string]string{"Host": wsHost}
		}
		ss.WSSettings = ws
	case "grpc":
		ss.GRPCSettings = &grpcSettings{ServiceName: serviceName}
	case "kcp":
		ss.KCPSettings = &kcpSettings{Header: kcpHeader{Type: headerType}}
	}

	switch security {
	case "reality":
		ss.RealitySettings = &realitySettings{
			ServerName:  sni,
			PublicKey:   q.Get("pbk"),
			ShortID:     q.Get("sid"),
			Fingerprint: fp,
		}
	case "tls":
		tls := &tlsSettings{ServerName: sni, AllowInsecure: insecure, Fingerprint: fp}
		if alpn != "" {
			tls.ALPN = splitNonEmpty(alpn, ",")
		}
		ss.TLSSettings = tls
	}
	return ss
}

func splitNonEmpty(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- vless / trojan ---------------------------------------------------------

type vlessOutbound struct {
	Vnext []vlessServer `json:"vnext"`
}

type vlessServer struct {
	Address string      `json:"address"`
	Port    int         `json:"port"`
	Users   []vlessUser `json:"users"`
}

type vlessUser struct {
	ID         string `json:"id"`
	Encryption string `json:"encryption,omitempty"`
	Flow       string `json:"flow,omitempty"`
}

type trojanOutbound struct {
	Servers []trojanServer `json:"servers"`
}

type trojanServer struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Password string `json:"password"`
}

func parseVLESSOrTrojan(scheme string, u *url.URL) (Config, error) {
	id := u.User.Username()
	host := u.Hostname()
	port := atoiOrZero(u.Port())
	if id == "" || host == "" || port <= 0 {
		return Config{}, fmt.Errorf("invalid %s link (missing credentials/host/port)", scheme)
	}
	q := u.Query()
	stream := buildStream(q, host)
	name := u.Fragment
	if name == "" {
		name = fmt.Sprintf("%s %s", strings.ToUpper(scheme), host)
	}

	if scheme == "trojan" {
		settings := trojanOutbound{Servers: []trojanServer{{
			Address: host, Port: port, Password: id,
		}}}
		return buildConfig("trojan", name, settings, stream)
	}

	encryption := q.Get("encryption")
	if encryption == "" {
		encryption = "none"
	}
	settings := vlessOutbound{Vnext: []vlessServer{{
		Address: host, Port: port,
		Users: []vlessUser{{
			ID: id, Encryption: encryption, Flow: q.Get("flow"),
		}},
	}}}
	return buildConfig("vless", name, settings, stream)
}

// ---- shadowsocks -------------------------------------------------------------

type ssOutbound struct {
	Servers []ssServer `json:"servers"`
}

type ssServer struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Method   string `json:"method"`
	Password string `json:"password"`
}

func parseSS(u *url.URL) (Config, error) {
	var method, password, host string
	var port int

	if u.User != nil {
		// ss://base64(method:password)@host:port#name
		raw := u.User.Username()
		dec, err := decodeBase64(raw)
		if err != nil {
			// Some links put method:password as plain userinfo.
			dec = []byte(raw)
		}
		mp := string(dec)
		i := strings.Index(mp, ":")
		if i < 0 {
			return Config{}, errors.New("invalid ss link (missing method:password)")
		}
		method, password = mp[:i], mp[i+1:]
		host = u.Hostname()
		port = atoiOrZero(u.Port())
	} else {
		// ss://base64(method:password@host:port)#name (SIP002)
		dec, err := decodeBase64(u.Host)
		if err != nil {
			return Config{}, fmt.Errorf("invalid ss link: %w", err)
		}
		s := string(dec)
		at := strings.LastIndex(s, "@")
		if at < 0 {
			return Config{}, errors.New("invalid ss link (missing @host:port)")
		}
		mp := s[:at]
		i := strings.Index(mp, ":")
		if i < 0 {
			return Config{}, errors.New("invalid ss link (missing method:password)")
		}
		method, password = mp[:i], mp[i+1:]
		host = s[at+1:]
		if h, p, err := splitHostPort(host); err == nil {
			host, port = h, p
		}
	}
	if method == "" || host == "" || port <= 0 {
		return Config{}, errors.New("invalid ss link (missing method/host/port)")
	}

	name := u.Fragment
	if name == "" {
		name = fmt.Sprintf("Shadowsocks %s", host)
	}
	settings := ssOutbound{Servers: []ssServer{{
		Address: host, Port: port, Method: method, Password: password,
	}}}
	return buildConfig("shadowsocks", name, settings, nil)
}

func splitHostPort(s string) (string, int, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return s, 0, errors.New("missing port")
	}
	return s[:i], atoiOrZero(s[i+1:]), nil
}

// ---- vmess ---------------------------------------------------------------------

type vmessLink struct {
	V    string `json:"v"`
	PS   string `json:"ps"`
	Add  string `json:"add"`
	Port string `json:"port"`
	ID   string `json:"id"`
	AID  string `json:"aid"`
	Scy  string `json:"scy"`
	Net  string `json:"net"`
	Type string `json:"type"`
	Host string `json:"host"`
	Path string `json:"path"`
	TLS  string `json:"tls"`
	SNI  string `json:"sni"`
	ALPN string `json:"alpn"`
	FP   string `json:"fp"`
}

type vmessOutbound struct {
	Vnext []vmessServer `json:"vnext"`
}

type vmessServer struct {
	Address string      `json:"address"`
	Port    int         `json:"port"`
	Users   []vmessUser `json:"users"`
}

type vmessUser struct {
	ID       string `json:"id"`
	AlterID  int    `json:"alterId"`
	Security string `json:"security,omitempty"`
}

func parseVMess(u *url.URL) (Config, error) {
	dec, err := decodeBase64(u.Host)
	if err != nil {
		return Config{}, fmt.Errorf("invalid vmess link: %w", err)
	}
	var vl vmessLink
	if err := json.Unmarshal(dec, &vl); err != nil {
		return Config{}, fmt.Errorf("invalid vmess link payload: %w", err)
	}
	if vl.Add == "" || vl.ID == "" {
		return Config{}, errors.New("invalid vmess link (missing add/id)")
	}
	port := atoiOrZero(vl.Port)
	if port <= 0 {
		return Config{}, errors.New("invalid vmess link (missing port)")
	}
	aid := atoiOrZero(vl.AID)
	sec := vl.Scy
	if sec == "" {
		sec = "auto"
	}
	q := url.Values{}
	q.Set("type", vl.Net)
	q.Set("path", vl.Path)
	q.Set("host", vl.Host)
	q.Set("sni", vl.SNI)
	q.Set("alpn", vl.ALPN)
	q.Set("fp", vl.FP)
	if vl.TLS == "tls" {
		q.Set("security", "tls")
	}
	stream := buildStream(q, vl.Add)

	name := strings.TrimSpace(vl.PS)
	if name == "" {
		name = fmt.Sprintf("VMess %s", vl.Add)
	}
	settings := vmessOutbound{Vnext: []vmessServer{{
		Address: vl.Add, Port: port,
		Users: []vmessUser{{ID: vl.ID, AlterID: aid, Security: sec}},
	}}}
	return buildConfig("vmess", name, settings, stream)
}

// ---- helpers ---------------------------------------------------------------------

func atoiOrZero(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	padded := s + strings.Repeat("=", (4-len(s)%4)%4)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.URLEncoding,
	} {
		if b, err := enc.DecodeString(padded); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("invalid base64 payload")
}
