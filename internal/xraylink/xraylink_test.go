package xraylink

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseVLESSWS(t *testing.T) {
	cfg, err := Parse("vless://12345678-abcd-4efg-90ab-1234567890ab@example.com:443?path=%2Fws&security=tls&encryption=none&host=cdn.example.com&sni=cdn.example.com&type=ws#My%20Server")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Name != "My Server" {
		t.Fatalf("name = %q", cfg.Name)
	}
	if !json.Valid([]byte(cfg.JSON)) {
		t.Fatalf("invalid json:\n%s", cfg.JSON)
	}
	for _, want := range []string{`"protocol": "vless"`, `"network": "ws"`, `"security": "tls"`, `"Host": "cdn.example.com"`, `"/ws"`, `"id": "12345678-abcd-4efg-90ab-1234567890ab"`} {
		if !strings.Contains(cfg.JSON, want) {
			t.Fatalf("json missing %s:\n%s", want, cfg.JSON)
		}
	}
}

func TestParseVLESSReality(t *testing.T) {
	cfg, err := Parse("vless://12345678-abcd-4efg-90ab-1234567890ab@1.2.3.4:443?security=reality&encryption=none&pbk=PUBKEY&sid=abcd&fp=chrome&type=tcp&flow=xtls-rprx-vision&sni=www.microsoft.com#Reality")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, want := range []string{`"security": "reality"`, `"publicKey": "PUBKEY"`, `"shortId": "abcd"`, `"flow": "xtls-rprx-vision"`, `"serverName": "www.microsoft.com"`} {
		if !strings.Contains(cfg.JSON, want) {
			t.Fatalf("json missing %s:\n%s", want, cfg.JSON)
		}
	}
}

func TestParseTrojanWS(t *testing.T) {
	cfg, err := Parse("trojan://secretpw@example.com:443?security=tls&sni=example.com&type=ws&host=example.com&path=%2Fpath&alpn=http%2F1.1#Trojan")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, want := range []string{`"protocol": "trojan"`, `"password": "secretpw"`, `"alpn": [`, `"http/1.1"`, `"network": "ws"`} {
		if !strings.Contains(cfg.JSON, want) {
			t.Fatalf("json missing %s:\n%s", want, cfg.JSON)
		}
	}
}

func TestParseSS(t *testing.T) {
	// ss://base64(method:password)@host:port#name
	link := "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpwYXNzMTIz@1.2.3.4:8388#SS-Test"
	cfg, err := Parse(link)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, want := range []string{`"protocol": "shadowsocks"`, `"method": "chacha20-ietf-poly1305"`, `"password": "pass123"`} {
		if !strings.Contains(cfg.JSON, want) {
			t.Fatalf("json missing %s:\n%s", want, cfg.JSON)
		}
	}
}

func TestParseVMess(t *testing.T) {
	payload := "ewogICJ2IjogIjIiLAogICJwcyI6ICJWTS1UZXN0IiwKICAiYWRkIjogImV4YW1wbGUuY29tIiwKICAicG9ydCI6ICI0NDMiLAogICJpZCI6ICJ1dWlkIiwKICAiYWlkIjogIjAiLAogICJuZXQiOiAid3MiLAogICJwYXRoIjogIi93cyIsCiAgImhvc3QiOiAiZXhhbXBsZS5jb20iLAogICJ0bHMiOiAidGxzIgp9"
	cfg, err := Parse("vmess://" + payload + "#VM")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, want := range []string{`"protocol": "vmess"`, `"address": "example.com"`, `"id": "uuid"`, `"network": "ws"`, `"security": "tls"`} {
		if !strings.Contains(cfg.JSON, want) {
			t.Fatalf("json missing %s:\n%s", want, cfg.JSON)
		}
	}
}

func TestParseUnsupported(t *testing.T) {
	if _, err := Parse("http://example.com"); err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
	if _, err := Parse(""); err == nil {
		t.Fatal("expected error for empty link")
	}
}
