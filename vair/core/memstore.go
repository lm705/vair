package core

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// memResultVer bumps whenever a config's ping/speed result changes (every
// entry_update). It's folded into the sort-order cache signature ONLY for the
// ping/speed sorts — so a re-read during/after a test recomputes the order
// instead of returning the stale cached one, while the idx-sorted default view
// (unaffected by results) keeps its cache and its fast path. It's just an atomic
// counter, so the hundreds of updates a bulk test fires are free; the actual
// re-sort happens at most once per frontend re-fetch (which is throttled).
var memResultVer int64

func bumpResultVer() { atomic.AddInt64(&memResultVer, 1) }

// ── in-memory window ─────────────────────────────────────────────────────────
// Serves the table window — sort / filter / proto / dedup / favorites / stats —
// from the in-memory state.tabEntries instead of SQLite, for speed (esp. ping/
// speed sort, which SQLite did as a full temp-b-tree). A one-slot cache holds the
// last filtered+sorted order per (tab, query signature) so repeated window fetches
// while scrolling are O(limit). Invalidated on any data / test-result change.

var (
	memCacheMu  sync.Mutex
	memCacheTab string
	memCacheSig string
	memCacheOrd []*ConfigEntry // filtered + sorted pointers for (memCacheTab, memCacheSig)
)

// loadConfigsIntoMemory fills state.tabEntries from the durable SQLite store at
// startup — all reads are served from memory.
func loadConfigsIntoMemory() {
	if store == nil {
		return
	}
	state.mu.RLock()
	ids := make([]string, len(state.tabs))
	for i, t := range state.tabs {
		ids[i] = t.ID
	}
	active := state.activeTab
	state.mu.RUnlock()
	for _, id := range ids {
		ents, err := store.allEntriesOrdered(id)
		if err != nil {
			continue
		}
		state.mu.Lock()
		state.tabEntries[id] = ents
		if active == id {
			state.entries = ents
		}
		state.mu.Unlock()
	}
}

// resetTabResultsMem clears every config's ping/speed result back to pending —
// in memory (the read source) and in SQLite (persistence). Returns the count.
func resetTabResultsMem(tabID string) int {
	state.mu.RLock()
	ents := state.tabEntries[tabID]
	state.mu.RUnlock()
	for _, e := range ents {
		e.mu.Lock()
		e.PingStatus = StatusPending
		e.Delay = -1
		e.PingErr = ""
		e.SpeedStatus = StatusPending
		e.SpeedMBps = 0
		e.SpeedLive = 0
		e.SpeedErr = ""
		e.mu.Unlock()
	}
	memInvalidate(tabID)
	if store != nil {
		store.resetTabResults(tabID)
	}
	return len(ents)
}

// memInvalidate drops the cached sorted order. Pass "" to clear unconditionally
// (e.g. after a batch of test results that may have touched any tab).
func memInvalidate(tabID string) {
	memCacheMu.Lock()
	if tabID == "" || memCacheTab == tabID {
		memCacheSig = ""
		memCacheOrd = nil
	}
	memCacheMu.Unlock()
}

// tabStatsT holds the header counters over the filtered set.
type tabStatsT struct {
	total, ok, fail int
	minPing         int64
	maxSpeed        float64
}

// chipProtoOf mirrors the client's chipProto(): protocol lowercased, with the
// SS-2022 split by cipher prefix. Used by the type-pill filter.
func chipProtoOf(protocol, security string) string {
	pr := strings.ToLower(protocol)
	if pr == "" {
		pr = "none"
	}
	if pr == "ss" && strings.HasPrefix(strings.ToLower(security), "2022-blake3-") {
		pr = "ss2022"
	}
	return pr
}

// sortKey is a per-entry snapshot of every field the filter/comparators read,
// taken under e.mu so the sort never races a concurrent test mutation.
type sortKey struct {
	e                                       *ConfigEntry
	idx                                     int
	delay                                   int64
	speed                                   float64
	pStatus, sStatus                        Status
	fav                                     bool
	name, host, network, security, protocol string // lowercased
	chip, body                              string
}

