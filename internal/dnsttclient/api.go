package dnsttclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
	"socksrevivepc/internal/dnsttcore/dns"
	"socksrevivepc/internal/dnsttcore/noise"
	"socksrevivepc/internal/dnsttcore/turbotunnel"
)

// Options configures the embedded DNSTT client.
type Options struct {
	ResolverType     string
	ResolverAddress  string
	PublicKeyHex     string
	Domain           string
	LocalAddress     string
	UTLSDistribution string
	StartupTimeout   time.Duration
	LogWriter        io.Writer
}

// Client is a running embedded DNSTT client instance.
type Client struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Stop shuts down the listener and DNS transport.
func (c *Client) Stop() {
	if c == nil {
		return
	}
	c.cancel()
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
	}
}

// Start starts an embedded DNSTT client and waits until the local TCP listener
// is open. It does not require dnstt-client.exe.
func Start(parent context.Context, opts Options) (*Client, error) {
	if opts.LogWriter != nil {
		log.SetOutput(opts.LogWriter)
		log.SetFlags(log.LstdFlags | log.LUTC)
	}
	if opts.StartupTimeout <= 0 {
		opts.StartupTimeout = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(parent)
	client := &Client{cancel: cancel, done: make(chan struct{})}
	ready := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		defer close(client.done)
		errCh <- runOptions(ctx, opts, ready)
	}()

	select {
	case <-ready:
		return client, nil
	case err := <-errCh:
		cancel()
		if err == nil {
			err = fmt.Errorf("embedded dnstt stopped during startup")
		}
		return nil, err
	case <-time.After(opts.StartupTimeout):
		// Keep the client alive. The local listener usually opens first, but on
		// slow networks the Noise/smux session can need a little more time.
		return client, nil
	case <-parent.Done():
		cancel()
		return nil, parent.Err()
	}
}

func runOptions(ctx context.Context, opts Options, ready chan<- struct{}) error {
	resolverType := strings.ToLower(strings.TrimSpace(opts.ResolverType))
	resolverAddress := strings.TrimSpace(opts.ResolverAddress)
	if resolverType == "" {
		resolverType = "doh"
	}
	if resolverAddress == "" {
		return fmt.Errorf("dnstt resolver is required")
	}
	if opts.PublicKeyHex == "" {
		return fmt.Errorf("dnstt public key is required")
	}
	if opts.Domain == "" {
		return fmt.Errorf("dnstt domain is required")
	}
	if opts.LocalAddress == "" {
		opts.LocalAddress = "127.0.0.1:2222"
	}
	if opts.UTLSDistribution == "" {
		opts.UTLSDistribution = "4*random,3*Firefox_120,1*Firefox_105,3*Chrome_120,1*Chrome_102,1*iOS_14,1*iOS_13"
	}

	domain, err := dns.ParseName(opts.Domain)
	if err != nil {
		return fmt.Errorf("invalid dnstt domain: %w", err)
	}
	localAddr, err := net.ResolveTCPAddr("tcp", opts.LocalAddress)
	if err != nil {
		return fmt.Errorf("invalid dnstt local address: %w", err)
	}
	pubkey, err := noise.DecodeKey(opts.PublicKeyHex)
	if err != nil {
		return fmt.Errorf("dnstt public key format error: %w", err)
	}

	tlsConfig, err := loadTLSConfig()
	if err != nil {
		return fmt.Errorf("dnstt TLS config error: %w", err)
	}
	utlsClientHelloID, err := sampleUTLSDistribution(opts.UTLSDistribution)
	if err != nil {
		return fmt.Errorf("dnstt uTLS profile error: %w", err)
	}
	if utlsClientHelloID != nil {
		log.Printf("dnstt uTLS fingerprint %s %s", utlsClientHelloID.Client, utlsClientHelloID.Version)
	}

	remoteAddr, pconn, err := makePacketConn(resolverType, resolverAddress, tlsConfig, utlsClientHelloID)
	if err != nil {
		return err
	}
	pconn = NewDNSPacketConn(pconn, remoteAddr, domain)
	return runContext(ctx, pubkey, domain, localAddr, remoteAddr, pconn, ready)
}

