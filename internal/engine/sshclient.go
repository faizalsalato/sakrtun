package engine

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"socksrevivepc/internal/config"
)

type sshBundle struct {
	Client       *ssh.Client
	Conn         net.Conn
	ControlHosts []string
}

type transportAttempt struct {
	Label       string
	ProxyHost   string
	ProxyPort   int
	TLSHost     string
	TLSPort     int
	ControlHost string
}

func connectSSH(ctx context.Context, p config.Profile, logger *Logger) (*sshBundle, error) {
	targetHost := p.SSH.Host
	targetPort := p.SSH.Port
	if p.Mode == config.ModeDNSTT {
		targetHost = p.DNSTT.LocalSSHHost
		targetPort = p.DNSTT.LocalSSHPort
	}

	attempts := buildTransportAttempts(p, targetHost, targetPort)
	if len(attempts) == 0 {
		attempts = []transportAttempt{{Label: "default"}}
	}

	logger.Add("info", "connecting SSH %s:%d using mode %s", targetHost, targetPort, p.Mode)
	var lastErr error
	for i, attempt := range attempts {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(attempts) > 1 {
			logger.Add("info", "connection attempt %d/%d via %s", i+1, len(attempts), attempt.Label)
		}

		conn, err := dialTransportAttempt(ctx, p, targetHost, targetPort, attempt, logger)
		if err != nil {
			lastErr = err
			if len(attempts) > 1 {
				logger.Add("warn", "connection attempt %d/%d failed before SSH handshake: %v", i+1, len(attempts), err)
			}
			continue
		}

		bundle, err := finishSSHHandshake(conn, p, targetHost, targetPort, logger)
		if err == nil {
			bundle.ControlHosts = controlHostsForAttempt(p, targetHost, attempt)
			if len(attempts) > 1 {
				logger.Add("info", "connection attempt %d/%d succeeded via %s", i+1, len(attempts), attempt.Label)
			}
			return bundle, nil
		}
		_ = conn.Close()
		lastErr = err
		if len(attempts) > 1 {
			logger.Add("warn", "connection attempt %d/%d failed during SSH handshake: %v", i+1, len(attempts), err)
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no transport attempts were available")
	}
	if len(attempts) > 1 {
		return nil, fmt.Errorf("all %d connection attempts failed; last error: %w", len(attempts), lastErr)
	}
	return nil, lastErr
}

func finishSSHHandshake(conn net.Conn, p config.Profile, targetHost string, targetPort int, logger *Logger) (*sshBundle, error) {
	sshCfg := &ssh.ClientConfig{
		User:            p.SSH.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(p.SSH.Password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         time.Duration(p.SSH.HandshakeTimeoutMs) * time.Millisecond,
		ClientVersion:   "SSH-2.0-SAKRTUN",
	}
	addr := net.JoinHostPort(targetHost, fmt.Sprint(targetPort))
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, sshCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh handshake failed: %w", err)
	}
	client := ssh.NewClient(cc, chans, reqs)
	logger.Add("info", "ssh authenticated as %s", p.SSH.Username)
	return &sshBundle{Client: client, Conn: conn}, nil
}

func buildTransportAttempts(p config.Profile, targetHost string, targetPort int) []transportAttempt {
	switch p.Mode {
	case config.ModePayload:
		return payloadProxyAttempts(p, targetHost, targetPort)
	case config.ModeSSL, config.ModePayloadSSL:
		return tlsHostAttempts(p, targetHost, targetPort)
	default:
		return []transportAttempt{{Label: net.JoinHostPort(targetHost, fmt.Sprint(targetPort))}}
	}
}

func controlHostsForAttempt(p config.Profile, targetHost string, attempt transportAttempt) []string {
	switch p.Mode {
	case config.ModePayload:
		if attempt.ProxyHost != "" {
			return []string{attempt.ProxyHost}
		}
		return []string{targetHost}
	case config.ModeSSL, config.ModePayloadSSL:
		if attempt.TLSHost != "" {
			return []string{attempt.TLSHost}
		}
		if p.TLS.Host != "" {
			host, _ := hostPortWithDefault(p.TLS.Host, p.TLS.Port)
			if host != "" {
				return []string{host}
			}
		}
		return []string{targetHost}
	default:
		return []string{targetHost}
	}
}

func payloadProxyAttempts(p config.Profile, targetHost string, targetPort int) []transportAttempt {
	proxyText := strings.TrimSpace(p.Proxy.Host)
	if proxyText == "" || p.Proxy.Port <= 0 {
		return []transportAttempt{{Label: "direct payload transport"}}
	}

	rawHosts := splitHostList(proxyText)
	if len(rawHosts) == 0 {
		return []transportAttempt{{Label: "direct payload transport"}}
	}
	out := make([]transportAttempt, 0, len(rawHosts))
	for _, raw := range rawHosts {
		host, port := hostPortWithDefault(raw, p.Proxy.Port)
		if host == "" || port <= 0 {
			continue
		}
		out = append(out, transportAttempt{ProxyHost: host, ProxyPort: port, Label: "proxy " + net.JoinHostPort(host, fmt.Sprint(port))})
	}
	if len(out) == 0 {
		return []transportAttempt{{Label: net.JoinHostPort(targetHost, fmt.Sprint(targetPort))}}
	}
	return out
}

func tlsHostAttempts(p config.Profile, targetHost string, targetPort int) []transportAttempt {
	hostText := strings.TrimSpace(p.TLS.Host)
	defaultPort := p.TLS.Port
	if defaultPort <= 0 {
		defaultPort = targetPort
	}
	if hostText == "" {
		return []transportAttempt{{TLSHost: targetHost, TLSPort: defaultPort, Label: "TLS " + net.JoinHostPort(targetHost, fmt.Sprint(defaultPort))}}
	}

	rawHosts := splitHostList(hostText)
	if len(rawHosts) == 0 {
		return []transportAttempt{{TLSHost: targetHost, TLSPort: defaultPort, Label: "TLS " + net.JoinHostPort(targetHost, fmt.Sprint(defaultPort))}}
	}
	out := make([]transportAttempt, 0, len(rawHosts))
	for _, raw := range rawHosts {
		host, port := hostPortWithDefault(raw, defaultPort)
		if host == "" || port <= 0 {
			continue
		}
		out = append(out, transportAttempt{TLSHost: host, TLSPort: port, Label: "TLS " + net.JoinHostPort(host, fmt.Sprint(port))})
	}
	if len(out) == 0 {
		return []transportAttempt{{TLSHost: targetHost, TLSPort: defaultPort, Label: "TLS " + net.JoinHostPort(targetHost, fmt.Sprint(defaultPort))}}
	}
	return out
}

func dialTransport(ctx context.Context, p config.Profile, targetHost string, targetPort int, logger *Logger) (net.Conn, error) {
	attempts := buildTransportAttempts(p, targetHost, targetPort)
	if len(attempts) == 0 {
		attempts = []transportAttempt{{Label: "default"}}
	}
	return dialTransportAttempt(ctx, p, targetHost, targetPort, attempts[0], logger)
}

func dialTransportAttempt(ctx context.Context, p config.Profile, targetHost string, targetPort int, attempt transportAttempt, logger *Logger) (net.Conn, error) {
	d := &net.Dialer{Timeout: time.Duration(p.SSH.HandshakeTimeoutMs) * time.Millisecond}
	addr := net.JoinHostPort(targetHost, fmt.Sprint(targetPort))

	switch p.Mode {
	case config.ModeDirect, config.ModeDNSTT:
		return d.DialContext(ctx, "tcp", addr)
	case config.ModeSSL:
		return dialTLSAttempt(ctx, d, p, targetHost, targetPort, attempt)
	case config.ModePayload:
		connectHost, connectPort := targetHost, targetPort
		if attempt.ProxyHost != "" && attempt.ProxyPort > 0 {
			connectHost, connectPort = attempt.ProxyHost, attempt.ProxyPort
		} else if p.Proxy.Host != "" && p.Proxy.Port > 0 {
			connectHost, connectPort = p.Proxy.Host, p.Proxy.Port
		}
		logger.Add("debug", "payload transport dialing %s", net.JoinHostPort(connectHost, fmt.Sprint(connectPort)))
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(connectHost, fmt.Sprint(connectPort)))
		if err != nil {
			return nil, err
		}
		if _, wrappedConn, err := WritePayload(conn, p, targetHost, targetPort, logger); err != nil {
			_ = conn.Close()
			return nil, err
		} else {
			conn = wrappedConn
		}
		return conn, nil
	case config.ModePayloadSSL:
		conn, err := dialTLSAttempt(ctx, d, p, targetHost, targetPort, attempt)
		if err != nil {
			return nil, err
		}
		if _, wrappedConn, err := WritePayload(conn, p, targetHost, targetPort, logger); err != nil {
			_ = conn.Close()
			return nil, err
		} else {
			conn = wrappedConn
		}
		return conn, nil
	default:
		return nil, fmt.Errorf("unsupported mode %s", p.Mode)
	}
}

