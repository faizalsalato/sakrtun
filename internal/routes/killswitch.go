package routes

import (
	"fmt"
	"runtime"
	"strconv"
)

// BlockInternet removes the machine's default routes so no traffic can leave
// outside the VPN. Hosts listed in allowHosts keep working through explicit
// routes over the physical gateway, so the app can still reach (and reconnect
// to) the VPN server while everything else stays offline.
//
// The returned Cleanup restores the original routing when Run() is called.
// Calling BlockInternet again while a block is still active is safe: route
// removal is idempotent and the previous Cleanup must simply be Run() first.
func BlockInternet(allowHosts []string, logger Logger) (*Cleanup, error) {
	cleanup := &Cleanup{logger: logger}
	logger.Add("warn", "kill switch: blocking internet access outside the VPN")
	switch runtime.GOOS {
	case "windows":
		blockWindows(allowHosts, cleanup, logger)
	case "linux":
		blockLinux(allowHosts, cleanup, logger)
	case "darwin":
		blockDarwin(allowHosts, cleanup, logger)
	default:
		return cleanup, fmt.Errorf("kill switch is not implemented for %s", runtime.GOOS)
	}
	return cleanup, nil
}

func blockWindows(allowHosts []string, cleanup *Cleanup, logger Logger) {
	gw, _, err := defaultGateway()
	if err != nil || gw == "" {
		logger.Add("warn", "kill switch: cannot detect IPv4 default gateway: %v", err)
	}
	if gw != "" {
		if err := run(logger, "route", "delete", "0.0.0.0", "mask", "0.0.0.0"); err == nil {
			cleanup.commands = append(cleanup.commands, []string{"route", "add", "0.0.0.0", "mask", "0.0.0.0", gw, "metric", "1"})
			addBypassWindows(allowHosts, gw, cleanup, logger)
		}
	}

	gw6, iface6, err := defaultGatewayIPv6()
	if err != nil || gw6 == "" {
		logger.Add("warn", "kill switch: cannot detect IPv6 default gateway: %v", err)
	}
	if gw6 != "" {
		if err := run(logger, "route", "-6", "delete", "::/0"); err == nil {
			restore := []string{"route", "-6", "add", "::/0", gw6}
			if idx, convErr := strconv.Atoi(iface6); convErr == nil && idx > 0 {
				restore = append(restore, "if", fmt.Sprint(idx))
				addBypassWindowsIPv6(allowHosts, gw6, idx, cleanup, logger)
			}
			cleanup.commands = append(cleanup.commands, restore)
		}
	}
}

func blockLinux(allowHosts []string, cleanup *Cleanup, logger Logger) {
	gw, iface, err := defaultGateway()
	if err != nil || gw == "" {
		logger.Add("warn", "kill switch: cannot detect IPv4 default gateway: %v", err)
	}
	if gw != "" {
		if err := run(logger, "ip", "route", "del", "default"); err == nil {
			restore := []string{"ip", "route", "add", "default", "via", gw}
			if iface != "" {
				restore = append(restore, "dev", iface)
			}
			cleanup.commands = append(cleanup.commands, restore)
			addBypassLinux(allowHosts, gw, iface, cleanup, logger)
		}
	}

	gw6, iface6, err := defaultGatewayIPv6()
	if err != nil || gw6 == "" {
		logger.Add("warn", "kill switch: cannot detect IPv6 default gateway: %v", err)
	}
	if gw6 != "" {
		if err := run(logger, "ip", "-6", "route", "del", "default"); err == nil {
			restore := []string{"ip", "-6", "route", "add", "default", "via", gw6}
			if iface6 != "" {
				restore = append(restore, "dev", iface6)
			}
			cleanup.commands = append(cleanup.commands, restore)
			addBypassLinuxIPv6(allowHosts, gw6, iface6, cleanup, logger)
		}
	}
}

func blockDarwin(allowHosts []string, cleanup *Cleanup, logger Logger) {
	gw, _, err := defaultGateway()
	if err != nil || gw == "" {
		logger.Add("warn", "kill switch: cannot detect IPv4 default gateway: %v", err)
	}
	if gw != "" {
		if err := run(logger, "route", "delete", "default"); err == nil {
			cleanup.commands = append(cleanup.commands, []string{"route", "add", "default", gw})
			addBypassDarwin(allowHosts, gw, cleanup, logger)
		}
	}

	gw6, _, err := defaultGatewayIPv6()
	if err != nil || gw6 == "" {
		logger.Add("warn", "kill switch: cannot detect IPv6 default gateway: %v", err)
	}
	if gw6 != "" {
		if err := run(logger, "route", "delete", "-inet6", "default"); err == nil {
			cleanup.commands = append(cleanup.commands, []string{"route", "add", "-inet6", "default", gw6})
			addBypassDarwinIPv6(allowHosts, gw6, cleanup, logger)
		}
	}
}
