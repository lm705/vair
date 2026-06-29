package main

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// configStore is the SQLite-backed store for tab configs. It replaces the
// in-memory state.tabEntries / state.entries so that huge lists (100k+) live on
// disk and only a window is held in RAM. See plan effervescent-zooming-bunny.
//
// Concurrency: rollback-journal (DELETE) mode → single file on disk. A writer
// briefly takes an exclusive lock, so we keep writes SHORT (chunked replaces,
// batched results) and release the lock between chunks; readers then interleave.
// All writes are serialized through writeMu (no SQLITE_BUSY between our own
// writers); busy_timeout is the backstop for a reader/writer overlap.
type configStore struct {
	db      *sql.DB
	writeMu sync.Mutex
	resMu   sync.Mutex     // guards resBuf
	resBuf  []resultUpdate // pending ping/speed results, flushed in batches
}

// resultUpdate is one pending test result. Batched (see queue*/flushResults) so a
// bulk test writes a few transactions instead of ~200k — essential in DELETE
// journal mode, where every transaction fsyncs.
type resultUpdate struct {
	tabID    string
	idx      int
	hasPing  bool
	delay    int64
	pStatus  string
	pErr     string
	hasSpeed bool
	mbps     float64
	sStatus  string
	sErr     string
	live     float64
}

var store *configStore

// configDBPath returns the on-disk DB path (data/configs.db). Uses tabsDir() so
// tests (TestMain in testsetup_test.go redirects LOCALAPPDATA/APPDATA to temp)
// never touch the real DB — the isolation that guards data after past incidents.
func configDBPath() string { return dataPath("configs.db") }

// Schema notes: we store the node's dedup key as a compact 64-bit body_hash, NOT
// the full body text (which nearly duplicated raw). The dedup-"hide" view filter
// compares hashes. Indexing an 8-byte integer instead of a ~200-byte string cuts
// the largest index — and the column — by ~20×.
const configSchema = `
CREATE TABLE IF NOT EXISTS configs (
	id          INTEGER PRIMARY KEY,
	tab_id      TEXT    NOT NULL,
	idx         INTEGER NOT NULL,
	raw         TEXT    NOT NULL,
	body_hash   INTEGER NOT NULL DEFAULT 0,
	name        TEXT,
	host        TEXT,
	port        INTEGER,
	network     TEXT,
	security    TEXT,
	protocol    TEXT,
	ping        INTEGER NOT NULL DEFAULT -1,
	ping_status TEXT    NOT NULL DEFAULT 'pending',
	ping_err    TEXT,
	speed       REAL    NOT NULL DEFAULT 0,
	speed_status TEXT   NOT NULL DEFAULT 'pending',
	speed_err   TEXT,
	speed_live  REAL    NOT NULL DEFAULT 0
);
`

// configIndexes is applied AFTER the body_hash migration, since one index
// references body_hash (which an old DB gains only during migration).
//
// Only TWO indexes: idx (covers the default sort + the keyset fast path + the
// tab_id filter) and body_hash (dedup-hide + the tab_id filter). We deliberately
// do NOT index ping/speed: the window's ORDER BY wraps them in a CASE (live rows
// before dead, etc.) which SQLite can't satisfy from an index anyway — it always
// builds a temp b-tree. Those indexes were never used by any query, yet cost
// ~2× the insert/delete work and ~16 MB. The legacy DROPs below remove them from
// existing DBs.
const configIndexes = `
DROP INDEX IF EXISTS idx_configs_tab_ping;
DROP INDEX IF EXISTS idx_configs_tab_speed;
CREATE INDEX IF NOT EXISTS idx_configs_tab_idx      ON configs(tab_id, idx);
CREATE INDEX IF NOT EXISTS idx_configs_tab_bodyhash ON configs(tab_id, body_hash);
`

// hashBody is the dedup key: a 64-bit FNV-1a hash of the node body (raw minus the
// "#name" fragment). Stored as INTEGER. Collisions are astronomically unlikely and
// would at worst hide one non-duplicate row in dedup view.
func hashBody(raw string) int64 {
	h := fnv.New64a()
	h.Write([]byte(nodeBody(strings.TrimSpace(raw))))
	return int64(h.Sum64())
}