func dialTLS(ctx context.Context, d *net.Dialer, p config.Profile, targetHost string, targetPort int) (net.Conn, error) {
	return dialTLSAttempt(ctx, d, p, targetHost, targetPort, transportAttempt{})
}

func dialTLSAttempt(ctx context.Context, d *net.Dialer, p config.Profile, targetHost string, targetPort int, attempt transportAttempt) (net.Conn, error) {
	host := strings.TrimSpace(attempt.TLSHost)
	port := attempt.TLSPort
	if host == "" {
		host = p.TLS.Host
	}
	if port <= 0 {
		port = p.TLS.Port
	}
	if host == "" {
		host = targetHost
	}
	if port == 0 {
		port = targetPort
	}
	serverName := p.TLS.ServerName
	if serverName == "" {
		serverName = host
	}
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{ServerName: serverName, InsecureSkipVerify: p.TLS.InsecureSkipVerify, MinVersion: tls.VersionTLS12}
	conn := tls.Client(raw, cfg)
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func splitHostList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case '#', ',', ';', '\n', '\r', '\t':
			return true
		default:
			return false
		}
	})
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out
}

func hostPortWithDefault(raw string, defaultPort int) (string, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", defaultPort
	}
	if strings.Contains(raw, "://") {
		// These fields are host fields, not URLs. Strip the scheme if a user pastes one.
		if i := strings.Index(raw, "://"); i >= 0 {
			raw = raw[i+3:]
		}
	}
	if h, p, err := net.SplitHostPort(raw); err == nil {
		pi, _ := strconv.Atoi(p)
		if pi > 0 {
			return strings.Trim(h, "[]"), pi
		}
	}
	if strings.HasPrefix(raw, "[") && strings.Contains(raw, "]:") {
		if h, p, err := net.SplitHostPort(raw); err == nil {
			pi, _ := strconv.Atoi(p)
			if pi > 0 {
				return strings.Trim(h, "[]"), pi
			}
		}
	}
	// host:port for IPv4/domain. IPv6 without brackets has multiple colons and
	// must keep the profile/default port.
	if strings.Count(raw, ":") == 1 {
		host, portText, ok := strings.Cut(raw, ":")
		if ok {
			if pi, err := strconv.Atoi(strings.TrimSpace(portText)); err == nil && pi > 0 {
				return strings.TrimSpace(host), pi
			}
		}
	}
	return strings.Trim(raw, "[]"), defaultPort
}
