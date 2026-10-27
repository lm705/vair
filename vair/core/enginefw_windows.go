//go:build windows

package core

import "vair/helper/winsys"

// The inbound door for the engine when the tunnel is not carrying its own TCP.
//
// With the gvisor stack, sing-box implements TCP in user space: nothing on the
// machine ever listens, and Windows Firewall has nothing to have an opinion
// about. The system and mixed stacks hand that work back to the operating
// system — which means real sockets, which means the firewall's default
// "block inbound unless allowed" applies to them. The tunnel then comes up,
// reports itself connected, and carries nothing, with the firewall silently in
// the middle. The same is documented elsewhere for the same two stacks.
//
// Scoped to the tunnel interface rather than the whole program. A rule that
// merely names the binary would also open sing-box's LAN-sharing listeners on
// whatever network the machine is attached to, and those are deliberately
// limited to private and domain profiles. Any profile is right here: a tunnel
// adapter is an unidentified network, which Windows files under Public.
//
// And a group of its own, deliberately: the kill switch clears its whole group
// before installing the allow list for a new tunnel, so a rule filed under that
// name is created and swept away in the same breath — which is what happened
// the first time this was written.
//
// sing-box registers a rule for itself as well — "sing-tun (…\sing-box.exe)",
// on the system and mixed stacks — and it is the broader of the two: the whole
// program, any port, any address, every profile, Public included, and it stays
// after the session and after Vair. With the proxy shared over the LAN that
// opened sing-box's listeners to any network the machine joined, past the
// local-subnet, private-network rule that sharing is meant to have. So that
// one is removed as soon as ours is in place, and again when the session ends;
// ours covers the tunnel by itself. Only the rule for Vair's own sing-box: one
// another program's copy registered is not ours to touch.
// ensureEngineFirewallRule opens it for iface (winsys.EnsureEngineRule).
// Best-effort, like every other firewall call here: without elevation netsh
// and PowerShell simply refuse, and TUN mode does not run without elevation
// anyway.
func ensureEngineFirewallRule(iface string) {
	if serviceMode() {
		return // the service opens it with the tunnel it runs
	}
	singbox := SingboxPath()
	if singbox == "" || iface == "" {
		return
	}
	if err := winsys.EnsureEngineRule(singbox, iface); err != nil {
		vlog("warning", "firewall: could not open the inbound rule for the engine: %v", err)
	}
}

// removeEngineFirewallRule closes it again, with the rule sing-box registered
// for Vair's binary. Idempotent.
func removeEngineFirewallRule() {
	if serviceMode() {
		return
	}
	winsys.RemoveEngineRule(SingboxPath())
}

// engineNeedsFirewallRule reports whether the stack about to run terminates
// connections in the operating system, and therefore needs the door open.
func engineNeedsFirewallRule() bool { return tunStack() != "gvisor" }
