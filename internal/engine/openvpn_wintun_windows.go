//go:build windows

package engine

import (
	"golang.zx2c4.com/wintun"
)

// ensureWintunAdapterForOpenVPN makes sure a Wintun adapter named "Wintun"
// exists. OpenVPN 2.6 with --windows-driver wintun reuses existing Wintun
// adapters (it hardcodes the "Wintun" adapter name), so the app creates one
// through the Wintun API when it is missing.
func ensureWintunAdapterForOpenVPN() error {
	if _, err := wintun.OpenAdapter("Wintun"); err == nil {
		return nil
	}
	_, err := wintun.CreateAdapter("Wintun", "OpenVPN", nil)
	return err
}
