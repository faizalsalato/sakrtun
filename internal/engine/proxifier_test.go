package engine

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveProxifierExecutableBundled(t *testing.T) {
	root := t.TempDir()
	bundled := filepath.Join(root, "tools", "proxifier", "Proxifier.exe")
	if err := os.MkdirAll(filepath.Dir(bundled), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundled, []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveProxifierExecutable(root, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != bundled {
		t.Fatalf("expected bundled %s, got %s", bundled, got)
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
	xmlStr := proxifierProfileXML("127.0.0.1:10808")
	if !strings.Contains(xmlStr, "<Address>127.0.0.1</Address>") || !strings.Contains(xmlStr, "<Port>10808</Port>") {
		t.Fatalf("profile does not contain the SOCKS address:\n%s", xmlStr)
	}
	var doc struct {
		XMLName xml.Name `xml:"ProxifierProfile"`
	}
	if err := xml.Unmarshal([]byte(xmlStr), &doc); err != nil {
		t.Fatalf("profile is not valid XML: %v", err)
	}
	if doc.XMLName.Local != "ProxifierProfile" {
		t.Fatalf("unexpected root element: %s", doc.XMLName.Local)
	}
}
