package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"socksrevivepc/internal/config"
	"socksrevivepc/internal/wintunloader"
)

const openVPNReadyLine = "Initialization Sequence Completed"

// startOpenVPN launches the OpenVPN process for the given profile. It returns
// the managed process plus the path of a temporary auth-user-pass file that
// this function created (empty when the profile uses its own AuthFile). The
// caller must remove that temporary file when the tunnel stops. OpenVPN
// creates and manages its own TUN adapter and routes, so the app does not
// start a local SOCKS proxy in this mode.
func startOpenVPN(ctx context.Context, root string, p config.Profile, logger *Logger) (*ManagedProcess, string, error) {
	cfg := p.OpenVPN
	exe, err := resolveOpenVPNExecutable(root, cfg.Executable)
	if err != nil {
		return nil, "", err
	}
	args := []string{"--config", cfg.ConfigPath}

	authFile := strings.TrimSpace(cfg.AuthFile)
	tempAuthFile := ""
	if authFile == "" && strings.TrimSpace(cfg.Username) != "" {
		tmp := filepath.Join(root, "logs", fmt.Sprintf("openvpn-auth-%s.txt", p.ID))
		content := cfg.Username + "\n" + cfg.Password + "\n"
		if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
			return nil, "", err
		}
		tempAuthFile = tmp
		authFile = tmp
	}
	if authFile != "" {
		args = append(args, "--auth-user-pass", authFile)
	}

	verb := cfg.Verbosity
	if verb <= 0 {
		verb = 3
	}
	args = append(args, "--verb", fmt.Sprint(verb))

	// The TAP adapter creation runs through the OpenVPN interactive service
	// ("could not talk to service" when it is stopped). The app runs elevated,
	// so make sure the service is up before starting OpenVPN.
	if err := ensureOpenVPNInteractiveService(); err != nil && logger != nil {
		logger.Add("warn", "openvpn interactive service: %v", err)
	}
	// With --windows-driver wintun, OpenVPN loads wintun.dll from its own
	// directory (or the system search path) and reuses the "Wintun" adapter,
	// so no system driver installation is needed: prepare the embedded Wintun
	// DLL, create the adapter when missing, and place the DLL next to
	// openvpn.exe.
	if runtime.GOOS == "windows" {
		if err := wintunloader.Prepare(logger); err != nil && logger != nil {
			logger.Add("warn", "wintun prepare: %v", err)
		}
		if err := ensureWintunAdapterForOpenVPN(); err != nil && logger != nil {
			logger.Add("warn", "wintun adapter for openvpn: %v", err)
		}
		sources := []string{
			filepath.Join(root, "tools", "wintun", "amd64", "wintun.dll"),
		}
		if appExe, err := os.Executable(); err == nil {
			sources = append(sources, filepath.Join(filepath.Dir(appExe), "wintun.dll"))
		}
		for _, src := range sources {
			data, err := os.ReadFile(src)
			if err != nil {
				continue
			}
			dst := filepath.Join(filepath.Dir(exe), "wintun.dll")
			if _, statErr := os.Stat(dst); statErr == nil {
				break
			}
			if err := os.WriteFile(dst, data, 0o755); err == nil && logger != nil {
				logger.Add("info", "wintun.dll placed next to openvpn.exe for the Wintun driver")
			}
			break
		}
	}

	// First attempt: the Wintun driver (no system driver install needed).
	wintunArgs := append(append([]string{}, args...), "--windows-driver", "wintun")
	proc, err := StartProcessWithReady(ctx, root, "openvpn", exe, wintunArgs, logger, openVPNReadyLine)
	if err != nil {
		if tempAuthFile != "" {
			_ = os.Remove(tempAuthFile)
		}
		return nil, "", err
	}
	if runtime.GOOS == "windows" {
		// Give the Wintun attempt a short window. When the process dies before
		// connecting (e.g. VMs where the Wintun device cannot be opened),
		// retry with the default driver selection (ovpn-dco with tap-windows6
		// fallback), installing the bundled driver package when missing.
		select {
		case <-proc.Ready():
			return proc, tempAuthFile, nil
		case <-proc.ExitChan():
			if exited, procErr := proc.Exited(); exited {
				if logger != nil {
					logger.Add("warn", "openvpn with wintun failed (%v); retrying with the default driver...", procErr)
				}
				if driverErr := ensureOpenVPNAdapter(root, logger); driverErr != nil && logger != nil {
					logger.Add("warn", "openvpn adapter driver: %v", driverErr)
				}
				proc, err = StartProcessWithReady(ctx, root, "openvpn", exe, args, logger, openVPNReadyLine)
				if err != nil {
					if tempAuthFile != "" {
						_ = os.Remove(tempAuthFile)
					}
					return nil, "", err
				}
				return proc, tempAuthFile, nil
			}
			if logger != nil {
				logger.Add("warn", "openvpn (wintun) stopped before connecting")
			}
		case <-time.After(10 * time.Second):
			// Still connecting with Wintun; the manager keeps waiting for the
			// ready line.
			return proc, tempAuthFile, nil
		case <-ctx.Done():
			return proc, tempAuthFile, nil
		}
	}
	return proc, tempAuthFile, nil
}

