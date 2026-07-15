package core

import (
	"fmt"
	"os"
	"time"
)

// PreloadSettings loads just settings.json (fast) so the shell can size the
// window immediately; the heavy Init (SQLite + configs) then runs in the
// background while the window is already up.
func PreloadSettings() {
	migrateDataLayout()
	loadSettings()
}

// Init loads persisted state at startup: opens the SQLite store, loads tabs,
// pulls configs into the in-memory window store, and loads settings. The engine
// extraction + connect-path setup is wired separately by the shell (SetBinDir).
// May run AFTER the window is shown — it announces the loaded state at the end
// so an already-mounted frontend picks it up.
func Init() {
	clearStaleProxy() // crash recovery: drop a system proxy left set by a dirty exit
	migrateDataLayout()
	if s, err := openConfigStore(); err == nil {
		store = s
		go s.resultFlusher()
	}
	tabsOK := loadTabs()
	// RECOVER, don't sweep: the DB may hold config rows for tab IDs that are
	// missing from tabs.json (its metadata got lost — e.g. a truncated write, or
	// an old build that clobbered it). Deleting those rows (the previous
	// sweepOrphanTabRows) destroyed the user's pasted tabs. Instead, rebuild a
	// tab entry for each orphan so its configs reappear. Only when tabs.json
	// parsed cleanly (tabsOK) — a parse failure already left the default [main],
	// and we must not re-save over the (still-readable) file.
	if store != nil && tabsOK {
		recoverOrphanTabs()
	}
	loadConfigsIntoMemory()
	if tabsOK {
		saveTabs()
	}
	loadSettings()
	// Announce to a frontend that may already be mounted (async startup).
	state.mu.RLock()
	tabsSnap := append([]Tab(nil), state.tabs...)
	state.mu.RUnlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: tabsSnap})
	state.broadcast(SSEEvent{Type: "active_tab", Payload: activeTabID()})
	loadedSignal(activeTabID())
}

// recoverOrphanTabs rebuilds tab metadata for any tab_id that has config rows
// in the DB but is missing from state.tabs (its tabs.json entry was lost). The
// configs are preserved; the tab reappears as a user tab with a recovered name.
func recoverOrphanTabs() {
	dbIDs, err := store.distinctTabIDs()
	if err != nil {
		return
	}
	state.mu.Lock()
	have := make(map[string]struct{}, len(state.tabs))
	for _, t := range state.tabs {
		have[t.ID] = struct{}{}
	}
	recovered := 0
	for _, id := range dbIDs {
		if id == "main" {
			continue
		}
		if _, ok := have[id]; ok {
			continue
		}
		state.tabs = append(state.tabs, Tab{
			ID:       id,
			Name:     fmt.Sprintf("Recovered %d", recovered+1),
			IsMain:   false,
			Closable: true,
		})
		recovered++
	}
	state.mu.Unlock()
	if recovered > 0 {
		fmt.Fprintf(os.Stderr, "ℹ recovered %d tab(s) from the store (metadata was missing from tabs.json)\n", recovered)
	}
}

// Row is the table DTO sent to the frontend (clean — no mutex; JSON keys match
// the Ф0 ConfigService so the existing virtual table renders it unchanged).
type Row struct {
	Index     int     `json:"index"`
	Raw       string  `json:"raw"` // full config URL (copy / connected-row match / "last" badge)
	Name      string  `json:"name"`
	Host      string  `json:"host"`
	Port      int     `json:"port"`
	Net       string  `json:"net"`
	Sec       string  `json:"sec"`
	Proto     string  `json:"proto"`
	Ping      int     `json:"ping"`
	Speed     float64 `json:"speed"`      // MB/s from the last speed test (0 = untested)
	SpeedLive float64 `json:"speed_live"` // MB/s streaming in while a speed test runs
	PingSt    string  `json:"ping_st"`    // "" | testing_ping | ok | failed | …
	SpeedSt   string  `json:"speed_st"`   // "" | testing_speed | ok | failed | skipped | …
	PingErr   string  `json:"ping_err"`   // failed-pill text ("timeout", …)
	SpeedErr  string  `json:"speed_err"`  // failed-pill text ("tiny response", …)
	Fav       bool    `json:"fav"`        // floated to the top + starred
}

// UITheme exposes the colour scheme ("light" or "" / "dark") to the shell — the
// native window/webview background must match the theme, or the compositor lag
// during a live resize flashes the mismatched colour (black on a light theme).
func UITheme() string {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.Theme
}

