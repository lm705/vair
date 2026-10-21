//go:build !windows

package core

// The firewall block is Windows-only for now, so there is nothing to read back.
func verifyBlockStanding() string { return "" }
