package core

import (
	"time"

	"vair/helper/winsys"
)

// The PowerShell runner and the Windows Firewall profile operations live in
// helper/winsys, which the helper service runs too. These are the names the
// rest of core has always called them by.

const nlPS = winsys.NL

func psQuote(s string) string                { return winsys.PSQuote(s) }
func psOutput(script string) (string, error) { return winsys.PS(script) }
func psErrorText(out string) string          { return winsys.PSErrorText(out) }
func isFirewallProfileName(s string) bool    { return winsys.IsProfileName(s) }
func isOutboundAction(s string) bool         { return winsys.IsOutboundAction(s) }
func parseFirewallState(out string) ([]string, map[string]fwProfile) {
	return winsys.ParseFirewallState(out)
}

func psOutputFor(script string, limit time.Duration) (string, error) {
	return winsys.PSFor(script, limit)
}

// fwProfile is one Windows firewall profile as it matters here.
type fwProfile = winsys.Profile

// The firewall-profile operations behind the kill-switch's test seams. With
// the helper service installed they are nothing for the app to do: taking the
// persistent block down through the service (removePersistentBlock) takes a
// 2.6.0 firewall block down with it, and the app has no rights to the
// firewall's profiles anyway.
var (
	setOutboundActionFor = func(profile, action string) error {
		if serviceMode() {
			return nil
		}
		return winsys.SetOutboundFor(profile, action)
	}
	setOutboundAction = func(action string) error {
		if serviceMode() {
			return nil
		}
		return winsys.SetOutbound(action)
	}
	removeKillSwitchRules = func() {
		if !serviceMode() {
			winsys.RemoveLegacyRules()
		}
	}
)
