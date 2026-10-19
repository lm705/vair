package shell

import "vair/core"

// ApplyTunProxyFirewall opens or closes the inbound firewall rule for the
// LAN-shared proxy ports based on the TunShareProxy toggle. Called on the
// relevant settings change and once at startup.
func ApplyTunProxyFirewall() {
	if core.TunShareProxyEnabled() {
		h, s := core.ProxyPorts()
		EnsureProxyFirewallRule(h, s)
	} else {
		RemoveProxyFirewallRule()
	}
}
