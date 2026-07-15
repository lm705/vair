//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

// Windows Firewall inbound rule for the LAN remote server. Without it, the
// default firewall silently drops inbound TCP from OTHER hosts (the phone) even
// though the server is listening — a loopback/local Test-NetConnection still
// succeeds, which is why "it works from the PC but not the phone" is confusing.
// We add a narrow allow rule (this one TCP port) when remote access is enabled
// and remove it when disabled. Best-effort: adding a firewall rule needs admin,
// so if Vair isn't elevated these no-op and the user adds the rule by hand.

const fwRuleName = "Vair Remote Access"

// ensureFirewallRule (re)creates the inbound allow rule for the given port.
func ensureFirewallRule(port int) {
	// Delete any stale rule first so a port change doesn't leave the old one open.
	removeFirewallRule()
	netsh("advfirewall", "firewall", "add", "rule",
		"name="+fwRuleName, "dir=in", "action=allow",
		"protocol=TCP", fmt.Sprintf("localport=%d", port),
		"profile=private,domain") // trusted networks only — not public Wi-Fi
}

// removeFirewallRule deletes the rule (idempotent — ignores "no rule" errors).
func removeFirewallRule() {
	netsh("advfirewall", "firewall", "delete", "rule", "name="+fwRuleName)
}

// netsh runs a netsh command with no console window, ignoring failures (e.g. not
// elevated). netsh itself is trusted; args are program-controlled constants.
func netsh(args ...string) {
	cmd := exec.Command("netsh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
}
