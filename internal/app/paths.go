package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// setupToolPaths prepends the directories of the known helper tools to the
// process PATH. The official installers (OpenVPN, Xray) do not always
// register themselves in PATH, so this makes bare executable names such as
// "openvpn" or "xray" resolvable no matter how they were installed. It only
// affects this process; nothing is written to the system registry.
func setupToolPaths(root string) {
	var dirs []string

	// The app's own tools folders always come first.
	for _, t := range []string{filepath.Join("tools", "xray"), filepath.Join("tools", "dnstt"), filepath.Join("tools", "openvpn")} {
		if dir := filepath.Join(root, t); isDir(dir) {
			dirs = append(dirs, dir)
		}
	}

	if runtime.GOOS == "windows" {
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
			if strings.TrimSpace(base) == "" {
				continue
			}
			for _, tool := range []string{
				filepath.Join(base, "OpenVPN", "bin"),
				filepath.Join(base, "Xray"),
			} {
				if isDir(tool) {
					dirs = append(dirs, tool)
				}
			}
		}
	}

	if len(dirs) == 0 {
		return
	}
	current := os.Getenv("PATH")
	os.Setenv("PATH", strings.Join(append(dirs, current), string(os.PathListSeparator)))
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
