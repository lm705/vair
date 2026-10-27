package core

import (
	"sync"
	"time"
)

// Undo of a deleted tab.
//
// "Delete tab" removed the tab and every configuration in it at once — no
// question, no way back — and a tab of the user's own configurations has no
// source to load them from again. The notification that follows the delete
// carries an Undo for a minute; this is what it puts back: the tab with its
// settings, where it stood in the tab bar, its rows with what was measured on
// them, its place among auto-connect's candidate tabs, and the focus if it had
// it.
//
// The rows stay in the store for that minute and are removed when it is over.
// They used to go at once, and an Undo had to wait for that delete and then
// write every row back: on a large tab over a minute with nothing on screen
// (the tab looked gone for good) and the database grown by the size of the tab.
// Kept, they need no writing at all, and the tab is back the moment it is
// asked for. Until they are removed the tab is listed as being deleted
// (persistedData.Deleting), so a program closed within that minute removes
// them at its next start rather than bringing the tab back as "Recovered N".

var lastTabDelete struct {
	sync.Mutex
	tab       Tab
	pos       int
	entries   []*ConfigEntry
	wasActive bool
	autoPos   int // its index in AutoTabs, -1 when it was not there
	timer     *time.Timer
}

const tabUndoFor = time.Minute

// rememberTabDelete is called by DeleteTab with the tab as it was. A tab
// deleted before can no longer be brought back, so its rows go now.
func rememberTabDelete(t Tab, pos int, entries []*ConfigEntry, wasActive bool) {
	settingsMu.RLock()
	autoPos := -1
	for i, id := range appSettings.AutoTabs {
		if id == t.ID {
			autoPos = i
			break
		}
	}
	settingsMu.RUnlock()
	lastTabDelete.Lock()
	prev := lastTabDelete.tab.ID
	if lastTabDelete.timer != nil {
		lastTabDelete.timer.Stop()
	}
	lastTabDelete.tab, lastTabDelete.pos, lastTabDelete.entries = t, pos, entries
	lastTabDelete.wasActive, lastTabDelete.autoPos = wasActive, autoPos
	lastTabDelete.timer = time.AfterFunc(tabUndoFor, expireTabDelete)
	lastTabDelete.Unlock()
	if prev != "" && prev != t.ID {
		removeDeletedTabRows(prev)
	}
}

// expireTabDelete ends the minute an Undo was possible in: the rows go.
func expireTabDelete() {
	lastTabDelete.Lock()
	id := lastTabDelete.tab.ID
	lastTabDelete.tab, lastTabDelete.entries, lastTabDelete.timer = Tab{}, nil, nil
	lastTabDelete.Unlock()
	if id != "" {
		removeDeletedTabRows(id)
	}
}

// removeDeletedTabRows removes a deleted tab's rows from the store, on its own
// goroutine — a large tab is a long delete — and then stops listing the tab as
// being deleted.
func removeDeletedTabRows(id string) {
	if store == nil {
		markTabDeleting(id, false)
		return
	}
	go func() {
		if store.deleteTabRows(id) == nil {
			markTabDeleting(id, false)
			saveTabs()
		}
	}()
}

// TabUndoResult is UndoDeleteTab's answer for the window.
type TabUndoResult struct {
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
}

// UndoDeleteTab puts the last deleted tab back.
func UndoDeleteTab() TabUndoResult {
	lastTabDelete.Lock()
	t, pos, entries := lastTabDelete.tab, lastTabDelete.pos, lastTabDelete.entries
	wasActive, autoPos := lastTabDelete.wasActive, lastTabDelete.autoPos
	if lastTabDelete.timer != nil {
		lastTabDelete.timer.Stop()
	}
	lastTabDelete.tab, lastTabDelete.entries, lastTabDelete.timer = Tab{}, nil, nil
	lastTabDelete.Unlock()
	if t.ID == "" {
		return TabUndoResult{Reason: undoNothing}
	}

	// Its rows are still in the store: no longer being deleted, and nothing to
	// write back.
	markTabDeleting(t.ID, false)
	state.mu.Lock()
	if pos < 0 || pos > len(state.tabs) {
		pos = len(state.tabs)
	}
	state.tabs = append(state.tabs[:pos], append([]Tab{t}, state.tabs[pos:]...)...)
	state.tabEntries[t.ID] = entries
	delete(state.cancelledTabs, t.ID)
	if wasActive {
		state.activeTab = t.ID
		state.entries = entries
	}
	state.mu.Unlock()
	memInvalidate(t.ID)

	if autoPos >= 0 {
		settingsMu.Lock()
		if autoPos > len(appSettings.AutoTabs) {
			autoPos = len(appSettings.AutoTabs)
		}
		appSettings.AutoTabs = append(appSettings.AutoTabs[:autoPos], append([]string{t.ID}, appSettings.AutoTabs[autoPos:]...)...)
		s := appSettings
		settingsMu.Unlock()
		saveSettings()
		broadcastSettings(s)
	}
	saveTabs()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: Tabs()})
	if wasActive {
		state.broadcast(SSEEvent{Type: "active_tab", Payload: ActiveTab()})
	}
	loadedSignal(t.ID)
	return TabUndoResult{Name: t.Name}
}
