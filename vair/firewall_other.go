//go:build !windows

package main

// Firewall rule management is Windows-only; no-op elsewhere.
func ensureFirewallRule(int) {}
func removeFirewallRule()     {}
