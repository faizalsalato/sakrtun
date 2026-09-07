//go:build windows

package platformtun

import (
	"strings"

	"socksrevivepc/internal/wintunloader"
)

type Logger interface {
	Add(level, format string, args ...any)
}

// Prepare validates and loads Wintun for the current process, then normalizes
// the device string used by tun2socks. It intentionally does not pre-open a
// WireGuard TUN adapter here: tun2socks must own the live adapter/session.
// Pre-opening and closing the adapter before tun2socks can make some Windows
// builds close/crash with no useful GUI error.
func Prepare(device, interfaceName string, mtu int, logger Logger) (string, string, func(), error) {
	if mtu <= 0 {
		mtu = 1500
	}

	if err := wintunloader.Prepare(logger); err != nil {
		return "", "", nil, err
	}

	device = normalizeWindowsDevice(device)
	interfaceName = normalizeWindowsInterface(interfaceName)
	if logger != nil {
		logger.Add("info", "Windows Wintun is ready: adapter=%s device=%s mtu=%d", interfaceName, device, mtu)
	}
	return device, interfaceName, nil, nil
}

// normalizeWindowsDevice returns the device model name expected by tun2socks v2
// on Windows. Values like "wintun://SocksRevive" are Linux-style URL device
// strings that made older builds search for a non-existent network interface
// and crash with "route ip+net: no such network interface", so every spelling
// maps to the plain "wintun" model.
func normalizeWindowsDevice(string) string {
	return "wintun"
}

func normalizeWindowsInterface(interfaceName string) string {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" || strings.EqualFold(interfaceName, "SocksRevive") {
		return "wintun"
	}
	return interfaceName
}
