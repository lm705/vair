//go:build windows

package core

// updateApplyBlocked returns why an update cannot be installed, or "" when it
// can. Windows replaces the running exe in place, which is what the manifest's
// asset is, so nothing blocks it.
func updateApplyBlocked() string { return "" }
