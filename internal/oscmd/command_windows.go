//go:build windows

package oscmd

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func applyPlatformOptions(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
