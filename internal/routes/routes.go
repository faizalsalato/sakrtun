package routes

import (
	"encoding/json"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"time"

	"socksrevivepc/internal/config"
	"socksrevivepc/internal/oscmd"
)

type Logger interface {
	Add(level, format string, args ...any)
}

type Cleanup struct {
	commands [][]string
	logger   Logger
}

func Apply(p config.Profile, proxyHosts []string, logger Logger) (*Cleanup, error) {
	if !p.Tun.Enabled || !p.Tun.RouteAll {
		return &Cleanup{logger: logger}, nil
	}
	logger.Add("info", "applying TUN routes; admin/root permission may be required")
	gw, iface, _, err := defaultGateway()
	if err != nil {
		logger.Add("warn", "cannot detect default gateway: %v", err)
	}

	cleanup := &Cleanup{logger: logger}
	switch runtime.GOOS {
	case "windows":
		applyWindowsRoutes(p, proxyHosts, gw, cleanup, logger)
	case "linux":
		dev := p.Tun.InterfaceName
		if dev == "" {
			dev = "socksrevive0"
		}
		_ = run(logger, "ip", "addr", "add", p.Tun.CIDR, "dev", dev)
		_ = run(logger, "ip", "link", "set", dev, "up")
		addBypassLinux(proxyHosts, gw, iface, cleanup, logger)
		if err := run(logger, "ip", "route", "replace", "0.0.0.0/1", "via", p.Tun.Gateway, "dev", dev); err == nil {
			cleanup.commands = append(cleanup.commands, []string{"ip", "route", "del", "0.0.0.0/1"})
		}
		if err := run(logger, "ip", "route", "replace", "128.0.0.0/1", "via", p.Tun.Gateway, "dev", dev); err == nil {
			cleanup.commands = append(cleanup.commands, []string{"ip", "route", "del", "128.0.0.0/1"})
		}
		applyLinuxIPv6Routes(p, proxyHosts, dev, cleanup, logger)
	case "darwin":
		dev := p.Tun.InterfaceName
		_ = run(logger, "ifconfig", dev, p.Tun.Gateway, p.Tun.Gateway, "up")
		addBypassDarwin(proxyHosts, gw, cleanup, logger)
		if err := run(logger, "route", "add", "0.0.0.0/1", p.Tun.Gateway); err == nil {
			cleanup.commands = append(cleanup.commands, []string{"route", "delete", "0.0.0.0/1"})
		}
		if err := run(logger, "route", "add", "128.0.0.0/1", p.Tun.Gateway); err == nil {
			cleanup.commands = append(cleanup.commands, []string{"route", "delete", "128.0.0.0/1"})
		}
		applyDarwinIPv6Routes(p, proxyHosts, dev, cleanup, logger)
	default:
		logger.Add("warn", "route automation not implemented for %s", runtime.GOOS)
	}
	return cleanup, nil
}

func (c *Cleanup) Run() {
	if c == nil {
		return
	}
	for i := len(c.commands) - 1; i >= 0; i-- {
		cmd := c.commands[i]
		if len(cmd) == 0 {
			continue
		}
		_ = run(c.logger, cmd[0], cmd[1:]...)
	}
}

