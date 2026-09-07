package updater

import (
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
