package tun

import (
	"fmt"
	"net"
	"runtime"
	"sync"
	"time"

	"github.com/xjasonlyu/tun2socks/v2/engine"
	"socksrevivepc/internal/config"
	"socksrevivepc/internal/platformtun"
)

type Logger interface {
	Add(level, format string, args ...any)
}

type Runner struct {
	mu      sync.Mutex
	active  bool
	logger  Logger
	cleanup func()
}

func NewRunner(logger Logger) *Runner { return &Runner{logger: logger} }

func (r *Runner) Start(p *config.Profile, socksAddr string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p == nil || !p.Tun.Enabled {
		return nil
	}
	if r.active {
		return fmt.Errorf("tun is already active")
	}
	if err := waitForProxyPort(socksAddr, 2500*time.Millisecond); err != nil {
		return fmt.Errorf("cannot start TUN because upstream SOCKS is not reachable at %s: %w", socksAddr, err)
	}
	device, iface, cleanup, err := platformtun.Prepare(p.Tun.Device, p.Tun.InterfaceName, p.Tun.MTU, r.logger)
	if err != nil {
		return err
	}
	p.Tun.Device = device
	p.Tun.InterfaceName = iface
	r.cleanup = cleanup
	key := &engine.Key{
		MTU:      p.Tun.MTU,
		Device:   p.Tun.Device,
		Proxy:    "socks5://" + socksAddr,
		LogLevel: "error",
	}
	if p.Tun.InterfaceName != "" && runtime.GOOS != "windows" {
		key.Interface = p.Tun.InterfaceName
	}
	if runtime.GOOS == "windows" && r.logger != nil {
		r.logger.Add("info", "Windows TUN: leaving tun2socks interface binding empty; the TUN adapter name is %s", p.Tun.InterfaceName)
	}
	engine.Insert(key)
	// tun2socks/v2 engine.Start() initializes the default netstack and then
	// returns; it does not block for the lifetime of the tunnel. The stack stays
	// alive globally until engine.Stop() is called. Older builds treated this
	// normal return as "engine exited immediately", which made TUN mode fail even
	// after the stack was correctly created.
	engine.Start()
	r.active = true
	if r.logger != nil {
		r.logger.Add("info", "tun2socks started: device=%s interface=%s proxy=socks5://%s", p.Tun.Device, p.Tun.InterfaceName, socksAddr)
	}
	return nil
}

func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return
	}
	engine.Stop()
	if r.cleanup != nil {
		r.cleanup()
		r.cleanup = nil
	}
	r.active = false
	if r.logger != nil {
		r.logger.Add("info", "tun2socks stopped")
	}
}

func waitForProxyPort(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 350*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		lastErr = err
		time.Sleep(150 * time.Millisecond)
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}
