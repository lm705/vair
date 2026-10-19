package core

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rule-hit statistics.
//
// The four rule lists — block, without VPN, through VPN, apps without VPN — are
// written blind. A person adds "netflix.com", it never fires because the traffic
// they meant goes to nflxvideo.net, and nothing in the program ever says so. A
// typo in a process name behaves the same way: silently, indistinguishably from
// a rule that simply had no occasion to match.
//
// So we count. Next to each entry the settings screen can then say "fired 41
// times, last a minute ago" or — the answer worth the whole feature — "never
// matched".
//
// Where the numbers come from: the engine's own log, read as it streams past on
// the way to the log panel. Nothing is added to the traffic path; if this file
// is wrong the counters are wrong and not one packet is affected.
//
// sing-box, at debug level, names the rule it matched and the value that did it:
//
//	router: match[3] domain_suffix=example.com => route(direct)
//	router: match[0] process_name=chrome.exe => route(direct)
//	router: match[1] domain=ads.example.org => reject
//
// which is exact — the engine's own answer, not our re-derivation of it. xray
// says less: its access line carries the destination and the outbound tag but
// not the rule, so there the entry is found by matching the destination the same
// way the rule generator classified it. Both land in the same store.
//
// What is NOT kept: destinations. A hit increments a counter attached to a line
// the user typed; the host that caused it is not stored, not broadcast and not
// written anywhere. The counters are per app session and live in memory only.

// ruleListKind names a user list. The values double as the keys the frontend
// reads, so they are the setting names without the noise.
const (
	ruleListBlock  = "block"
	ruleListDirect = "direct"
	ruleListProxy  = "proxy"
	ruleListApps   = "apps"
)

// ruleHit is one line's tally.
type ruleHit struct {
	Hits int       `json:"hits"`
	Last time.Time `json:"-"`
}

// RuleHitDTO is one line as the settings screen needs it.
type RuleHitDTO struct {
	Hits int   `json:"hits"`
	Last int64 `json:"last"` // unix seconds, 0 = never
	// Pending marks a line that is not in the running engine's configuration —
	// added or edited since the current connection started. Its zero count means
	// "not applied yet", which is a different thing from "never matched", and
	// showing the wrong one of those sends people hunting a bug in a rule that
	// was simply never loaded.
	Pending bool `json:"pending"`
	// Tracked is false for entries we cannot attribute: geosite:/geoip:
	// categories are matched inside the engine's own database, and a hit on one
	// carries no value we could tie back to the line.
	Tracked bool `json:"tracked"`
}

// RuleStatsDTO is the whole answer for the settings screen.
type RuleStatsDTO struct {
	Enabled bool `json:"enabled"`
	// Observed is how many rule matches have been seen in total, attributed or
	// not. Zero means "no data yet" — nothing has been routed since counting
	// began — and the screen must not read that as "none of your rules work".
	Observed int                              `json:"observed"`
	Since    int64                            `json:"since"`
	Lists    map[string]map[string]RuleHitDTO `json:"lists"`
}

var ruleStats = struct {
	mu       sync.Mutex
	hits     map[string]map[string]*ruleHit // list → entry text → tally
	inEffect map[string]map[string]bool     // list → entry text present in the running config
	observed int
	since    time.Time
}{
	hits:     map[string]map[string]*ruleHit{},
	inEffect: map[string]map[string]bool{},
}

// ruleStatsEnabled reports whether counting is on. Off by default: it costs the
// engine a debug-level log, which is a lot of lines on a busy connection.
func ruleStatsEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.RuleStats
}

// armRuleStats records which entries went into the configuration the engine is
// about to run. Called from the config builders — the one place that knows what
// the engine will actually see, as opposed to what the settings say right now.
func armRuleStats(block, direct, proxy, apps []string) {
	snap := func(entries []string) map[string]bool {
		m := make(map[string]bool, len(entries))
		for _, e := range entries {
			if e = strings.TrimSpace(e); e != "" {
				m[e] = true
			}
		}
		return m
	}
	ruleStats.mu.Lock()
	defer ruleStats.mu.Unlock()
	ruleStats.inEffect = map[string]map[string]bool{
		ruleListBlock:  snap(block),
		ruleListDirect: snap(direct),
		ruleListProxy:  snap(proxy),
		ruleListApps:   snap(apps),
	}
	if ruleStats.since.IsZero() {
		ruleStats.since = time.Now()
	}
}