func memSig(q windowQuery, favorites []string) string {
	s := q.sort + "\x00" + q.filter + "\x00" + strings.Join(q.proto, ",") + "\x00" +
		boolByte(q.dedupHide) + "\x00" + strings.Join(favorites, "\x01") +
		"\x00" + strings.Join(q.exclude, "\x01") // exclude view filter (toggles the cache)
	// Only ping/speed order depends on live test results — fold in the result
	// version so those views re-sort as results arrive; idx-sorted views keep a
	// stable cache regardless of tests.
	if q.sort == "ping" || q.sort == "speed" {
		s += "\x00" + strconv.FormatInt(atomic.LoadInt64(&memResultVer), 10)
	}
	return s
}
func boolByte(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// filterTerm is one '+'-separated piece of the filter query: a value plus the
// canonical column "key" it applies to ("" = any column / general).
type filterTerm struct{ key, val string }

// canonField maps a recognized "field:" prefix to its canonical column key.
func canonField(f string) string {
	switch f {
	case "name":
		return "name"
	case "host":
		return "host"
	case "type", "proto", "protocol":
		return "type"
	case "transport", "net", "network":
		return "transport"
	case "security", "tls", "sec":
		return "security"
	}
	return ""
}

// classifyBare classifies a bare value that is a well-known transport / security
// / protocol token into its column, so "ws" means transport, "tls" security,
// "vless" type. Returns "" for anything else (e.g. a country name).
func classifyBare(v string) string {
	switch v {
	case "tcp", "ws", "grpc", "xhttp", "h2", "http", "httpupgrade", "splithttp", "kcp", "quic":
		return "transport"
	case "tls", "reality", "xtls", "none":
		return "security"
	case "vless", "vmess", "trojan", "ss", "ss2022", "hysteria2", "hy2", "tuic":
		return "type"
	}
	return ""
}

// parseFilterTerms splits the filter into '+'-separated terms, each resolved to a
// column key. A term carries a column only when it writes its own "field:" prefix
// or its bare value is a recognized transport/security/protocol token (auto-
// classified). Any other bare value is "general" (matches any column) — it does
// NOT inherit a preceding field. So "germany+ws+tls" is AND across name/transport/
// security, "germany+poland" is general-OR, and "name:germany+poland" is name
// germany AND poland-anywhere. Everything lowercased.
func parseFilterTerms(filter string) []filterTerm {
	s := strings.ToLower(strings.TrimSpace(filter))
	if s == "" {
		return nil
	}
	var out []filterTerm
	for _, part := range strings.Split(s, "+") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if c := strings.IndexByte(part, ':'); c > 0 {
			if key := canonField(strings.TrimSpace(part[:c])); key != "" {
				val := strings.TrimSpace(part[c+1:])
				if val == "" {
					continue
				}
				out = append(out, filterTerm{key, val})
				continue
			}
		}
		out = append(out, filterTerm{classifyBare(part), part}) // "" key = general
	}
	return out
}

// termHitKey reports whether a value matches the entry in the given column key.
func termHitKey(k *sortKey, key, val string) bool {
	switch key {
	case "name":
		return strings.Contains(k.name, val)
	case "host":
		return strings.Contains(k.host, val)
	case "type":
		return strings.Contains(k.protocol, val) || strings.Contains(k.chip, val)
	case "transport":
		return strings.Contains(k.network, val)
	case "security":
		return strings.Contains(k.security, val)
	default: // general — any column
		return strings.Contains(k.name, val) || strings.Contains(k.host, val) ||
			strings.Contains(k.network, val) || strings.Contains(k.security, val) ||
			strings.Contains(k.protocol, val)
	}
}

// filterTermsMatch groups terms by column key: a key is satisfied if ANY of its
// terms match (OR within a column), and EVERY distinct key must be satisfied
// (AND across columns).
func filterTermsMatch(terms []filterTerm, k *sortKey) bool {
	satisfied := make(map[string]bool, len(terms))
	for _, t := range terms {
		if !satisfied[t.key] { // once a key's term matches, the rest are skipped
			satisfied[t.key] = termHitKey(k, t.key, t.val)
		}
	}
	for _, ok := range satisfied {
		if !ok {
			return false
		}
	}
	return true
}

