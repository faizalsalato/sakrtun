package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"socksrevivepc/internal/config"
)

const (
	socksVersion        = 0x05
	socksCmdConnect     = 0x01
	socksCmdUDPAssoc    = 0x03
	socksAtypIPv4       = 0x01
	socksAtypDomain     = 0x03
	socksAtypIPv6       = 0x04
	socksRepOK          = 0x00
	socksRepFail        = 0x01
	socksRepUnsupported = 0x07
)

type SocksServer struct {
	Addr     string
	SSH      *ssh.Client
	Logger   *Logger
	DNS      []string
	UDPGW    config.UDPGWConfig
	listener net.Listener
	stopOnce sync.Once
}

type socksRequest struct {
	Command byte
	Host    string
	Port    int
}

func (r socksRequest) Addr() string {
	return net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
}

func (s *SocksServer) Start() error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	s.listener = ln
	s.Logger.Add("info", "local SOCKS5 listening on %s", s.Addr)
	go s.acceptLoop()
	return nil
}

func (s *SocksServer) Stop() {
	s.stopOnce.Do(func() {
		if s.listener != nil {
			_ = s.listener.Close()
		}
	})
}

func (s *SocksServer) acceptLoop() {
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *SocksServer) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	if err := s.handshake(c); err != nil {
		if !isExpectedProbeError(err) {
			s.Logger.Add("debug", "socks handshake failed: %v", err)
		}
		return
	}
	req, err := readSocksRequest(c)
	if err != nil {
		s.Logger.Add("debug", "socks request failed: %v", err)
		return
	}

	switch req.Command {
	case socksCmdConnect:
		s.handleConnect(c, req)
	case socksCmdUDPAssoc:
		if s.UDPGW.Enabled {
			s.handleUDPGWAssociate(c)
		} else {
			s.handleDNSUDPAssociate(c)
		}
	default:
		_ = writeReply(c, socksRepUnsupported)
		s.Logger.Add("debug", "socks unsupported command %d", req.Command)
	}
}

func (s *SocksServer) handleConnect(c net.Conn, req socksRequest) {
	dest := req.Addr()
	if s.SSH == nil {
		_ = writeReply(c, socksRepFail)
		s.Logger.Add("warn", "ssh dial %s failed: ssh session is not available", dest)
		return
	}
	remote, err := dialSSHDirectTCP(s.SSH, req, c.RemoteAddr(), s.Logger)
	if err != nil {
		_ = writeReply(c, 0x05)
		s.Logger.Add("warn", "ssh dial %s failed: %v", dest, err)
		return
	}
	defer remote.Close()
	_ = writeReply(c, socksRepOK)
	_ = c.SetDeadline(time.Time{})
	s.Logger.Add("debug", "socks connected %s", dest)
	proxyCopy(c, remote)
}

func (s *SocksServer) handleDNSUDPAssociate(c net.Conn) {
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		_ = writeReply(c, socksRepFail)
		s.Logger.Add("warn", "socks UDP associate failed: %v", err)
		return
	}
	defer udp.Close()

	if err := writeReplyWithAddr(c, socksRepOK, udp.LocalAddr().(*net.UDPAddr)); err != nil {
		s.Logger.Add("debug", "socks UDP associate reply failed: %v", err)
		return
	}

	_ = c.SetDeadline(time.Time{})
	s.Logger.Add("info", "local SOCKS5 UDP associate listening on %s for DNS over SSH", udp.LocalAddr().String())

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, c)
		close(done)
	}()

	buf := make([]byte, 64*1024)
	for {
		_ = udp.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, clientAddr, err := udp.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-done:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			s.Logger.Add("debug", "socks UDP read failed: %v", err)
			return
		}

		resp, err := s.handleSocksUDPDatagram(buf[:n])
		if err != nil {
			if shouldLogUDPError(err) {
				s.Logger.Add("debug", "%v", err)
			}
			continue
		}
		_, _ = udp.WriteToUDP(resp, clientAddr)
	}
}