// noteRuleHit credits one line of one list.
func noteRuleHit(list, entry string) {
	ruleStats.mu.Lock()
	defer ruleStats.mu.Unlock()
	m := ruleStats.hits[list]
	if m == nil {
		m = map[string]*ruleHit{}
		ruleStats.hits[list] = m
	}
	h := m[entry]
	if h == nil {
		h = &ruleHit{}
		m[entry] = h
	}
	h.Hits++
	h.Last = time.Now()
}

// RuleStats reports the tallies for the settings screen.
func RuleStats() RuleStatsDTO {
	settingsMu.RLock()
	enabled := appSettings.RuleStats
	lists := map[string][]string{
		ruleListBlock:  append([]string(nil), appSettings.BlockDomains...),
		ruleListDirect: append([]string(nil), appSettings.DirectDomains...),
		ruleListProxy:  append([]string(nil), appSettings.ProxyDomains...),
		ruleListApps:   append([]string(nil), appSettings.DirectApps...),
	}
	settingsMu.RUnlock()

	out := RuleStatsDTO{Enabled: enabled, Lists: map[string]map[string]RuleHitDTO{}}
	ruleStats.mu.Lock()
	defer ruleStats.mu.Unlock()
	out.Observed = ruleStats.observed
	if !ruleStats.since.IsZero() {
		out.Since = ruleStats.since.Unix()
	}
	for list, entries := range lists {
		m := map[string]RuleHitDTO{}
		for _, e := range entries {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			d := RuleHitDTO{
				Pending: !ruleStats.inEffect[list][e],
				Tracked: ruleEntryTracked(e),
			}
			if h := ruleStats.hits[list][e]; h != nil {
				d.Hits = h.Hits
				d.Last = h.Last.Unix()
			}
			m[e] = d
		}
		out.Lists[list] = m
	}
	return out
}

// ruleEntryTracked reports whether a hit on this entry could ever be attributed
// to it. Only the engine's own category databases are opaque to us.
func ruleEntryTracked(entry string) bool {
	lower := strings.ToLower(strings.TrimSpace(entry))
	return !strings.HasPrefix(lower, "geosite:") && !strings.HasPrefix(lower, "geoip:")
}

// ─────────────────────── reading the engines' logs ───────────────────────

// singboxMatchRe pulls the three things that matter out of sing-box's match
// line: the matcher's name, the value, and what was done with it.
//
//	router: match[3] domain_suffix=example.com => route(direct)
//	router: match[4] domain_suffix=[a.example.com b.example.net] => route(direct)
//
// The second form is the one to watch: a rule that carries several values —
// which is every rule we generate from a list of more than one line — prints the
// whole set, not the member that matched. Those are resolved through the
// destination (see singboxDest).
//
// The rule index is deliberately ignored. It would need a map from our
// generated rules array, which changes with the mode, and the action already
// says which list fired — a value can appear in two lists, but not with the
// same outcome.
var singboxMatchRe = regexp.MustCompile(`router: match\[\d+\] ([a-z_]+)=(\[[^\]]*\]|\S+) => (route\(([a-z0-9_-]+)\)|reject)`)

// The connection id sing-box stamps on every line about one connection, and the
// two lines that name where that connection is going. The sniffed domain is the
// better answer where both exist: it is what the router actually matched on.
var (
	singboxIDRe    = regexp.MustCompile(`\[(\d+) [^\]]*\]`)
	singboxDestRe  = regexp.MustCompile(`inbound connection to (\S+)`)
	singboxSniffRe = regexp.MustCompile(`router: sniffed protocol: [^,]+, domain: (\S+)`)
	singboxProcRe  = regexp.MustCompile(`router: found process path: (.+?), user:`)
)

