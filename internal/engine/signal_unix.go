//go:build !windows

package engine

import "os"

func ioSignalInterrupt() os.Signal { return os.Interrupt }
