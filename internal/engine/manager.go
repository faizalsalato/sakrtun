package engine

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"socksrevivepc/internal/config"
	"socksrevivepc/internal/dnsttclient"
	"socksrevivepc/internal/routes"
	"socksrevivepc/internal/tun"
)

type Status struct {
	Running     bool   `json:"running"`
	Connecting  bool   `json:"connecting"`
	ProfileID   string `json:"profile_id"`
	Mode        string `json:"mode"`
	SocksAddr   string `json:"socks_addr"`
	Tun         bool   `json:"tun"`
	TunRouteAll bool   `json:"tun_route_all"`
	KillSwitch  bool   `json:"kill_switch"`
	StartedAt   string `json:"started_at"`
}

type Manager struct {
	root            string
	mu              sync.Mutex
	logger          *Logger
	status          Status
	cancel          context.CancelFunc
	ssh             *sshBundle
	socks           *SocksServer
	dnstt           *ManagedProcess
	embeddedDNSTT   *dnsttclient.Client
	xray            *ManagedProcess
	openvpn         *ManagedProcess
	openvpnAuthFile string
	tun             *tun.Runner
	routeCleanup    *routes.Cleanup
	manualStop      bool
	reconnecting    bool

	killSwitch             bool
	killSwitchCleanup      *routes.Cleanup
	killSwitchHosts        []string
	killSwitchAppliedHosts []string
	lastProfile            config.Profile
}

func NewManager(root string) *Manager {
	lg := NewLogger()
	return &Manager{root: root, logger: lg, tun: tun.NewRunner(lg)}
}

func (m *Manager) LogsSince(id int64) []LogEntry { return m.logger.Since(id) }

// AddLog appends a log entry. External tools such as the updater use it to
// report progress in the Logs tab.
func (m *Manager) AddLog(level, format string, args ...any) {
	m.logger.Add(level, format, args...)
}

