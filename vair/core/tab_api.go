package core

import (
	"fmt"
	"time"
)

// TabDTO is a tab for the frontend tab bar.
type TabDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsMain   bool   `json:"is_main"`
	Closable bool   `json:"closable"`
	Count    int    `json:"count"`
	Fetching bool   `json:"fetching"`             // a reload/fetch is in flight → show the spinner
	Dedup    string `json:"dedup_mode,omitempty"` // "" | hide | delete — view re-queries on change
	// All counts the tab's configurations including the ones its exclude filter
	// and "hide duplicates" keep out of view: a tab whose filter hid everything
	// read as "This tab is empty", with hints to add configurations it had.
	All int `json:"all"`
	// SourceError is why the tab's source did not load the last time, or "".
	// The table learned it only from a one-time event, so a window or web page
	// opened afterwards showed an empty tab with nothing to say why.
	SourceError string `json:"source_error,omitempty"`
}

// tabSourceError is the first reason a source of the tab failed on its last
// fetch: a URL, a file or the GitHub import.
func tabSourceError(t Tab) string {
	for _, s := range t.Subs {
		if s.Error != "" {
			return s.Error
		}
	}
	for _, f := range t.SourceFiles {
		if f.Error != "" && !f.Disabled {
			return f.Name + ": " + f.Error
		}
	}
	if t.GitHubError != "" {
		return "GitHub: " + t.GitHubError
	}
	return ""
}

// Tabs returns all tabs (with live config counts + fetching state).
func Tabs() []TabDTO {
	state.mu.RLock()
	tabs := make([]Tab, len(state.tabs))
	copy(tabs, state.tabs)
	fetching := make(map[string]bool, len(state.fetching))
	for id, f := range state.fetching {
		fetching[id] = f
	}
	state.mu.RUnlock()
	out := make([]TabDTO, 0, len(tabs))
	for _, t := range tabs {
		out = append(out, TabDTO{
			ID: t.ID, Name: t.Name, IsMain: t.IsMain, Closable: t.Closable,
			Count: memTabVisibleCount(t.ID), Fetching: fetching[t.ID], Dedup: t.DedupMode,
			All: memTabCount(t.ID), SourceError: tabSourceError(t),
		})
	}
	return out
}

// SwitchTab makes id the active tab (what the table displays + tests/connects act on).
// SwitchTab makes id the active tab. Returns whether a reload/fetch is in
// flight for it RIGHT NOW (read under the same lock — no stale-snapshot race),
// so the UI can show/clear the spinner reliably; the tab-tagged "loading"
// broadcast covers backend-initiated switches too (1.10 handleTabSwitch).
func SwitchTab(id string) bool {
	state.mu.Lock()
	state.activeTab = id
	state.entries = state.tabEntries[id]
	inFlight := state.fetching[id]
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "active_tab", Payload: id})
	if inFlight {
		state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: id})
	}
	return inFlight
}

// CreateTab adds a new empty user tab and returns it.
func CreateTab() TabDTO {
	n := nextTabNumber()
	tab := Tab{
		ID:       fmt.Sprintf("tab-%d-%d", n, time.Now().UnixMilli()),
		Name:     fmt.Sprintf("Tab %d", n),
		Closable: true,
	}
	state.mu.Lock()
	state.tabs = append(state.tabs, tab)
	delete(state.cancelledTabs, tab.ID)
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: Tabs()})
	saveTabs()
	return TabDTO{ID: tab.ID, Name: tab.Name, Closable: true}
}

// DeleteTab removes a user tab (never main) + its configs. Mirrors 1.10 handleTabDelete.
func DeleteTab(id string) {
	if id == "main" {
		return
	}
	state.mu.Lock()
	state.cancelledTabs[id] = true
	var removed Tab
	pos := -1
	for i, t := range state.tabs {
		if t.ID == id {
			removed, pos = t, i
			state.tabs = append(state.tabs[:i], state.tabs[i+1:]...)
			break
		}
	}
	entries := state.tabEntries[id]
	wasActive := state.activeTab == id
	delete(state.tabEntries, id)
	if wasActive {
		state.activeTab = "main"
		state.entries = state.tabEntries["main"]
	}
	state.mu.Unlock()
	// Written down as "being deleted" with the tab list, until its rows are gone
	// from the database (see persistedData.Deleting).
	markTabDeleting(id, true)
	// Kept for the Undo on the notification (tab_undo.go) — before the tab
	// leaves the candidate tabs, so its place there can be restored too. Its rows
	// stay in the store for that minute and go when it ends.
	if pos >= 0 {
		rememberTabDelete(removed, pos, entries, wasActive)
	} else {
		removeDeletedTabRows(id)
	}
	memInvalidate(id)
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: Tabs()})
	state.broadcast(SSEEvent{Type: "active_tab", Payload: ActiveTab()})
	loadedSignal("main")
	saveTabs()
	dropFromAutoPool(id)
}

// RenameTab renames a tab.
func RenameTab(id, name string) {
	if name == "" {
		return
	}
	state.mu.Lock()
	for i := range state.tabs {
		if state.tabs[i].ID == id {
			state.tabs[i].Name = name
			break
		}
	}
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: Tabs()})
	saveTabs()
}

// dropFromAutoPool takes a deleted tab out of auto-connect's candidate tabs. It
// stayed there, invisible in the panel, and a pool of deleted tabs had nothing
// to connect to. With nothing left, the default — SOURCES — applies, as for an
// empty selection.
func dropFromAutoPool(id string) {
	settingsMu.Lock()
	kept := make([]string, 0, len(appSettings.AutoTabs))
	for _, t := range appSettings.AutoTabs {
		if t != id {
			kept = append(kept, t)
		}
	}
	changed := len(kept) != len(appSettings.AutoTabs)
	if changed {
		appSettings.AutoTabs = kept
	}
	s := appSettings
	settingsMu.Unlock()
	if changed {
		saveSettings()
		broadcastSettings(s)
	}
}
