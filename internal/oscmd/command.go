package oscmd

import (
	"context"
	"os/exec"
)

// Command creates an exec.Cmd with platform-specific defaults that are safe for
// the GUI application. On Windows, the implementation hides child console
// windows so helpers like powershell.exe, netsh.exe, route.exe, xray.exe, and
// dnstt-client.exe do not flash a terminal window.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	applyPlatformOptions(cmd)
	return cmd
}

// CommandContext is the context-aware version of Command.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	applyPlatformOptions(cmd)
	return cmd
}
