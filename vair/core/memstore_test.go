package core

import (
	"reflect"
	"testing"
)

// memSetup installs entries as a tab's in-memory working copy and returns a
// cleanup func. memOrder/memWindow read state.tabEntries (configs live in RAM,
// SQLite is just persistence), so this exercises the PRODUCTION read path —
// sort/filter/dedup/favorites/stats — without a DB.
func memSetup(tab string, entries []*ConfigEntry) func() {
	state.mu.Lock()
	if state.tabEntries == nil {
		state.tabEntries = map[string][]*ConfigEntry{}
	}
	state.tabEntries[tab] = entries
	state.mu.Unlock()
	memInvalidate(tab)
	return func() {
		state.mu.Lock()
		delete(state.tabEntries, tab)
		state.mu.Unlock()
		memInvalidate(tab)
	}
}

func memIdxOrder(tab string, q windowQuery) ([]int, int) {
	rows, total, _ := memWindow(tab, q, q.favorites, false)
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = r.Index
	}
	return out, total
}

func TestMemStore(t *testing.T) {
	tab := "mt1"
	// Distinct ports → distinct bodies, so the favorites test (keyed by body)
	// floats exactly one row.
	entries := []*ConfigEntry{
		mkEntry(0, "vless://u@h:1443?type=tcp#zero", -1, StatusPending, 0, StatusPending),
		mkEntry(1, "vless://u@h:2443?type=tcp#one", 100, StatusOK, 0, StatusPending),
		mkEntry(2, "vless://u@h:3443?type=tcp#two", 50, StatusOK, 20, StatusOK),
		mkEntry(3, "vless://u@h:4443?type=tcp#three", -1, StatusFailed, 0, StatusFailed),
	}
	defer memSetup(tab, entries)()

	// idx sort
	if got, total := memIdxOrder(tab, windowQuery{sort: "idx"}); !reflect.DeepEqual(got, []int{0, 1, 2, 3}) || total != 4 {
		t.Errorf("idx sort=%v total=%d", got, total)
	}
	// ping sort: live by ping ASC (2 then 1), then dead by idx (0 then 3)
	if got, _ := memIdxOrder(tab, windowQuery{sort: "ping"}); !reflect.DeepEqual(got, []int{2, 1, 0, 3}) {
		t.Errorf("ping sort=%v want [2 1 0 3]", got)
	}
	// speed sort: rank0(2), rank1(1), rank3(3), rank4(0)
	if got, _ := memIdxOrder(tab, windowQuery{sort: "speed"}); !reflect.DeepEqual(got, []int{2, 1, 3, 0}) {
		t.Errorf("speed sort=%v want [2 1 3 0]", got)
	}
	// window offset/limit
	if got, total := memIdxOrder(tab, windowQuery{sort: "idx", offset: 1, limit: 2}); !reflect.DeepEqual(got, []int{1, 2}) || total != 4 {
		t.Errorf("window=%v total=%d", got, total)
	}
	// filter (search by name fragment)
	if got, total := memIdxOrder(tab, windowQuery{filter: "two"}); !reflect.DeepEqual(got, []int{2}) || total != 1 {
		t.Errorf("filter=%v total=%d", got, total)
	}
	// '+' filter: same column = OR (two names) → both; different columns = AND.
	// Entries are network=tcp, security=reality, protocol=vless.
	if got, _ := memIdxOrder(tab, windowQuery{filter: "zero+two"}); !reflect.DeepEqual(got, []int{0, 2}) {
		t.Errorf("filter zero+two=%v want [0 2] (name OR)", got)
	}
	if got, _ := memIdxOrder(tab, windowQuery{filter: "two+tcp"}); !reflect.DeepEqual(got, []int{2}) {
		t.Errorf("filter two+tcp=%v want [2] (name AND transport)", got)
	}
	if _, total := memIdxOrder(tab, windowQuery{filter: "tcp+xhttp"}); total != 4 {
		t.Errorf("filter tcp+xhttp total=%d want 4 (transport OR)", total)
	}
	if _, total := memIdxOrder(tab, windowQuery{filter: "tcp+reality"}); total != 4 {
		t.Errorf("filter tcp+reality total=%d want 4 (transport AND security)", total)
	}
	if _, total := memIdxOrder(tab, windowQuery{filter: "two+vmess"}); total != 0 {
		t.Errorf("filter two+vmess total=%d want 0 (type AND mismatch)", total)
	}
	// A bare term does NOT inherit a preceding field: prefix. "name:two+zero" is
	// name=two AND zero-anywhere → empty (idx2 has no "zero").
	if _, total := memIdxOrder(tab, windowQuery{filter: "name:two+zero"}); total != 0 {
		t.Errorf("filter name:two+zero total=%d want 0 (no field inheritance)", total)
	}
	// proto pill filter (all four are vless → all match "vless"; none match "vmess")
	if _, total := memIdxOrder(tab, windowQuery{proto: []string{"vless"}}); total != 4 {
		t.Errorf("proto vless total=%d want 4", total)
	}
	if _, total := memIdxOrder(tab, windowQuery{proto: []string{"vmess"}}); total != 0 {
		t.Errorf("proto vmess total=%d want 0", total)
	}
	// header stats over the unfiltered set: 4 total, 2 ping-ok (idx1,2), 1 failed (idx3)
	if _, _, st := memWindow(tab, windowQuery{}, nil, true); st.total != 4 || st.ok != 2 || st.fail != 1 {
		t.Errorf("stats=(%d,%d,%d) want (4,2,1)", st.total, st.ok, st.fail)
	}
	// favorites float to top
	if got, _ := memIdxOrder(tab, windowQuery{sort: "idx", favorites: []string{entries[3].Raw}}); got[0] != 3 {
		t.Errorf("favorites first=%v want idx3 on top", got)
	}
	// memIndices covers the whole filtered set in order
	if got := memIndices(tab, windowQuery{sort: "idx"}, nil); !reflect.DeepEqual(got, []int{0, 1, 2, 3}) {
		t.Errorf("memIndices=%v want [0 1 2 3]", got)
	}

	// dedup hide: two configs with the same body (differ only by #name)
	tab2 := "mt2"
	dups := []*ConfigEntry{
		mkEntry(0, "vless://u@h:443?type=tcp#a", -1, StatusPending, 0, StatusPending),
		mkEntry(1, "vless://u@h:443?type=tcp#b", -1, StatusPending, 0, StatusPending), // same body as 0
		mkEntry(2, "vless://u@h:8443?type=tcp#c", -1, StatusPending, 0, StatusPending),
	}
	defer memSetup(tab2, dups)()
	if got, total := memIdxOrder(tab2, windowQuery{sort: "idx", dedupHide: true}); !reflect.DeepEqual(got, []int{0, 2}) || total != 2 {
		t.Errorf("dedupHide=%v total=%d want [0 2]/2", got, total)
	}
	// without dedup, all three
	if _, total := memIdxOrder(tab2, windowQuery{sort: "idx"}); total != 3 {
		t.Errorf("no-dedup total=%d want 3", total)
	}
}