// openConfigStore opens (creating if needed) the config DB and applies the schema.
func openConfigStore() (*configStore, error) {
	if err := os.MkdirAll(tabsDir(), 0755); err != nil {
		return nil, err
	}
	// journal_mode(DELETE): rollback-journal mode → a SINGLE file on disk (no
	// persistent -wal/-shm; the -journal appears only briefly during a write and
	// is deleted on commit). Trade-off vs WAL: a writer briefly blocks readers, so
	// big writes are chunked (replaceTabConfigs) and test results are batched
	// (queue*/flushResults) to keep each write short. synchronous(FULL) is the safe
	// pairing for rollback mode (durable across power loss); affordable now that
	// writes are few + batched. auto_vacuum(incremental) hands freed pages back to
	// the OS via PRAGMA incremental_vacuum after each replace.
	dsn := "file:" + filepath.ToSlash(configDBPath()) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(DELETE)&_pragma=synchronous(FULL)" +
		"&_pragma=auto_vacuum(incremental)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Serialize init on one connection so the pragmas + VACUUM below all run on
	// the same conn; bump to several connections afterwards for concurrent reads.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(configSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	migrated := migrateBodyHash(db) // legacy `body TEXT` column → compact body_hash (one-time)
	if _, err := db.Exec(configIndexes); err != nil {
		db.Close()
		return nil, fmt.Errorf("indexes: %w", err)
	}
	// Opening with journal_mode(DELETE) above already removed any legacy -wal/-shm.
	// Now compact with VACUUM when there's space to reclaim: a DB created before
	// auto_vacuum (reports mode 0) gets converted to incremental, and a just-run
	// migration left the dropped body column + its index as free pages. VACUUM
	// rewrites the file compactly. Skipped on a steady-state DB (no migration, av
	// already incremental).
	var av int
	db.QueryRow(`PRAGMA auto_vacuum`).Scan(&av)
	if av == 0 {
		db.Exec(`PRAGMA auto_vacuum=INCREMENTAL`)
	}
	if migrated || av == 0 {
		if _, err := db.Exec(`VACUUM`); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ config store compact: %v\n", err)
		}
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	return &configStore{db: db}, nil
}

// columnExists reports whether a table has a given column. Reads the result-set
// column names from a zero-row SELECT — reliable across drivers (modernc's PRAGMA
// table_info scanning was flaky).
func columnExists(db *sql.DB, table, col string) bool {
	rows, err := db.Query(`SELECT * FROM "` + table + `" LIMIT 0`)
	if err != nil {
		return false
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return false
	}
	for _, c := range cols {
		if c == col {
			return true
		}
	}
	return false
}

