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
	for i, t := range state.tabs {
		if t.ID == id {
			state.tabs = append(state.tabs[:i], state.tabs[i+1:]...)
			break
		}
	}
	delete(state.tabEntries, id)
	if state.activeTab == id {
		state.activeTab = "main"
		state.entries = state.tabEntries["main"]
	}
	state.mu.Unlock()
	memInvalidate(id)
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: Tabs()})
	state.broadcast(SSEEvent{Type: "active_tab", Payload: ActiveTab()})
	loadedSignal("main")
	saveTabs()
	if store != nil {
		go store.deleteTabRows(id)
	}
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
