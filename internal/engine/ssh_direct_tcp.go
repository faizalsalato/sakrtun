package engine

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type directTCPIPPayload struct {
	DestAddr   string
	DestPort   uint32
	OriginAddr string
	OriginPort uint32
}

type sshChannelConn struct {
	ssh.Channel
	local  net.Addr
	remote net.Addr
}

func (c *sshChannelConn) LocalAddr() net.Addr {
	return c.local
}

func (c *sshChannelConn) RemoteAddr() net.Addr {
	return c.remote
}

func (c *sshChannelConn) SetDeadline(time.Time) error {
	return nil
}

func (c *sshChannelConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *sshChannelConn) SetWriteDeadline(time.Time) error {
	return nil
}

func dialSSHDirectTCP(client *ssh.Client, dest socksRequest, origin net.Addr, logger *Logger) (net.Conn, error) {
	addr := dest.Addr()
	remote, err := client.Dial("tcp", addr)
	if err == nil {
		return remote, nil
	}

	if !shouldRetrySSHIPv6Bracket(dest.Host, err) {
		return nil, err
	}

	if logger != nil {
		logger.Add("debug", "retrying SSH direct-tcpip IPv6 target with bracketed host [%s]:%d", strings.Trim(dest.Host, "[]"), dest.Port)
	}
	remote, retryErr := openSSHDirectTCPIP(client, bracketIPv6Host(dest.Host), dest.Port, origin)
	if retryErr == nil {
		return remote, nil
	}
	return nil, fmt.Errorf("%w; bracketed IPv6 retry failed: %v", err, retryErr)
}

func dialSSHDirectTCPAddr(client *ssh.Client, addr string, origin net.Addr, logger *Logger) (net.Conn, error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return client.Dial("tcp", addr)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	return dialSSHDirectTCP(client, socksRequest{Host: host, Port: port}, origin, logger)
}

func openSSHDirectTCPIP(client *ssh.Client, destHost string, destPort int, origin net.Addr) (net.Conn, error) {
	originHost, originPort := splitOriginAddr(origin)
	payload := ssh.Marshal(directTCPIPPayload{
		DestAddr:   destHost,
		DestPort:   uint32(destPort),
		OriginAddr: originHost,
		OriginPort: uint32(originPort),
	})
	ch, reqs, err := client.OpenChannel("direct-tcpip", payload)
	if err != nil {
		return nil, err
	}
	go ssh.DiscardRequests(reqs)
	return &sshChannelConn{
		Channel: ch,
		local:   tcpAddrOrDummy(originHost, originPort),
		remote:  tcpAddrOrDummy(destHost, destPort),
	}, nil
}

func shouldRetrySSHIPv6Bracket(host string, err error) bool {
	if err == nil || !isIPv6Literal(host) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "too many colons in address") || strings.Contains(msg, "missing port in address")
}

func isIPv6Literal(host string) bool {
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() == nil && ip.To16() != nil
}

func bracketIPv6Host(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host
	}
	return "[" + strings.Trim(host, "[]") + "]"
}

func splitOriginAddr(addr net.Addr) (string, int) {
	if addr == nil {
		return "127.0.0.1", 0
	}
	host, portText, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "127.0.0.1", 0
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		port = 0
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return host, port
}

func tcpAddrOrDummy(host string, port int) net.Addr {
	h := strings.Trim(host, "[]")
	ip := net.ParseIP(h)
	if ip != nil {
		return &net.TCPAddr{IP: ip, Port: port}
	}
	return dummyAddr(net.JoinHostPort(h, strconv.Itoa(port)))
}

type dummyAddr string

func (d dummyAddr) Network() string { return "tcp" }
func (d dummyAddr) String() string  { return string(d) }
