package core

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"unicode/utf8"
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
//
// An IP or CIDR entry matches the address a connection is made to. In TUN
// without FakeIP that is every connection: the program has resolved the name
// before it connects. In proxy mode, and in TUN with FakeIP, a program that
// asks for a site by name hands over only the name, and the rule does not see
// an address — resolving every name in advance to compare it would cost a
// lookup per site and show the names to one more resolver. The settings say
// so instead.

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

// ruleMatcher is one written line as the engines receive it: the sing-box
// matcher and its value, and the xray entry. A category (geosite:, geoip:) has
// no sing-box matcher — key is "" — and xrayIP says which xray field it goes in.
type ruleMatcher struct {
	key    string // domain, domain_suffix, domain_regex, ip_cidr; "" for a category
	value  string
	xray   string
	xrayIP bool
}

// errRuleHost and errRuleRegexp are what a line can be wrong with. The settings
// screen shows them in its own words, so they are kinds, not prose.
var (
	errRuleHost   = errors.New("not a domain name, IP address or CIDR")
	errRuleRegexp = errors.New("not a valid regular expression")
)

// parseRule reads one written line into the matcher the engines get, the same
// way every time: for the configuration, for the DNS rules that follow it, for
// the rule counters, and for the settings screen when the line is typed.
//
// What people paste is taken for what they meant: a link is its host
// ("https://example.com/page" is example.com), "*.example.com" and
// ".example.com" are example.com (a plain entry already covers every
// subdomain), a port is dropped, and a name in another script is written the
// way the engines see it on the wire (сайт.рф is xn--80aswg.xn--p1ai). Those
// used to go in as typed and never matched anything.
//
// A regexp: line is checked with Go's regexp package — the one both engines
// use. A line that does not compile is an error: sing-box refused to start on
// one (FATAL, so the TUN connection failed), and xray skipped it in silence.
func parseRule(entry string) (ruleMatcher, error) {
	e := strings.TrimSpace(entry)
	lower := strings.ToLower(e)
	cut := func(prefix string) string { return strings.TrimSpace(e[len(prefix):]) }
	switch {
	case e == "":
		return ruleMatcher{}, errRuleHost
	case strings.HasPrefix(lower, "geoip:"):
		return ruleMatcher{xray: lower, xrayIP: true}, nil
	case strings.HasPrefix(lower, "geosite:"):
		return ruleMatcher{xray: lower}, nil
	case strings.HasPrefix(lower, "regexp:"):
		v := cut("regexp:")
		if v == "" {
			return ruleMatcher{}, errRuleRegexp
		}
		if _, err := regexp.Compile(v); err != nil {
			return ruleMatcher{}, fmt.Errorf("%w: %v", errRuleRegexp, err)
		}
		return ruleMatcher{key: "domain_regex", value: v, xray: "regexp:" + v}, nil
	case strings.HasPrefix(lower, "full:"):
		v, err := cleanRuleHost(cut("full:"))
		if err != nil {
			return ruleMatcher{}, err
		}
		return ruleMatcher{key: "domain", value: v, xray: "full:" + v}, nil
	case strings.HasPrefix(lower, "domain:"):
		v, err := cleanRuleHost(cut("domain:"))
		if err != nil {
			return ruleMatcher{}, err
		}
		return ruleMatcher{key: "domain_suffix", value: v, xray: "domain:" + v}, nil
	}
	if isIPOrCIDR(e) {
		return ruleMatcher{key: "ip_cidr", value: e, xray: e, xrayIP: true}, nil
	}
	v, err := cleanRuleHost(e)
	if err != nil {
		return ruleMatcher{}, err
	}
	if isIPOrCIDR(v) { // a link to an address: "http://1.2.3.4/"
		return ruleMatcher{key: "ip_cidr", value: v, xray: v, xrayIP: true}, nil
	}
	return ruleMatcher{key: "domain_suffix", value: v, xray: "domain:" + v}, nil
}

// cleanRuleHost turns what was written for a site into the name the engines
// match: scheme, user, path, port and wildcard dropped, lower case, and IDN
// labels in their ASCII form.
func cleanRuleHost(v string) (string, error) {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, "://"); i >= 0 {
		v = v[i+3:]
	}
	if i := strings.IndexAny(v, "/?#"); i >= 0 {
		v = v[:i]
	}
	if i := strings.LastIndex(v, "@"); i >= 0 {
		v = v[i+1:]
	}
	if h, _, err := net.SplitHostPort(v); err == nil {
		v = h
	}
	v = strings.Trim(v, "[]")
	if isIPOrCIDR(v) {
		return v, nil
	}
	v = strings.TrimPrefix(v, "*.")
	v = strings.Trim(v, ".")
	v = strings.ToLower(v)
	if v == "" {
		return "", errRuleHost
	}
	labels := strings.Split(v, ".")
	for i, l := range labels {
		if l == "" {
			return "", errRuleHost
		}
		if !isASCII(l) {
			a, err := punycode(l)
			if err != nil {
				return "", errRuleHost
			}
			l = "xn--" + a
			labels[i] = l
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return "", errRuleHost
			}
		}
	}
	return strings.Join(labels, "."), nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// punycode is RFC 3492's encoder, for one label already in lower case: what an