// ensureOpenVPNInteractiveService starts the OpenVPN interactive service
// (OpenVPNServiceInteractive) when it exists. OpenVPN uses it to create the
// TAP adapter; when the service is stopped, the tunnel dies with
// "create_adapter: could not talk to service". Error 1056 means the service
// is already running, which is not a failure.
func ensureOpenVPNInteractiveService() error {
	if runtime.GOOS != "windows" {
		return nil
	}
	out, err := exec.Command("sc.exe", "start", "OpenVPNServiceInteractive").CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if strings.Contains(text, "1056") || strings.Contains(text, "already running") {
			return nil
		}
		return fmt.Errorf("sc start failed: %v (%s)", err, text)
	}
	return nil
}

// ensureOpenVPNAdapter makes sure the machine has a virtual adapter driver
// OpenVPN can use (ovpn-dco preferred, tap-windows6 as fallback). Used when
// the Wintun attempt fails. The bundled driver packages live in
// tools/openvpn/driver; pnputil installs them and tapctl (bundled with
// OpenVPN) creates the adapter.
func ensureOpenVPNAdapter(root string, logger *Logger) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	tapctl := filepath.Join(root, "tools", "openvpn", "tapctl.exe")
	if _, err := os.Stat(tapctl); err != nil {
		return nil // non-bundled setup - leave it to the system installation
	}
	if out, err := exec.Command(tapctl, "list").CombinedOutput(); err == nil {
		text := string(out)
		if strings.Contains(text, "ovpn-dco") || strings.Contains(text, "tap0901") {
			return nil // a usable adapter driver is already present
		}
	}
	dcoInf := filepath.Join(root, "tools", "openvpn", "driver", "ovpn-dco", "ovpndco.inf")
	if _, err := os.Stat(dcoInf); err == nil {
		if logger != nil {
			logger.Add("info", "openvpn: installing the bundled ovpn-dco driver...")
		}
		// pnputil reports an error when the package already exists; that is
		// not a failure - the driver store already has it.
		_, _ = exec.Command("pnputil.exe", "/add-driver", dcoInf, "/install").CombinedOutput()
		if out, err := exec.Command(tapctl, "create", "--hwid", "ovpn-dco", "--name", "SAKR TUN DCO").CombinedOutput(); err == nil {
			if logger != nil {
				logger.Add("info", "openvpn: ovpn-dco adapter ready")
			}
			return nil
		} else if strings.Contains(strings.ToLower(string(out)), "exists") {
			return nil
		}
	}
	tapInf := filepath.Join(root, "tools", "openvpn", "driver", "tap0901", "OemVista.inf")
	if _, err := os.Stat(tapInf); err != nil {
		return fmt.Errorf("no bundled OpenVPN driver package found")
	}
	if logger != nil {
		logger.Add("info", "openvpn: installing the bundled TAP-Windows6 driver...")
	}
	_, _ = exec.Command("pnputil.exe", "/add-driver", tapInf, "/install").CombinedOutput()
	if out, err := exec.Command(tapctl, "create", "--hwid", "tap0901", "--name", "OpenVPN TAP-Windows6").CombinedOutput(); err != nil {
		text := strings.TrimSpace(string(out))
		if !strings.Contains(strings.ToLower(text), "exists") {
			return fmt.Errorf("tapctl create: %v (%s)", err, text)
		}
	}
	if logger != nil {
		logger.Add("info", "openvpn: TAP-Windows6 adapter ready")
	}
	return nil
}

// resolveOpenVPNExecutable finds the openvpn binary. It prefers the copy
// bundled in tools/openvpn so the app is self-contained, then falls back to
// the usual resolution (app root, then PATH), and finally checks the standard
// Windows install locations, because the official installer does not always
// add itself to PATH.
func resolveOpenVPNExecutable(root, exe string) (string, error) {
	exe = strings.TrimSpace(exe)
	if exe == "" {
		// Bare name lets resolveExecutable fall back to PATH.
		exe = "openvpn"
	}
	if strings.EqualFold(exe, "openvpn") || strings.EqualFold(exe, "openvpn.exe") {
		if runtime.GOOS == "windows" {
			candidate := filepath.Join(root, "tools", "openvpn", "openvpn.exe")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		} else {
			candidate := filepath.Join(root, "tools", "openvpn", "openvpn")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
	}
	if resolved, err := resolveExecutable(root, exe); err == nil {
		return resolved, nil
	}
	if runtime.GOOS == "windows" && (strings.EqualFold(exe, "openvpn") || strings.EqualFold(exe, "openvpn.exe")) {
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
			if strings.TrimSpace(base) == "" {
				continue
			}
			candidate := filepath.Join(base, "OpenVPN", "bin", "openvpn.exe")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("openvpn executable not found (searched tools/openvpn, the app folder, PATH and the standard Windows install locations)")
}
