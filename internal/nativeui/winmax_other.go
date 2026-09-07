//go:build !windows

package nativeui

// maximizeWindow is a no-op on platforms other than Windows: fyne's public API
// has no maximize, and the configured default window size is used instead.
func maximizeWindow(string) {}