// migrateBodyHash upgrades a legacy DB (a `body TEXT` column + its big index) to
// the compact `body_hash` integer. Additive and crash-safe: rows are never
// deleted; it adds the column, fills it from raw, swaps the index, then drops the
// old column (best-effort — if DROP COLUMN isn't supported the column just
// lingers, harmless). A fresh DB already has body_hash → no-op.
func migrateBodyHash(db *sql.DB) (migrated bool) {
	// Migration is complete only once the legacy `body` column is gone. A fresh DB
	// never has it; a finished migration has dropped it.
	if !columnExists(db, "configs", "body") {
		return false
	}
	fmt.Fprintf(os.Stderr, "config store: migrating body→body_hash (one-time)…\n")
	if !columnExists(db, "configs", "body_hash") {
		if _, err := db.Exec(`ALTER TABLE configs ADD COLUMN body_hash INTEGER NOT NULL DEFAULT 0`); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ add body_hash: %v\n", err)
			return false
		}
	}
	// Fill body_hash from raw for still-unset rows, walking by id in chunks (O(n);
	// each chunk its own transaction, WAL truncated between so the migration
	// doesn't bloat it). The body_hash=0 filter makes a resumed/repeat run cheap.
	var lastID int64
	for {
		rows, err := db.Query(`SELECT id, raw FROM configs WHERE body_hash=0 AND id > ? ORDER BY id LIMIT 5000`, lastID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ migrate scan: %v\n", err)
			return
		}
		type kv struct {
			id  int64
			raw string
		}
		var batch []kv
		for rows.Next() {
			var r kv
			if rows.Scan(&r.id, &r.raw) == nil {
				batch = append(batch, r)
			}
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		tx, err := db.Begin()
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ migrate tx: %v\n", err)
			return
		}
		st, _ := tx.Prepare(`UPDATE configs SET body_hash=? WHERE id=?`)
		for _, r := range batch {
			h := hashBody(r.raw)
			if h == 0 {
				h = 1 // keep 0 reserved as "unset" so a resumed run is correct
			}
			st.Exec(h, r.id)
			lastID = r.id
		}
		st.Close()
		tx.Commit()
	}
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_configs_tab_bodyhash ON configs(tab_id, body_hash)`)
	db.Exec(`DROP INDEX IF EXISTS idx_configs_tab_body`)
	if _, err := db.Exec(`ALTER TABLE configs DROP COLUMN body`); err != nil {
		// Old SQLite without DROP COLUMN: rebuild the table without body so the
		// migration still completes (and won't re-run every startup).
		fmt.Fprintf(os.Stderr, "config store: DROP COLUMN unsupported (%v), rebuilding table…\n", err)
		rebuildWithoutBody(db)
	}
	return true
}

// rebuildWithoutBody recreates `configs` without the legacy body column (fallback
// when ALTER TABLE DROP COLUMN isn't available). Runs in one transaction.
func rebuildWithoutBody(db *sql.DB) {
	tx, err := db.Begin()
	if err != nil {
		return
	}
	stmts := []string{
		`CREATE TABLE configs_new (
			id INTEGER PRIMARY KEY, tab_id TEXT NOT NULL, idx INTEGER NOT NULL, raw TEXT NOT NULL,
			body_hash INTEGER NOT NULL DEFAULT 0, name TEXT, host TEXT, port INTEGER, network TEXT,
			security TEXT, protocol TEXT, ping INTEGER NOT NULL DEFAULT -1, ping_status TEXT NOT NULL DEFAULT 'pending',
			ping_err TEXT, speed REAL NOT NULL DEFAULT 0, speed_status TEXT NOT NULL DEFAULT 'pending',
			speed_err TEXT, speed_live REAL NOT NULL DEFAULT 0)`,
		`INSERT INTO configs_new SELECT id, tab_id, idx, raw, body_hash, name, host, port, network, security,
			protocol, ping, ping_status, ping_err, speed, speed_status, speed_err, speed_live FROM configs`,
		`DROP TABLE configs`,
		`ALTER TABLE configs_new RENAME TO configs`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			tx.Rollback()
			fmt.Fprintf(os.Stderr, "⚠ rebuild: %v\n", err)
			return
		}
	}
	tx.Commit()
}

// storeReplace writes a tab's configs to the store; no-op when the store isn't
// open (tests, or a failed open). Errors are logged, not fatal — during the
// SQLite migration the in-memory path is still authoritative.
func storeReplace(tabID string, entries []*ConfigEntry) {
	// Update the in-memory working copy (the read source) AND persist to
	// SQLite. Every writer already calls storeReplace, so this single hook keeps
	// memory + disk in sync without touching each writer.
	state.mu.Lock()
	state.tabEntries[tabID] = entries
	if state.activeTab == tabID {
		state.entries = entries
	}
	state.mu.Unlock()
	memInvalidate(tabID) // drop the cached sorted order for this tab
	if store == nil {
		return
	}
	if err := store.replaceTabConfigs(tabID, entries); err != nil {
		vlog("warning", "config store replace %s: %v", tabID, err)
	}
}

// ── write ───────────────────────────────────────────────────────────────────

// replaceTabConfigs replaces every row of a tab with the given entries. Used by
// the fetch paths instead of `state.tabEntries[id] = entries`.
func (s *configStore) replaceTabConfigs(tabID string, entries []*ConfigEntry) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// Pin ONE connection so the temporary synchronous=OFF below applies to this
	// bulk load (and only it). OFF skips the per-commit fsync — the dominant cost
	// on a 200k-row refresh. Safe here: configs are re-fetchable, so a power loss
	// mid-load just means reloading the source. Restored on the way out.
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.ExecContext(ctx, `PRAGMA synchronous=OFF`)
	defer conn.ExecContext(ctx, `PRAGMA synchronous=FULL`)

	// Delete the whole tab in one statement (index-driven, fast — the chunked
	// subquery delete was O(n²)-ish and dominated big refreshes).
	if _, err := conn.ExecContext(ctx, `DELETE FROM configs WHERE tab_id = ?`, tabID); err != nil {
		return err
	}
	// Insert all rows in one transaction with a single reused prepared statement.
	// Per-row Exec is actually faster than multi-row VALUES in the pure-Go driver
	// (same number of param binds, no extra slice building); the wins here are one
	// transaction + one prepared statement + sync off.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO configs
		(tab_id, idx, raw, body_hash, name, host, port, network, security, protocol,
		 ping, ping_status, ping_err, speed, speed_status, speed_err, speed_live)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, tabID, e.Index, e.Raw, hashBody(e.Raw),
			e.Name, e.Host, e.Port, e.Network, e.Security, e.Protocol,
			e.Delay, string(e.PingStatus), e.PingErr,
			e.SpeedMBps, string(e.SpeedStatus), e.SpeedErr, e.SpeedLive); err != nil {
			stmt.Close()
			tx.Rollback()
			return err
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		return err
	}
	conn.ExecContext(ctx, `PRAGMA incremental_vacuum`) // hand freed pages back to the OS
	return nil
}

// deleteTabRows removes every config of a tab (e.g. tab closed / sources cleared).
func (s *configStore) deleteTabRows(tabID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`DELETE FROM configs WHERE tab_id = ?`, tabID)
	return err
}

// sweepOrphanTabRows deletes rows whose tab no longer exists — e.g. a tab delete
// whose background row-cleanup didn't finish before the app exited. Called once
// at startup with the surviving tab IDs. A no-op when validIDs is empty (never
// wipe everything as a precaution).
func (s *configStore) sweepOrphanTabRows(validIDs []string) error {
	if len(validIDs) == 0 {
		return nil
	}
	ph := make([]string, len(validIDs))
	args := make([]interface{}, len(validIDs))
	for i, id := range validIDs {
		ph[i] = "?"
		args[i] = id
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`DELETE FROM configs WHERE tab_id NOT IN (`+strings.Join(ph, ",")+`)`, args...)
	return err
}

// deleteEntriesByIdx removes just the given rows of a tab (by idx) in chunks,
// without rewriting the rows that stay. Backs "delete selected" — far faster than
// replacing the whole tab when only a few rows of a large tab are removed. The
// surviving rows keep their idx (gaps are fine: the in-memory store and every
// index consumer look entries up by idx, not by array position).
func (s *configStore) deleteEntriesByIdx(tabID string, idxs []int) error {
	if len(idxs) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	const chunk = 4000
	for off := 0; off < len(idxs); off += chunk {
		end := off + chunk
		if end > len(idxs) {
			end = len(idxs)
		}
		part := idxs[off:end]
		ph := make([]string, len(part))
		args := make([]interface{}, 0, len(part)+1)
		args = append(args, tabID)
		for i, ix := range part {
			ph[i] = "?"
			args = append(args, ix)
		}
		if _, err := s.db.Exec(`DELETE FROM configs WHERE tab_id=? AND idx IN (`+strings.Join(ph, ",")+`)`, args...); err != nil {
			return err
		}
	}
	return nil
}

// deleteAll wipes every config (used by import, which rebuilds all tabs).
func (s *configStore) deleteAll() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`DELETE FROM configs`)
	return err
}

// queuePing / queueSpeed buffer a test result for the next batched flush instead
// of writing it immediately (a bulk test produces ~200k of these — one
// transaction each would be far too many fsyncs in DELETE journal mode).
func (s *configStore) queuePing(tabID string, idx int, delay int64, status, errMsg string) {
	if s == nil {
		return
	}
	s.resMu.Lock()
	s.resBuf = append(s.resBuf, resultUpdate{tabID: tabID, idx: idx, hasPing: true, delay: delay, pStatus: status, pErr: errMsg})
	s.resMu.Unlock()
}
func (s *configStore) queueSpeed(tabID string, idx int, delay int64, pStatus, pErr string, mbps float64, sStatus, sErr string, live float64) {
	if s == nil {
		return
	}
	s.resMu.Lock()
	s.resBuf = append(s.resBuf, resultUpdate{tabID: tabID, idx: idx,
		hasPing: true, delay: delay, pStatus: pStatus, pErr: pErr,
		hasSpeed: true, mbps: mbps, sStatus: sStatus, sErr: sErr, live: live})
	s.resMu.Unlock()
}

// flushResults writes all buffered results in one transaction. Safe to call with
// nothing pending (no-op) and with a nil store.
func (s *configStore) flushResults() {
	if s == nil {
		return
	}
	s.resMu.Lock()
	buf := s.resBuf
	s.resBuf = nil
	s.resMu.Unlock()
	if len(buf) == 0 {
		return
	}
	// The in-memory entries were already mutated by the test; drop the cached sort
	// order so a ping/speed-sorted view re-orders with the fresh results.
	memInvalidate("")
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	pingStmt, err := tx.Prepare(`UPDATE configs SET ping=?, ping_status=?, ping_err=? WHERE tab_id=? AND idx=?`)
	if err != nil {
		tx.Rollback()
		return
	}
	speedStmt, err := tx.Prepare(`UPDATE configs SET speed=?, speed_status=?, speed_err=?, speed_live=? WHERE tab_id=? AND idx=?`)
	if err != nil {
		pingStmt.Close()
		tx.Rollback()
		return
	}
	for _, u := range buf {
		if u.hasPing {
			pingStmt.Exec(u.delay, u.pStatus, u.pErr, u.tabID, u.idx)
		}
		if u.hasSpeed {
			speedStmt.Exec(u.mbps, u.sStatus, u.sErr, u.live, u.tabID, u.idx)
		}
	}
	pingStmt.Close()
	speedStmt.Close()
	tx.Commit()
}

// resultFlusher periodically flushes buffered test results. Started once in
// production (not in tests). The ~400ms cadence bounds how stale a switched-to
// tab's results can be during a running test.
func (s *configStore) resultFlusher() {
	for range time.Tick(400 * time.Millisecond) {
		s.flushResults()
	}
}

// ── read ────────────────────────────────────────────────────────────────────

// windowQuery describes a window request: sort + filter + dedup + favorites +
// the [offset, offset+limit) slice. Mirrors what the client-side rebuildTable +
// matches() + sort comparator used to do, now done in SQL.
type windowQuery struct {
	sort      string   // "idx" (default) | "ping" | "speed"
	filter    string   // free-text search (name/host/network/security/protocol)
	proto     []string // type-pill filter (chip protocols: vless, ss2022, …); empty = all
	dedupHide bool     // hide body-duplicates (keep first by idx)
	favorites []string // raw URLs that float to the top
	offset    int
	limit     int
}

func (s *configStore) tabCount(tabID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM configs WHERE tab_id=?`, tabID).Scan(&n)
	return n, err
}