// ClearLogs removes all in-memory log entries (used by the "Clear logs"
// button in the Logs tab).
func (m *Manager) ClearLogs() {
	m.logger.Clear()
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) Start(p config.Profile) error {
	config.ApplyDefaults(&p)
	if err := config.Validate(p); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	if m.status.Running || m.status.Connecting {
		m.mu.Unlock()
		cancel()
		return fmt.Errorf("another profile is already running or connecting")
	}
	m.cancel = cancel
	m.manualStop = false
	m.lastProfile = p
	m.killSwitchHosts = profileControlHosts(p)
	m.status = Status{Connecting: true, ProfileID: p.ID, Mode: string(p.Mode), Tun: p.Tun.Enabled, TunRouteAll: p.Tun.RouteAll, KillSwitch: m.killSwitch, StartedAt: time.Now().Format(time.RFC3339)}
	// Keep the internet blocked (kill switch) while connecting. The bypass
	// routes built for this profile let the connection reach its own servers.
	_ = m.syncKillSwitchLocked()
	m.mu.Unlock()

	m.logger.Add("info", "starting profile: %s", p.Name)

	fail := func(format string, args ...any) error {
		err := fmt.Errorf(format, args...)
		m.logger.Add("error", "%v", err)
		m.stop(false)
		return err
	}

	ensureCurrent := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.cancel == nil || !m.status.Connecting || m.status.ProfileID != p.ID {
			return context.Canceled
		}
		return nil
	}

	var socksAddr string
	var bypass []string
	switch p.Mode {
	case config.ModeOpenVPN:
		proc, authFile, err := startOpenVPN(ctx, m.root, p, m.logger)
		if err != nil {
			return fail("start openvpn failed: %w", err)
		}
		m.mu.Lock()
		m.openvpn = proc
		m.openvpnAuthFile = authFile
		m.mu.Unlock()
		timeout := time.Duration(p.OpenVPN.StartupTimeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		started := time.Now()
		select {
		case <-proc.Ready():
			m.logger.Add("info", "openvpn is connected")
		case <-proc.ExitChan():
			if exited, procErr := proc.Exited(); exited {
				m.stop(false)
				if procErr != nil {
					return fmt.Errorf("openvpn exited before connecting: %w", procErr)
				}
				return fmt.Errorf("openvpn exited before connecting")
			}
		case <-ctx.Done():
			return fail("connection cancelled: %w", ctx.Err())
		case <-time.After(timeout - time.Since(started)):
			if exited, procErr := proc.Exited(); exited {
				m.stop(false)
				if procErr != nil {
					return fmt.Errorf("openvpn did not connect within %s: %w", timeout, procErr)
				}
				return fmt.Errorf("openvpn did not connect within %s", timeout)
			}
			return fail("openvpn did not report connected within %s", timeout)
		}
		if err := ensureCurrent(); err != nil {
			proc.Stop()
			return fail("connection cancelled: %w", err)
		}
		bypass = nil
	case config.ModeXray:
		exe, err := resolveXrayExecutable(m.root, p.Xray.Executable)
		if err != nil {
			return fail("start xray failed: %w", err)
		}
		args := p.Xray.Args
		if strings.TrimSpace(p.Xray.ConfigJSON) != "" {
			// The profile carries an inline JSON config written in the manual
			// editor. Persist it next to the other configs and point xray at it.
			configPath := filepath.Join(m.root, "configs", "xray-profile-"+p.ID+".json")
			if err := os.WriteFile(configPath, []byte(p.Xray.ConfigJSON), 0o600); err != nil {
				return fail("write xray profile config failed: %w", err)
			}
			args = []string{"run", "-config", configPath}
		}
		proc, err := StartProcess(ctx, m.root, "xray", exe, args, m.logger)
		if err != nil {
			return fail("start xray failed: %w", err)
		}
		m.mu.Lock()
		m.xray = proc
		m.mu.Unlock()
		time.Sleep(time.Duration(p.Xray.StartupTimeoutMs) * time.Millisecond)
		if err := ensureCurrent(); err != nil {
			proc.Stop()
			return fail("connection cancelled: %w", err)
		}
		socksAddr = net.JoinHostPort(p.Xray.LocalSocksHost, fmt.Sprint(p.Xray.LocalSocksPort))
		if exited, procErr := proc.Exited(); exited {
			m.stop(false)
			if procErr != nil {
				return fmt.Errorf("xray exited before opening local SOCKS on %s: %w", socksAddr, procErr)
			}
			return fmt.Errorf("xray exited before opening local SOCKS on %s", socksAddr)
		}
		if err := waitForTCP(ctx, socksAddr, time.Duration(p.Xray.StartupTimeoutMs)*time.Millisecond); err != nil {
			return fail("xray local SOCKS is not listening on %s: %w", socksAddr, err)
		}
		bypass = nil
	default:
		if p.Mode == config.ModeDNSTT {
			if p.DNSTT.UseEmbedded {
				client, err := dnsttclient.Start(ctx, dnsttclient.Options{
					ResolverType:     p.DNSTT.ResolverType,
					ResolverAddress:  p.DNSTT.ResolverAddress,
					PublicKeyHex:     p.DNSTT.PublicKey,
					Domain:           p.DNSTT.Domain,
					LocalAddress:     net.JoinHostPort(p.DNSTT.LocalSSHHost, fmt.Sprint(p.DNSTT.LocalSSHPort)),
					UTLSDistribution: p.DNSTT.UTLSDistribution,
					StartupTimeout:   time.Duration(p.DNSTT.StartupTimeoutMs) * time.Millisecond,
					LogWriter:        dnsttLogWriter{logger: m.logger},
				})
				if err != nil {
					return fail("start embedded dnstt failed: %w", err)
				}
				m.mu.Lock()
				m.embeddedDNSTT = client
				m.mu.Unlock()
			} else {
				proc, err := StartProcess(ctx, m.root, "dnstt", p.DNSTT.Executable, p.DNSTT.Args, m.logger)
				if err != nil {
					return fail("start dnstt failed: %w", err)
				}
				m.mu.Lock()
				m.dnstt = proc
				m.mu.Unlock()
				time.Sleep(time.Duration(p.DNSTT.StartupTimeoutMs) * time.Millisecond)
			}
			if err := ensureCurrent(); err != nil {
				return fail("connection cancelled: %w", err)
			}
		}
		sshc, err := connectSSH(ctx, p, m.logger)
		if err != nil {
			return fail("%w", err)
		}
		if err := ensureCurrent(); err != nil {
			_ = sshc.Client.Close()
			_ = sshc.Conn.Close()
			return fail("connection cancelled: %w", err)
		}
		m.mu.Lock()
		m.ssh = sshc
		m.mu.Unlock()
		socksAddr = net.JoinHostPort(p.Local.SocksHost, fmt.Sprint(p.Local.SocksPort))
		ss := &SocksServer{Addr: socksAddr, SSH: sshc.Client, Logger: m.logger, DNS: profileDNSServers(p), UDPGW: p.UDPGW}
		if err := ss.Start(); err != nil {
			return fail("%w", err)
		}
		m.mu.Lock()
		m.socks = ss
		m.status.SocksAddr = socksAddr
		m.mu.Unlock()
		if err := waitForTCP(ctx, socksAddr, 2500*time.Millisecond); err != nil {
			return fail("local SOCKS is not listening on %s: %w", socksAddr, err)
		}
		bypass = effectiveBypassHosts(p, sshc.ControlHosts)
	}

	if err := ensureCurrent(); err != nil {
		return fail("connection cancelled: %w", err)
	}
	if p.Tun.Enabled {
		if err := m.tun.Start(&p, socksAddr); err != nil {
			return fail("%w", err)
		}
		cleanup, err := routes.Apply(p, bypass, m.logger)
		if err != nil {
			m.logger.Add("warn", "route setup error: %v", err)
		}
		m.mu.Lock()
		m.routeCleanup = cleanup
		m.mu.Unlock()
	}

	m.mu.Lock()
	current := m.cancel != nil && m.status.Connecting && m.status.ProfileID == p.ID
	err := ctx.Err()
	if err == nil && current {
		m.status = Status{Running: true, ProfileID: p.ID, Mode: string(p.Mode), SocksAddr: socksAddr, Tun: p.Tun.Enabled, TunRouteAll: p.Tun.RouteAll, KillSwitch: m.killSwitch, StartedAt: time.Now().Format(time.RFC3339)}
		// With the tunnel up (TUN RouteAll or OpenVPN) the VPN owns the
		// routing; release the physical block but keep the bypass hosts so a
		// later reconnect can still reach the servers.
		m.killSwitchHosts = bypass
		_ = m.syncKillSwitchLocked()
	}
	m.mu.Unlock()
	if err != nil || !current {
		m.stop(false)
		if err != nil {
			return err
		}
		return context.Canceled
	}
	m.logger.Add("info", "profile is connected; local socks=%s tun=%v", socksAddr, p.Tun.Enabled)
	m.startMonitor(ctx, p)
	return nil
}

