package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"socksrevivepc/internal/config"
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

	proc, err := StartProcessWithReady(ctx, root, "openvpn", exe, args, logger, openVPNReadyLine)
	if err != nil {
		// Only remove the file this function created; never delete a user
		// configured --auth-user-pass file on failure.
		if tempAuthFile != "" {
			_ = os.Remove(tempAuthFile)
		}
		return nil, "", err
	}
	return proc, tempAuthFile, nil
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
