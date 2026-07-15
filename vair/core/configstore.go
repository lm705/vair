package core

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
	// journal_mode(DELETE) — the 1.10 choice (deliberately NOT WAL: a single file
	// on disk, no persistent -wal/-shm sidecars). With synchronous(FULL) this is
	// crash-SAFE: a kill mid-write leaves the rollback -journal, which SQLite
	// replays on the next open to roll the incomplete transaction back — no
	// corruption. The corruption we saw came NOT from DELETE mode but from
	// replaceTabConfigs downgrading to `synchronous=OFF` during bulk loads (a
	// hard kill then left the main file half-written with no journal to recover);
	// that downgrade is removed.
	//
	// auto_vacuum=NONE (was incremental): the incremental pointer-map is the
	// structure that was corrupting ("Bad ptr map entry ...") when a huge write
	// or the follow-up `PRAGMA incremental_vacuum` got killed mid-flight — it
	// rewrites ptr-map pages on every size change, a wide interrupt window on a
	// 260k-row paste. Without it there's no ptr map to corrupt; freed pages are
	// reused by later inserts instead of returned to the OS (fine for a config
	// store — disk is cheap, and a shrink is rare). No per-write vacuum anymore.
	dsn := "file:" + filepath.ToSlash(configDBPath()) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(DELETE)&_pragma=synchronous(FULL)" +
		"&_pragma=auto_vacuum(none)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Crash-recovery: if the on-disk DB is corrupt ("database disk image is
	// malformed" — e.g. the process was killed mid-write), every read fails and
	// the app shows 0 configs forever. Detect it up front, quarantine the bad
	// file, and start fresh so the app is usable again (SOURCES re-fetches;
	// pasted tabs are lost but the corrupt file is kept as .corrupt-<ts> for
	// manual recovery).
	if dbLooksCorrupt(db) {
		db.Close()
		bad := configDBPath()
		// A malformed image is unrecoverable (quick_check failed hard), so don't
		// keep 185 MB corpses accumulating — drop the file and its sidecars and
		// start clean. SOURCES re-fetches; pasted tabs are lost.
		for _, sfx := range []string{"", "-journal", "-wal", "-shm"} {
			os.Remove(bad + sfx)
		}
		// Sweep any leftover *.corrupt-* from earlier crashes too.
		if matches, _ := filepath.Glob(bad + ".corrupt-*"); matches != nil {
			for _, m := range matches {
				os.Remove(m)
			}
		}
		fmt.Fprintf(os.Stderr, "⚠ config DB was corrupt — recreated fresh (old data dropped)\n")
		if db, err = sql.Open("sqlite", dsn); err != nil {
			return nil, err
		}
	}
	// Serialize init on one connection so the pragmas below run on the same conn;
	// bump to several connections afterwards for concurrent reads.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(configSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	migrateBodyHash(db) // legacy `body TEXT` column → compact body_hash (one-time)
	if _, err := db.Exec(configIndexes); err != nil {
		db.Close()
		return nil, fmt.Errorf("indexes: %w", err)
	}
	// No VACUUM / auto_vacuum conversion here anymore: converting auto_vacuum
	// requires a full-file VACUUM (rewrites everything — minutes on a big DB and
	// a wide corruption window if killed). We keep whatever mode the file already
	// has; fresh files are created with auto_vacuum=NONE (see DSN). A legacy
	// incremental DB just keeps its ptr map — we simply never actively vacuum it.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	return &configStore{db: db}, nil
}

// dbLooksCorrupt does a CHEAP probe (no full-DB scan — quick_check on a large
// file added seconds to startup). Reads the schema header + one row; a
// malformed image fails these with "malformed"/"not a database". This catches
// the header/root-corruption case immediately; any deeper page corruption
// surfaces later as a read error, which the callers already handle by falling
// back to an empty tab (never a crash). A brand-new/empty file passes.
func dbLooksCorrupt(db *sql.DB) bool {
	isCorrupt := func(err error) bool {
		if err == nil {
			return false
		}
		e := strings.ToLower(err.Error())
		return strings.Contains(e, "malformed") || strings.Contains(e, "not a database") ||
			strings.Contains(e, "disk image")
	}
	var v int
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&v); isCorrupt(err) {
		return true
	}
	// Touch the configs btree root (fast: LIMIT 1). Ignore "no such table" (a
	// fresh DB before the schema exec) — only a corruption error counts.
	var x int
	err := db.QueryRow(`SELECT 1 FROM configs LIMIT 1`).Scan(&x)
	if err != nil && err != sql.ErrNoRows && !strings.Contains(strings.ToLower(err.Error()), "no such table") {
		return isCorrupt(err)
	}
	return false
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
// memReplace updates the in-memory working copy (the READ source) only — the
// UI can be served from it immediately, before the slower SQLite write.
func memReplace(tabID string, entries []*ConfigEntry) {
	state.mu.Lock()
	state.tabEntries[tabID] = entries
	if state.activeTab == tabID {
		state.entries = entries
	}
	state.mu.Unlock()
	memInvalidate(tabID) // drop the cached sorted order for this tab
}