func profileDNSServers(p config.Profile) []string {
	// Custom DNS servers take precedence everywhere (SOCKS DNS-over-SSH
	// resolver and TUN adapter).
	if len(p.DNS.Servers) > 0 {
		return append([]string{}, p.DNS.Servers...)
	}
	out := append([]string{}, p.Tun.DNS...)
	if p.Tun.IPv6Enabled {
		out = append(out, p.Tun.IPv6DNS...)
	}
	return out
}

func waitForTCP(ctx context.Context, addr string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		d := net.Dialer{Timeout: 350 * time.Millisecond}
		c, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = c.Close()
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}

func effectiveBypassHosts(p config.Profile, activeControlHosts []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(activeControlHosts)+1)
	add := func(host string) {
		host = strings.TrimSpace(host)
		if host == "" {
			return
		}
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = strings.Trim(h, "[]")
		}
		if isLocalBypassHost(host) || seen[host] {
			return
		}
		seen[host] = true
		out = append(out, host)
	}
	for _, host := range activeControlHosts {
		add(host)
	}
	if len(out) == 0 && p.Mode != config.ModeDNSTT {
		// Fallback for old profile formats or unexpected transports. This still only
		// adds the direct SSH host, not every rotated proxy from the profile.
		add(p.SSH.Host)
	}
	if p.Mode == config.ModeDNSTT && p.DNSTT.UseEmbedded {
		add(dnsttResolverHost(p.DNSTT.ResolverAddress))
	}
	return out
}

func isLocalBypassHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsUnspecified()
}

func dnsttResolverHost(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err == nil {
			return u.Hostname()
		}
	}
	host, _, err := net.SplitHostPort(s)
	if err == nil {
		return host
	}
	return s
}

func (m *Manager) Stop() {
	m.stop(true)
}

func (m *Manager) stop(manual bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if manual {
		m.manualStop = true
	}
	m.stopLocked()
}

func (m *Manager) startMonitor(ctx context.Context, p config.Profile) {
	if !p.Reconnect.Enabled {
		return
	}
	interval := time.Duration(p.Reconnect.CheckIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 10 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if m.isManualStopped() {
					return
				}
				if err := m.probeConnection(p); err != nil {
					m.logger.Add("warn", "connection monitor detected tunnel loss: %v", err)
					m.reconnectLoop(p, err)
					return
				}
			}
		}
	}()
}

