package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"socksrevivepc/internal/oscmd"
)

type ManagedProcess struct {
	cmd       *exec.Cmd
	name      string
	logger    *Logger
	mu        sync.Mutex
	exited    chan struct{}
	ready     chan struct{}
	readyOnce sync.Once
	readyLine string

	exitMu     sync.Mutex
	exitErr    error
	exitedFlag atomic.Bool
}

func StartProcess(ctx context.Context, root, name, exe string, args []string, logger *Logger) (*ManagedProcess, error) {
	return startProcess(ctx, root, name, exe, args, logger, "")
}

// StartProcessWithReady behaves like StartProcess but closes Ready() when the
// process prints a line containing readyLine. Used for OpenVPN, which reports a
// successful connection with "Initialization Sequence Completed".
func StartProcessWithReady(ctx context.Context, root, name, exe string, args []string, logger *Logger, readyLine string) (*ManagedProcess, error) {
	return startProcess(ctx, root, name, exe, args, logger, readyLine)
}

func startProcess(ctx context.Context, root, name, exe string, args []string, logger *Logger, readyLine string) (*ManagedProcess, error) {
	if strings.TrimSpace(exe) == "" {
		return nil, errors.New(name + " executable path is empty")
	}
	resolved, err := resolveExecutable(root, exe)
	if err != nil {
		return nil, err
	}
	cmd := oscmd.CommandContext(ctx, resolved, args...)
	cmd.Dir = root
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	p := &ManagedProcess{
		cmd:       cmd,
		name:      name,
		logger:    logger,
		exited:    make(chan struct{}),
		ready:     make(chan struct{}),
		readyLine: readyLine,
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	logger.Add("info", "%s started: %s %s", name, resolved, strings.Join(args, " "))
	go p.pipe(stdout, "info")
	go p.pipe(stderr, "warn")
	go func() {
		err := cmd.Wait()
		p.exitMu.Lock()
		p.exitErr = err
		p.exitMu.Unlock()
		p.exitedFlag.Store(true)
		close(p.exited)
		if err != nil {
			logger.Add("warn", "%s stopped: %v", name, err)
		} else {
			logger.Add("info", "%s stopped", name)
		}
	}()
	return p, nil
}

// resolveXrayExecutable finds the xray binary. Besides the usual resolution
// (app root, then PATH), it retries with the bare name "xray", so installs in
// common locations picked up by setupToolPaths are found even when the
// default tools/xray path does not exist.
func resolveXrayExecutable(root, exe string) (string, error) {
	exe = strings.TrimSpace(exe)
	if exe == "" {
		exe = filepath.Join("tools", "xray", xrayExeName())
	}
	if resolved, err := resolveExecutable(root, exe); err == nil {
		return resolved, nil
	}
	if resolved, err := resolveExecutable(root, xrayExeName()); err == nil {
		return resolved, nil
	}
	return "", fmt.Errorf("xray executable not found (searched %s, the app folder and PATH)", exe)
}

func xrayExeName() string {
	if runtime.GOOS == "windows" {
		return "xray.exe"
	}
	return "xray"
}

// resolveExecutable returns an absolute path to exe. A relative path is first
// tested against the app root, then resolved through the system PATH. This lets
// OpenVPN/Xray either live in tools/ or be installed system-wide.
func resolveExecutable(root, exe string) (string, error) {
	exe = strings.TrimSpace(exe)
	if filepath.IsAbs(exe) {
		if _, err := os.Stat(exe); err != nil {
			return "", fmt.Errorf("%s not found: %w", exe, err)
		}
		return exe, nil
	}
	candidate := filepath.Join(root, exe)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	if p, err := exec.LookPath(exe); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found (checked %s and PATH)", exe, candidate)
}

// Exited reports whether the process has exited and, if so, its exit error.
// Unlike a plain channel read, it can be called repeatedly and always returns
// the same result once the process is gone.
func (p *ManagedProcess) Exited() (bool, error) {
	if p == nil {
		return true, nil
	}
	if !p.exitedFlag.Load() {
		return false, nil
	}
	p.exitMu.Lock()
	err := p.exitErr
	p.exitMu.Unlock()
	return true, err
}

// ExitChan returns a channel that closes when the process exits. Unlike
// Exited(), it does not consume the exit error, so Exited() can still be called
// afterwards.
func (p *ManagedProcess) ExitChan() <-chan struct{} {
	if p == nil || p.exited == nil {
		return nil
	}
	return p.exited
}

// Ready returns a channel closed when the process reports its ready line.
func (p *ManagedProcess) Ready() <-chan struct{} {
	if p == nil || p.ready == nil {
		return nil
	}
	return p.ready
}

func (p *ManagedProcess) pipe(r io.Reader, level string) {
	s := bufio.NewScanner(r)
	// Some helpers (xray, openvpn) occasionally print very long lines; the
	// default 64 KB scanner limit truncates them and logs a confusing error.
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line != "" {
			p.logger.Add(level, "%s: %s", p.name, line)
			if p.readyLine != "" && strings.Contains(line, p.readyLine) {
				p.readyOnce.Do(func() { close(p.ready) })
			}
		}
	}
}

// Stop terminates the managed process. It is safe to call multiple times and
// from multiple goroutines, and it is a no-op when the process already exited.
func (p *ManagedProcess) Stop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		_ = p.cmd.Process.Kill()
		return
	}
	_ = p.cmd.Process.Signal(ioSignalInterrupt())
	// Give the process a moment to shut down gracefully before killing it.
	select {
	case <-p.exited:
	case <-time.After(500 * time.Millisecond):
		_ = p.cmd.Process.Kill()
	}
}