func applyWindowsRoutes(p config.Profile, proxyHosts []string, gw string, cleanup *Cleanup, logger Logger) {
	alias, ifIndex, err := waitWindowsTunInterface(p.Tun.InterfaceName, 6*time.Second, logger)
	if err != nil {
		logger.Add("warn", "cannot find Windows Wintun adapter for routes: %v", err)
		return
	}
	ip, mask := ipv4CIDRToAddressAndMask(p.Tun.CIDR, p.Tun.Gateway)
	logger.Add("info", "Windows TUN route adapter: alias=%s ifIndex=%d ip=%s mask=%s", alias, ifIndex, ip, mask)

	_ = run(logger, "netsh", "interface", "ipv4", "set", "interface", fmt.Sprint(ifIndex), "metric=1")
	if err := run(logger, "netsh", "interface", "ipv4", "set", "address", "name="+alias, "static", ip, mask); err != nil {
		logger.Add("warn", "failed to set Wintun IPv4 address; routes may not work until Windows finishes creating the adapter")
	}
	dns := p.EffectiveTunDNS()
	if len(dns) > 0 {
		_ = run(logger, "netsh", "interface", "ipv4", "set", "dnsservers", "name="+alias, "static", dns[0], "validate=no")
		for i, server := range dns[1:] {
			server = strings.TrimSpace(server)
			if server == "" {
				continue
			}
			_ = run(logger, "netsh", "interface", "ipv4", "add", "dnsservers", "name="+alias, server, fmt.Sprint(i+2), "validate=no")
		}
	}

	// Bypass routes must stay on the original physical gateway so the SSH/Xray/DNSTT
	// control connection never loops back into the TUN default route.
	addBypassWindows(proxyHosts, gw, cleanup, logger)

	addWindowsIPv4SplitRoute(alias, ifIndex, "0.0.0.0/1", "0.0.0.0", "128.0.0.0", cleanup, logger)
	addWindowsIPv4SplitRoute(alias, ifIndex, "128.0.0.0/1", "128.0.0.0", "128.0.0.0", cleanup, logger)

	applyWindowsIPv6Routes(p, proxyHosts, alias, ifIndex, cleanup, logger)
}

func applyWindowsIPv6Routes(p config.Profile, proxyHosts []string, alias string, ifIndex int, cleanup *Cleanup, logger Logger) {
	if !p.Tun.IPv6Enabled && p.Tun.AllowIPv6Leak {
		logger.Add("info", "IPv6 TUN is disabled and IPv6 leak blocking is off; leaving normal IPv6 routes untouched")
		return
	}

	_ = run(logger, "netsh", "interface", "ipv6", "set", "interface", fmt.Sprint(ifIndex), "metric=1")
	ipv6Addr, prefixLen := ipv6CIDRToAddressAndPrefix(p.Tun.IPv6CIDR)
	if p.Tun.IPv6Enabled {
		logger.Add("info", "IPv6 TUN routing enabled: address=%s/%s", ipv6Addr, prefixLen)
		_ = run(logger, "netsh", "interface", "ipv6", "add", "address", "interface="+alias, "address="+ipv6Addr, "store=active")
		setWindowsIPv6DNS(alias, p.Tun.IPv6DNS, logger)
		if gw, idxStr, err := defaultGatewayIPv6(); err == nil {
			if idx, convErr := strconv.Atoi(idxStr); convErr == nil && idx > 0 {
				addBypassWindowsIPv6(proxyHosts, gw, idx, cleanup, logger)
			}
		}
	} else {
		logger.Add("info", "IPv6 TUN is disabled; adding IPv6 split routes as leak protection")
	}
	addWindowsIPv6SplitRoute(alias, "::/1", cleanup, logger)
	addWindowsIPv6SplitRoute(alias, "8000::/1", cleanup, logger)
}

func setWindowsIPv6DNS(alias string, dns []string, logger Logger) {
	if len(dns) == 0 {
		return
	}
	first := strings.TrimSpace(dns[0])
	if first == "" {
		return
	}
	_ = run(logger, "netsh", "interface", "ipv6", "set", "dnsservers", "name="+alias, "static", first, "validate=no")
	for i, server := range dns[1:] {
		server = strings.TrimSpace(server)
		if server == "" {
			continue
		}
		_ = run(logger, "netsh", "interface", "ipv6", "add", "dnsservers", "name="+alias, server, fmt.Sprint(i+2), "validate=no")
	}
}

