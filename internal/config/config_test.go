package config

import "testing"

func TestEffectiveTunDNS(t *testing.T) {
	p := Profile{Tun: TunConfig{DNS: []string{"1.1.1.1", "8.8.8.8"}}}
	if got := p.EffectiveTunDNS(); len(got) != 2 || got[0] != "1.1.1.1" {
		t.Fatalf("default DNS = %v", got)
	}
	p.DNS.Servers = []string{"9.9.9.9", "149.112.112.112:53"}
	if got := p.EffectiveTunDNS(); len(got) != 2 || got[0] != "9.9.9.9" {
		t.Fatalf("custom DNS = %v", got)
	}
}

func TestValidateCustomDNS(t *testing.T) {
	base := Profile{Name: "dns", Mode: ModeDirect, SSH: SSHConfig{Host: "h", Port: 22, Username: "u", Password: "p"}, Local: LocalConfig{SocksPort: 1080}}
	for _, servers := range [][]string{
		{"1.1.1.1", "8.8.8.8:53", "dns.google"},
		{"[2606:4700:4700::1111]:53"},
	} {
		p := base
		p.DNS.Servers = servers
		if err := Validate(p); err != nil {
			t.Fatalf("valid servers %v rejected: %v", servers, err)
		}
	}
	for _, servers := range [][]string{
		{"bad server"},
		{"host/name"},
	} {
		p := base
		p.DNS.Servers = servers
		if err := Validate(p); err == nil {
			t.Fatalf("invalid servers %v accepted", servers)
		}
	}
}
