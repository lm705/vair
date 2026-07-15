//go:build !windows

package core

// RestartAdmin is Windows-only (UAC elevation).
func RestartAdmin() {}
