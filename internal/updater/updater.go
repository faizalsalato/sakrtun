// Package updater downloads and installs the latest OpenVPN and Xray
// releases into the app's tools folder.
package updater

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Logger reports progress. It mirrors the engine logger signature.
type Logger func(level, format string, args ...any)

const downloadTimeout = 10 * time.Minute

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type release struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

func fetchLatestRelease(repo string) (release, error) {
	var r release
	client := &http.Client{Timeout: downloadTimeout}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return r, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "SAKRTUN")
	resp, err := client.Do(req)
	if err != nil {
		return r, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return r, fmt.Errorf("github api returned 404 (repository not found or not accessible)")
		}
		return r, fmt.Errorf("github api returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return r, err
	}
	return r, nil
}

func downloadFile(url, dest string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "SAKRTUN")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %s", resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func unzip(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(f.Name)
		if name == "." || name == "" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(filepath.Join(dest, name))
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}
	return nil
}

func parseVersion(output, marker string) string {
	fields := strings.Fields(output)
	for i, f := range fields {
		if f == marker && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

func xrayExeName() string {
	if runtime.GOOS == "windows" {
		return "xray.exe"
	}
	return "xray"
}

func openvpnExeName() string {
	if runtime.GOOS == "windows" {
		return "openvpn.exe"
	}
	return "openvpn"
}

// ---- app self-update --------------------------------------------------------

const appReleaseRepo = "faizalsalato/sakrtun"

// appReleaseAssetNames are the accepted zip asset names for Windows releases
// in the GitHub release page (compared case-insensitively).
var appReleaseAssetNames = []string{
	"SAKRTUN-windows-amd64.zip",
	"sakrtun-windows-amd64.zip",
	"SAKRTUN-windows-64.zip",
	"sakrtun-windows-64.zip",
}

// LatestAppVersion returns the latest app version from the GitHub releases of
// faizalsalato/sakrtun (tag without the leading "v").
func LatestAppVersion() (string, error) {
	r, err := fetchLatestRelease(appReleaseRepo)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(r.TagName, "v"), nil
}

// UpdateApp downloads the latest app release zip from GitHub and merges its
// files over the app folder (exe, DLLs, tools, configs). On Windows the
// running executable cannot be overwritten in place, so it is renamed to
// SAKRTUN.exe.old first and removed on the next start. The app must be
// restarted to run the new version.
func UpdateApp(root, currentVersion string, log Logger) (string, error) {
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("app auto-update is only supported on Windows")
	}
	r, err := fetchLatestRelease(appReleaseRepo)
	if err != nil {
		return "", err
	}
	ver := strings.TrimPrefix(r.TagName, "v")
	var url string
	for _, a := range r.Assets {
		for _, n := range appReleaseAssetNames {
			if strings.EqualFold(a.Name, n) {
				url = a.URL
				break
			}
		}
		if url != "" {
			break
		}
	}
	if url == "" {
		return "", fmt.Errorf("no app release zip found in release %s (expected one of %v)", r.TagName, appReleaseAssetNames)
	}
	tmp, err := os.MkdirTemp("", "sakr-app")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	zipPath := filepath.Join(tmp, "app.zip")
	if log != nil {
		log("info", "app update: downloading %s ...", url)
	}
	if err := downloadFile(url, zipPath); err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	extract := filepath.Join(tmp, "ext")
	if err := os.MkdirAll(extract, 0o755); err != nil {
		return "", err
	}
	if err := unzip(zipPath, extract); err != nil {
		return "", fmt.Errorf("extract failed: %w", err)
	}
	src := releaseSourceDir(extract)
	if err := replaceAppFiles(src, root, log); err != nil {
		return "", err
	}
	// Old renamed executable is left for cleanup at the next start (it is
	// still locked by this running process).
	if log != nil {
		log("info", "app updated to %s; restart the app to apply", ver)
	}
	return ver, nil
}

// releaseSourceDir strips a single top-level folder from an extracted zip, so
// both "flat" zips and GitHub's "folder inside zip" layout work.
func releaseSourceDir(extract string) string {
	entries, err := os.ReadDir(extract)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return extract
	}
	return filepath.Join(extract, entries[0].Name())
}

// replaceAppFiles merges the extracted release files into the app folder.
func replaceAppFiles(src, root string, log Logger) error {
	// Rename the running executable so the new one can be written.
	if exe := filepath.Join(root, "SAKRTUN.exe"); fileExists(exe) {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil && log != nil {
			log("warn", "cannot rename the running executable: %v", err)
		}
	}
	return copyTreeMerge(src, root)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// copyTreeMerge copies every file under src into dst, keeping the relative
// paths and creating directories as needed.
func copyTreeMerge(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(path, target)
	})
}

func InstalledOpenVPNVersion(root string) string {
	exe := filepath.Join(root, "tools", "openvpn", openvpnExeName())
	out, err := exec.Command(exe, "--version").Output()
	if err != nil {
		return ""
	}
	return parseVersion(string(out), "OpenVPN")
}

func LatestOpenVPNVersion() (string, error) {
	r, err := fetchLatestRelease("OpenVPN/openvpn")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(r.TagName, "v"), nil
}

