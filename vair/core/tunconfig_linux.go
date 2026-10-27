package core

// Adapting a TUN configuration for the privileged helper.
//
// The rest of the engine code builds a configuration shaped by what Windows
// needs, because that is the platform it was written for. Four of those choices
// are wrong here, and rather than thread platform conditionals through builders
// that are already dense, this rewrites them in one place afterwards. The
// transform is small, mechanical, and — because the helper validates what it
// receives — testable against the exact thing that will judge it.

// tunHelperRuleSetDir is the packaged copy of the country rules — the fallback,
// not the first choice.
//
// The app's own ~/.local/share/vair/rulesets is refreshed from upstream and is
// therefore the one worth using, but it is also writable by the user, which is
// precisely the account the helper does not trust: a config pointing root's
// engine at a file that account can rewrite is the hole the validator exists to
// close. So the bytes are handed to the helper instead, and it reads them back
// out of a directory only root can write. This directory stays as the answer
// for a set the app cannot read.
const tunHelperRuleSetDir = "/usr/lib/vair/rulesets"

// tunIfaceName is fixed, unlike the Windows "vair-<unixtime>".
//
// A unique name per session is a Windows answer to stale adapters. Here the
// helper owns the interface and removes it on teardown, and the validator
// requires a name it recognises — an arbitrary one would let a caller have root
// reconfigure some other interface.
func tunIfaceName() string { return "vair0" }

// adaptTUNConfig rewrites a freshly built TUN configuration for the helper.
func adaptTUNConfig(cfg map[string]interface{}) {
	inbounds, _ := cfg["inbounds"].([]interface{})
	for _, raw := range inbounds {
		in, ok := raw.(map[string]interface{})
		if !ok || in["type"] != "tun" {
			continue
		}
		in["interface_name"] = tunIfaceName()

		// The stack stays the one the user chose (TUN TCP/IP stack), as on
		// Windows. It was forced to "mixed" here, so on Linux the setting did
		// nothing while the Session panel reported it as the one in use. The
		// choice also matters less here than there: with auto_redirect, TCP is
		// redirected to the engine by nftables and never reaches the stack,
		// which is left with UDP.

		// strict_route is a Windows WFP filter. On Linux the same field means
		// something different, and sing-box's own documentation warns against
		// combining it with auto_redirect — so the kill switch cannot simply be
		// carried over. auto_redirect installs nftables rules instead, which
		// also fixes two things the plain route takes away: ICMP (ping stops
		// working without it) and Docker's bridge networks.
		delete(in, "strict_route")
		in["auto_redirect"] = true
	}

	// Rule-set paths are left alone here — resolveRuleSets handles them, because
	// choosing between the staged copy and the packaged one needs a helper
	// connection and this function has none.
}

// packagedRuleSet is the copy of a set that shipped with the package, in a
// directory the helper trusts, or "" when there is none.
func packagedRuleSet(name string) string {
	p := tunHelperRuleSetDir + "/" + name
	if !fileExists(p) {
		return ""
	}
	return p
}
