package core

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestMigrateBodyHash builds a legacy (body TEXT + body index) DB by hand and
// verifies migrateBodyHash fills body_hash, drops the old column, and keeps the
// dedup key correct (two rows sharing a body get the same hash).
func TestMigrateBodyHash(t *testing.T) {
	os.MkdirAll(tabsDir(), 0755)
	path := filepath.Join(tabsDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE configs (id INTEGER PRIMARY KEY, tab_id TEXT NOT NULL, idx INTEGER NOT NULL,
		raw TEXT NOT NULL, body TEXT NOT NULL, name TEXT, host TEXT, port INTEGER, network TEXT,
		security TEXT, protocol TEXT, ping INTEGER NOT NULL DEFAULT -1, ping_status TEXT NOT NULL DEFAULT 'pending',
		ping_err TEXT, speed REAL NOT NULL DEFAULT 0, speed_status TEXT NOT NULL DEFAULT 'pending',
		speed_err TEXT, speed_live REAL NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	db.Exec(`CREATE INDEX idx_configs_tab_body ON configs(tab_id, body)`)
	if !columnExists(db, "configs", "body") {
		t.Fatal("precondition: legacy table should have a body column")
	}
	ins := func(idx int, raw string) {
		db.Exec(`INSERT INTO configs (tab_id,idx,raw,body,name,host,port,network,security,protocol)
			VALUES ('t',?,?,?,?,?,?,?,?,?)`, idx, raw, nodeBody(raw), "n", "h", 443, "tcp", "reality", "vless")
	}
	ins(0, "vless://u@h:443?type=tcp#a")
	ins(1, "vless://u@h:443?type=tcp#b") // same body as 0
	ins(2, "vless://u@h:8443?type=tcp#c")

	migrateBodyHash(db)

	if columnExists(db, "configs", "body") {
		t.Error("legacy body column should be dropped")
	}
	if !columnExists(db, "configs", "body_hash") {
		t.Fatal("body_hash column missing after migration")
	}
	var zeros int
	db.QueryRow(`SELECT COUNT(*) FROM configs WHERE body_hash=0`).Scan(&zeros)
	if zeros != 0 {
		t.Errorf("%d rows left with body_hash=0", zeros)
	}
	var distinct int
	db.QueryRow(`SELECT COUNT(DISTINCT body_hash) FROM configs`).Scan(&distinct)
	if distinct != 2 {
		t.Errorf("distinct body_hash=%d want 2 (rows 0,1 share a body)", distinct)
	}
	db.Close()
}

func mkEntry(idx int, raw string, ping int64, ps Status, speed float64, ss Status) *ConfigEntry {
	return &ConfigEntry{
		Index: idx, Raw: raw, Name: raw, Host: "h", Port: 443,
		Network: "tcp", Security: "reality", Protocol: "vless",
		Delay: ping, PingStatus: ps, SpeedMBps: speed, SpeedStatus: ss,
	}
}

// TestConfigStore covers the SQLite persistence layer that production still uses:
// replace, count, the raw set (reload-delta), the ordered load path
// (loadConfigsIntoMemory), incremental delete, and the batched result flush.
// Sort/filter/dedup live in memstore now and are tested in memstore_test.go.
func TestConfigStore(t *testing.T) {
	s, err := openConfigStore() // path is under tabsDir() → temp (TestMain)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()

	tab := "t1"
	entries := []*ConfigEntry{
		mkEntry(0, "vless://u@h:443?type=tcp#zero", -1, StatusPending, 0, StatusPending),
		mkEntry(1, "vless://u@h:443?type=tcp#one", 100, StatusOK, 0, StatusPending),
		mkEntry(2, "vless://u@h:443?type=tcp#two", 50, StatusOK, 20, StatusOK),
		mkEntry(3, "vless://u@h:443?type=tcp#three", -1, StatusFailed, 0, StatusFailed),
	}
	if err := s.replaceTabConfigs(tab, entries); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.tabCount(tab); n != 4 {
		t.Fatalf("count=%d want 4", n)
	}

	// allEntriesOrdered round-trips the rows in idx order with fields intact
	// (this is the load path that fills the in-memory store at startup).
	got, err := s.allEntriesOrdered(tab)
	if err != nil {
		t.Fatal(err)
	}
	idxs := make([]int, len(got))
	for i, e := range got {
		idxs[i] = e.Index
	}
	if !reflect.DeepEqual(idxs, []int{0, 1, 2, 3}) {
		t.Errorf("allEntriesOrdered idx=%v want [0 1 2 3]", idxs)
	}
	if got[2].Delay != 50 || got[2].PingStatus != StatusOK || got[2].SpeedMBps != 20 {
		t.Errorf("row idx2 round-trip wrong: %+v", got[2])
	}

	// tabRawSet returns every stored raw (drives the reload +N/−M delta).
	rs, err := s.tabRawSet(tab)
	if err != nil || len(rs) != 4 {
		t.Errorf("tabRawSet len=%d err=%v want 4", len(rs), err)
	}

	// Batched result flush persists ping+speed (production's queue→flush path).
	s.queuePing(tab, 0, 30, "ok", "")
	s.queueSpeed(tab, 1, 100, "ok", "", 99.5, "ok", "", 0)
	s.flushResults()
	after, _ := s.allEntriesOrdered(tab)
	if after[0].Delay != 30 || after[0].PingStatus != StatusOK {
		t.Errorf("after flush idx0 ping=%v status=%v want 30/ok", after[0].Delay, after[0].PingStatus)
	}
	if after[1].SpeedMBps != 99.5 || after[1].SpeedStatus != StatusOK {
		t.Errorf("after flush idx1 speed=%v status=%v want 99.5/ok", after[1].SpeedMBps, after[1].SpeedStatus)
	}

	// Incremental delete removes only the named rows; survivors keep their idx.
	if err := s.deleteEntriesByIdx(tab, []int{1, 2}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.tabCount(tab); n != 2 {
		t.Errorf("after deleteEntriesByIdx count=%d want 2", n)
	}
	left, _ := s.allEntriesOrdered(tab)
	leftIdx := []int{left[0].Index, left[1].Index}
	if !reflect.DeepEqual(leftIdx, []int{0, 3}) {
		t.Errorf("after delete idx=%v want [0 3] (gaps preserved)", leftIdx)
	}

	// replace clears old rows
	if err := s.replaceTabConfigs(tab, entries[:1]); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.tabCount(tab); n != 1 {
		t.Errorf("after replace count=%d want 1", n)
	}
	t.Log("OK: schema, replace, count, ordered load, raw set, batched flush, incremental delete")
}
