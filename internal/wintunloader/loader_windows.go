//go:build windows

package wintunloader

import (
	"embed"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

//go:embed assets/wintun/windows/amd64/* assets/wintun/windows/arm64/*
var embeddedWintun embed.FS

type Logger interface {
	Add(level, format string, args ...any)
}

// Prepare makes Wintun available before tun2socks/WireGuard tries to load it.
// Windows cannot create a real TUN adapter without the signed wintun.dll layer,
// so this function validates the DLL architecture, extracts embedded assets when
// present, and updates the current process DLL search path.
func Prepare(logger Logger) error {
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("Wintun embedded loader only supports amd64/arm64, current GOARCH=%s", arch)
	}

	exeDir := executableDir()
	workDir := workingDir()

	candidateDirs := uniqueNonEmpty([]string{
		exeDir,
		workDir,
		filepath.Join(exeDir, "tools", "wintun", arch),
		filepath.Join(workDir, "tools", "wintun", arch),
		filepath.Join(exeDir, "tools", "wintun"),
		filepath.Join(workDir, "tools", "wintun"),
	})

	for _, dir := range candidateDirs {
		path := filepath.Join(dir, "wintun.dll")
		if ok, reason := validDLLForArch(path, arch); ok {
			installDir, installPath, err := ensureProcessDLL(path, exeDir, arch)
			if err != nil {
				return err
			}
			activateDLLDirectory(installDir, append(candidateDirs, installDir)...)
			if logger != nil {
				logger.Add("info", "Wintun DLL ready: %s", installPath)
			}
			return nil
		} else if fileExists(path) && logger != nil {
			logger.Add("warn", "Ignoring invalid Wintun DLL at %s: %s", path, reason)
		}
	}

	embeddedPath := filepath.ToSlash(filepath.Join("assets", "wintun", "windows", arch, "wintun.dll"))
	data, err := embeddedWintun.ReadFile(embeddedPath)
	if err == nil {
		if err := validateDLLBytes(data, arch); err != nil {
			return fmt.Errorf("embedded wintun.dll is invalid for %s: %w", arch, err)
		}
		installDir, installPath, err := writeEmbeddedDLL(data, exeDir, arch)
		if err != nil {
			return err
		}
		activateDLLDirectory(installDir, append(candidateDirs, installDir)...)
		if logger != nil {
			logger.Add("info", "Embedded Wintun extracted: %s", installPath)
		}
		return nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read embedded wintun.dll failed: %w", err)
	}

	activateDLLDirectory("", candidateDirs...)
	return fmt.Errorf("wintun.dll was not found. The TUN engine is compiled into the app, but Windows still requires the signed Wintun DLL. Put the official %s wintun.dll at tools/wintun/%s/wintun.dll, run scripts/embed_wintun_from_tools.ps1, and rebuild", arch, arch)
}

func ensureProcessDLL(source, exeDir, arch string) (string, string, error) {
	if ok, reason := validDLLForArch(source, arch); !ok {
		return "", "", fmt.Errorf("source Wintun DLL is invalid: %s: %s", source, reason)
	}
	if source == filepath.Join(exeDir, "wintun.dll") {
		return exeDir, source, nil
	}
	installDir := preferredInstallDir(exeDir, arch)
	installPath := filepath.Join(installDir, "wintun.dll")
	if samePath(source, installPath) {
		return installDir, installPath, nil
	}
	if ok, _ := validDLLForArch(installPath, arch); ok {
		return installDir, installPath, nil
	}
	if err := copyFile(source, installPath); err != nil {
		// Fall back to the source directory if we cannot copy beside the exe.
		return filepath.Dir(source), source, nil
	}
	return installDir, installPath, nil
}

