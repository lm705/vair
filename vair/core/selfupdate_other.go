//go:build !windows

package core

// replaceAndRelaunch is Windows-only for now.
func replaceAndRelaunch(_ string) error { return nil }

// CleanupUpdateLeftovers is Windows-only for now.
func CleanupUpdateLeftovers() {}