func applyLinuxIPv6Routes(p config.Profile, proxyHosts []string, dev string, cleanup *Cleanup, logger Logger) {
	if !p.Tun.IPv6Enabled && p.Tun.AllowIPv6Leak {
		logger.Add("info", "IPv6 TUN is disabled and IPv6 leak blocking is off; leaving normal IPv6 routes untouched")
		return
	}
	if p.Tun.IPv6Enabled {
		logger.Add("info", "IPv6 TUN routing enabled: cidr=%s", p.Tun.IPv6CIDR)
		_ = run(logger, "ip", "-6", "addr", "add", p.Tun.IPv6CIDR, "dev", dev)
		if gw, iface, err := defaultGatewayIPv6(); err == nil {
			addBypassLinuxIPv6(proxyHosts, gw, iface, cleanup, logger)
		}
	} else {
		logger.Add("info", "IPv6 TUN is disabled; adding IPv6 split routes as leak protection")
	}
	if err := run(logger, "ip", "-6", "route", "replace", "::/1", "dev", dev, "metric", "1"); err == nil {
		cleanup.commands = append(cleanup.commands, []string{"ip", "-6", "route", "del", "::/1"})
	}
	if err := run(logger, "ip", "-6", "route", "replace", "8000::/1", "dev", dev, "metric", "1"); err == nil {
		cleanup.commands = append(cleanup.commands, []string{"ip", "-6", "route", "del", "8000::/1"})
	}
}

func applyDarwinIPv6Routes(p config.Profile, proxyHosts []string, dev string, cleanup *Cleanup, logger Logger) {
	if !p.Tun.IPv6Enabled && p.Tun.AllowIPv6Leak {
		logger.Add("info", "IPv6 TUN is disabled and IPv6 leak blocking is off; leaving normal IPv6 routes untouched")
		return
	}
	ipv6Addr, prefixLen := ipv6CIDRToAddressAndPrefix(p.Tun.IPv6CIDR)
	if p.Tun.IPv6Enabled {
		logger.Add("info", "IPv6 TUN routing enabled: address=%s/%s", ipv6Addr, prefixLen)
		_ = run(logger, "ifconfig", dev, "inet6", ipv6Addr, "prefixlen", prefixLen, "up")
		if gw, _, err := defaultGatewayIPv6(); err == nil {
			addBypassDarwinIPv6(proxyHosts, gw, cleanup, logger)
		}
	} else {
		logger.Add("info", "IPv6 TUN is disabled; adding IPv6 split routes as leak protection")
	}
	if err := run(logger, "route", "add", "-inet6", "::/1", "-interface", dev); err == nil {
		cleanup.commands = append(cleanup.commands, []string{"route", "delete", "-inet6", "::/1"})
	}
	if err := run(logger, "route", "add", "-inet6", "8000::/1", "-interface", dev); err == nil {
		cleanup.commands = append(cleanup.commands, []string{"route", "delete", "-inet6", "8000::/1"})
	}
}

func addWindowsIPv4SplitRoute(alias string, ifIndex int, prefix, legacyDest, legacyMask string, cleanup *Cleanup, logger Logger) {
	args := []string{"interface", "ipv4", "add", "route", "prefix=" + prefix, "interface=" + alias, "nexthop=0.0.0.0", "metric=1", "store=active"}
	if err := run(logger, "netsh", args...); err == nil {
		cleanup.commands = append(cleanup.commands, []string{"netsh", "interface", "ipv4", "delete", "route", "prefix=" + prefix, "interface=" + alias, "nexthop=0.0.0.0", "store=active"})
		return
	}
	// Fallback for older Windows/netsh variants.
	if err := run(logger, "route", "add", legacyDest, "mask", legacyMask, "0.0.0.0", "metric", "1", "if", fmt.Sprint(ifIndex)); err == nil {
		cleanup.commands = append(cleanup.commands, []string{"route", "delete", legacyDest, "mask", legacyMask})
	}
}

func addWindowsIPv6SplitRoute(alias, prefix string, cleanup *Cleanup, logger Logger) {
	args := []string{"interface", "ipv6", "add", "route", "prefix=" + prefix, "interface=" + alias, "nexthop=::", "metric=1", "store=active"}
	if err := run(logger, "netsh", args...); err == nil {
		cleanup.commands = append(cleanup.commands, []string{"netsh", "interface", "ipv6", "delete", "route", "prefix=" + prefix, "interface=" + alias, "nexthop=::", "store=active"})
	}
}

