package core

import (
	"strings"
	"unicode/utf8"
)

// RenameEntry renames config idx in the active tab (not the Sources tab). It
// rewrites the node name in the raw URL, persists, migrates raw-keyed references
// (last-connected + live ConnState), and emits entry_update. Ported from 1.10
// handleRenameEntry. Returns false on the Sources tab / bad idx / empty name.
// raw is the configuration the table showed at idx (see resolveRows).
func RenameEntry(idx int, raw, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	// 120 characters, not bytes: a cut by bytes split a Cyrillic letter in two
	// and left half of it in the name.
	if utf8.RuneCountInString(name) > maxEntryName {
		name = string([]rune(name)[:maxEntryName])
	}
	tabID := activeTabID()
	if tabID == "main" {
		return false
	}
	entry, ok := entryFor(tabID, idx, raw)
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
		for i, r := range appSettings.LastConnectedChain {
			if r == oldRaw {
				appSettings.LastConnectedChain[i] = newRaw
			}
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
	state.broadcast(SSEEvent{Type: "entry_update", Payload: entryPayload(entry.snap()), Tab: tabID})
	saveTabs()
	saveSettings()
	return true
}

// maxEntryName is the longest name a rename keeps, in characters.
const maxEntryName = 120

// nextEntryIndexLocked is the index the next config added to a tab should take:
// one past the highest one in use.
//
// NOT the number of entries. A delete leaves the survivors their indices (see
// DeleteEntries), so after one the count is smaller than the highest index and
// counting hands a new config an index another row already owns. Two rows with
// one index is not a cosmetic problem: memEntry answers with whichever the
// positional lookup lands on, so connecting to one row started the other config
// — and an entry_update about either rewrote both rows, which is the "the row
// showed something else for a moment" of it.
//
// Caller holds state.mu.
func nextEntryIndexLocked(tabID string) int {
	next := 0
	for _, e := range state.tabEntries[tabID] {
		if e.Index >= next {
			next = e.Index + 1
		}
	}
	return next
}

// DeleteEntries removes the given config indices from the active tab (not the
// Sources tab) in place — survivors keep their idx (consumers look up by idx, not
// position). Ported from 1.10 handleTabDeleteEntries. raws, when given, are the
// configurations the table showed at those indices (see resolveRows). Returns
// how many were removed; the delete can be undone (UndoDelete).
func DeleteEntries(indices []int, raws []string) int {
	id := activeTabID()
	if id == "main" || len(indices) == 0 {
		return 0
	}
	indices = resolveRows(id, indices, raws)
	if len(indices) == 0 {
		return 0
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
	removed := len(src) - len(kept)
	rememberDelete(id, append([]*ConfigEntry(nil), src...), kept, entryIndices(src), entryIndices(kept))
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
	return removed
}

// DeleteDuplicatesOf removes every other copy of one configuration, keeping the
// one the user pointed at.
//
// "Another copy" is dedupKey's answer, not string equality: the same server
// arrives from different sources with its parameters in a different order, a
// transport spelled by an alias, defaults written out or left off, and a
// different name after the #. All of those are one server, and the table's own
// "hide duplicates" already treats them as one — this is the same question,
// asked destructively.
//
// The kept row keeps its index; the rest are removed by the ordinary delete
// path, so the tab is persisted and the table reloads exactly as it does for a
// manual delete. Returns how many went. raw is the configuration the table
// showed at idx (see resolveRows).
func DeleteDuplicatesOf(idx int, raw string) int {
	tabID := activeTabID()
	if tabID == "main" {
		return 0
	}
	keep, ok := entryFor(tabID, idx, raw)
	if !ok {
		return 0
	}
	idx = keep.snap().Index
	want := dedupKey(keep.snap().Raw)

	state.mu.RLock()
	entries := append([]*ConfigEntry(nil), state.tabEntries[tabID]...)
	state.mu.RUnlock()

	var doomed []int
	for _, e := range entries {
		es := e.snap()
		if es.Index == idx {
			continue
		}
		if dedupKey(es.Raw) == want {
			doomed = append(doomed, es.Index)
		}
	}
	if len(doomed) == 0 {
		return 0
	}
	return DeleteEntries(doomed, nil)
}
