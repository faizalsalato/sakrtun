//go:build !windows

package engine

// ensureWintunAdapterForOpenVPN is a no-op on non-Windows systems.
func ensureWintunAdapterForOpenVPN() error {
	return nil
}