func (s *SocksServer) handleSocksUDPDatagram(packet []byte) ([]byte, error) {
	addr, payload, err := parseSocksUDP(packet)
	if err != nil {
		return nil, err
	}
	if addr.Port != 53 {
		return nil, fmt.Errorf("dropping unsupported UDP target %s; only DNS/UDP port 53 is proxied for SSH modes", addr.Addr())
	}
	answer, err := s.resolveDNSOverSSHTCP(addr, payload)
	if err != nil {
		return nil, fmt.Errorf("DNS over SSH failed for %s: %w", addr.Addr(), err)
	}
	return buildSocksUDP(addr, answer)
}

func (s *SocksServer) resolveDNSOverSSHTCP(addr socksRequest, query []byte) ([]byte, error) {
	servers := s.dnsServers(addr)
	var lastErr error
	for _, server := range servers {
		remote, err := dialSSHDirectTCPAddr(s.SSH, server, nil, s.Logger)
		if err != nil {
			lastErr = err
			continue
		}
		_ = remote.SetDeadline(time.Now().Add(8 * time.Second))
		resp, err := exchangeDNSTCP(remote, query)
		_ = remote.Close()
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no DNS server configured")
}

func (s *SocksServer) dnsServers(requested socksRequest) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		v = normalizeHostPort(v, "53")
		if v == "" {
			return
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if requested.Host != "" {
		add(net.JoinHostPort(requested.Host, strconv.Itoa(requested.Port)))
	}
	for _, dns := range s.DNS {
		add(dns)
	}
	add("1.1.1.1:53")
	add("8.8.8.8:53")
	return out
}

func normalizeHostPort(v, defaultPort string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if host, port, err := net.SplitHostPort(v); err == nil {
		return net.JoinHostPort(host, port)
	}
	if ip := net.ParseIP(v); ip != nil {
		return net.JoinHostPort(ip.String(), defaultPort)
	}
	if strings.Count(v, ":") == 1 {
		host, port, ok := strings.Cut(v, ":")
		if ok && host != "" && port != "" {
			return net.JoinHostPort(host, port)
		}
	}
	return net.JoinHostPort(v, defaultPort)
}

func exchangeDNSTCP(conn net.Conn, query []byte) ([]byte, error) {
	if len(query) == 0 || len(query) > 65535 {
		return nil, fmt.Errorf("invalid DNS query length %d", len(query))
	}
	prefix := make([]byte, 2)
	binary.BigEndian.PutUint16(prefix, uint16(len(query)))
	if _, err := conn.Write(append(prefix, query...)); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(conn, prefix); err != nil {
		return nil, err
	}
	ln := int(binary.BigEndian.Uint16(prefix))
	if ln <= 0 || ln > 65535 {
		return nil, fmt.Errorf("invalid DNS response length %d", ln)
	}
	resp := make([]byte, ln)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (s *SocksServer) handshake(c net.Conn) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c, header); err != nil {
		return err
	}
	if header[0] != socksVersion {
		return errors.New("not socks5")
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return err
	}
	_, err := c.Write([]byte{socksVersion, 0x00})
	return err
}

func readSocksRequest(c net.Conn) (socksRequest, error) {
	var req socksRequest
	h := make([]byte, 4)
	if _, err := io.ReadFull(c, h); err != nil {
		return req, err
	}
	if h[0] != socksVersion {
		return req, fmt.Errorf("invalid socks version %d", h[0])
	}
	req.Command = h[1]
	host, err := readSocksHost(c, h[3])
	if err != nil {
		return req, err
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return req, err
	}
	req.Host = host
	req.Port = int(binary.BigEndian.Uint16(pb))
	return req, nil
}

