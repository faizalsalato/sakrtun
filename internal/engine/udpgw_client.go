package engine

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	udpgwMaxFrame = 64 * 1024

	udpgwProtocolBadVPN = "badvpn"
	udpgwProtocolLegacy = "legacy"

	udpgwFlagKeepAlive = 1 << 0
	udpgwFlagRebind    = 1 << 1
	udpgwFlagDNS       = 1 << 2
	udpgwFlagIPv6      = 1 << 3
)

type udpgwSession struct {
	mu       sync.Mutex
	nextID   uint16
	pairID   map[string]uint16
	idClient map[uint16]*net.UDPAddr
}

func (s *SocksServer) handleUDPGWAssociate(c net.Conn) {
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		_ = writeReply(c, socksRepFail)
		s.Logger.Add("warn", "socks UDP associate failed: %v", err)
		return
	}
	defer udp.Close()

	gwAddr := net.JoinHostPort(s.UDPGW.Host, strconv.Itoa(s.UDPGW.Port))
	if s.SSH == nil {
		_ = writeReply(c, socksRepFail)
		s.Logger.Add("warn", "udpgw dial through SSH failed %s: ssh session is not available", gwAddr)
		return
	}
	gwConn, err := dialSSHDirectTCPAddr(s.SSH, gwAddr, c.RemoteAddr(), s.Logger)
	if err != nil {
		_ = writeReply(c, socksRepFail)
		s.Logger.Add("warn", "udpgw dial through SSH failed %s: %v", gwAddr, err)
		return
	}
	defer gwConn.Close()

	if err := writeReplyWithAddr(c, socksRepOK, udp.LocalAddr().(*net.UDPAddr)); err != nil {
		s.Logger.Add("debug", "socks UDP associate reply failed: %v", err)
		return
	}

	_ = c.SetDeadline(time.Time{})
	proto := normalizeUDPGWProtocol(s.UDPGW.Protocol)
	s.Logger.Add("info", "SOCKS5 UDP associate using UDPGW %s over SSH; protocol=%s local UDP=%s", gwAddr, proto, udp.LocalAddr().String())

	done := make(chan struct{})
	closeOnce := sync.Once{}
	stop := func() {
		closeOnce.Do(func() {
			_ = c.Close()
			_ = gwConn.Close()
			_ = udp.Close()
			close(done)
		})
	}

	go func() {
		_, _ = io.Copy(io.Discard, c)
		stop()
	}()

	sess := &udpgwSession{
		nextID:   1,
		pairID:   make(map[string]uint16),
		idClient: make(map[uint16]*net.UDPAddr),
	}

	go s.readUDPGWReplies(done, proto, gwConn, udp, sess)
	s.forwardUDPToUDPGW(done, proto, gwConn, udp, sess)
	stop()
}

func normalizeUDPGWProtocol(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case udpgwProtocolLegacy:
		return udpgwProtocolLegacy
	default:
		return udpgwProtocolBadVPN
	}
}

func (s *SocksServer) forwardUDPToUDPGW(done <-chan struct{}, proto string, gwConn net.Conn, udp *net.UDPConn, sess *udpgwSession) {
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
			s.Logger.Add("debug", "udpgw local UDP read failed: %v", err)
			return
		}

		addr, payload, err := parseSocksUDP(buf[:n])
		if err != nil {
			if shouldLogUDPError(err) {
				s.Logger.Add("debug", "udpgw parse SOCKS UDP failed: %v", err)
			}
			continue
		}

		connID, isNew, err := sess.idFor(clientAddr, addr)
		if err != nil {
			s.Logger.Add("debug", "udpgw session id failed for %s: %v", addr.Addr(), err)
			continue
		}

		frame, err := udpgwBuildClientFrame(proto, connID, isNew, addr, payload)
		if err != nil {
			if shouldLogUDPError(err) {
				s.Logger.Add("debug", "udpgw frame build failed for %s: %v", addr.Addr(), err)
			}
			continue
		}
		_ = gwConn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err := gwConn.Write(frame); err != nil {
			s.Logger.Add("warn", "udpgw TCP write failed: %v", err)
			return
		}
	}
}

