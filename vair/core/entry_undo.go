package core

import (
	"sync"
	"time"
)

// ── Rows the user acted on ──────────────────────────────────────────────────
//
// The table addresses a configuration by its index, and an index is a position:
// a refresh of the tab's source rebuilds the tab and numbers it again from the
// top. A selection or a menu opened before that refresh named another
// configuration after it — the menu opened on B edited A, and Delete removed A
// while B was the one selected. The table sends the configuration it showed
// along with the index, and these find that configuration wherever it went.

// resolveRows turns (index, raw) pairs into the indices the configurations have
// now. An empty raw trusts the index; a raw that is no longer in the tab is
// dropped. Each row is used once, so a selection of two identical copies still
// means two rows.
func resolveRows(tabID string, indices []int, raws []string) []int {
	state.mu.RLock()
	src := append([]*ConfigEntry(nil), state.tabEntries[tabID]...)
	byIdx := make(map[int]string, len(src))
	for _, e := range src {
		byIdx[e.Index] = e.Raw
	}
	state.mu.RUnlock()

	var byRaw map[string][]int // built only when an index has moved
	used := make(map[int]bool, len(indices))
	out := make([]int, 0, len(indices))
	for k, idx := range indices {
		raw := ""
		if k < len(raws) {
			raw = raws[k]
		}
		if cur, ok := byIdx[idx]; ok && !used[idx] && (raw == "" || cur == raw) {
			used[idx] = true
			out = append(out, idx)
			continue
		}
		if raw == "" {
			continue
		}
		if byRaw == nil {
			byRaw = make(map[string][]int, len(src))
			for _, e := range src {
				byRaw[byIdx[e.Index]] = append(byRaw[byIdx[e.Index]], e.Index)
			}
		}
		for _, cand := range byRaw[raw] {
			if !used[cand] {
				used[cand] = true
				out = append(out, cand)
				break
			}
		}
	}
	return out
}

// entryFor is resolveRows for one row.
func entryFor(tabID string, idx int, raw string) (*ConfigEntry, bool) {
	r := resolveRows(tabID, []int{idx}, []string{raw})
	if len(r) == 0 {
		return nil, false
	}
	return memEntry(tabID, r[0])
}

// IndicesOf is where each of raws sits in the tab now, -1 for one that is gone:
// the table's selection follows its configurations through a refresh.
func IndicesOf(tabID string, raws []string) []int {
	state.mu.RLock()
	byRaw := make(map[string][]int)
	for _, e := range state.tabEntries[tabID] {
		byRaw[e.Raw] = append(byRaw[e.Raw], e.Index)
	}
	state.mu.RUnlock()
	out := make([]int, len(raws))
	for k, raw := range raws {
		out[k] = -1
		if c := byRaw[raw]; len(c) > 0 {
			out[k], byRaw[raw] = c[0], c[1:]
		}
	}
	return out
}

// ── Undo of the last delete ────────────────────────────────────────────────

// lastDelete is the tab as it was before the last delete, kept for the "Undo"
// on the notification that follows it. Only while the tab is exactly what that
// delete left — anything that changed it since (a paste, a refresh, a move)
// makes putting the old list back a second, unasked-for change — and only for
// a minute: the deleted rows of a large tab are a lot to hold on to.
var lastDelete struct {
	sync.Mutex
	tab    string
	before []*ConfigEntry
	idx    []int // before[i]'s index then
	after  []*ConfigEntry
	afterI []int
	timer  *time.Timer
}

const deleteUndoFor = time.Minute

// rememberDelete records a delete. Caller holds state.mu (read or write) or
// passes slices nobody else mutates.
func rememberDelete(tabID string, before, after []*ConfigEntry, beforeIdx, afterIdx []int) {
	lastDelete.Lock()
	defer lastDelete.Unlock()
	if lastDelete.timer != nil {
		lastDelete.timer.Stop()
	}
	lastDelete.tab, lastDelete.before, lastDelete.idx = tabID, before, beforeIdx
	lastDelete.after, lastDelete.afterI = after, afterIdx
	lastDelete.timer = time.AfterFunc(deleteUndoFor, forgetDelete)
}

func forgetDelete() {
	lastDelete.Lock()
	defer lastDelete.Unlock()
	lastDelete.tab, lastDelete.before, lastDelete.idx, lastDelete.after, lastDelete.afterI = "", nil, nil, nil, nil
}

func entryIndices(es []*ConfigEntry) []int {
	out := make([]int, len(es))
	for i, e := range es {
		out[i] = e.Index
	}
	return out
}

// UndoResult is UndoDelete's answer for the table.
type UndoResult struct {
	Restored int    `json:"restored"`
	Reason   string `json:"reason,omitempty"`
}

// Why an undo did not happen, for the notification.
const (
	undoNothing = "nothing to restore"
	undoChanged = "the tab has changed since — the deleted configs cannot be put back"
)

// UndoDelete puts the last delete back: the rows, their places and their test
// results. Returns how many came back, or the reason none did.
func UndoDelete() (int, string) {
	lastDelete.Lock()
	tab, before, bIdx, after, aIdx := lastDelete.tab, lastDelete.before, lastDelete.idx, lastDelete.after, lastDelete.afterI
	lastDelete.Unlock()
	if tab == "" {
		return 0, undoNothing
	}
	state.mu.Lock()
	cur := state.tabEntries[tab]
	same := len(cur) == len(after)
	for i := 0; same && i < len(cur); i++ {
		same = cur[i] == after[i] && cur[i].Index == aIdx[i]
	}
	if !same {
		state.mu.Unlock()
		forgetDelete()
		return 0, undoChanged
	}
	restored := append([]*ConfigEntry(nil), before...)
	for i, e := range restored {
		e.Index = bIdx[i]
	}
	state.tabEntries[tab] = restored
	if state.activeTab == tab {
		state.entries = restored
	}
	state.mu.Unlock()
	forgetDelete()
	memInvalidate(tab)
	loadedSignal(tab)
	// One transaction: in a tab of its own (no source to fetch it again) the old
	// rows going and the restored ones arriving must not be separable.
	if store != nil {
		if err := store.rewriteTabAtomic(tab, restored); err != nil {
			vlog("warning", "undo delete %s: %v", tab, err)
		}
	}
	saveTabs()
	return len(before) - len(after), ""
}
