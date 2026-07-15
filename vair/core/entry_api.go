package core

import "strings"

// RenameEntry renames config idx in the active tab (not the Sources tab). It
// rewrites the node name in the raw URL, persists, migrates raw-keyed references
// (last-connected + live ConnState), and emits entry_update. Ported from 1.10
// handleRenameEntry. Returns false on the Sources tab / bad idx / empty name.
func RenameEntry(idx int, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if len(name) > 120 {
		name = name[:120]
	}
	tabID := activeTabID()
	if tabID == "main" {
		return false
	}
	entry, ok := memEntry(tabID, idx)
	if !ok {
		return false
	}
	entry.mu.Lock()
	oldRaw := entry.Raw
	newRaw := setNodeName(oldRaw, name)
	entry.Raw = newRaw
	entry.Name = name
	entry.mu.Unlock()
	if store != nil {
		store.updateName(tabID, idx, name, newRaw)
	}
	if oldRaw != newRaw {
		settingsMu.Lock()
		if appSettings.LastConnectedRaw == oldRaw {
			appSettings.LastConnectedRaw = newRaw
		}
		settingsMu.Unlock()
		cm := state.conn
		cm.mu.Lock()
		if cm.state.ConnRaw == oldRaw {
			cm.state.ConnRaw = newRaw
		}
		for i, cr := range cm.state.ChainRaws {
			if cr == oldRaw {
				cm.state.ChainRaws[i] = newRaw
			}
		}
		cm.mu.Unlock()
	}
	state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	saveTabs()
	saveSettings()
	return true
}

// DeleteEntries removes the given config indices from the active tab (not the
// Sources tab) in place — survivors keep their idx (consumers look up by idx, not
// position). Ported from 1.10 handleTabDeleteEntries.
func DeleteEntries(indices []int) {
	id := activeTabID()
	if id == "main" || len(indices) == 0 {
		return
	}
	// Big deletes take a moment (the in-place rebuild + the SQLite delete of up
	// to 260k rows), so show the "Deleting configs…" spinner — 1.10 only above
	// 2000, to avoid a flicker on small deletes; loadedSignal clears it.
	big := len(indices) > 2000
	if big {
		state.mu.Lock()
		state.fetching[id] = true
		state.mu.Unlock()
		state.broadcast(SSEEvent{Type: "loading", Payload: map[string]string{"op": "delete"}, Tab: id})
	}
	toRemove := make(map[int]bool, len(indices))
	for _, idx := range indices {
		toRemove[idx] = true
	}
	state.mu.Lock()
	src := state.tabEntries[id]
	kept := make([]*ConfigEntry, 0, len(src))
	for _, e := range src {
		if !toRemove[e.Index] {
			kept = append(kept, e)
		}
	}
	state.tabEntries[id] = kept
	if state.activeTab == id {
		state.entries = kept
	}
	deletedAll := len(kept) == 0
	state.mu.Unlock()
	memInvalidate(id)
	// Persist. Deleting EVERYTHING (select-all) is a single whole-tab DELETE —
	// vastly faster than chunking 260k idx values through `WHERE idx IN (…)`.
	if store != nil {
		if deletedAll {
			store.deleteTabRows(id)
		} else {
			store.deleteEntriesByIdx(id, indices)
		}
	}
	// Spinner stays up for the whole operation (the 1.10 feel — the table is
	// replaced by "Deleting configs…" until it's done), then loadedSignal clears
	// it and the tab re-renders.
	if big {
		state.mu.Lock()
		delete(state.fetching, id)
		state.mu.Unlock()
	}
	loadedSignal(id)
	saveTabs()
}
