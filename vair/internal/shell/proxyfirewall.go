package shell

import "vair/core"

// ApplyProxyFirewall opens or closes the inbound firewall rule for the proxy
// ports, following the address they listen on: open when other devices are
// meant to reach them — proxy mode with LAN access, or TUN-mode sharing, which
// listens on the same address — closed when they listen on localhost. It used
// to follow the TUN sharing toggle alone, so proxy mode with "Allow LAN
// access" got no rule and other devices were dropped by the firewall. Called
// on the relevant settings change and once at startup.
func ApplyProxyFirewall() {
	if core.ProxyOpenToLAN() {
		h, s := core.ProxyPorts()
		EnsureProxyFirewallRule(h, s)
	} else {
		RemoveProxyFirewallRule()
	}
}
