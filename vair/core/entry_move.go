package core

import (
	"sync"
	"sync/atomic"
	"time"
)

// Moving configurations within a tab: the drag-and-drop of rows in the table.

// Where MoveEntries puts the moved configs when target names no row.
const (
	MoveToEnd   = -1
	MoveToStart = -2
)

// MoveEntries puts the given configs of a tab, in the order they already had,
// immediately before the config whose index is target — or after it, when
// after is set — or at the end (MoveToEnd) or the start (MoveToStart) of the
// tab. "After" is its own case because the table may be filtered: below the
// last row it shows is not the end of the tab. It returns the moved configs'
// new indices, for the table to keep them selected, and nil when nothing moved.
//
// Not in Sources: its order is the sources' own, rebuilt on every refresh. A
// tab with a source of its own can be reordered, and a refresh of that source
// rebuilds the list in the source's order, as it always has.
//
// Not during a PING ALL / SPEED ALL of the tab. A tab's order on disk is its
// indices (the tab is loaded ORDER BY idx, and memEntry finds a row by its
// position), so a move renumbers the tab — and a bulk run tests copies loaded
// from the store and hands each result back under the index the copy was
// loaded with, which after a move is another row's. Everything else that tests
// — AUTO's sweep, the re-test after a refresh, a single row's ping — works on
// the tab's own entries and reads the index when it writes, so it follows the
// move. Refusing those too meant that with AUTO on a tab could not be
// reordered for as long as its candidates were being tested.
func MoveEntries(tabID string, indices []int, target int, after bool) []int {
	if tabID == "" || tabID == "main" || len(indices) == 0 {
		return nil
	}
	if bulkTestOnTab(tabID) {
		vlog("info", "move: PING ALL / SPEED ALL is running on this tab — try again when it finishes")
		return nil
	}
	moving := make(map[int]bool, len(indices))
	for _, i := range indices {
		moving[i] = true
	}
	// Dropped onto one of the rows being moved: there is nowhere else to go.
	if target >= 0 && moving[target] {
		return nil
	}

	state.mu.Lock()
	src := state.tabEntries[tabID]
	moved := make([]*ConfigEntry, 0, len(indices))
	rest := make([]*ConfigEntry, 0, len(src))
	for _, e := range src {
		if moving[e.Index] {
			moved = append(moved, e)
		} else {
			rest = append(rest, e)
		}
	}
	if len(moved) == 0 {
		state.mu.Unlock()
		return nil
	}
	pos := len(rest)
	switch {
	case target == MoveToStart:
		pos = 0
	case target >= 0:
		found := false
		for i, e := range rest {
			if e.Index == target {
				pos, found = i, true
				if after {
					pos++
				}
				break
			}
		}
		if !found {
			state.mu.Unlock()
			return nil
		}
	}
	out := make([]*ConfigEntry, 0, len(src))
	out = append(out, rest[:pos]...)
	out = append(out, moved...)
	out = append(out, rest[pos:]...)

	same := len(out) == len(src)
	for i := range out {
		if !same || out[i] != src[i] {
			same = false
			break
		}
	}
	if same {
		state.mu.Unlock()
		return nil
	}
	// Renumber to the new order: index = position, as a freshly loaded tab has.
	remap := make(map[int]int, len(out))
	for i, e := range out {
		e.mu.Lock()
		if e.Index != i {
			remap[e.Index] = i
			e.Index = i
		}
		e.mu.Unlock()
	}
	state.tabEntries[tabID] = out
	if state.activeTab == tabID {
		state.entries = out
	}
	state.mu.Unlock()
	memInvalidate(tabID)

	// The connection names its row by index too.
	cm := state.conn
	cm.mu.Lock()
	connMoved := false
	if cm.state.ConnTab == tabID {
		if ni, ok := remap[cm.state.EntryIndex]; ok {
			cm.state.EntryIndex = ni
			connMoved = true
		}
	}
	cm.mu.Unlock()

	// The table reads from memory, so it is told now — not after the disk.
	// Written after, the signal waited on the store, which a running sweep keeps
	// busy with its results, and the table went on showing the old order.
	loadedSignal(tabID)
	if connMoved {
		state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	}
	saveTabOrder(tabID)
	res := make([]int, len(moved))
	for i := range moved {
		res[i] = pos + i
	}
	return res
}

// bulkTestOnTab reports a PING ALL / SPEED ALL run on the tab (see MoveEntries).
func bulkTestOnTab(tabID string) bool {
	if atomic.LoadInt32(&state.pingRunning) == 0 && atomic.LoadInt32(&state.speedRunning) == 0 {
		return false
	}
	testMu.Lock()
	defer testMu.Unlock()
	return testingTab == tabID
}

// The order is written to disk off the caller's path. The store is shared with
// a running sweep's results and the session log, and a move made while they
// were writing waited for them — the call came back seconds later, and the
// table with it. One writer per tab: moves that land while it writes are
// folded into one more write of whatever the order is by then.
var orderSave = struct {
	sync.Mutex
	running map[string]bool
	again   map[string]bool
}{running: map[string]bool{}, again: map[string]bool{}}

func saveTabOrder(tabID string) {
	if store == nil {
		return
	}
	orderSave.Lock()
	if orderSave.running[tabID] {
		orderSave.again[tabID] = true
		orderSave.Unlock()
		return
	}
	orderSave.running[tabID] = true
	orderSave.Unlock()
	go func() {
		for {
			state.mu.RLock()
			out := append([]*ConfigEntry(nil), state.tabEntries[tabID]...)
			state.mu.RUnlock()
			start := time.Now()
			// Results still queued under the old indices go in first; the
			// whole tab is then written from memory, which already holds them
			// in the right rows, so whatever the flush put in the wrong place
			// is replaced.
			store.flushResults()
			if err := store.rewriteTabAtomic(tabID, out); err != nil {
				vlog("error", "move: could not save the new order: %v", err)
			} else if d := time.Since(start); d > time.Second {
				vlog("info", "move: the new order took %s to save", d.Round(time.Millisecond))
			}
			orderSave.Lock()
			if orderSave.again[tabID] {
				orderSave.again[tabID] = false
				orderSave.Unlock()
				continue
			}
			delete(orderSave.running, tabID)
			orderSave.Unlock()
			return
		}
	}()
}
