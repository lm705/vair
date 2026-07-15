//go:build !windows

package main

// listRunningProcessNames is Windows-only for now (toolhelp snapshot).
func listRunningProcessNames() []string { return nil }