func readSocksHost(r io.Reader, atyp byte) (string, error) {
	switch atyp {
	case socksAtypIPv4:
		b := make([]byte, 4)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	case socksAtypDomain:
		l := []byte{0}
		if _, err := io.ReadFull(r, l); err != nil {
			return "", err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return string(b), nil
	case socksAtypIPv6:
		b := make([]byte, 16)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	default:
		return "", fmt.Errorf("unsupported address type %d", atyp)
	}
}

func parseSocksUDP(packet []byte) (socksRequest, []byte, error) {
	var req socksRequest
	if len(packet) < 4 {
		return req, nil, errors.New("short socks UDP packet")
	}
	if packet[0] != 0 || packet[1] != 0 {
		return req, nil, errors.New("invalid socks UDP reserved field")
	}
	if packet[2] != 0 {
		return req, nil, errors.New("fragmented socks UDP packets are not supported")
	}
	atyp := packet[3]
	off := 4
	switch atyp {
	case socksAtypIPv4:
		if len(packet) < off+4+2 {
			return req, nil, errors.New("short socks UDP ipv4 packet")
		}
		req.Host = net.IP(packet[off : off+4]).String()
		off += 4
	case socksAtypDomain:
		if len(packet) < off+1 {
			return req, nil, errors.New("short socks UDP domain packet")
		}
		ln := int(packet[off])
		off++
		if len(packet) < off+ln+2 {
			return req, nil, errors.New("short socks UDP domain payload")
		}
		req.Host = string(packet[off : off+ln])
		off += ln
	case socksAtypIPv6:
		if len(packet) < off+16+2 {
			return req, nil, errors.New("short socks UDP ipv6 packet")
		}
		req.Host = net.IP(packet[off : off+16]).String()
		off += 16
	default:
		return req, nil, fmt.Errorf("unsupported socks UDP address type %d", atyp)
	}
	req.Port = int(binary.BigEndian.Uint16(packet[off : off+2]))
	off += 2
	return req, packet[off:], nil
}

func buildSocksUDP(addr socksRequest, payload []byte) ([]byte, error) {
	var out []byte
	out = append(out, 0, 0, 0)
	ip := net.ParseIP(addr.Host)
	if v4 := ip.To4(); v4 != nil {
		out = append(out, socksAtypIPv4)
		out = append(out, v4...)
	} else if v6 := ip.To16(); v6 != nil {
		out = append(out, socksAtypIPv6)
		out = append(out, v6...)
	} else {
		if len(addr.Host) > 255 {
			return nil, errors.New("socks UDP domain is too long")
		}
		out = append(out, socksAtypDomain, byte(len(addr.Host)))
		out = append(out, []byte(addr.Host)...)
	}
	pb := make([]byte, 2)
	binary.BigEndian.PutUint16(pb, uint16(addr.Port))
	out = append(out, pb...)
	out = append(out, payload...)
	return out, nil
}

func writeReply(c net.Conn, code byte) error {
	return writeReplyWithAddr(c, code, &net.UDPAddr{IP: net.IPv4zero, Port: 0})
}

func writeReplyWithAddr(c net.Conn, code byte, addr *net.UDPAddr) error {
	if addr == nil {
		addr = &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	}
	ip := addr.IP
	if ip == nil || ip.IsUnspecified() {
		ip = net.IPv4(127, 0, 0, 1)
	}
	var resp []byte
	resp = append(resp, socksVersion, code, 0x00)
	if v4 := ip.To4(); v4 != nil {
		resp = append(resp, socksAtypIPv4)
		resp = append(resp, v4...)
	} else if v6 := ip.To16(); v6 != nil {
		resp = append(resp, socksAtypIPv6)
		resp = append(resp, v6...)
	} else {
		resp = append(resp, socksAtypIPv4, 127, 0, 0, 1)
	}
	pb := make([]byte, 2)
	binary.BigEndian.PutUint16(pb, uint16(addr.Port))
	resp = append(resp, pb...)
	_, err := c.Write(resp)
	return err
}

// proxyCopy pipes bytes in both directions until one side closes, then shuts
// down the other direction and waits for both copy goroutines to finish. This
// avoids leaking a blocked copy goroutine for every proxied connection.
func proxyCopy(a net.Conn, b net.Conn) {
	done := make(chan struct{}, 2)
	copyOneWay := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		// Let the other copy goroutine observe EOF instead of hanging forever.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go copyOneWay(a, b)
	go copyOneWay(b, a)
	<-done
	<-done
}

func isExpectedProbeError(err error) bool {
	return errors.Is(err, io.EOF) || strings.Contains(err.Error(), "connection reset")
}

func shouldLogUDPError(err error) bool {
	msg := err.Error()
	return !strings.Contains(msg, "unsupported UDP target")
}
