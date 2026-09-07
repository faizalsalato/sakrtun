//go:build windows

package nativeui

import (
	"syscall"
	"time"
	"unsafe"
)

const swMaximize = 3

var (
	user32                       = syscall.NewLazyDLL("user32.dll")
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procShowWindow               = user32.NewProc("ShowWindow")
	procGetCurrentProcessID      = kernel32.NewProc("GetCurrentProcessId")
	procIsZoomed                 = user32.NewProc("IsZoomed")
)

// enumFound receives the HWND of the main window (the one matching the
// expected title) owned by this process. EnumWindows is synchronous, so no
// locking is needed.
var (
	enumFound  uintptr
	enumTarget string
)

func enumWindowsProc(hwnd, lParam uintptr) uintptr {
	var pid uint32
	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if uintptr(pid) != lParam {
		return 1 // keep enumerating
	}
	buf := make([]uint16, 256)
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || syscall.UTF16ToString(buf[:n]) != enumTarget {
		// Helper windows (for example the GLFW message window) belong to the
		// same process; only the window with the app title is the real one.
		return 1
	}
	enumFound = hwnd
	return 0 // stop enumeration
}

// maximizeWindow waits for the main window of this process and then maximizes
// it, so the app opens using the full "normal" screen area (with the taskbar
// visible). Fyne's public API only offers SetFullScreen; a true maximize is
// done through Win32 here. It returns immediately and works in a background
// goroutine because the window only exists after ShowAndRun.
func maximizeWindow(title string) {
	enumTarget = title
	go func() {
		cb := syscall.NewCallback(enumWindowsProc)
		pid, _, _ := procGetCurrentProcessID.Call()

		var hwnd uintptr
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) && hwnd == 0 {
			enumFound = 0
			procEnumWindows.Call(cb, pid)
			hwnd = enumFound
			if hwnd == 0 {
				time.Sleep(150 * time.Millisecond)
			}
		}
		if hwnd == 0 {
			return
		}

		// Maximize and keep re-asserting it for a few seconds: fyne/glfw may
		// resize the window right after showing it, which undoes the first
		// maximize. Stop as soon as the window reports the zoomed state.
		for i := 0; i < 20; i++ {
			procShowWindow.Call(hwnd, swMaximize)
			time.Sleep(300 * time.Millisecond)
			zoomed, _, _ := procIsZoomed.Call(hwnd)
			if zoomed != 0 {
				return
			}
		}
	}()
}
