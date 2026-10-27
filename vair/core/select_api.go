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

// testFailed reports whether a row's last ping or speed test failed — what
// "Delete failed" removes.
func testFailed(e *ConfigEntry) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.PingStatus == StatusFailed || e.SpeedStatus == StatusFailed
}

// failedCount is how many rows DeleteFailed would remove: the whole tab, not
// only the filtered view, because that is what it works on.
func failedCount(tabID string) int {
	if tabID == "" || tabID == "main" {
		return 0
	}
	n := 0
	for _, e := range loadTabEntries(tabID) {
		if testFailed(e) {
			n++
		}
	}
	return n
}

// DeleteFailed removes every config whose ping OR speed test failed. Done
// server-side (over the whole tab) so it works regardless of which rows the
// windowed client currently holds. No-op on main (its entries are re-fetched).
// Returns how many were removed; the delete can be undone (UndoDelete).
func DeleteFailed(tabID string) int {
	if tabID == "" || tabID == "main" {
		return 0
	}
	all := loadTabEntries(tabID)
	allIdx := entryIndices(all)
	var kept []*ConfigEntry
	for _, e := range all {
		if !testFailed(e) {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(all) {
		return 0
	}
	for i, e := range kept {
		e.Index = i
	}
	storeReplace(tabID, kept) // re-indexed; SQLite is the source of truth
	rememberDelete(tabID, all, kept, allIdx, entryIndices(kept))
	loadedSignal(tabID)
	saveTabs()
	return len(all) - len(kept)
}

// ConnectChain connects the given entries of the ACTIVE tab as a chain, in the
// order given: idxs[0] is the entry hop, idxs[last] is the exit. The caller
// sends them in the order the user picked them. Returns "" on success or a human-readable
// rejection ("a chain needs at least 2 configs", mixed-engine reason, …).
//
// raws are the same configurations, index for index: a refresh of the tab's
// source numbers the rows again, and a chain built from the numbers alone was
// built from whatever rows had taken the selected ones' places. One that is
// gone altogether refuses the chain rather than shortening it.
func ConnectChain(idxs []int, raws []string, mode string) string {
	var entries []*ConfigEntry
	var nodes []*Node
	tab := activeTabID()
	rows := resolveRows(tab, idxs, raws)
	if len(rows) != len(idxs) {
		return "chain: a selected config is no longer in the tab"
	}
	for _, i := range rows {
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
	if _, reason := splitChain(nodes); reason != "" {
		return reason
	}
	// User explicitly connected → arm auto-keepalive but mark user-owned so the
	// supervisor's pool-honor switch won't move it.
	noteUserConnAction()
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
