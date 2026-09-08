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
		if resp.StatusCode == http.StatusForbidden {
			return r, fmt.Errorf("github api returned 403 (rate limited or this network's IP is blocked)")
		}
		return r, fmt.Errorf("github api returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return r, err
	}
	return r, nil
}

// fetchLatestTag returns the tag of the latest GitHub release for repo by
// following the website redirect (https://github.com/<repo>/releases/latest).
// This avoids the GitHub REST API entirely: the website is not subject to the
// anonymous API rate limit (60/hour per IP) nor to the 403 blocks applied to
// datacenter/VPN IPs, so it keeps working when the traffic leaves through a
// VPN tunnel. It falls back to the API when the website is unreachable.
func fetchLatestTag(repo string) (string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest("GET", "https://github.com/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "SAKRTUN")
	var webErr error
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if idx := strings.LastIndex(loc, "/tag/"); idx >= 0 {
			tag := loc[idx+len("/tag/"):]
			if tag != "" {
				return tag, nil
			}
		}
		webErr = fmt.Errorf("github website returned %s without a release tag", resp.Status)
	} else {
		webErr = err
	}
	// Fallback to the API.
	r, apiErr := fetchLatestRelease(repo)
	if apiErr != nil {
		return "", fmt.Errorf("github website failed: %v (api also failed: %v)", webErr, apiErr)
	}
	return r.TagName, nil
}

func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: downloadTimeout}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "SAKRTUN")
	resp, err := client.Do(req)
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

// LatestAppVersion returns the latest app version from the GitHub releases of
// faizalsalato/sakrtun (tag without the leading "v").
func LatestAppVersion() (string, error) {
	tag, err := fetchLatestTag(appReleaseRepo)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(tag, "v"), nil
}

// appSetupURLs returns the candidate download URLs for the app installer of
// the given tag. The direct release-download URL is tried first (it works
// without the GitHub API), and any API-discovered asset URLs are used as
// fallback when the API is reachable.
func appSetupURLs(tag string) []string {
	ver := strings.TrimPrefix(tag, "v")
	urls := []string{
		fmt.Sprintf("https://github.com/%s/releases/download/%s/SAKRTUN-Setup-%s.exe", appReleaseRepo, tag, ver),
	}
	if r, err := fetchLatestRelease(appReleaseRepo); err == nil {
		for _, a := range r.Assets {
			n := strings.ToLower(a.Name)
			if strings.HasPrefix(n, "sakrtun-setup-") && strings.HasSuffix(n, ".exe") {
				urls = append(urls, a.URL)
			}
		}
	}
	return urls
}

// UpdateApp downloads the latest SAKRTUN-Setup installer from GitHub and runs
// it silently. The Inno Setup installer updates the existing installation,
// closing and restarting the app automatically when needed.
func UpdateApp(root, currentVersion string, log Logger) (string, error) {
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("app auto-update is only supported on Windows")
	}
	tag, err := fetchLatestTag(appReleaseRepo)
	if err != nil {
		return "", err
	}
	ver := strings.TrimPrefix(tag, "v")
	tmp, err := os.MkdirTemp("", "sakr-app")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	setupPath := filepath.Join(tmp, "SAKRTUN-Setup.exe")
	var lastErr error
	for _, url := range appSetupURLs(tag) {
		if log != nil {
			log("info", "app update: downloading %s ...", url)
		}
		if err := downloadFile(url, setupPath); err == nil {
			lastErr = nil
			break
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("download failed: %w", lastErr)
	}
	if log != nil {
		log("info", "app update: installing %s silently (the app restarts automatically) ...", ver)
	}
	out, err := exec.Command(setupPath, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("installer failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return ver, nil
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
	tag, err := fetchLatestTag("OpenVPN/openvpn")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(tag, "v"), nil
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
	tag, err := fetchLatestTag("XTLS/Xray-core")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(tag, "v"), nil
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
	tag, err := fetchLatestTag("XTLS/Xray-core")
	if err != nil {
		return "", err
	}
	ver := strings.TrimPrefix(tag, "v")
	suffix := xrayAssetSuffix()
	if suffix == "" {
		return "", fmt.Errorf("no xray release asset for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	// The direct release-download URL works without the GitHub API; the
	// API-discovered asset URL is used as fallback when the API is reachable.
	urls := []string{
		fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/%s/Xray-%s.zip", tag, suffix),
	}
	if r, apiErr := fetchLatestRelease("XTLS/Xray-core"); apiErr == nil {
		for _, a := range r.Assets {
			if strings.HasPrefix(a.Name, "Xray-") && strings.HasSuffix(a.Name, suffix) {
				urls = append(urls, a.URL)
				break
			}
		}
	}
	tmp, err := os.MkdirTemp("", "sakr-xray")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	zipPath := filepath.Join(tmp, "xray.zip")
	var lastErr error
	for _, url := range urls {
		if log != nil {
			log("info", "xray update: downloading %s ...", url)
		}
		if err := downloadFile(url, zipPath); err == nil {
			lastErr = nil
			break
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("download failed: %w", lastErr)
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
	if log != nil {
		log("info", "xray updated to %s", ver)
	}
	return ver, nil
}