func (m *Manager) isManualStopped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.manualStop
}

func (m *Manager) markReconnecting() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.manualStop || m.reconnecting {
		return false
	}
	m.reconnecting = true
	return true
}

func (m *Manager) clearReconnecting() {
	m.mu.Lock()
	m.reconnecting = false
	m.mu.Unlock()
}

func (m *Manager) probeConnection(p config.Profile) error {
	m.mu.Lock()
	sshc := m.ssh
	xray := m.xray
	openvpn := m.openvpn
	socksAddr := m.status.SocksAddr
	running := m.status.Running
	m.mu.Unlock()

	if !running {
		return nil
	}
	if openvpn != nil {
		if exited, err := openvpn.Exited(); exited {
			if err != nil {
				return fmt.Errorf("openvpn exited: %w", err)
			}
			return fmt.Errorf("openvpn exited")
		}
		return nil
	}
	if xray != nil {
		if exited, err := xray.Exited(); exited {
			if err != nil {
				return fmt.Errorf("xray exited: %w", err)
			}
			return fmt.Errorf("xray exited")
		}
		if socksAddr != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return waitForTCP(ctx, socksAddr, 2*time.Second)
		}
		return nil
	}
	if sshc != nil && sshc.Client != nil {
		if sshc.Conn != nil {
			_ = sshc.Conn.SetDeadline(time.Now().Add(5 * time.Second))
			defer sshc.Conn.SetDeadline(time.Time{})
		}
		_, _, err := sshc.Client.SendRequest("keepalive@openssh.com", true, nil)
		if err != nil {
			return fmt.Errorf("ssh keepalive failed: %w", err)
		}
	}
	return nil
}

func (m *Manager) reconnectLoop(p config.Profile, cause error) {
	if !m.markReconnecting() {
		return
	}
	defer m.clearReconnecting()

	delay := time.Duration(p.Reconnect.DelaySeconds) * time.Second
	if delay <= 0 {
		delay = 3 * time.Second
	}
	maxRetries := p.Reconnect.MaxRetries
	m.logger.Add("warn", "connection lost (%v); auto reconnect is enabled", cause)
	if p.Tun.Enabled {
		m.logger.Add("info", "destroying TUN before reconnect")
	}
	m.stop(false)
	if p.Tun.Enabled {
		time.Sleep(1200 * time.Millisecond)
	}

	for attempt := 1; maxRetries <= 0 || attempt <= maxRetries; attempt++ {
		if m.isManualStopped() {
			m.logger.Add("info", "auto reconnect cancelled by user")
			return
		}
		m.logger.Add("info", "reconnect attempt %d%s in %s", attempt, reconnectLimitSuffix(maxRetries), delay)
		select {
		case <-time.After(delay):
		}
		if m.isManualStopped() {
			m.logger.Add("info", "auto reconnect cancelled by user")
			return
		}
		if err := m.Start(p); err != nil {
			if m.isManualStopped() {
				m.logger.Add("info", "auto reconnect cancelled by user")
				return
			}
			m.logger.Add("warn", "reconnect attempt %d failed: %v", attempt, err)
			if p.Tun.Enabled {
				m.logger.Add("info", "destroying TUN after failed reconnect attempt")
				m.stop(false)
				time.Sleep(1200 * time.Millisecond)
			}
			continue
		}
		m.logger.Add("info", "reconnected successfully")
		return
	}
	m.logger.Add("error", "auto reconnect stopped after %d failed attempt(s)", maxRetries)
}

func reconnectLimitSuffix(maxRetries int) string {
	if maxRetries <= 0 {
		return " (unlimited)"
	}
	return fmt.Sprintf("/%d", maxRetries)
}