// memOrder returns the filtered+sorted entry pointers for a tab+query, using the
// cache when the signature matches.
func memOrder(tabID string, q windowQuery, favorites []string) []*ConfigEntry {
	sig := memSig(q, favorites)
	memCacheMu.Lock()
	if memCacheTab == tabID && memCacheSig == sig && memCacheOrd != nil {
		ord := memCacheOrd
		memCacheMu.Unlock()
		return ord
	}
	memCacheMu.Unlock()

	// Fast path: the default view (idx order, no filter/proto/dedup/favorites) is
	// just the in-memory slice as-is — skip the per-entry snapshot + sort entirely.
	if (q.sort == "" || q.sort == "idx") && q.filter == "" && len(q.proto) == 0 && !q.dedupHide && len(q.exclude) == 0 && len(favorites) == 0 {
		state.mu.RLock()
		ord := append([]*ConfigEntry(nil), state.tabEntries[tabID]...)
		state.mu.RUnlock()
		memCacheMu.Lock()
		memCacheTab, memCacheSig, memCacheOrd = tabID, sig, ord
		memCacheMu.Unlock()
		return ord
	}

	// Favorites are matched by BODY (the raw without its #name), so a config
	// stays starred under any name and across tabs. nodeBody() also normalizes
	// older raw-keyed favorites, so a settings file from before this change keeps
	// working without a migration.
	favSet := make(map[string]struct{}, len(favorites))
	for _, f := range favorites {
		favSet[nodeBody(f)] = struct{}{}
	}
	// The filter is one or more '+'-separated AND terms — every term must match.
	// Each term may carry a "field:" prefix (name:, host:, type:/proto:,
	// transport:/network:, security:) to restrict it to one column; without a
	// recognized prefix it matches across all of them. e.g. "name:germany+tcp" or
	// "transport:ws+security:reality". Everything is lowercased; an unrecognized
	// prefix is treated as a plain value (so a value with ':' still works).
	filterTerms := parseFilterTerms(q.filter)
	protoSet := make(map[string]struct{}, len(q.proto))
	for _, p := range q.proto {
		protoSet[strings.ToLower(strings.TrimSpace(p))] = struct{}{}
	}

	state.mu.RLock()
	src := state.tabEntries[tabID]
	keys := make([]sortKey, 0, len(src))
	var seen map[string]struct{}
	if q.dedupHide {
		seen = make(map[string]struct{}, len(src))
	}
	for _, e := range src {
		e.mu.Lock()
		k := sortKey{
			e: e, idx: e.Index, delay: e.Delay, speed: e.SpeedMBps,
			pStatus: e.PingStatus, sStatus: e.SpeedStatus,
			name: strings.ToLower(e.Name), host: strings.ToLower(e.Host),
			network: strings.ToLower(e.Network), security: strings.ToLower(e.Security),
			protocol: strings.ToLower(e.Protocol),
		}
		raw := e.Raw
		sec := e.Security
		e.mu.Unlock()
		// Body is needed for the favorite match and/or dedup — compute it once.
		var body string
		if len(favSet) > 0 || q.dedupHide {
			body = nodeBody(strings.TrimSpace(raw))
		}
		k.chip = chipProtoOf(k.protocol, sec)
		_, k.fav = favSet[body]
		// text filter — every AND term must match
		if len(filterTerms) > 0 && !filterTermsMatch(filterTerms, &k) {
			continue
		}
		if len(protoSet) > 0 {
			if _, ok := protoSet[k.chip]; !ok {
				continue
			}
		}
		// Per-tab exclude filter as a VIEW filter (shouldSkip lowercases both sides,
		// so the sortKey's already-lowercased fields are fine).
		if len(q.exclude) > 0 && shouldSkip(k.name, k.protocol, k.host, k.network, k.security, q.exclude) {
			continue
		}
		if q.dedupHide {
			k.body = body
			if _, dup := seen[k.body]; dup {
				continue
			}
			seen[k.body] = struct{}{}
		}
		keys = append(keys, k)
	}
	state.mu.RUnlock()

	sort.SliceStable(keys, func(i, j int) bool { return lessKey(&keys[i], &keys[j], q.sort) })
	out := make([]*ConfigEntry, len(keys))
	for i := range keys {
		out[i] = keys[i].e
	}
	memCacheMu.Lock()
	memCacheTab, memCacheSig, memCacheOrd = tabID, sig, out
	memCacheMu.Unlock()
	return out
}