// ActiveTab returns the id of the currently active tab.
func ActiveTab() string { return activeTabID() }

// TableTab is the tab the config table displays — now the active tab (driven by
// the tab bar). Kept as a named alias so Window/Count/Connect/tests share it.
func TableTab() string { return activeTabID() }

// TabCount returns the number of configs in a tab.
func TabCount(tabID string) int { return memTabCount(tabID) }

// tabExcludeFilter returns the tab's exclude rules when the filter is enabled,
// nil otherwise. Applied as a VIEW filter on read (memWindow), so toggling the
// filter on/off re-filters the visible list instantly with no re-fetch and no
// data loss — the store keeps every fetched config.
func tabExcludeFilter(tabID string) []string {
	state.mu.RLock()
	defer state.mu.RUnlock()
	for _, t := range state.tabs {
		if t.ID == tabID {
			if t.ExcludeDisabled {
				return nil
			}
			return t.ExcludeFilter
		}
	}
	return nil
}

// tabDedupHide reports whether the tab's per-tab dedup mode is "hide" (a
// reversible view filter). "delete" already removed the dupes server-side, so
// only "hide" affects the windowed read.
func tabDedupHide(tabID string) bool {
	state.mu.RLock()
	defer state.mu.RUnlock()
	for _, t := range state.tabs {
		if t.ID == tabID {
			return t.DedupMode == "hide"
		}
	}
	return false
}

// Window returns rows [offset, offset+limit) of a tab plus the total matching
// count, applying the given sort/filter and the tab's own dedup mode. Only the
// visible slice is materialised (the low-RAM windowed read).
func Window(tabID, sort, filter string, proto []string, offset, limit int) ([]Row, int) {
	settingsMu.RLock()
	favs := append([]string(nil), appSettings.Favorites...)
	settingsMu.RUnlock()
	favSet := make(map[string]struct{}, len(favs))
	for _, f := range favs {
		favSet[nodeBody(f)] = struct{}{}
	}
	q := windowQuery{sort: sort, filter: filter, proto: proto, dedupHide: tabDedupHide(tabID), exclude: tabExcludeFilter(tabID), offset: offset, limit: limit, favorites: favs}
	entries, total, _ := memWindow(tabID, q, favs, false)
	rows := make([]Row, len(entries))
	for i, e := range entries {
		_, fav := favSet[nodeBody(e.Raw)]
		rows[i] = Row{
			Index: e.Index, Raw: e.Raw, Name: e.Name, Host: e.Host, Port: e.Port,
			Net: e.Network, Sec: e.Security, Proto: e.Protocol,
			Ping: int(e.Delay), Speed: e.SpeedMBps, SpeedLive: e.SpeedLive,
			PingSt: string(e.PingStatus), SpeedSt: string(e.SpeedStatus),
			PingErr: e.PingErr, SpeedErr: e.SpeedErr, Fav: fav,
		}
	}
	return rows, total
}

// WindowCount returns how many configs in the tab match the filter (the size the
// virtual table is laid out to).
func WindowCount(tabID, filter string, proto []string) int {
	settingsMu.RLock()
	favs := append([]string(nil), appSettings.Favorites...)
	settingsMu.RUnlock()
	q := windowQuery{sort: "idx", filter: filter, proto: proto, dedupHide: tabDedupHide(tabID), exclude: tabExcludeFilter(tabID), offset: 0, limit: 0, favorites: favs}
	_, total, _ := memWindow(tabID, q, favs, false)
	return total
}

// StatsDTO feeds the header (configs / ping ok / failed / best ping / best speed
// / traffic).
type StatsDTO struct {
	Configs   int     `json:"configs"`
	PingOK    int     `json:"ping_ok"`
	Failed    int     `json:"failed"`
	BestPing  int64   `json:"best_ping"`  // ms; 0 = none
	BestSpeed float64 `json:"best_speed"` // MB/s; 0 = none
	TotalUp   int64   `json:"total_up"`
	TotalDown int64   `json:"total_down"`
}