func (s *SocksServer) readUDPGWReplies(done <-chan struct{}, proto string, gwConn net.Conn, udp *net.UDPConn, sess *udpgwSession) {
	br := bufio.NewReaderSize(gwConn, 256*1024)
	for {
		select {
		case <-done:
			return
		default:
		}
		payload, err := udpgwReadFrame(br, udpgwMaxFrame)
		if err != nil {
			select {
			case <-done:
				return
			default:
			}
			if !errors.Is(err, io.EOF) {
				s.Logger.Add("debug", "udpgw TCP read failed: %v", err)
			}
			return
		}
		connID, src, data, err := udpgwParseReplyPayload(proto, payload)
		if err != nil {
			s.Logger.Add("debug", "udpgw bad reply frame: %v", err)
			continue
		}
		clientAddr := sess.clientFor(connID)
		if clientAddr == nil {
			continue
		}
		packet, err := buildSocksUDP(src, data)
		if err != nil {
			s.Logger.Add("debug", "udpgw SOCKS UDP reply build failed: %v", err)
			continue
		}
		_, _ = udp.WriteToUDP(packet, clientAddr)
	}
}

func (s *udpgwSession) idFor(client *net.UDPAddr, dest socksRequest) (uint16, bool, error) {
	if client == nil {
		return 0, false, errors.New("missing local UDP client address")
	}
	key := client.String() + "|" + dest.Addr()
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.pairID[key]; ok {
		s.idClient[id] = cloneUDPAddr(client)
		return id, false, nil
	}
	id := s.nextID
	if id == 0 {
		id = 1
	}
	s.nextID = id + 1
	if s.nextID == 0 {
		s.nextID = 1
	}
	s.pairID[key] = id
	s.idClient[id] = cloneUDPAddr(client)
	return id, true, nil
}

func (s *udpgwSession) clientFor(id uint16) *net.UDPAddr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneUDPAddr(s.idClient[id])
}

func cloneUDPAddr(a *net.UDPAddr) *net.UDPAddr {
	if a == nil {
		return nil
	}
	out := *a
	if a.IP != nil {
		out.IP = append(net.IP(nil), a.IP...)
	}
	return &out
}