func (m *Manager) stopLocked() {
	wasActive := m.status.Running || m.status.Connecting
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if m.routeCleanup != nil {
		m.routeCleanup.Run()
		m.routeCleanup = nil
	}
	if m.tun != nil {
		m.tun.Stop()
	}
	if m.socks != nil {
		m.socks.Stop()
		m.socks = nil
	}
	if m.ssh != nil {
		_ = m.ssh.Client.Close()
		_ = m.ssh.Conn.Close()
		m.ssh = nil
	}
	if m.xray != nil {
		m.xray.Stop()
		m.xray = nil
	}
	if m.embeddedDNSTT != nil {
		m.embeddedDNSTT.Stop()
		m.embeddedDNSTT = nil
	}
	if m.dnstt != nil {
		m.dnstt.Stop()
		m.dnstt = nil
	}
	// Remove the temporary OpenVPN auth-user-pass file so plain text
	// credentials never stay on disk after the tunnel stops.
	if m.openvpnAuthFile != "" {
		_ = os.Remove(m.openvpnAuthFile)
		m.openvpnAuthFile = ""
	}
	if wasActive {
		m.logger.Add("info", "disconnected")
	}
	m.status = Status{KillSwitch: m.killSwitch}
	// If the kill switch is on, re-block the internet now that the tunnel is
	// gone. Otherwise make sure any previous block is fully restored.
	_ = m.syncKillSwitchLocked()
}

// SetKillSwitch enables or disables the global kill switch. When enabled, all
// internet access is blocked while the VPN is not connected; only the VPN
// servers of profile (or, if profile is empty, of the last connected profile)
// stay reachable so the tunnel can still connect or reconnect. When a
// full-device tunnel (TUN RouteAll or OpenVPN) is up, the VPN owns the routing
// and the block is released. The state is meant to be persisted by the UI in
// the global app settings, not in the profile.
func (m *Manager) SetKillSwitch(enabled bool, profile config.Profile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killSwitch = enabled
	if enabled {
		if profile.ID != "" {
			m.lastProfile = profile
			m.killSwitchHosts = profileControlHosts(profile)
		} else if m.lastProfile.ID != "" {
			m.killSwitchHosts = profileControlHosts(m.lastProfile)
		}
	}
	return m.syncKillSwitchLocked()
}

// KillSwitch reports whether the kill switch is currently enabled.
func (m *Manager) KillSwitch() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.killSwitch
}

// syncKillSwitchLocked applies or removes the kill switch route block to match
// the current killSwitch flag and tunnel state. The caller must hold m.mu.
func (m *Manager) syncKillSwitchLocked() error {
	m.status.KillSwitch = m.killSwitch
	// While a full-device tunnel is active the VPN already owns the routing,
	// so the physical block must be released.
	tunnelOwnsRouting := m.status.Running && (m.status.Mode == string(config.ModeOpenVPN) || (m.status.Tun && m.status.TunRouteAll))
	if !m.killSwitch || tunnelOwnsRouting {
		if m.killSwitchCleanup != nil {
			m.killSwitchCleanup.Run()
			m.killSwitchCleanup = nil
			m.logger.Add("info", "kill switch: internet access restored")
		}
		return nil
	}
	hosts := append([]string(nil), m.killSwitchHosts...)
	if m.killSwitchCleanup != nil && sameStringSlices(m.killSwitchAppliedHosts, hosts) {
		return nil
	}
	if m.killSwitchCleanup != nil {
		m.killSwitchCleanup.Run()
		m.killSwitchCleanup = nil
	}
	cleanup, err := routes.BlockInternet(hosts, m.logger)
	if err != nil {
		m.logger.Add("error", "kill switch: failed to block internet: %v", err)
		return err
	}
	m.killSwitchCleanup = cleanup
	m.killSwitchAppliedHosts = hosts
	m.logger.Add("warn", "kill switch: internet blocked until the VPN connects")
	return nil
}

// profileControlHosts lists the hosts a profile needs to reach while the
// internet is blocked: the SSH target, the TLS front, rotated proxies and the
// DNSTT resolver.
func profileControlHosts(p config.Profile) []string {
	seen := map[string]bool{}
	var out []string
	add := func(host string) {
		host = strings.TrimSpace(host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = strings.Trim(h, "[]")
		}
		host = strings.TrimSpace(host)
		if host == "" || isLocalBypassHost(host) || seen[host] {
			return
		}
		seen[host] = true
		out = append(out, host)
	}
	add(p.SSH.Host)
	add(p.TLS.Host)
	for _, h := range splitHostList(p.Proxy.Host) {
		add(h)
	}
	add(dnsttResolverHost(p.DNSTT.ResolverAddress))
	return out
}

func sameStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type dnsttLogWriter struct {
	logger *Logger
}

func (w dnsttLogWriter) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	if line != "" && w.logger != nil {
		w.logger.Add("dnstt", "%s", line)
	}
	return len(p), nil
}