// singboxDest remembers where each in-flight connection is going, so a match
// line that names a whole value set can still be credited to one line.
//
// Bounded and disposable: entries are dropped as they are used, and the whole
// map is emptied rather than grown past the cap. Losing a few attributions
// under a flood is the right trade — this is a counter, not an audit log.
type connFacts struct {
	host string // where it is going
	proc string // and what asked for it, when the engine looked that up
}

var singboxDest = struct {
	sync.Mutex
	m map[string]connFacts
}{m: map[string]connFacts{}}

const singboxDestCap = 512

// rememberConn stores one fact about a connection, keeping the other.
func rememberConn(id string, set func(*connFacts)) {
	if id == "" {
		return
	}
	singboxDest.Lock()
	defer singboxDest.Unlock()
	if len(singboxDest.m) >= singboxDestCap {
		singboxDest.m = map[string]connFacts{}
	}
	f := singboxDest.m[id]
	set(&f)
	singboxDest.m[id] = f
}

func rememberDest(id, host string) {
	if host == "" {
		return
	}
	rememberConn(id, func(f *connFacts) { f.host = host })
}

func rememberProc(id, proc string) {
	if proc == "" {
		return
	}
	rememberConn(id, func(f *connFacts) { f.proc = proc })
}

func takeConn(id string) connFacts {
	singboxDest.Lock()
	defer singboxDest.Unlock()
	f := singboxDest.m[id]
	delete(singboxDest.m, id)
	return f
}

// hostOnly drops the port from "example.com:443" / "1.2.3.4:80".
func hostOnly(hostport string) string {
	if strings.HasPrefix(hostport, "[") {
		if i := strings.Index(hostport, "]"); i >= 0 {
			return hostport[1:i]
		}
	}
	if i := strings.LastIndex(hostport, ":"); i >= 0 && !strings.Contains(hostport[i+1:], ":") {
		if _, err := strconv.Atoi(hostport[i+1:]); err == nil {
			return hostport[:i]
		}
	}
	return hostport
}

// xrayAcceptedRe pulls destination and outbound tag out of xray's access line.
// The destination is written three different ways depending on how the request
// arrived, and the arrow comes in two shapes:
//
//	accepted http://example.com/ [http-in -> direct]
//	accepted //example.com:443 [http-in -> direct]
//	accepted tcp:example.com:443 [socks-in >> proxy]
var xrayAcceptedRe = regexp.MustCompile(`accepted (\S+) \[[^\]]*(?:->|>>) ?([a-z0-9_-]+)\]`)

// xrayDestHost reduces any of those spellings to the host alone.
func xrayDestHost(dest string) string {
	// An IPv6 literal is full of colons, so it has to be recognised before
	// anything else goes looking for a scheme or a port separator.
	if strings.HasPrefix(dest, "[") {
		if i := strings.Index(dest, "]"); i >= 0 {
			return dest[1:i]
		}
	}
	if i := strings.Index(dest, "://"); i >= 0 {
		dest = dest[i+3:] // http://example.com/ → example.com/
	} else if strings.HasPrefix(dest, "//") {
		dest = dest[2:] // //example.com:443 → example.com:443
	} else if i := strings.Index(dest, ":"); i >= 0 && !strings.Contains(dest[:i], ".") {
		// A network prefix, not a port: "tcp:example.com:443". An IPv6 literal
		// never gets here — it arrives bracketed.
		dest = dest[i+1:]
	}
	if i := strings.IndexAny(dest, "/?"); i >= 0 {
		dest = dest[:i]
	}
	if i := strings.LastIndex(dest, ":"); i >= 0 && !strings.Contains(dest[i+1:], ":") {
		if _, err := strconv.Atoi(dest[i+1:]); err == nil {
			dest = dest[:i]
		}
	}
	return dest
}