// windowCols is the column list every window/entry read scans, in order.
const windowCols = `idx, raw, name, host, port, network, security, protocol,
	ping, ping_status, ping_err, speed, speed_status, speed_err, speed_live`

// scanWindowRows reads rows selected with windowCols into ConfigEntry values.
func scanWindowRows(r *sql.Rows) ([]ConfigEntry, error) {
	defer r.Close()
	var rows []ConfigEntry
	for r.Next() {
		var e ConfigEntry
		var ps, ss string
		if err := r.Scan(&e.Index, &e.Raw, &e.Name, &e.Host, &e.Port, &e.Network, &e.Security, &e.Protocol,
			&e.Delay, &ps, &e.PingErr, &e.SpeedMBps, &ss, &e.SpeedErr, &e.SpeedLive); err != nil {
			return nil, err
		}
		e.PingStatus, e.SpeedStatus = Status(ps), Status(ss)
		rows = append(rows, e)
	}
	return rows, r.Err()
}

// tabRawSet returns the set of raw URLs currently stored for a tab. Used to
// compute the reload delta (added/removed counts) before a replace.
func (s *configStore) tabRawSet(tabID string) (map[string]struct{}, error) {
	r, err := s.db.Query(`SELECT raw FROM configs WHERE tab_id=?`, tabID)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	set := make(map[string]struct{})
	for r.Next() {
		var raw string
		if err := r.Scan(&raw); err != nil {
			return nil, err
		}
		set[raw] = struct{}{}
	}
	return set, r.Err()
}

