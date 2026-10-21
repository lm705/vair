//go:build windows

package core

// Undoing Vair on the way out.
//
// Every other uninstaller deletes files. This one has to give the machine back
// first, because a VPN client is not only files: while it runs it can be holding
// the Windows Firewall at "block every outbound connection" (the kill switch),
// a system-wide proxy setting pointing at a listener that is about to vanish,
// and a TUN adapter with the default route through it. Remove the program with
// those in place and the user is left with no internet and nothing left on disk
// that knows how to fix it.
//
// The work itself is not new code: engaging any of those writes a marker, and
// the same markers are what the crash-recovery path at startup reads. Uninstall
// is the same recovery, asked for on purpose — which is why this file is short,
// and why it must stay that way. Anything that can leave the machine changed
// gets a marker and a release, and then it is already handled here.

// UninstallCleanup releases everything Vair changed outside its own folders and
// returns a line per step, for the uninstaller's log and for a bug report.
//
// It runs in a process of its own (Vair.exe --uninstall-cleanup) started by the
// uninstaller, elevated: the firewall parts need administrator, and the answer
// to "what if it isn't" is to say so rather than to fail silently.
func UninstallCleanup() []string {
	var out []string

	if killSwitchHeld() {
		releaseKillSwitch()
		if killSwitchHeld() {
			out = append(out, "kill switch: COULD NOT RELEASE — the firewall is still blocking outbound traffic; run as administrator")
		} else {
			out = append(out, "kill switch: released, the firewall is back to what it was")
		}
	} else {
		out = append(out, "kill switch: not engaged")
	}

	if _, held := readProxyLock(); held {
		clearStaleProxy()
		out = append(out, "system proxy: restored")
	} else {
		out = append(out, "system proxy: not set by Vair")
	}

	removeEngineFirewallRule()
	out = append(out, "firewall: the engine's inbound rule is gone")

	removeTUNAdapter()
	out = append(out, "TUN adapters: swept")

	return out
}
