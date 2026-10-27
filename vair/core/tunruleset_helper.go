package core

import (
	"os"
	"path/filepath"
	"strings"

	"vair/helper/api"
)

// resolveRuleSets rewrites every rule_set path to one the helper will accept.
//
// For each set, in order of preference: hand the app's own (refreshed) file to
// the helper via stage and point at where the helper put it; fall back to the
// packaged copy where the helper can read one (Linux); drop the set. Dropping matters — sing-box treats a rule_set
// path that does not resolve as FATAL, so a missing file would turn a stale
// country list into a tunnel that refuses to start. A dropped country simply
// takes the default route, which is what already happens when a refresh fails.
func resolveRuleSets(cfg map[string]interface{}, stage func(name string, data []byte) (string, error)) {
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
			if staged, err := stage(name, data); err == nil {
				set["path"] = staged
				kept = append(kept, set)
				continue
			} else {
				// Worth a line: the tunnel will still come up on the packaged
				// rules, so this would otherwise be a silent downgrade to
				// whatever shipped with the release.
				vlog("warn", "rule-set %s could not be staged (%v) — using the packaged copy", name, err)
			}
		}

		packaged := packagedRuleSet(name)
		if packaged == "" {
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
