//go:build !windows

package platformtun

import "strings"

type Logger interface {
	Add(level, format string, args ...any)
}

func Prepare(device, interfaceName string, mtu int, logger Logger) (string, string, func(), error) {
	device = strings.TrimSpace(device)
	interfaceName = strings.TrimSpace(interfaceName)
	return device, interfaceName, nil, nil
}