func speedRank(k *sortKey) int {
	// Any real measured speed ranks first — regardless of the CURRENT status.
	// Requiring sStatus==OK (the 1.10 rule) dropped a config whose status went
	// back to pending/testing (cancelled re-test, refresh) into the ping-only
	// band, where speed-FAILED configs (tiny response, eof) could sit above an
	// actual result. Failure paths zero SpeedMBps, so speed>0 ⇒ a valid result.
	if k.speed > 0 {
		return 0
	}
	if k.pStatus == StatusOK && k.delay > 0 {
		return 1
	}
	if k.sStatus == StatusTestingSpeed || k.pStatus == StatusTestingPing {
		return 2
	}
	if k.pStatus == StatusFailed {
		return 3
	}
	return 4
}

// lessKey is the comparator matching the legacy SQL orderBy / JS comparators:
// favorites first, then the active sort.
func lessKey(a, b *sortKey, mode string) bool {
	if a.fav != b.fav {
		return a.fav // favorites (true) sort before non-favorites
	}
	switch mode {
	case "ping":
		al, bl := a.delay > 0, b.delay > 0
		if al != bl {
			return al // live (ping>0) before dead
		}
		if al && a.delay != b.delay {
			return a.delay < b.delay
		}
		return a.idx < b.idx
	case "speed":
		ra, rb := speedRank(a), speedRank(b)
		if ra != rb {
			return ra < rb
		}
		if ra == 0 && a.speed != b.speed {
			return a.speed > b.speed // speed DESC
		}
		if ra == 1 && a.delay != b.delay {
			return a.delay < b.delay // ping ASC
		}
		return a.idx < b.idx
	default: // idx
		return a.idx < b.idx
	}
}

// memWindow returns the requested window of rows (as snapshots), the total
// matching count, and — when withStats — the header counters.
func memWindow(tabID string, q windowQuery, favorites []string, withStats bool) (rows []ConfigEntry, total int, st tabStatsT) {
	ord := memOrder(tabID, q, favorites)
	total = len(ord)
	off := q.offset
	if off < 0 {
		off = 0
	}
	if off > total {
		off = total
	}
	end := off + q.limit
	if q.limit <= 0 || end > total {
		end = total
	}
	win := ord[off:end]
	rows = make([]ConfigEntry, len(win))
	for i, e := range win {
		rows[i] = e.snap()
	}
	if withStats {
		st.total = total
		for _, e := range ord {
			e.mu.Lock()
			ps, d, sp := e.PingStatus, e.Delay, e.SpeedMBps
			e.mu.Unlock()
			if ps == StatusOK {
				st.ok++
			} else if ps == StatusFailed {
				st.fail++
			}
			if d > 0 && (st.minPing == 0 || d < st.minPing) {
				st.minPing = d
			}
			if sp > 0 && sp > st.maxSpeed {
				st.maxSpeed = sp
			}
		}
	}
	return
}

// memIndices returns every matching entry index in screen order (for "ping/speed
// all" over the whole filtered set).
func memIndices(tabID string, q windowQuery, favorites []string) []int {
	ord := memOrder(tabID, q, favorites)
	out := make([]int, len(ord))
	for i, e := range ord {
		out[i] = e.Index
	}
	return out
}

