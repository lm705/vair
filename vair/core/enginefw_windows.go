//go:build windows

package core

import "fmt"

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
// sing-box registers a rule for itself as well — "sing-tun (…\sing-box.exe)"
// is visible in the firewall on any machine that has run a tunnel — and it is
// the broader of the two: the whole program, every address. This one stays
// anyway, and is not merely a duplicate of it. It is ours to remove when the
// tunnel goes (the engine's outlives every session, on a machine that may never
// run one again), it is scoped to the tunnel interface, and it does not depend
// on the engine having managed to register anything: the case this exists for
// is a tunnel that comes up and carries nothing, which is exactly the shape of
// a machine where that registration silently failed.
const (
	engineFirewallRule  = "Vair TUN engine"
	engineFirewallGroup = "Vair tunnel engine"
)

// ensureEngineFirewallRule opens it for iface. Best-effort, like every other
// firewall call here: without elevation netsh and PowerShell simply refuse, and
// TUN mode does not run without elevation anyway.
func ensureEngineFirewallRule(iface string) {
	singbox := func() string {
		state.mu.RLock()
		defer state.mu.RUnlock()
		return state.singboxBin
	}()
	if singbox == "" || iface == "" {
		return
	}
	removeEngineFirewallRule()
	script := fmt.Sprintf(
		"New-NetFirewallRule -DisplayName '%s' -Group '%s' -Direction Inbound -Action Allow "+
			"-Program '%s' -InterfaceAlias '%s' -Profile Any | Out-Null",
		engineFirewallRule, engineFirewallGroup, psQuote(singbox), psQuote(iface))
	if _, err := psOutput(script); err != nil {
		vlog("warning", "firewall: could not open the inbound rule for the engine: %v", err)
	}
}

// removeEngineFirewallRule closes it again. Idempotent — a rule that is not
// there is not an error.
func removeEngineFirewallRule() {
	psOutput(fmt.Sprintf( //nolint:errcheck
		"Remove-NetFirewallRule -DisplayName '%s' -ErrorAction SilentlyContinue", engineFirewallRule))
}

// engineNeedsFirewallRule reports whether the stack about to run terminates
// connections in the operating system, and therefore needs the door open.
func engineNeedsFirewallRule() bool { return tunStack() != "gvisor" }