// Stats returns header stats for the tab under the current filter/proto and the
// tab's dedup mode.
func Stats(tabID, filter string, proto []string) StatsDTO {
	settingsMu.RLock()
	favs := append([]string(nil), appSettings.Favorites...)
	tup, tdown := appSettings.StatsTotalUp, appSettings.StatsTotalDown
	settingsMu.RUnlock()
	q := windowQuery{sort: "idx", filter: filter, proto: proto, dedupHide: tabDedupHide(tabID), exclude: tabExcludeFilter(tabID), offset: 0, limit: 0, favorites: favs}
	_, _, st := memWindow(tabID, q, favs, true) // withStats
	return StatsDTO{
		Configs: st.total, PingOK: st.ok, Failed: st.fail,
		BestPing: st.minPing, BestSpeed: st.maxSpeed,
		TotalUp: tup, TotalDown: tdown,
	}
}

// ToggleFavorite stars/unstars row idx (keyed by node body so it survives rename
// and works across tabs), persists, and re-sorts. Returns the new favorite state.
func ToggleFavorite(idx int) bool {
	e, ok := memEntry(TableTab(), idx)
	if !ok {
		return false
	}
	body := nodeBody(e.Raw)
	settingsMu.Lock()
	favs := appSettings.Favorites
	found := -1
	for i, f := range favs {
		if nodeBody(f) == body {
			found = i
			break
		}
	}
	nowFav := found < 0
	if nowFav {
		appSettings.Favorites = append(favs, body)
	} else {
		appSettings.Favorites = append(favs[:found], favs[found+1:]...)
	}
	settingsMu.Unlock()
	saveSettings()
	loadedSignal(TableTab()) // favorites float to the top → re-sort
	return nowFav
}

// EnsureUserTab returns the id of the first non-main tab, creating one if none
// exists (configs can't be pasted into the main/Sources tab).
func EnsureUserTab() string {
	state.mu.RLock()
	for _, t := range state.tabs {
		if !t.IsMain {
			id := t.ID
			state.mu.RUnlock()
			return id
		}
	}
	state.mu.RUnlock()

	n := nextTabNumber()
	tab := Tab{
		ID:       fmt.Sprintf("tab-%d-%d", n, time.Now().UnixMilli()),
		Name:     fmt.Sprintf("Tab %d", n),
		IsMain:   false,
		Closable: true,
	}
	state.mu.Lock()
	state.tabs = append(state.tabs, tab)
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()
	return tab.ID
}

// ── Paste (chunk-streamed) ─────────────────────────────────────────────────
// A large paste arrives from the frontend in several ≤40MB chunks (Wails caps
// a single binding call at 64MB). BeginPaste pins the TARGET TAB up front (so
// switching tabs mid-paste can't reroute later chunks), shows the spinner and
// keeps the tab marked fetching; PasteChunk parses + appends IN MEMORY only;
// EndPaste persists the whole tab with ONE SQLite write (the 1.10 pattern —
// per-chunk storeReplace was O(n²) and very slow).

// BeginPaste marks tabID as loading for the duration of a (possibly
// multi-chunk) paste.
func BeginPaste(tabID string) {
	state.mu.Lock()
	state.fetching[tabID] = true
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: tabID})
}

// PasteChunk parses one chunk and appends the entries to the tab in memory.
// Returns how many configs the chunk contained.
func PasteChunk(tabID, raw string) int {
	newEntries := parseConfigLines(raw)
	if len(newEntries) == 0 {
		return 0
	}
	state.mu.Lock()
	base := len(state.tabEntries[tabID])
	for i, e := range newEntries {
		e.Index = base + i
	}
	state.tabEntries[tabID] = append(state.tabEntries[tabID], newEntries...)
	if state.activeTab == tabID {
		state.entries = state.tabEntries[tabID]
	}
	state.mu.Unlock()
	memInvalidate(tabID)
	return len(newEntries)
}

// EndPaste finishes a paste. The configs are already in memory (PasteChunk),
// so the UI is refreshed IMMEDIATELY — the spinner clears and the table renders
// from memory — and the (slower) SQLite persist runs after, without the user
// waiting on it.
func EndPaste(tabID string) {
	state.mu.Lock()
	delete(state.fetching, tabID)
	state.mu.Unlock()
	loadedSignal(tabID) // UI shows the pasted configs now (served from memory)
	dbPersist(tabID, loadTabEntries(tabID))
	saveTabs()
}

// Paste ingests raw config lines into a tab in one shot (small pastes, tests).
func Paste(tabID, raw string) int {
	BeginPaste(tabID)
	n := PasteChunk(tabID, raw)
	EndPaste(tabID)
	return n
}