func ipv4CIDRToAddressAndMask(cidr, fallbackIP string) (string, string) {
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err == nil && ip.To4() != nil {
		return ip.String(), net.IP(ipnet.Mask).String()
	}
	if parsed := net.ParseIP(strings.TrimSpace(fallbackIP)); parsed != nil && parsed.To4() != nil {
		return parsed.String(), "255.254.0.0"
	}
	return "198.18.0.1", "255.254.0.0"
}

func ipv6CIDRToAddressAndPrefix(cidr string) (string, string) {
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err == nil && ip.To4() == nil && ip.To16() != nil {
		ones, _ := ipnet.Mask.Size()
		return ip.String(), fmt.Sprint(ones)
	}
	return "fd00:534f:434b::1", "64"
}

type windowsAdapter struct {
	Name           string `json:"Name"`
	InterfaceAlias string `json:"InterfaceAlias"`
	InterfaceIndex int    `json:"InterfaceIndex"`
	IfIndex        int    `json:"ifIndex"`
	Status         string `json:"Status"`
	InterfaceDesc  string `json:"InterfaceDescription"`
}

func waitWindowsTunInterface(preferred string, timeout time.Duration, logger Logger) (string, int, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		alias, idx, err := windowsTunInterface(preferred)
		if err == nil && alias != "" && idx > 0 {
			return alias, idx, nil
		}
		lastErr = err
		time.Sleep(300 * time.Millisecond)
	}
	if lastErr != nil {
		return "", 0, lastErr
	}
	return "", 0, fmt.Errorf("not found")
}

func windowsTunInterface(preferred string) (string, int, error) {
	preferred = strings.TrimSpace(preferred)
	if preferred == "" {
		preferred = "wintun"
	}
	ps := `$preferred = ` + strconv.Quote(preferred) + `
$adapters = @(Get-NetAdapter -ErrorAction SilentlyContinue | Where-Object {
    $_.Status -ne 'Disabled' -and (
        $_.Name -ieq $preferred -or
        $_.InterfaceDescription -like '*Wintun*' -or
        $_.InterfaceDescription -like '*WireGuard*' -or
        $_.Name -like '*wintun*'
    )
} | Sort-Object @{Expression={if ($_.Name -ieq $preferred) {0} else {1}}}, InterfaceIndex | Select-Object -First 1 Name,InterfaceDescription,InterfaceIndex,ifIndex,Status)
if ($adapters.Count -eq 0) { exit 2 }
$adapters[0] | ConvertTo-Json -Compress`
	out, err := oscmd.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps).Output()
	if err != nil {
		return "", 0, err
	}
	var a windowsAdapter
	if err := json.Unmarshal(out, &a); err != nil {
		return "", 0, err
	}
	idx := a.InterfaceIndex
	if idx == 0 {
		idx = a.IfIndex
	}
	name := a.Name
	if name == "" {
		name = a.InterfaceAlias
	}
	if name == "" || idx == 0 {
		return "", 0, fmt.Errorf("invalid adapter json %q", strings.TrimSpace(string(out)))
	}
	return name, idx, nil
}

func addBypassWindows(hosts []string, gw string, cleanup *Cleanup, logger Logger) {
	if gw == "" {
		return
	}
	for _, ip := range resolveIPv4(hosts) {
		if err := run(logger, "route", "add", ip, "mask", "255.255.255.255", gw, "metric", "1"); err == nil {
			cleanup.commands = append(cleanup.commands, []string{"route", "delete", ip, "mask", "255.255.255.255"})
		}
	}
}