func udpgwReadFrame(r *bufio.Reader, max int) ([]byte, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint16(lenBuf[:]))
	if n <= 0 || n > max {
		return nil, fmt.Errorf("invalid udpgw frame length %d", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

func udpgwBuildClientFrame(proto string, connID uint16, isNew bool, dest socksRequest, data []byte) ([]byte, error) {
	if normalizeUDPGWProtocol(proto) == udpgwProtocolLegacy {
		return udpgwBuildLegacyFrame(connID, 0, dest, data)
	}
	return udpgwBuildBadVPNFrame(connID, isNew, dest, data)
}

func udpgwParseReplyPayload(proto string, payload []byte) (uint16, socksRequest, []byte, error) {
	if normalizeUDPGWProtocol(proto) == udpgwProtocolLegacy {
		return udpgwParseLegacyReplyPayload(payload)
	}
	return udpgwParseBadVPNReplyPayload(payload)
}

// udpgwBuildBadVPNFrame implements the normal badvpn-udpgw PacketProto frame:
// length(little-endian uint16) + flags(1) + conid(little-endian uint16) +
// IPv4/IPv6 destination + destination port(network byte order) + UDP payload.
// This is the same framing used by the Android badvpn UDPGW client and supports
// IPv6 targets with UDPGW_CLIENT_FLAG_IPV6.
func udpgwBuildBadVPNFrame(connID uint16, isNew bool, dest socksRequest, data []byte) ([]byte, error) {
	ip := net.ParseIP(dest.Host)
	if ip == nil {
		return nil, fmt.Errorf("badvpn UDPGW requires an IP target, got %q", dest.Host)
	}
	flags := byte(0)
	if isNew {
		flags |= udpgwFlagRebind
	}

	var addr []byte
	if v4 := ip.To4(); v4 != nil {
		addr = append(addr, v4...)
	} else if v6 := ip.To16(); v6 != nil {
		flags |= udpgwFlagIPv6
		addr = append(addr, v6...)
	} else {
		return nil, fmt.Errorf("invalid UDPGW target IP %q", dest.Host)
	}

	payloadLen := 1 + 2 + len(addr) + 2 + len(data)
	if payloadLen <= 0 || payloadLen > 65535 {
		return nil, fmt.Errorf("UDPGW payload too large: %d", payloadLen)
	}
	out := make([]byte, 2+payloadLen)
	binary.LittleEndian.PutUint16(out[0:2], uint16(payloadLen))
	out[2] = flags
	binary.LittleEndian.PutUint16(out[3:5], connID)
	copy(out[5:5+len(addr)], addr)
	portOff := 5 + len(addr)
	binary.BigEndian.PutUint16(out[portOff:portOff+2], uint16(dest.Port))
	copy(out[portOff+2:], data)
	return out, nil
}

func udpgwParseBadVPNReplyPayload(payload []byte) (uint16, socksRequest, []byte, error) {
	var src socksRequest
	if len(payload) < 1+2+4+2 {
		return 0, src, nil, fmt.Errorf("short badvpn udpgw payload %d", len(payload))
	}
	flags := payload[0]
	if flags&udpgwFlagKeepAlive != 0 {
		return 0, src, nil, errors.New("unexpected udpgw keepalive reply")
	}
	connID := binary.LittleEndian.Uint16(payload[1:3])
	off := 3
	if flags&udpgwFlagIPv6 != 0 {
		if len(payload) < off+16+2 {
			return 0, src, nil, fmt.Errorf("short badvpn udpgw ipv6 payload %d", len(payload))
		}
		src.Host = net.IP(payload[off : off+16]).String()
		off += 16
	} else {
		if len(payload) < off+4+2 {
			return 0, src, nil, fmt.Errorf("short badvpn udpgw ipv4 payload %d", len(payload))
		}
		src.Host = net.IP(payload[off : off+4]).String()
		off += 4
	}
	src.Port = int(binary.BigEndian.Uint16(payload[off : off+2]))
	off += 2
	return connID, src, payload[off:], nil
}

// Legacy frame kept only for older experimental PC builds. It is IPv4-only.
func udpgwBuildLegacyFrame(connID uint16, x byte, dest socksRequest, data []byte) ([]byte, error) {
	ip := net.ParseIP(dest.Host)
	v4 := ip.To4()
	if v4 == nil {
		return nil, fmt.Errorf("legacy UDPGW only supports IPv4 UDP targets; got %s", dest.Addr())
	}
	payloadLen := 2 + 1 + 4 + 2 + len(data)
	if payloadLen <= 0 || payloadLen > 65535 {
		return nil, fmt.Errorf("UDPGW payload too large: %d", payloadLen)
	}
	out := make([]byte, 2+payloadLen)
	binary.LittleEndian.PutUint16(out[0:2], uint16(payloadLen))
	binary.BigEndian.PutUint16(out[2:4], connID)
	out[4] = x
	copy(out[5:9], v4)
	binary.BigEndian.PutUint16(out[9:11], uint16(dest.Port))
	copy(out[11:], data)
	return out, nil
}

func udpgwParseLegacyReplyPayload(payload []byte) (uint16, socksRequest, []byte, error) {
	var src socksRequest
	if len(payload) < 2+1+4+2 {
		return 0, src, nil, fmt.Errorf("short legacy udpgw payload %d", len(payload))
	}
	connID := binary.BigEndian.Uint16(payload[0:2])
	src.Host = net.IP(payload[3:7]).String()
	src.Port = int(binary.BigEndian.Uint16(payload[7:9]))
	return connID, src, payload[9:], nil
}
