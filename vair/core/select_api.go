package core

// Selection / bulk helpers over the windowed store — ported from the 1.10
// handlers (handleTabIndices / handleTabRaws / handleTabDeleteFailed /
// handleConnectChain / handleTabReorder).

// selQuery assembles the windowQuery the selection endpoints share (dedup mode
// comes from the tab, matching the windowed read).
func selQuery(tabID, sort, filter string, proto []string) (windowQuery, []string) {
	settingsMu.RLock()
	favs := append([]string(nil), appSettings.Favorites...)
	settingsMu.RUnlock()
	q := windowQuery{sort: sort, filter: filter, proto: proto, dedupHide: tabDedupHide(tabID), exclude: tabExcludeFilter(tabID), favorites: favs}
	return q, favs
}

// Indices returns the FULL ordered index list of the current view (every
// matching row, not just loaded windows) — shift-range / select-all need it.
func Indices(tabID, sort, filter string, proto []string) []int {
	q, favs := selQuery(tabID, sort, filter, proto)
	idxs := memIndices(tabID, q, favs)
	if idxs == nil {
		idxs = []int{}
	}
	return idxs
}

// OrderedRaws pairs every matching row's index with its raw URL, in screen
// order ("select all" → copy the whole filtered set).
type OrderedRaws struct {
	Idx []int    `json:"idx"`
	Raw []string `json:"raw"`
}

// RawsAll returns every matching row's index + raw in screen order.
func RawsAll(tabID, sort, filter string, proto []string) OrderedRaws {
	q, favs := selQuery(tabID, sort, filter, proto)
	idx, raw := memRawsOrdered(tabID, q, favs)
	if idx == nil {
		idx = []int{}
	}
	if raw == nil {
		raw = []string{}
	}
	return OrderedRaws{Idx: idx, Raw: raw}
}

// RawsForIndices returns the raws for the given entry indices in the same
// order (shift-range copy over rows the client never loaded).
func RawsForIndices(tabID string, idxs []int) []string {
	raw := memRawsForIndices(tabID, idxs)
	if raw == nil {
		raw = []string{}
	}
	return raw
}

// DeleteFailed removes every config whose ping OR speed test failed. Done
// server-side (over the whole tab) so it works regardless of which rows the
// windowed client currently holds. No-op on main (its entries are re-fetched).
// Returns the number of remaining configs.
func DeleteFailed(tabID string) int {
	if tabID == "" || tabID == "main" {
		return 0
	}
	var kept []*ConfigEntry
	for _, e := range loadTabEntries(tabID) {
		if e.PingStatus != StatusFailed && e.SpeedStatus != StatusFailed {
			kept = append(kept, e)
		}
	}
	for i, e := range kept {
		e.Index = i
	}
	storeReplace(tabID, kept) // re-indexed; SQLite is the source of truth
	loadedSignal(tabID)
	saveTabs()
	return len(kept)
}

// ConnectChain connects the given entries of the ACTIVE tab (top→bottom screen
// order: entry → exit) as a chain. Returns "" on success or a human-readable
// rejection ("a chain needs at least 2 configs", mixed-engine reason, …).
func ConnectChain(idxs []int, mode string) string {
	var entries []*ConfigEntry
	var nodes []*Node
	tab := activeTabID()
	for _, i := range idxs {
		e, ok := memEntry(tab, i)
		if !ok {
			return "bad idx"
		}
		n, perr := parseNode(e.Raw)
		if perr != nil {
			return "chain: unparseable config in selection"
		}
		entries = append(entries, e)
		nodes = append(nodes, n)
	}
	if len(entries) < 2 {
		return "a chain needs at least 2 configs"
	}
	// Validate engine compatibility up-front so the UI gets a clean rejection
	// before we tear down any existing connection.
	if reason := chainEngineReason(nodes); reason != "" {
		return reason
	}
	// User explicitly connected → arm auto-keepalive but mark user-owned so the
	// supervisor's pool-honor switch won't move it.
	autoWant.Store(true)
	autoManaged.Store(false)
	autoLiveRtt.Store(0)
	autoProbeNow.Store(true)
	connMode := ModeProxy
	if mode == "tun" {
		connMode = ModeTUN
	}
	go startChain(entries, connMode)
	return ""
}

// ReorderTabs applies a new tab order (drag-reorder in the tab bar). Unknown
// ids are ignored; tabs missing from the list keep their relative order at
// the end.
func ReorderTabs(ids []string) {
	state.mu.Lock()
	tabMap := make(map[string]Tab)
	for _, t := range state.tabs {
		tabMap[t.ID] = t
	}
	newTabs := make([]Tab, 0, len(ids))
	for _, id := range ids {
		if t, ok := tabMap[id]; ok {
			newTabs = append(newTabs, t)
			delete(tabMap, id)
		}
	}
	for _, t := range state.tabs {
		if _, ok := tabMap[t.ID]; ok {
			newTabs = append(newTabs, t)
		}
	}
	state.tabs = newTabs
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()
}