// dbPersist writes the tab to SQLite (the durable copy).
func dbPersist(tabID string, entries []*ConfigEntry) {
	if store == nil {
		return
	}
	if err := store.replaceTabConfigs(tabID, entries); err != nil {
		vlog("warning", "config store replace %s: %v", tabID, err)
	}
}

// storeReplace keeps memory + disk in sync in one call (memory first).
func storeReplace(tabID string, entries []*ConfigEntry) {
	memReplace(tabID, entries)
	dbPersist(tabID, entries)
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
	// NOTE: no `PRAGMA synchronous=OFF` here. OFF skipped the WAL fsync and made
	// a kill mid-bulk-load corrupt the DB. Under WAL + synchronous=NORMAL the
	// whole load is one transaction that only fsyncs the WAL at commit, which is
	// already fast for a 200k-row refresh and crash-safe.

	// Delete the whole tab in one statement (index-driven, fast — the chunked
	// subquery delete was O(n²)-ish and dominated big refreshes).
	if _, err := conn.ExecContext(ctx, `DELETE FROM configs WHERE tab_id = ?`, tabID); err != nil {
		return err
	}
	// Insert in BATCHED multi-row statements (one Exec of `VALUES (…),(…),…` per
	// 500 rows is dramatically faster than 500 single Execs in the pure-Go
	// driver). And COMMIT every ~30k rows: a huge single transaction leaves a
	// 100MB+ rollback journal, so a kill mid-write risks a slow/partial rollback;
	// small periodic commits keep the -journal tiny and delete it after each
	// commit, so a kill loses at most the last uncommitted batch (partial tab —
	// re-pasteable) instead of corrupting the file. 17 cols × 500 = 8500 binds,
	// under SQLite's 32766 cap.
	const nCols = 17
	const batch = 900 // 17×900 = 15300 binds, under SQLite's 32766 cap; fewer Execs
	const commitEvery = 30000
	rowPH := "(?" + strings.Repeat(",?", nCols-1) + ")"
	const insertPrefix = `INSERT INTO configs
		(tab_id, idx, raw, body_hash, name, host, port, network, security, protocol,
		 ping, ping_status, ping_err, speed, speed_status, speed_err, speed_live) VALUES `

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	sinceCommit := 0
	flush := func(chunk []*ConfigEntry) error {
		if len(chunk) == 0 {
			return nil
		}
		phs := make([]string, len(chunk))
		args := make([]interface{}, 0, len(chunk)*nCols)
		for i, e := range chunk {
			phs[i] = rowPH
			args = append(args, tabID, e.Index, e.Raw, hashBody(e.Raw),
				e.Name, e.Host, e.Port, e.Network, e.Security, e.Protocol,
				e.Delay, string(e.PingStatus), e.PingErr,
				e.SpeedMBps, string(e.SpeedStatus), e.SpeedErr, e.SpeedLive)
		}
		_, err := tx.ExecContext(ctx, insertPrefix+strings.Join(phs, ","), args...)
		return err
	}
	for i := 0; i < len(entries); i += batch {
		end := i + batch
		if end > len(entries) {
			end = len(entries)
		}
		if err := flush(entries[i:end]); err != nil {
			tx.Rollback()
			return err
		}
		sinceCommit += end - i
		if sinceCommit >= commitEvery {
			if err := tx.Commit(); err != nil {
				return err
			}
			if tx, err = conn.BeginTx(ctx, nil); err != nil {
				return err
			}
			sinceCommit = 0
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// No incremental_vacuum: with auto_vacuum=NONE there's no ptr map to walk,
	// and it was a corruption vector on interrupt. Freed pages are reused by the
	// next insert instead of returned to the OS.
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
// distinctTabIDs returns every tab_id that currently has at least one config
// row (used to recover tabs whose metadata was lost from tabs.json).
func (s *configStore) distinctTabIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT tab_id FROM configs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil && id != "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

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
	exclude   []string // per-tab exclude filter — a VIEW filter (hides matching configs
	                    // on read; the store keeps them, so toggling it is instant both ways)
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
