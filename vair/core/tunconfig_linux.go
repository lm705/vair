package core

import (
	"os"
	"path/filepath"
	"strings"

	"vair/helper/api"
)

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

		// gvisor is a userspace network stack: portable, and slower. On Linux
		// "mixed" uses the kernel for TCP and gvisor for UDP, which is what
		// sing-box recommends here.
		in["stack"] = "mixed"

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

// resolveRuleSets rewrites every rule_set path to one the helper will accept.
//
// For each set, in order of preference: hand the app's own (refreshed) file to
// the helper via stage and point at where the helper put it; fall back to the
// packaged copy; drop the set. Dropping matters — sing-box treats a rule_set
// path that does not resolve as FATAL, so a missing file would turn a stale
// country list into a tunnel that refuses to start. A dropped country simply
// takes the default route, which is what already happens when a refresh fails.
func resolveRuleSets(cfg map[string]interface{}, stage func(name string, data []byte) error) {
	route, _ := cfg["route"].(map[string]interface{})
	if route == nil {
		return
	}
	sets, _ := route["rule_set"].([]interface{})
	kept := make([]interface{}, 0, len(sets))
	for _, raw := range sets {
		set, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		p, _ := set["path"].(string)
		if p == "" {
			kept = append(kept, set) // a remote set: no file of ours involved
			continue
		}
		name := filepath.Base(strings.ReplaceAll(p, "\\", "/"))

		if data, err := os.ReadFile(p); err == nil && len(data) > 0 && len(data) <= api.MaxRuleSetBytes {
			if err := stage(name, data); err == nil {
				set["path"] = api.RuleSetPath(name)
				kept = append(kept, set)
				continue
			} else {
				// Worth a line: the tunnel will still come up on the packaged
				// rules, so this would otherwise be a silent downgrade to
				// whatever shipped with the release.
				vlog("warn", "rule-set %s could not be staged (%v) — using the packaged copy", name, err)
			}
		}

		packaged := tunHelperRuleSetDir + "/" + name
		if !fileExists(packaged) {
			continue
		}
		set["path"] = packaged
		kept = append(kept, set)
	}
	if len(kept) == 0 {
		delete(route, "rule_set")
	} else {
		route["rule_set"] = kept
	}
}
