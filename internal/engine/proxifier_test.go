package engine

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxifierCandidatesOrder(t *testing.T) {
	root := t.TempDir()
	got := proxifierCandidates(root)
	want := []string{
		`C:\Program Files (x86)\Proxifier\Proxifier.exe`,
		`C:\Program Files\Proxifier\Proxifier.exe`,
		filepath.Join(root, "tools", "proxifier", "Proxifier.exe"),
		`C:\Proxifier\Proxifier.exe`,
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected candidate count: %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate %d: got %s want %s", i, got[i], want[i])
		}
	}
}

func TestResolveProxifierExecutableCustomRelative(t *testing.T) {
	root := t.TempDir()
	custom := filepath.Join("tools", "myprox", "Proxifier.exe")
	if err := os.MkdirAll(filepath.Join(root, "tools", "myprox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, custom), []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveProxifierExecutable(root, custom)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != filepath.Join(root, custom) {
		t.Fatalf("expected %s, got %s", filepath.Join(root, custom), got)
	}
	if _, err := resolveProxifierExecutable(root, "tools/missing/Proxifier.exe"); err == nil {
		t.Fatal("expected error for missing custom path")
	}
}

func TestProxifierProfileXML(t *testing.T) {
	for _, portable := range []bool{false, true} {
		xmlStr := proxifierProfileXML("127.0.0.1:10808", portable, []string{"SAKRTUN.exe", "xray.exe"})
		if !strings.Contains(xmlStr, "<Address>127.0.0.1</Address>") || !strings.Contains(xmlStr, "<Port>10808</Port>") {
			t.Fatalf("profile does not contain the SOCKS address (portable=%v):\n%s", portable, xmlStr)
		}
		var doc struct {
			XMLName xml.Name `xml:"ProxifierProfile"`
		}
		if err := xml.Unmarshal([]byte(xmlStr), &doc); err != nil {
			t.Fatalf("profile is not valid XML (portable=%v): %v", portable, err)
		}
		if doc.XMLName.Local != "ProxifierProfile" {
			t.Fatalf("unexpected root element: %s", doc.XMLName.Local)
		}
		if !strings.Contains(xmlStr, "<Applications>SAKRTUN.exe; xray.exe</Applications>") {
			t.Fatal("profile must contain the tunnel bypass rule")
		}
		if !strings.Contains(xmlStr, `<Udp mode="mode_block_443" />`) {
			t.Fatal("profile must block UDP 443 (QUIC) so browsers use TCP over the proxy")
		}
		if portable {
			if !strings.Contains(xmlStr, "ProxificationPortableEngine") {
				t.Fatal("portable profile must include the portable proxification engine")
			}
			if !strings.Contains(xmlStr, `version="102"`) {
				t.Fatal("portable profile must use version 102")
			}
		} else {
			if strings.Contains(xmlStr, "ProxificationPortableEngine") {
				t.Fatal("normal profile must not include the portable proxification engine")
			}
			if !strings.Contains(xmlStr, `version="101"`) {
				t.Fatal("normal profile must use version 101")
			}
		}
	}
}
