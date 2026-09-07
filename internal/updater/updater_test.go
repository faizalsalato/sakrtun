package updater

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestParseVersion(t *testing.T) {
	out := "Xray 26.3.27 (Xray, Penetrates Everything.) d2758a0 (go1.26.1 windows/amd64)\nA unified platform."
	if got := parseVersion(out, "Xray"); got != "26.3.27" {
		t.Fatalf("xray version = %q", got)
	}
	ov := "OpenVPN 2.7.6 [git:v2.7.6/327a33de2aa21883] Windows [SSL (OpenSSL)] [LZO] [LZ4]"
	if got := parseVersion(ov, "OpenVPN"); got != "2.7.6" {
		t.Fatalf("openvpn version = %q", got)
	}
	if got := parseVersion("nothing here", "Xray"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestXrayAssetSuffix(t *testing.T) {
	got := xrayAssetSuffix()
	if got == "" {
		t.Fatal("suffix must not be empty for supported platforms")
	}
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" && got != "windows-64.zip" {
		t.Fatalf("windows/amd64 suffix = %q", got)
	}
}

func TestReleaseSourceDir(t *testing.T) {
	dir := t.TempDir()
	// Flat layout stays as-is.
	if got := releaseSourceDir(dir); got != dir {
		t.Fatalf("flat layout = %q", got)
	}
	// Single top-level folder is stripped.
	inner := filepath.Join(dir, "SAKRTUN-windows-amd64")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := releaseSourceDir(dir); got != inner {
		t.Fatalf("nested layout = %q, want %q", got, inner)
	}
}

func TestCopyTreeMerge(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "tools", "xray"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "SAKRTUN.exe"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "tools", "xray", "xray.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyTreeMerge(src, dst); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"SAKRTUN.exe", filepath.Join("tools", "xray", "xray.exe")} {
		if _, err := os.Stat(filepath.Join(dst, p)); err != nil {
			t.Fatalf("missing %s after merge: %v", p, err)
		}
	}
}
