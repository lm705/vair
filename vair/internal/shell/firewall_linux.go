package shell

// Firewall rule management is Windows-only; no-op elsewhere.
func EnsureFirewallRule(int)           {}
func RemoveFirewallRule()              {}
func EnsureProxyFirewallRule(int, int) {}
func RemoveProxyFirewallRule()         {}