// noteEngineLine offers one line of engine output to the counters.
//
// Called for every line the engines print, so the cheap rejection comes first:
// a substring test on the raw bytes before any regular expression runs, and the
// whole thing short-circuits when the feature is off.
func noteEngineLine(src, line string) {
	if !ruleStatsEnabled() {
		return
	}
	switch src {
	case "singbox":
		// sing-box colours its output even into a pipe, and the colour sits
		// around the connection id — "[\x1b[38;5;148m3400311066\x1b[0m 0ms]" —
		// which is exactly the field the value-set lines are matched by.
		line = stripANSI(line)
		switch {
		case strings.Contains(line, "inbound connection to "):
			if d := singboxDestRe.FindStringSubmatch(line); d != nil {
				rememberDest(singboxConnID(line), hostOnly(d[1]))
			}
		case strings.Contains(line, "sniffed protocol:"):
			if d := singboxSniffRe.FindStringSubmatch(line); d != nil {
				rememberDest(singboxConnID(line), d[1])
			}
		case strings.Contains(line, "found process path:"):
			// "router: found process path: /usr/bin/curl, user: ubuntu" — the only
			// place the engine names the program, and the only way to tell which
			// line of an apps list of several fired.
			if d := singboxProcRe.FindStringSubmatch(line); d != nil {
				rememberProc(singboxConnID(line), baseName(d[1]))
			}
		case strings.Contains(line, "router: match["):
			m := singboxMatchRe.FindStringSubmatch(line)
			if m == nil {
				return
			}
			// key, value, outbound ("" for reject), and where this connection was
			// headed — needed only when the value is a whole set.
			attributeMatch(m[1], m[2], m[4], takeConn(singboxConnID(line)))
		}
	case "xray":
		if !strings.Contains(line, "accepted ") {
			return
		}
		m := xrayAcceptedRe.FindStringSubmatch(line)
		if m == nil {
			return
		}
		attributeDestination(xrayDestHost(m[1]), m[2])
	}
}

// singboxConnID returns the connection id sing-box stamps on the line, or "".
func singboxConnID(line string) string {
	if m := singboxIDRe.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}

// attributeMatch credits sing-box's own answer: the matcher that fired, the
// value it fired on, and the outbound it chose. dest is where the connection was
// going, and is used only when the engine reported a whole value set rather than
// the member that matched.
func attributeMatch(key, value, outbound string, conn connFacts) {
	ruleStats.mu.Lock()
	ruleStats.observed++
	ruleStats.mu.Unlock()

	var list string
	switch {
	case outbound == "": // "=> reject"
		list = ruleListBlock
	case key == "process_name":
		list = ruleListApps
	case outbound == "direct":
		list = ruleListDirect
	case outbound == "proxy":
		list = ruleListProxy
	default:
		return // an outbound of ours that no user list can name
	}
	// A whole value set: the engine matched one of them and did not say which,
	// so the destination decides — the same first-match walk the engine did.
	if strings.HasPrefix(value, "[") {
		var entry string
		if key == "process_name" {
			entry = entryMatchingProcess(list, conn.proc)
		} else if conn.host != "" {
			entry = entryMatchingHost(list, conn.host)
		}
		if entry != "" {
			noteRuleHit(list, entry)
		}
		return
	}
	if entry := entryForMatcher(list, key, value); entry != "" {
		noteRuleHit(list, entry)
	}
}

// entryForMatcher finds the line of a list that produced a given matcher.
//
// The generator collapses a whole list into one rule per matcher type, so the
// engine reports the value rather than the line — and the value is what the
// line was written as, up to the prefix the generator stripped.
func entryForMatcher(list, key, value string) string {
	for _, e := range ruleListEntries(list) {
		k, v := lineMatcher(list, e)
		if k != key {
			continue
		}
		if v == value || (key == "ip_cidr" && sameCIDR(v, value)) {
			return e
		}
	}
	return ""
}

// attributeDestination credits a line by matching the destination ourselves —
// the xray path, where the log names the outbound but not the rule.
func attributeDestination(host, outbound string) {
	ruleStats.mu.Lock()
	ruleStats.observed++
	ruleStats.mu.Unlock()

	var list string
	switch outbound {
	case "direct":
		list = ruleListDirect
	case "proxy":
		list = ruleListProxy
	case "block", "blocked":
		list = ruleListBlock
	default:
		return
	}
	if entry := entryMatchingHost(list, host); entry != "" {
		noteRuleHit(list, entry)
	}
}