func writeEmbeddedDLL(data []byte, exeDir, arch string) (string, string, error) {
	if err := validateDLLBytes(data, arch); err != nil {
		return "", "", err
	}
	installDir := preferredInstallDir(exeDir, arch)
	installPath := filepath.Join(installDir, "wintun.dll")
	if ok, _ := validDLLForArch(installPath, arch); ok {
		return installDir, installPath, nil
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create Wintun install directory failed: %w", err)
	}
	if err := os.WriteFile(installPath, data, 0o644); err != nil {
		cacheDir := filepath.Join(userCacheDir(), "SocksRevivePC", "wintun", arch)
		cachePath := filepath.Join(cacheDir, "wintun.dll")
		if err2 := os.MkdirAll(cacheDir, 0o755); err2 != nil {
			return "", "", fmt.Errorf("write embedded Wintun failed: %w; cache fallback mkdir failed: %v", err, err2)
		}
		if err2 := os.WriteFile(cachePath, data, 0o644); err2 != nil {
			return "", "", fmt.Errorf("write embedded Wintun failed: %w; cache fallback write failed: %v", err, err2)
		}
		return cacheDir, cachePath, nil
	}
	return installDir, installPath, nil
}

func preferredInstallDir(exeDir, arch string) string {
	if exeDir != "" {
		return exeDir
	}
	return filepath.Join(userCacheDir(), "SocksRevivePC", "wintun", arch)
}

func executableDir() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return ""
	}
	return filepath.Dir(exe)
}

func workingDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func userCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		return os.TempDir()
	}
	return dir
}

func activateDLLDirectory(primary string, dirs ...string) {
	if primary != "" {
		_ = setDLLDirectory(primary)
	}
	prependPath(dirs...)
}

func setDLLDirectory(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("SetDllDirectoryW")
	ptr, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	r1, _, callErr := proc.Call(uintptr(unsafe.Pointer(ptr)))
	if r1 == 0 {
		return callErr
	}
	return nil
}

func prependPath(dirs ...string) {
	dirs = uniqueNonEmpty(dirs)
	if len(dirs) == 0 {
		return
	}
	current := os.Getenv("PATH")
	parts := strings.Split(current, string(os.PathListSeparator))
	var cleaned []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" && !containsPath(dirs, p) {
			cleaned = append(cleaned, p)
		}
	}
	os.Setenv("PATH", strings.Join(append(dirs, cleaned...), string(os.PathListSeparator)))
}

func uniqueNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !containsPath(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func containsPath(paths []string, p string) bool {
	for _, existing := range paths {
		if samePath(existing, p) {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func validDLLForArch(path, arch string) (bool, string) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err.Error()
	}
	if st.IsDir() {
		return false, "path is a directory"
	}
	if st.Size() <= 1024 {
		return false, "file is too small"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err.Error()
	}
	if err := validateDLLBytes(data, arch); err != nil {
		return false, err.Error()
	}
	return true, ""
}

func validateDLLBytes(data []byte, arch string) error {
	if len(data) <= 1024 {
		return fmt.Errorf("file is too small")
	}
	if len(data) < 0x40 || data[0] != 'M' || data[1] != 'Z' {
		return fmt.Errorf("not a Windows PE DLL")
	}
	peOffset := int(binary.LittleEndian.Uint32(data[0x3c:0x40]))
	if peOffset <= 0 || len(data) < peOffset+6 {
		return fmt.Errorf("invalid PE header")
	}
	if string(data[peOffset:peOffset+4]) != "PE\x00\x00" {
		return fmt.Errorf("missing PE signature")
	}
	machine := binary.LittleEndian.Uint16(data[peOffset+4 : peOffset+6])
	want := uint16(0x8664) // IMAGE_FILE_MACHINE_AMD64
	if arch == "arm64" {
		want = 0xaa64 // IMAGE_FILE_MACHINE_ARM64
	}
	if machine != want {
		return fmt.Errorf("wrong architecture machine=0x%04x expected=0x%04x for %s", machine, want, arch)
	}
	return nil
}

func copyFile(source, target string) error {
	if ok, reason := validDLLForArch(source, runtime.GOARCH); !ok {
		return fmt.Errorf("source DLL is missing or invalid: %s: %s", source, reason)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return closeErr
	}
	return nil
}