func addBypassLinux(hosts []string, gw, iface string, cleanup *Cleanup, logger Logger) {
	if gw == "" {
		return
	}
	for _, ip := range resolveIPv4(hosts) {
		args := []string{"route", "add", ip + "/32", "via", gw}
		if iface != "" {
			args = append(args, "dev", iface)
		}
		if err := run(logger, "ip", args...); err == nil {
			cleanup.commands = append(cleanup.commands, []string{"ip", "route", "del", ip + "/32"})
		}
	}
}

func addBypassDarwin(hosts []string, gw string, cleanup *Cleanup, logger Logger) {
	if gw == "" {
		return
	}
	for _, ip := range resolveIPv4(hosts) {
		if err := run(logger, "route", "add", "-host", ip, gw); err == nil {
			cleanup.commands = append(cleanup.commands, []string{"route", "delete", "-host", ip})
		}
	}
}

func addBypassWindowsIPv6(hosts []string, gw string, ifIndex int, cleanup *Cleanup, logger Logger) {
	if gw == "" || ifIndex == 0 {
		return
	}
	for _, ip := range resolveIPv6(hosts) {
		prefix := ip + "/128"
		if err := run(logger, "netsh", "interface", "ipv6", "add", "route", "prefix="+prefix, "interface="+fmt.Sprint(ifIndex), "nexthop="+gw, "metric=1", "store=active"); err == nil {
			cleanup.commands = append(cleanup.commands, []string{"netsh", "interface", "ipv6", "delete", "route", "prefix=" + prefix, "interface=" + fmt.Sprint(ifIndex), "nexthop=" + gw, "store=active"})
		}
	}
}

func addBypassLinuxIPv6(hosts []string, gw, iface string, cleanup *Cleanup, logger Logger) {
	for _, ip := range resolveIPv6(hosts) {
		args := []string{"-6", "route", "add", ip + "/128"}
		if gw != "" {
			args = append(args, "via", gw)
		}
		if iface != "" {
			args = append(args, "dev", iface)
		}
		if err := run(logger, "ip", args...); err == nil {
			cleanup.commands = append(cleanup.commands, []string{"ip", "-6", "route", "del", ip + "/128"})
		}
	}
}

func addBypassDarwinIPv6(hosts []string, gw string, cleanup *Cleanup, logger Logger) {
	if gw == "" {
		return
	}
	for _, ip := range resolveIPv6(hosts) {
		if err := run(logger, "route", "add", "-inet6", "-host", ip, gw); err == nil {
			cleanup.commands = append(cleanup.commands, []string{"route", "delete", "-inet6", "-host", ip})
		}
	}
}

func resolveIPv6(hosts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if ip := net.ParseIP(host); ip != nil {
			if ip.To4() == nil && ip.To16() != nil && !seen[ip.String()] {
				seen[ip.String()] = true
				out = append(out, ip.String())
			}
			continue
		}
		ips, err := net.LookupIP(host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			if ip.To4() == nil && ip.To16() != nil && !seen[ip.String()] {
				seen[ip.String()] = true
				out = append(out, ip.String())
			}
		}
	}
	return out
}

func resolveIPv4(hosts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" || net.ParseIP(host) != nil && net.ParseIP(host).To4() == nil {
			continue
		}
		ips, err := net.LookupIP(host)
		if err != nil {
			if ip := net.ParseIP(host); ip != nil && ip.To4() != nil && !seen[ip.String()] {
				seen[ip.String()] = true
				out = append(out, ip.String())
			}
			continue
		}
		for _, ip := range ips {
			v4 := ip.To4()
			if v4 != nil && !seen[v4.String()] {
				seen[v4.String()] = true
				out = append(out, v4.String())
			}
		}
	}
	return out
}