// entryMatchingHost is the same matching the generator asks the engine for,
// done here: first line wins, as in the generated rules.
func entryMatchingHost(list, host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	addr, err := netip.ParseAddr(host)
	isIP := err == nil
	for _, e := range ruleListEntries(list) {
		key, val := lineMatcher(list, e)
		switch key {
		case "domain":
			if !isIP && host == strings.ToLower(val) {
				return e
			}
		case "domain_suffix":
			if !isIP && hostHasSuffix(host, strings.ToLower(val)) {
				return e
			}
		case "domain_regex":
			if !isIP {
				if re, err := regexp.Compile(val); err == nil && re.MatchString(host) {
					return e
				}
			}
		case "ip_cidr":
			if isIP {
				if p, err := netip.ParsePrefix(withPrefixLen(val)); err == nil && p.Contains(addr) {
					return e
				}
			}
		}
	}
	return ""
}

// entryMatchingProcess finds the app list line that names a program. Written
// forgivingly on purpose: people type "chrome" for chrome.exe, and the list is
// theirs to write, not a syntax to get right.
func entryMatchingProcess(list, proc string) string {
	if proc == "" {
		return ""
	}
	trim := func(s string) string {
		return strings.TrimSuffix(strings.ToLower(s), ".exe")
	}
	want := trim(proc)
	for _, e := range ruleListEntries(list) {
		if trim(strings.TrimSpace(e)) == want {
			return e
		}
	}
	return ""
}

// baseName is filepath.Base for both separators — the engine reports a path in
// the platform's own spelling and we only ever want the program's name.
func baseName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// hostHasSuffix implements a domain suffix match: "example.com" covers
// "a.example.com" and "example.com" itself, and not "notexample.com".
func hostHasSuffix(host, suffix string) bool {
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

// lineMatcher says how one written line becomes a matcher, mirroring
// classifyRules — the same reading, so a line is found by the same shape it was
// generated into. Which list it came from settles the one thing the text cannot:
// the apps list holds executable names, and "firefox" there is a process, not a
// domain.
func lineMatcher(list, entry string) (key, value string) {
	e := strings.TrimSpace(entry)
	if list == ruleListApps {
		return "process_name", e
	}
	lower := strings.ToLower(e)
	switch {
	case strings.HasPrefix(lower, "geosite:"), strings.HasPrefix(lower, "geoip:"):
		return "", ""
	case strings.HasPrefix(lower, "full:"):
		return "domain", e[len("full:"):]
	case strings.HasPrefix(lower, "regexp:"):
		return "domain_regex", e[len("regexp:"):]
	case strings.HasPrefix(lower, "domain:"):
		return "domain_suffix", e[len("domain:"):]
	case isIPOrCIDR(e):
		return "ip_cidr", e
	default:
		return "domain_suffix", e
	}
}

// withPrefixLen makes a bare address into the /32 or /128 the engine prints.
func withPrefixLen(v string) string {
	if strings.Contains(v, "/") {
		return v
	}
	if strings.Contains(v, ":") {
		return v + "/128"
	}
	return v + "/32"
}

// sameCIDR compares what the user wrote with what the engine printed: "1.2.3.4"
// and "1.2.3.4/32" are the same rule.
func sameCIDR(a, b string) bool {
	pa, ea := netip.ParsePrefix(withPrefixLen(a))
	pb, eb := netip.ParsePrefix(withPrefixLen(b))
	return ea == nil && eb == nil && pa == pb
}

// ruleListEntries returns a list's current entries.
func ruleListEntries(list string) []string {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	switch list {
	case ruleListBlock:
		return append([]string(nil), appSettings.BlockDomains...)
	case ruleListDirect:
		return append([]string(nil), appSettings.DirectDomains...)
	case ruleListProxy:
		return append([]string(nil), appSettings.ProxyDomains...)
	case ruleListApps:
		return append([]string(nil), appSettings.DirectApps...)
	}
	return nil
}
