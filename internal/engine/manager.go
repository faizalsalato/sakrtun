package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"

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
	// Traffic reports the post-connect traffic probe result:
	// "" = not probed yet, "ok" = traffic passes, "failed" = connected but
	// no traffic is passing (the remote server may be offline).
	Traffic string `json:"traffic"`
}

type Manager struct {
	root             string
	mu               sync.Mutex
	logger           *Logger
	status           Status
	cancel           context.CancelFunc
	ssh              *sshBundle
	socks            *SocksServer
	dnstt            *ManagedProcess
	embeddedDNSTT    *dnsttclient.Client
	xray             *ManagedProcess
	openvpn          *ManagedProcess
	openvpnAuthFile  string
	proxifier        *exec.Cmd
	proxifierProfile string
	ipv6Blocked      bool
	tun              *tun.Runner
	routeCleanup     *routes.Cleanup
	manualStop       bool
	reconnecting     bool

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
	m.killSwitchHosts = profileControlHosts(p, m.root)
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
		bypass = xrayServerHosts(p, m.root)
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
	// Without a full TUN the system IPv6 is not routed through the tunnel, so
	// IPv6-capable sites would see the machine's real IP. Disable the IPv6
	// bindings while the tunnel is up unless the profile explicitly allows the
	// leak (or the tunnel itself carries IPv6).
	if !p.Tun.IPv6Enabled && !p.Tun.AllowIPv6Leak {
		if err := setSystemIPv6Enabled(false); err != nil {
			m.logger.Add("warn", "could not disable system IPv6: %v", err)
		} else {
			m.mu.Lock()
			m.ipv6Blocked = true
			m.mu.Unlock()
			m.logger.Add("info", "system IPv6 disabled to prevent IP leaks")
		}
	}
	// In Proxifier mode the local SOCKS proxy is pushed into Proxifier so the
	// apps are forced through the tunnel without a TUN adapter.
	if routeModeOf(p) == "proxifier" {
		if err := m.startProxifier(p, socksAddr); err != nil {
			m.logger.Add("warn", "proxifier could not be started: %v", err)
		}
	}
	// Verify that real traffic can pass through the tunnel. The tunnel can be
	// up (process running, SOCKS listening) while the remote server is dead,
	// which would leave the UI showing "Connected" with no internet. This
	// probe reports that state clearly in the logs and in the UI.
	if socksAddr != "" {
		go m.verifyTraffic(ctx, socksAddr)
	}
	m.startMonitor(ctx, p)
	m.startKeepAlive(ctx, p)
	return nil
}

// probeTrafficViaSOCKS dials well-known IPs through the local SOCKS proxy of
// the tunnel. Reaching any of them means real traffic is passing.
func probeTrafficViaSOCKS(ctx context.Context, socksAddr string) error {
	d, err := proxy.SOCKS5("tcp", socksAddr, nil, &net.Dialer{Timeout: 8 * time.Second})
	if err != nil {
		return fmt.Errorf("socks dialer: %w", err)
	}
	targets := []string{"1.1.1.1:80", "8.8.8.8:53", "9.9.9.9:80"}
	var lastErr error
	for _, t := range targets {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := d.Dial("tcp", t)
		if err != nil {
			lastErr = err
			continue
		}
		_ = conn.Close()
		return nil
	}
	return fmt.Errorf("no traffic through %s: %w", socksAddr, lastErr)
}

// verifyTraffic runs a one-shot traffic probe shortly after connecting and
// records the outcome in the status and the logs.
func (m *Manager) verifyTraffic(ctx context.Context, socksAddr string) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second):
	}
	if err := probeTrafficViaSOCKS(ctx, socksAddr); err != nil {
		m.mu.Lock()
		m.status.Traffic = "failed"
		m.mu.Unlock()
		m.logger.Add("warn", "connected but no traffic is passing the tunnel: %v (the server may be offline or blocking this network)", err)
		return
	}
	m.mu.Lock()
	m.status.Traffic = "ok"
	m.mu.Unlock()
	m.logger.Add("info", "tunnel traffic verified OK")
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

// startKeepAlive sends SSH keepalives for the whole connection lifetime,
// using the profile's "Keep alive seconds" value. This generates traffic on
// idle tunnels (preventing silent drops by NAT/middleboxes) and detects a
// dead connection early: it either triggers the reconnect loop or, when
// reconnect is disabled, stops the tunnel cleanly so the UI shows
// Disconnected instead of a stale Connected.
func (m *Manager) startKeepAlive(ctx context.Context, p config.Profile) {
	interval := time.Duration(p.SSH.KeepAliveSeconds) * time.Second
	if interval <= 0 {
		interval = 20 * time.Second
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
				m.mu.Lock()
				sshc := m.ssh
				m.mu.Unlock()
				if sshc == nil || sshc.Client == nil {
					continue // not an SSH-based mode (xray/openvpn)
				}
				if err := sendSSHKeepAlive(sshc); err != nil {
					m.logger.Add("warn", "keepalive failed, tunnel lost: %v", err)
					if p.Reconnect.Enabled {
						m.reconnectLoop(p, err)
					} else {
						m.stop(false)
					}
					return
				}
			}
		}
	}()
}

