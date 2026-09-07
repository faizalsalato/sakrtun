//go:build !windows

package oscmd

import "os/exec"

func applyPlatformOptions(cmd *exec.Cmd) {}