// openvpnInstallerURL returns the Windows amd64 MSI download URL for the
// given release version. The GitHub release may only carry source tarballs,
// so it falls back to the official OpenVPN community downloads server.
func openvpnInstallerURL(version string) (string, error) {
	if r, err := fetchLatestRelease("OpenVPN/openvpn"); err == nil {
		for _, a := range r.Assets {
			n := strings.ToLower(a.Name)
			if strings.HasSuffix(n, "-amd64.msi") && !strings.HasSuffix(n, ".asc") {
				return a.URL, nil
			}
		}
	}
	base := "https://swupdate.openvpn.org/community/releases"
	client := &http.Client{Timeout: 20 * time.Second}
	for _, build := range []string{"I001", "I002", "I003", "I004", "I005"} {
		url := fmt.Sprintf("%s/OpenVPN-%s-%s-amd64.msi", base, version, build)
		resp, err := client.Head(url)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return url, nil
		}
	}
	return "", fmt.Errorf("no openvpn amd64 msi found for version %s", version)
}

// UpdateOpenVPN downloads the latest official OpenVPN Windows installer, runs
// it silently (updating the system install and its TAP/wintun drivers), and
// then refreshes the bundled copy in tools/openvpn from the updated install.
// Requires administrator rights (the app already runs elevated). The tunnel
// must be disconnected first so the bundled openvpn.exe can be replaced.
func UpdateOpenVPN(root string, log Logger) (string, error) {
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("openvpn auto-update is only supported on Windows (use your package manager)")
	}
	r, err := fetchLatestRelease("OpenVPN/openvpn")
	if err != nil {
		return "", err
	}
	ver := strings.TrimPrefix(r.TagName, "v")
	url, err := openvpnInstallerURL(ver)
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "sakr-ovpn")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	msi := filepath.Join(tmp, "openvpn.msi")
	if log != nil {
		log("info", "openvpn update: downloading %s ...", url)
	}
	if err := downloadFile(url, msi); err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	if log != nil {
		log("info", "openvpn update: installing %s silently (this can take a minute) ...", ver)
	}
	out, err := exec.Command("msiexec", "/i", msi, "/qn", "/norestart").CombinedOutput()
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "3010") {
		return "", fmt.Errorf("msiexec failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	// Refresh the bundled copy from the updated system install.
	src := filepath.Join(os.Getenv("ProgramFiles"), "OpenVPN", "bin")
	dest := filepath.Join(root, "tools", "openvpn")
	if entries, readErr := os.ReadDir(src); readErr == nil {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return "", err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dest, e.Name())); err != nil {
				return "", fmt.Errorf("replace %s failed (disconnect first): %w", e.Name(), err)
			}
		}
		if log != nil {
			log("info", "openvpn updated to %s (bundled copy refreshed)", ver)
		}
	} else {
		if log != nil {
			log("warn", "openvpn installer finished, but the system install folder was not found at %s", src)
		}
	}
	return ver, nil
}

func InstalledXrayVersion(root string) string {
	exe := filepath.Join(root, "tools", "xray", xrayExeName())
	out, err := exec.Command(exe, "version").Output()
	if err != nil {
		return ""
	}
	return parseVersion(string(out), "Xray")
}

func LatestXrayVersion() (string, error) {
	r, err := fetchLatestRelease("XTLS/Xray-core")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(r.TagName, "v"), nil
}

func xrayAssetSuffix() string {
	switch runtime.GOOS {
	case "windows":
		if runtime.GOARCH == "arm64" {
			return "windows-arm64-v8a.zip"
		}
		return "windows-64.zip"
	case "linux":
		if runtime.GOARCH == "arm64" {
			return "linux-arm64-v8a.zip"
		}
		return "linux-64.zip"
	case "darwin":
		return "macos-64.zip"
	}
	return ""
}

// UpdateXray downloads the latest Xray release and replaces the bundled
// executable and data files in tools/xray. The tunnel must be disconnected
// first, otherwise the running xray.exe cannot be replaced on Windows.
func UpdateXray(root string, log Logger) (string, error) {
	r, err := fetchLatestRelease("XTLS/Xray-core")
	if err != nil {
		return "", err
	}
	suffix := xrayAssetSuffix()
	if suffix == "" {
		return "", fmt.Errorf("no xray release asset for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	var url string
	for _, a := range r.Assets {
		if strings.HasPrefix(a.Name, "Xray-") && strings.HasSuffix(a.Name, suffix) {
			url = a.URL
			break
		}
	}
	if url == "" {
		return "", fmt.Errorf("no xray release asset found for %s", suffix)
	}
	tmp, err := os.MkdirTemp("", "sakr-xray")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	zipPath := filepath.Join(tmp, "xray.zip")
	if log != nil {
		log("info", "xray update: downloading %s ...", url)
	}
	if err := downloadFile(url, zipPath); err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	extract := filepath.Join(tmp, "ext")
	if err := os.MkdirAll(extract, 0o755); err != nil {
		return "", err
	}
	if err := unzip(zipPath, extract); err != nil {
		return "", fmt.Errorf("extract failed: %w", err)
	}
	dest := filepath.Join(root, "tools", "xray")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	for _, name := range []string{xrayExeName(), "geoip.dat", "geosite.dat"} {
		src := filepath.Join(extract, name)
		if _, err := os.Stat(src); err == nil {
			if err := copyFile(src, filepath.Join(dest, name)); err != nil {
				return "", fmt.Errorf("replace %s failed (disconnect first): %w", name, err)
			}
		}
	}
	ver := strings.TrimPrefix(r.TagName, "v")
	if log != nil {
		log("info", "xray updated to %s", ver)
	}
	return ver, nil
}