// allEntriesOrdered loads every entry of a tab in idx order as fresh
// *ConfigEntry. Used by "test all" and other whole-tab consumers.
func (s *configStore) allEntriesOrdered(tabID string) ([]*ConfigEntry, error) {
	r, err := s.db.Query(`SELECT `+windowCols+` FROM configs WHERE tab_id=? ORDER BY idx ASC`, tabID)
	if err != nil {
		return nil, err
	}
	vals, err := scanWindowRows(r)
	if err != nil {
		return nil, err
	}
	out := make([]*ConfigEntry, len(vals))
	for i := range vals {
		e := vals[i]
		out[i] = &e
	}
	return out, nil
}

// resetTabResults clears every ping/speed result of a tab back to pending (used
// by the sourceless-tab auto-refresh, which "reloads" by resetting in place).
func (s *configStore) resetTabResults(tabID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE configs SET ping=-1, ping_status='pending', ping_err='',
		speed=0, speed_status='pending', speed_err='', speed_live=0 WHERE tab_id=?`, tabID)
	return err
}

// updateName rewrites a config's display name (used by rename).
func (s *configStore) updateName(tabID string, idx int, name, raw string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE configs SET name=?, raw=?, body=? WHERE tab_id=? AND idx=?`,
		name, raw, nodeBody(strings.TrimSpace(raw)), tabID, idx)
	return err
}
