//go:build !windows

package core

// Linux has no equivalent: the helper's nftables rules already decide what
// reaches the tunnel, and nothing there blocks a local listener by default.
func ensureEngineFirewallRule(string) {}
func removeEngineFirewallRule()       {}
func engineNeedsFirewallRule() bool   { return false }