// internationalised name is on the wire, and so in SNI and in the Host header,
// which is what the engines compare against.
func punycode(label string) (string, error) {
	const (
		base, tmin, tmax, skew, damp = 36, 1, 26, 38, 700
		initialBias, initialN        = 72, 128
	)
	digit := func(d int) byte {
		if d < 26 {
			return byte('a' + d)
		}
		return byte('0' + d - 26)
	}
	adapt := func(delta, numPoints int, first bool) int {
		if first {
			delta /= damp
		} else {
			delta /= 2
		}
		delta += delta / numPoints
		k := 0
		for delta > ((base-tmin)*tmax)/2 {
			delta /= base - tmin
			k += base
		}
		return k + (base-tmin+1)*delta/(delta+skew)
	}
	runes := []rune(label)
	var out []byte
	for _, r := range runes {
		if r < 0x80 {
			out = append(out, byte(r))
		}
	}
	basic := len(out)
	h := basic
	if basic > 0 {
		out = append(out, '-')
	}
	n, delta, bias := initialN, 0, initialBias
	for h < len(runes) {
		m := -1
		for _, r := range runes {
			if int(r) >= n && (m < 0 || int(r) < m) {
				m = int(r)
			}
		}
		if m < 0 || (m-n) > (1<<30)/(h+1) {
			return "", errRuleHost
		}
		delta += (m - n) * (h + 1)
		n = m
		for _, r := range runes {
			if int(r) < n {
				delta++
			}
			if int(r) == n {
				q := delta
				for k := base; ; k += base {
					t := k - bias
					if t < tmin {
						t = tmin
					} else if t > tmax {
						t = tmax
					}
					if q < t {
						break
					}
					out = append(out, digit(t+(q-t)%(base-t)))
					q = (q - t) / (base - t)
				}
				out = append(out, digit(q))
				bias = adapt(delta, h+1, h == basic)
				delta = 0
				h++
			}
		}
		delta++
		n++
	}
	return string(out), nil
}

// RuleCheck is the settings screen's answer for one typed line: the line as it
// will be stored, or the kind of mistake in it ("host" or "regexp") and the
// engine's own words for a regexp.
type RuleCheck struct {
	Entry  string `json:"entry"`
	Value  string `json:"value"`
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// CheckRules reads typed lines the way the configuration will (parseRule) and
// returns each in the form to keep. Plain entries stay plain: "*.example.com"
// comes back as "example.com", not "domain:example.com", which is what the
// person would have written had they known.
func CheckRules(entries []string) []RuleCheck {
	out := make([]RuleCheck, 0, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		c := RuleCheck{Entry: e}
		m, err := parseRule(e)
		switch {
		case errors.Is(err, errRuleRegexp):
			c.Error = "regexp"
			if s := err.Error(); strings.Contains(s, ": ") {
				c.Detail = s[strings.Index(s, ": ")+2:]
			}
		case err != nil:
			c.Error = "host"
		default:
			c.Value = storedRuleForm(e, m)
		}
		out = append(out, c)
	}
	return out
}

// storedRuleForm is the line to keep for a parsed entry: its prefix as written,
// lower case, and the cleaned value.
func storedRuleForm(entry string, m ruleMatcher) string {
	lower := strings.ToLower(strings.TrimSpace(entry))
	switch {
	case m.key == "":
		return m.xray
	case strings.HasPrefix(lower, "regexp:"):
		return "regexp:" + m.value
	case strings.HasPrefix(lower, "full:"):
		return "full:" + m.value
	case strings.HasPrefix(lower, "domain:"):
		return "domain:" + m.value
	}
	return m.value
}

// invalidRules returns the lines of a list that cannot be rules, for the log:
// they are left out of the configuration rather than break it.
func invalidRules(entries []string) []string {
	var bad []string
	for _, e := range entries {
		if strings.TrimSpace(e) == "" {
			continue
		}
		if _, err := parseRule(e); err != nil {
			bad = append(bad, strings.TrimSpace(e))
		}
	}
	return bad
}

// classifyRules sorts raw rule entries into per-engine matcher buckets. A line
// that cannot be a rule is left out (see parseRule); the log names it once per
// connection (armRuleStats).
func classifyRules(entries []string) ruleBuckets {
	var b ruleBuckets
	for _, e := range entries {
		m, err := parseRule(e)
		if err != nil {
			continue
		}
		if m.xrayIP {
			b.xIPs = append(b.xIPs, m.xray)
		} else {
			b.xDomains = append(b.xDomains, m.xray)
		}
		switch m.key {
		case "domain_suffix":
			b.sSuffix = append(b.sSuffix, m.value)
		case "domain":
			b.sExact = append(b.sExact, m.value)
		case "domain_regex":
			b.sRegex = append(b.sRegex, m.value)
		case "ip_cidr":
			b.sIPs = append(b.sIPs, m.value)
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