func defaultGatewayIPv6() (gateway string, iface string, err error) {
	switch runtime.GOOS {
	case "windows":
		ps := `Get-NetRoute -AddressFamily IPv6 -DestinationPrefix "::/0" -ErrorAction SilentlyContinue |
    Where-Object { $_.NextHop -and $_.NextHop -ne "::" } |
    Sort-Object RouteMetric, InterfaceMetric |
    Select-Object -First 1 NextHop, InterfaceIndex | ConvertTo-Json -Compress`
		out, err := oscmd.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps).Output()
		if err != nil {
			return "", "", err
		}
		var row struct {
			NextHop        string `json:"NextHop"`
			InterfaceIndex int    `json:"InterfaceIndex"`
		}
		if err := json.Unmarshal(out, &row); err != nil {
			return "", "", err
		}
		if row.NextHop == "" || row.InterfaceIndex == 0 {
			return "", "", fmt.Errorf("IPv6 default gateway not found")
		}
		return row.NextHop, fmt.Sprint(row.InterfaceIndex), nil
	case "linux":
		out, err := oscmd.Command("ip", "-6", "route", "show", "default").Output()
		if err != nil {
			return "", "", err
		}
		fields := strings.Fields(string(out))
		for i, f := range fields {
			if f == "via" && i+1 < len(fields) {
				gateway = fields[i+1]
			}
			if f == "dev" && i+1 < len(fields) {
				iface = fields[i+1]
			}
		}
		if gateway == "" && iface == "" {
			return "", "", fmt.Errorf("IPv6 default gateway not found")
		}
		return gateway, iface, nil
	case "darwin":
		out, err := oscmd.Command("route", "-n", "get", "-inet6", "default").Output()
		if err != nil {
			return "", "", err
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "gateway:") {
				gateway = strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
			}
			if strings.HasPrefix(line, "interface:") {
				iface = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
			}
		}
		if gateway == "" && iface == "" {
			return "", "", fmt.Errorf("IPv6 default gateway not found")
		}
		return gateway, iface, nil
	default:
		return "", "", fmt.Errorf("IPv6 default gateway detection not implemented for %s", runtime.GOOS)
	}
}

func run(logger Logger, name string, args ...string) error {
	cmd := oscmd.Command(name, args...)
	out, err := cmd.CombinedOutput()
	line := strings.TrimSpace(string(out))
	if err != nil {
		logger.Add("warn", "%s %s failed: %v %s", name, strings.Join(args, " "), err, line)
		return err
	}
	if line != "" {
		logger.Add("debug", "%s: %s", name, line)
	} else if strings.EqualFold(name, "netsh") || strings.EqualFold(name, "route") || strings.EqualFold(name, "ip") {
		logger.Add("debug", "%s %s: OK", name, strings.Join(args, " "))
	}
	return nil
}

func defaultGateway() (gateway string, iface string, ifIndex int, err error) {
	switch runtime.GOOS {
	case "linux":
		out, err := oscmd.Command("ip", "route", "show", "default").Output()
		if err != nil {
			return "", "", 0, err
		}
		fields := strings.Fields(string(out))
		for i, f := range fields {
			if f == "via" && i+1 < len(fields) {
				gateway = fields[i+1]
			}
			if f == "dev" && i+1 < len(fields) {
				iface = fields[i+1]
			}
		}
		return gateway, iface, 0, nil
	case "darwin":
		out, err := oscmd.Command("route", "-n", "get", "default").Output()
		if err != nil {
			return "", "", 0, err
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "gateway:") {
				gateway = strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
			}
			if strings.HasPrefix(line, "interface:") {
				iface = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
			}
		}
		return gateway, iface, 0, nil
	case "windows":
		ps := `Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue |
Where-Object { $_.NextHop -and $_.NextHop -ne '0.0.0.0' } |
Sort-Object RouteMetric, InterfaceMetric |
Select-Object -First 1 NextHop,InterfaceAlias,InterfaceIndex |
ConvertTo-Json -Compress`
		out, err := oscmd.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps).Output()
		if err != nil {
			return "", "", 0, err
		}
		var r struct {
			NextHop        string
			InterfaceAlias string
			InterfaceIndex int
		}
		if err := json.Unmarshal(out, &r); err != nil {
			return "", "", 0, err
		}
		return r.NextHop, r.InterfaceAlias, r.InterfaceIndex, nil
	default:
		return "", "", 0, fmt.Errorf("unsupported OS")
	}
}