// sendSSHKeepAlive sends an SSH keepalive request and waits for the reply.
// It must NOT set a deadline on the shared connection: that deadline applies
// to every read/write of the tunnel (all SSH channels multiplex the same
// net.Conn), so a slow reply would kill the whole tunnel and trigger
// reconnects. A per-call timeout is used instead, leaving the tunnel traffic
// untouched.
func sendSSHKeepAlive(sshc *sshBundle) error {
	done := make(chan error, 1)
	go func() {
		_, _, err := sshc.Client.SendRequest("keepalive@openssh.com", true, nil)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		return fmt.Errorf("keepalive timed out")
	}
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
		if err := sendSSHKeepAlive(sshc); err != nil {
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
	if m.proxifier != nil {
		_ = m.proxifier.Process.Kill()
		m.proxifier = nil
	}
	if m.proxifierProfile != "" {
		_ = os.Remove(m.proxifierProfile)
		m.proxifierProfile = ""
	}
	if m.ipv6Blocked {
		if err := setSystemIPv6Enabled(true); err != nil {
			m.logger.Add("warn", "could not re-enable system IPv6: %v", err)
		} else {
			m.logger.Add("info", "system IPv6 re-enabled")
		}
		m.ipv6Blocked = false
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
			m.killSwitchHosts = profileControlHosts(profile, m.root)
		} else if m.lastProfile.ID != "" {
			m.killSwitchHosts = profileControlHosts(m.lastProfile, m.root)
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
// internet is blocked: the SSH target, the TLS front, rotated proxies, the
// DNSTT resolver and the Xray servers.
func profileControlHosts(p config.Profile, root string) []string {
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
	for _, h := range xrayServerHosts(p, root) {
		add(h)
	}
	return out
}

// xrayServerHosts extracts the remote server addresses from the Xray config
// (inline JSON first, then the config file). These hosts must stay reachable
// through the physical gateway once the TUN route-all takes over, otherwise
// the Xray control connection loops back into the TUN and starves the socket
// buffers (\"bind: ... queue was full\"), producing a tunnel that connects but
// passes no data.
func xrayServerHosts(p config.Profile, root string) []string {
	raw := strings.TrimSpace(p.Xray.ConfigJSON)
	if raw == "" && strings.TrimSpace(p.Xray.ConfigPath) != "" {
		if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p.Xray.ConfigPath))); err == nil {
			raw = string(b)
		}
	}
	if raw == "" {
		return nil
	}
	var cfg struct {
		Outbounds []struct {
			Protocol string `json:"protocol"`
			Settings struct {
				Vnext []struct {
					Address string `json:"address"`
				} `json:"vnext"`
				Servers []struct {
					Address string `json:"address"`
				} `json:"servers"`
			} `json:"settings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" || isLocalBypassHost(h) || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	for _, ob := range cfg.Outbounds {
		switch ob.Protocol {
		case "vless", "vmess":
			for _, v := range ob.Settings.Vnext {
				add(v.Address)
			}
		case "trojan", "shadowsocks":
			for _, s := range ob.Settings.Servers {
				add(s.Address)
			}
		}
	}
	return out
}

// routeModeOf returns the effective route mode of the profile ("tun",
// "proxifier" or "proxy"). ApplyDefaults already normalizes RouteMode, but
// this stays defensive for callers that skip it.
func routeModeOf(p config.Profile) string {
	switch strings.TrimSpace(p.Tun.RouteMode) {
	case "tun":
		return "tun"
	case "proxifier":
		return "proxifier"
	case "proxy":
		return "proxy"
	}
	if p.Tun.Enabled {
		return "tun"
	}
	return "proxy"
}

// startProxifier registers the license (when configured), writes a Proxifier
// profile pointing at the local SOCKS proxy and launches Proxifier with it.
// The process is killed when the tunnel stops.
func (m *Manager) startProxifier(p config.Profile, socksAddr string) error {
	if strings.TrimSpace(p.Proxifier.LicenseKey) != "" {
		if err := registerProxifierLicense(p.Proxifier.LicenseName, p.Proxifier.LicenseKey); err != nil {
			m.logger.Add("warn", "proxifier license registration failed: %v", err)
		} else {
			m.logger.Add("info", "proxifier license registered")
		}
	}
	exe, err := resolveProxifierExecutable(m.root, p.Proxifier.ExePath)
	if err != nil {
		return err
	}
	// The Portable Edition uses a different profile schema than the normal
	// build. Writing the wrong schema makes Proxifier warn that the profile
	// "belongs to another application".
	portable := isBundledProxifier(m.root, exe)
	// Use a unique file name per connection: Proxifier imports the loaded
	// file into its profile list, and reusing the same name makes it warn
	// that the file already exists. Stale files of previous connections are
	// removed first.
	pattern := filepath.Join(m.root, "configs", "proxifier-profile-"+p.ID+"-*.ppx")
	if stale, _ := filepath.Glob(pattern); len(stale) > 0 {
		for _, s := range stale {
			_ = os.Remove(s)
		}
	}
	profilePath := filepath.Join(m.root, "configs", fmt.Sprintf("proxifier-profile-%s-%d.ppx", p.ID, time.Now().Unix()))
	bypassApps := proxifierBypassApps(p)
	if err := os.WriteFile(profilePath, []byte(proxifierProfileXML(socksAddr, portable, bypassApps)), 0o600); err != nil {
		return err
	}
	cmd := exec.Command(exe, profilePath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch %s: %w", exe, err)
	}
	m.mu.Lock()
	m.proxifier = cmd
	m.proxifierProfile = profilePath
	m.mu.Unlock()
	m.logger.Add("info", "proxifier started with SOCKS proxy %s", socksAddr)
	return nil
}

// isBundledProxifier reports whether the resolved executable is the bundled
// Portable Edition copy inside tools/proxifier.
func isBundledProxifier(root, exe string) bool {
	rel, err := filepath.Rel(root, exe)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		strings.HasPrefix(filepath.ToSlash(rel), "tools/proxifier/"))
}

// proxifierBypassApps returns the process names that must bypass the proxy in
// the generated Proxifier profile: the app itself and the tunnel tools. Forcing
// them through the local SOCKS port would loop their own control connection.
func proxifierBypassApps(p config.Profile) []string {
	apps := []string{"SAKRTUN.exe"}
	switch p.Mode {
	case config.ModeXray:
		apps = append(apps, "xray.exe")
	case config.ModeOpenVPN:
		apps = append(apps, "openvpn.exe")
	default:
		apps = append(apps, "dnstt-client.exe")
	}
	return apps
}

// setSystemIPv6Enabled enables or disables the IPv6 protocol bindings on all
// network adapters. It is used to prevent IPv6 leaks in proxy/Proxifier modes,
// where the tunnel does not carry IPv6 but the OS would still prefer native
// IPv6 routes and reveal the machine's real address to IPv6-capable sites.
func setSystemIPv6Enabled(enabled bool) error {
	if runtime.GOOS != "windows" {
		// On other systems the routes handle IPv6 (see routes.Apply).
		return nil
	}
	cmd := "Disable-NetAdapterBinding"
	if enabled {
		cmd = "Enable-NetAdapterBinding"
	}
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		cmd+" -Name '*' -ComponentID ms_tcpip6 -ErrorAction SilentlyContinue").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v (%s)", cmd, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// registerProxifierLicense writes the Proxifier registration into the
// registry key the official Proxifier build reads:
// HKCU\Software\Initex\Proxifier\License with the Name and Key values.
func registerProxifierLicense(name, key string) error {
	path := `HKCU\Software\Initex\Proxifier\License`
	if name == "" {
		name = "SAKR TUN"
	}
	if out, err := exec.Command("reg.exe", "add", path, "/v", "Name", "/t", "REG_SZ", "/d", name, "/f").CombinedOutput(); err != nil {
		return fmt.Errorf("reg add Name: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("reg.exe", "add", path, "/v", "Key", "/t", "REG_SZ", "/d", key, "/f").CombinedOutput(); err != nil {
		return fmt.Errorf("reg add Key: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// proxifierCandidates returns the Proxifier.exe search order: the normal
// Program Files installation first, then the bundled tools/proxifier copy.
func proxifierCandidates(root string) []string {
	return []string{
		`C:\Program Files (x86)\Proxifier\Proxifier.exe`,
		`C:\Program Files\Proxifier\Proxifier.exe`,
		filepath.Join(root, "tools", "proxifier", "Proxifier.exe"),
		`C:\Proxifier\Proxifier.exe`,
	}
}

// resolveProxifierExecutable locates Proxifier.exe: the custom profile path
// first, then the normal Program Files installation, then the bundled copy in
// tools/proxifier, then PATH.
func resolveProxifierExecutable(root, custom string) (string, error) {
	custom = strings.TrimSpace(custom)
	if custom != "" {
		p := custom
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("proxifier executable not found: %s", custom)
	}
	for _, cand := range proxifierCandidates(root) {
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	if found, err := exec.LookPath("Proxifier.exe"); err == nil {
		return found, nil
	}
	return "", fmt.Errorf("Proxifier is not installed and not bundled in tools/proxifier; install Proxifier, place Proxifier.exe in tools/proxifier, or set its path in the profile")
}

// proxifierProfileXML builds a Proxifier profile that routes all traffic
// through the given SOCKS5 proxy, leaving localhost direct. The normal build
// uses the standard schema (version 101); the Portable Edition uses its own
// schema (version 102 with the portable proxification engine) - loading the
// wrong one makes Proxifier warn that the profile belongs to another app.
// The tunnel processes themselves are bypassed (Direct), otherwise Proxifier
// would force the Xray/OpenVPN control connection through its own SOCKS port
// and trigger its "Infinite Connection Loop Detection".
func proxifierProfileXML(socksAddr string, portable bool, bypassApps []string) string {
	host := "127.0.0.1"
	port := "10808"
	if h, p, err := net.SplitHostPort(socksAddr); err == nil {
		host, port = h, p
	}
	apps := strings.Join(bypassApps, "; ")
	version := "101"
	productID := "0"
	portableEngine := ""
	if portable {
		version = "102"
		productID = "1"
		portableEngine = `    <ProxificationPortableEngine subsystem="32">` + "\n" +
			`      <Type hotpatch="true">Prologue</Type>` + "\n" +
			`      <Location>Winsock</Location>` + "\n" +
			`    </ProxificationPortableEngine>` + "\n" +
			`    <ProxificationPortableEngine subsystem="64">` + "\n" +
			`      <Type hotpatch="false">Prologue</Type>` + "\n" +
			`      <Location>Winsock</Location>` + "\n" +
			`    </ProxificationPortableEngine>` + "\n"
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<ProxifierProfile version="` + version + `" platform="Windows" product_id="` + productID + `" product_minver="400">` + "\n" +
		`  <Options>` + "\n" +
		`    <Resolve>` + "\n" +
		`      <AutoModeDetection enabled="true" />` + "\n" +
		`      <ViaProxy enabled="true" />` + "\n" +
		`      <BlockNonATypes enabled="false" />` + "\n" +
		`      <ExclusionList OnlyFromListMode="false">%ComputerName%; localhost; *.local</ExclusionList>` + "\n" +
		`      <DnsUdpMode>0</DnsUdpMode>` + "\n" +
		`    </Resolve>` + "\n" +
		`    <Encryption mode="basic" />` + "\n" +
		`    <ConnectionLoopDetection enabled="true" resolve="true" />` + "\n" +
		`    <Udp mode="mode_bypass" />` + "\n" +
		`    <LeakPreventionMode enabled="false" />` + "\n" +
		`    <ProcessOtherUsers enabled="false" />` + "\n" +
		`    <ProcessServices enabled="false" />` + "\n" +
		`    <HandleDirectConnections enabled="false" />` + "\n" +
		`    <HttpProxiesSupport enabled="false" />` + "\n" +
		portableEngine +
		`  </Options>` + "\n" +
		`  <ProxyList>` + "\n" +
		`    <Proxy id="100" type="SOCKS5">` + "\n" +
		`      <Address>` + host + `</Address>` + "\n" +
		`      <Port>` + port + `</Port>` + "\n" +
		`      <Options>48</Options>` + "\n" +
		`    </Proxy>` + "\n" +
		`  </ProxyList>` + "\n" +
		`  <ChainList />` + "\n" +
		`  <RuleList>` + "\n" +
		`    <Rule enabled="true">` + "\n" +
		`      <Action type="Direct" />` + "\n" +
		`      <Targets>localhost; 127.0.0.1; %ComputerName%; ::1</Targets>` + "\n" +
		`      <Name>Localhost</Name>` + "\n" +
		`    </Rule>` + "\n" +
		`    <Rule enabled="true">` + "\n" +
		`      <Action type="Direct" />` + "\n" +
		`      <Applications>` + apps + `</Applications>` + "\n" +
		`      <Name>SAKR TUN bypass</Name>` + "\n" +
		`    </Rule>` + "\n" +
		`    <Rule enabled="true">` + "\n" +
		`      <Action type="Proxy">100</Action>` + "\n" +
		`      <Name>SAKR TUN</Name>` + "\n" +
		`    </Rule>` + "\n" +
		`  </RuleList>` + "\n" +
		`</ProxifierProfile>` + "\n"
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
