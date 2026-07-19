package core

import (
	"net"
	"strings"
)

// User routing rules. The Settings "custom domains" chip-lists (direct / through
// VPN / block) accept more than plain domains now — each entry is classified so
// both engines route it correctly:
//
//   - plain domain            → suffix match ("domain:x" / domain_suffix)
//   - IP or CIDR              → ip / ip_cidr
//   - full:host               → exact domain match
//   - regexp:expr             → regex domain match
//   - domain:suffix           → suffix match (explicit)
//   - geosite:cat / geoip:cc  → xray only (passed through); skipped for sing-box,
//                               which needs a compiled rule-set for categories
//
// Rules are applied at a FIXED priority — block → direct → proxy → mode rules —
// so there is no rule-ordering UI to fuss with; that covers the real cases.

// ruleBuckets holds one action's matchers, pre-split per engine.
type ruleBuckets struct {
	xDomains []string // xray "domain" entries (already prefixed)
	xIPs     []string // xray "ip" entries (raw ip/cidr or geoip:cc)
	sSuffix  []string // sing-box domain_suffix
	sExact   []string // sing-box domain (exact)
	sRegex   []string // sing-box domain_regex
	sIPs     []string // sing-box ip_cidr (raw only)
}

func (b ruleBuckets) empty() bool {
	return len(b.xDomains) == 0 && len(b.xIPs) == 0 &&
		len(b.sSuffix) == 0 && len(b.sExact) == 0 && len(b.sRegex) == 0 && len(b.sIPs) == 0
}

// isIPOrCIDR reports whether s is a bare IP address or a CIDR block.
func isIPOrCIDR(s string) bool {
	if _, _, err := net.ParseCIDR(s); err == nil {
		return true
	}
	return net.ParseIP(s) != nil
}

// classifyRules sorts raw rule entries into per-engine matcher buckets.
func classifyRules(entries []string) ruleBuckets {
	var b ruleBuckets
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		lower := strings.ToLower(e)
		switch {
		case strings.HasPrefix(lower, "geoip:"):
			b.xIPs = append(b.xIPs, e) // xray-only category
		case strings.HasPrefix(lower, "geosite:"):
			b.xDomains = append(b.xDomains, e) // xray-only category
		case strings.HasPrefix(lower, "full:"):
			v := e[len("full:"):]
			b.xDomains = append(b.xDomains, "full:"+v)
			b.sExact = append(b.sExact, v)
		case strings.HasPrefix(lower, "regexp:"):
			v := e[len("regexp:"):]
			b.xDomains = append(b.xDomains, "regexp:"+v)
			b.sRegex = append(b.sRegex, v)
		case strings.HasPrefix(lower, "domain:"):
			v := e[len("domain:"):]
			b.xDomains = append(b.xDomains, "domain:"+v)
			b.sSuffix = append(b.sSuffix, v)
		case isIPOrCIDR(e):
			b.xIPs = append(b.xIPs, e)
			b.sIPs = append(b.sIPs, e)
		default: // plain domain → suffix match
			b.xDomains = append(b.xDomains, "domain:"+e)
			b.sSuffix = append(b.sSuffix, e)
		}
	}
	return b
}

// xrayRules emits xray routing rules for one action (outboundTag = "direct" /
// "proxy" / "block"). At most one domain rule + one ip rule.
func xrayRules(b ruleBuckets, tag string) []interface{} {
	var out []interface{}
	if len(b.xDomains) > 0 {
		out = append(out, map[string]interface{}{"type": "field", "domain": b.xDomains, "outboundTag": tag})
	}
	if len(b.xIPs) > 0 {
		out = append(out, map[string]interface{}{"type": "field", "ip": b.xIPs, "outboundTag": tag})
	}
	return out
}

// singboxRules emits sing-box route rules for one action. direct/proxy set
// "outbound"; block sets action "reject". Each matcher type is its OWN rule so
// they combine as OR (sing-box ANDs different fields within a single rule).
func singboxRules(b ruleBuckets, outbound string, block bool) []interface{} {
	mk := func(field string, vals []string) map[string]interface{} {
		r := map[string]interface{}{field: vals}
		if block {
			r["action"] = "reject"
		} else {
			r["outbound"] = outbound
		}
		return r
	}
	var out []interface{}
	if len(b.sSuffix) > 0 {
		out = append(out, mk("domain_suffix", b.sSuffix))
	}
	if len(b.sExact) > 0 {
		out = append(out, mk("domain", b.sExact))
	}
	if len(b.sRegex) > 0 {
		out = append(out, mk("domain_regex", b.sRegex))
	}
	if len(b.sIPs) > 0 {
		out = append(out, mk("ip_cidr", b.sIPs))
	}
	return out
}

// effectiveBlockRules returns the "block" rule list, or nil when toggled off.
func effectiveBlockRules() []string {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	if appSettings.BlockDomainsDisabled {
		return nil
	}
	return trimList(appSettings.BlockDomains)
}

func trimList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, d := range in {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}
