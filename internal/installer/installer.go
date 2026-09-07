// Package installer installs SAKR TUN into the Windows Program Files folder
// and creates Start Menu and Desktop shortcuts.
package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"socksrevivepc/internal/oscmd"
)

const installDirName = "SAKR TUN"

// InstallToProgramFiles copies the running app folder into
// %ProgramFiles%\SAKR TUN and creates Start Menu and Desktop shortcuts that
// point to the installed executable. Running from an already installed folder
// re-copies everything, so this also acts as a simple "update install".
// Returns the install directory.
func InstallToProgramFiles(root string) (string, error) {
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("install to Program Files is only supported on Windows")
	}
	base := os.Getenv("ProgramFiles")
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("ProgramFiles environment variable is not set")
	}
	dest := filepath.Join(base, installDirName)
	if err := copyTree(root, dest); err != nil {
		return "", err
	}
	exe := filepath.Join(dest, "SAKRTUN.exe")
	if _, err := os.Stat(exe); err != nil {
		return "", fmt.Errorf("installed executable not found after copy: %w", err)
	}
	programs := filepath.Join(os.Getenv("ProgramData"), "Microsoft", "Windows", "Start Menu", "Programs")
	if err := createShortcut(filepath.Join(programs, "SAKR TUN.lnk"), exe, dest); err != nil {
		return "", err
	}
	// The desktop shortcut is best-effort (desktop paths vary).
	if desktop := os.Getenv("USERPROFILE"); desktop != "" {
		_ = createShortcut(filepath.Join(desktop, "Desktop", "SAKR TUN.lnk"), exe, dest)
	}
	return dest, nil
}

// copyTree copies the whole app folder, skipping logs.
func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if name == "logs" {
			continue
		}
		s, d := filepath.Join(src, name), filepath.Join(dst, name)
		if e.IsDir() {
			if err := copyTree(s, d); err != nil {
				return err
			}
			continue
		}
		in, err := os.Open(s)
		if err != nil {
			return err
		}
		out, err := os.Create(d)
		if err != nil {
			in.Close()
			return err
		}
		if _, err := out.ReadFrom(in); err != nil {
			in.Close()
			out.Close()
			return err
		}
		in.Close()
		if err := out.Close(); err != nil {
			return err
		}
	}
	return nil
}

// createShortcut creates a .lnk file via the Windows Script Host COM object.
func createShortcut(linkPath, target, workDir string) error {
	ps := fmt.Sprintf(
		`$w = New-Object -ComObject WScript.Shell; $s = $w.CreateShortcut('%s'); $s.TargetPath = '%s'; $s.WorkingDirectory = '%s'; $s.Description = 'SAKR TUN'; $s.Save()`,
		strings.ReplaceAll(linkPath, "'", "''"),
		strings.ReplaceAll(target, "'", "''"),
		strings.ReplaceAll(workDir, "'", "''"),
	)
	out, err := oscmd.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create shortcut failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