func makePacketConn(resolverType, resolverAddress string, tlsConfig *tls.Config, utlsClientHelloID *utls.ClientHelloID) (net.Addr, net.PacketConn, error) {
	switch resolverType {
	case "doh", "https":
		addr := turbotunnel.DummyAddr{}
		var rt http.RoundTripper
		if utlsClientHelloID == nil {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			transport.TLSClientConfig = tlsConfig.Clone()
			baseDialContext := transport.DialContext
			if baseDialContext == nil {
				baseDialContext = (&net.Dialer{}).DialContext
			}
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if network == "tcp" {
					resolvedAddr, _, err := resolveAddrIPv4(ctx, addr)
					if err != nil {
						return nil, err
					}
					addr = resolvedAddr
					network = "tcp4"
				}
				return baseDialContext(ctx, network, addr)
			}
			rt = transport
		} else {
			utlsConfig := &utls.Config{RootCAs: tlsConfig.RootCAs, MinVersion: tlsConfig.MinVersion}
			rt = NewUTLSRoundTripper(utlsConfig, utlsClientHelloID, true)
		}
		pconn, err := NewHTTPPacketConn(rt, resolverAddress, 32)
		return addr, pconn, err
	case "dot", "tls":
		addr := turbotunnel.DummyAddr{}
		var dialTLSContext func(ctx context.Context, network, addr string) (net.Conn, error)
		if utlsClientHelloID == nil {
			dialTLSContext = (&tls.Dialer{Config: tlsConfig.Clone()}).DialContext
		} else {
			utlsConfig := &utls.Config{RootCAs: tlsConfig.RootCAs, MinVersion: tlsConfig.MinVersion}
			dialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return utlsDialContext(ctx, network, addr, utlsConfig, utlsClientHelloID)
			}
		}
		pconn, err := NewTLSPacketConn(resolverAddress, dialTLSContext)
		return addr, pconn, err
	case "udp", "dns":
		addr, err := net.ResolveUDPAddr("udp", resolverAddress)
		if err != nil {
			return nil, nil, err
		}
		pconn, err := net.ListenUDP("udp", nil)
		return addr, pconn, err
	default:
		return nil, nil, fmt.Errorf("unknown dnstt resolver type %q", resolverType)
	}
}

func runContext(ctx context.Context, pubkey []byte, domain dns.Name, localAddr *net.TCPAddr, remoteAddr net.Addr, pconn net.PacketConn, ready chan<- struct{}) error {
	defer pconn.Close()

	ln, err := net.ListenTCP("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("opening dnstt local listener: %w", err)
	}
	defer ln.Close()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		_ = pconn.Close()
	}()
	close(ready)
	log.Printf("dnstt local listener ready at %s", ln.Addr())

	mtu := dnsNameCapacity(domain) - 8 - 1 - numPadding - 1
	if mtu < 80 {
		return fmt.Errorf("domain %s leaves only %d bytes for payload", domain, mtu)
	}
	log.Printf("dnstt effective MTU %d", mtu)

	conn, err := kcp.NewConn2(remoteAddr, nil, 0, 0, pconn)
	if err != nil {
		return fmt.Errorf("opening dnstt KCP connection: %w", err)
	}
	defer func() {
		log.Printf("end dnstt session %08x", conn.GetConv())
		conn.Close()
	}()
	log.Printf("begin dnstt session %08x", conn.GetConv())
	conn.SetStreamMode(true)
	conn.SetNoDelay(0, 0, 0, 1)
	conn.SetWindowSize(turbotunnel.QueueSize/2, turbotunnel.QueueSize/2)
	if rc := conn.SetMtu(mtu); !rc {
		return fmt.Errorf("setting dnstt MTU failed")
	}

	rw, err := noise.NewClient(conn, pubkey)
	if err != nil {
		return err
	}

	smuxConfig := smux.DefaultConfig()
	smuxConfig.Version = 2
	smuxConfig.KeepAliveTimeout = idleTimeout
	smuxConfig.MaxStreamBuffer = 1 * 1024 * 1024
	sess, err := smux.Client(rw, smuxConfig)
	if err != nil {
		return fmt.Errorf("opening dnstt smux session: %w", err)
	}
	defer sess.Close()

	for {
		local, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			if err, ok := err.(net.Error); ok && err.Temporary() {
				continue
			}
			return err
		}
		go func() {
			defer local.Close()
			err := handle(local.(*net.TCPConn), sess, conn.GetConv())
			if err != nil {
				log.Printf("dnstt handle: %v", err)
			}
		}()
	}
}