// memRawsOrdered returns the matching entries' indices and raw URLs in screen
// order. Backs "copy / select all": the windowed client copies the whole
// filtered set without ever loading every row. Raw is effectively immutable, so
// it's read without the per-entry lock.
func memRawsOrdered(tabID string, q windowQuery, favorites []string) (idx []int, raw []string) {
	ord := memOrder(tabID, q, favorites)
	idx = make([]int, len(ord))
	raw = make([]string, len(ord))
	for i, e := range ord {
		idx[i] = e.Index
		raw[i] = e.Raw
	}
	return idx, raw
}

// memRawsForIndices returns the raw URLs for the given indices (in the same
// order). Backs shift-range copy, where the client picked a span of rows it may
// not have loaded. Missing/misaligned indices yield "".
func memRawsForIndices(tabID string, idxs []int) []string {
	state.mu.RLock()
	defer state.mu.RUnlock()
	src := state.tabEntries[tabID]
	out := make([]string, len(idxs))
	for i, ix := range idxs {
		if ix >= 0 && ix < len(src) && src[ix] != nil && src[ix].Index == ix {
			out[i] = src[ix].Raw
			continue
		}
		for _, e := range src { // fallback if not positionally aligned
			if e.Index == ix {
				out[i] = e.Raw
				break
			}
		}
	}
	return out
}

// memTabCount returns the total number of configs in a tab (unfiltered) — the
// denominator for the "matching / total" filter count.
func memTabCount(tabID string) int {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return len(state.tabEntries[tabID])
}

// memTabVisibleCount is the tab's config count AS SHOWN — the exclude filter is
// a view filter, so excluded configs stay in the store but shouldn't be counted
// on the tab chip (else the chip would read higher than the table). Fast path
// (no filter) returns the raw length; otherwise it scans with shouldSkip.
func memTabVisibleCount(tabID string) int {
	excl := tabExcludeFilter(tabID)
	if len(excl) == 0 {
		return memTabCount(tabID)
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	n := 0
	for _, e := range state.tabEntries[tabID] {
		if !shouldSkip(e.Name, e.Protocol, e.Host, e.Network, e.Security, excl) {
			n++
		}
	}
	return n
}

// memEntryByRaw finds a config by its raw URL in memory, preferring preferTab
// then any tab. Exact raw match first; if none, falls back to matching by node
// BODY — a source refresh re-disambiguates names and rewrites the raw's #name
// fragment, so the exact raw of a connected config can stop existing while the
// same server (body) is still in the list. (Raw is effectively immutable per
// entry, so an unlocked read is safe enough for this infrequent path.)
func memEntryByRaw(preferTab, raw string) (*ConfigEntry, string, bool) {
	state.mu.RLock()
	defer state.mu.RUnlock()
	if preferTab != "" {
		for _, e := range state.tabEntries[preferTab] {
			if e.Raw == raw {
				return e, preferTab, true
			}
		}
	}
	for tid, ents := range state.tabEntries {
		for _, e := range ents {
			if e.Raw == raw {
				return e, tid, true
			}
		}
	}
	body := nodeBody(raw)
	if body == "" {
		return nil, "", false
	}
	if preferTab != "" {
		for _, e := range state.tabEntries[preferTab] {
			if nodeBody(e.Raw) == body {
				return e, preferTab, true
			}
		}
	}
	for tid, ents := range state.tabEntries {
		for _, e := range ents {
			if nodeBody(e.Raw) == body {
				return e, tid, true
			}
		}
	}
	return nil, "", false
}

// memEntry returns the active tab's entry by its index (in-memory, by position
// since tabEntries is index-ordered), as a fresh pointer-safe lookup.
func memEntry(tabID string, idx int) (*ConfigEntry, bool) {
	state.mu.RLock()
	defer state.mu.RUnlock()
	src := state.tabEntries[tabID]
	if idx >= 0 && idx < len(src) && src[idx] != nil && src[idx].Index == idx {
		return src[idx], true
	}
	for _, e := range src { // fallback if not positionally aligned
		if e.Index == idx {
			return e, true
		}
	}
	return nil, false
}
