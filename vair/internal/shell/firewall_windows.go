//go:build windows

package shell

import (
	"os"

	"vair/core"
	"vair/helper/winsys"
)

// Windows Firewall inbound rules for the LAN remote server and the LAN-shared
// proxy. The rules themselves — names, scope, profiles — are in helper/winsys,
// which the helper service runs too; these decide which programs they name.
// With the helper service installed the service makes them, without the app
// needing rights; without it this is best-effort: adding a firewall rule needs
// admin, so if Vair isn't elevated these no-op and Windows asks or blocks.

// EnsureFirewallRule (re)creates the inbound allow rule for the web interface
// on port, for this program only.
func EnsureFirewallRule(port int) {
	if core.ServiceMode() {
		core.ServiceFirewallRemote(port)
		return
	}
	exe, _ := os.Executable()
	winsys.EnsureRemoteRule(port, exe)
}

// RemoveFirewallRule deletes the rule (idempotent).
func RemoveFirewallRule() {
	if core.ServiceMode() {
		core.ServiceFirewallRemote(0)
		return
	}
	winsys.RemoveRemoteRule()
}

// EnsureProxyFirewallRule (re)creates the rules for the LAN-shared proxy: TCP
// on both ports, and UDP for Vair's own relay (proxy mode) and sing-box (TUN
// mode).
func EnsureProxyFirewallRule(httpPort, socksPort int) {
	if core.ServiceMode() {
		core.ServiceFirewallLAN(httpPort, socksPort)
		return
	}
	exe, _ := os.Executable()
	winsys.EnsureLANProxyRule(httpPort, socksPort, core.SingboxPath(), exe)
}

// RemoveProxyFirewallRule deletes them (idempotent).
func RemoveProxyFirewallRule() {
	if core.ServiceMode() {
		core.ServiceFirewallLAN(0, 0)
		return
	}
	winsys.RemoveLANProxyRule()
}
