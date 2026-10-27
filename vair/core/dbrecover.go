package core

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A damaged configs.db.
//
// It used to be deleted on the spot, together with every copy kept from an
// earlier crash: a tab of the user's own configurations — which exists nowhere
// else, tabs.json holds only its settings — the measurement history and the
// connection log went with it, and the window said nothing. Now the damaged
// file is kept beside the new one (the newest copy only), whatever can still
// be read from it is carried into the new database, and the user is told what
// happened and where the copy is.

// dbSalvage is what could be read from a damaged database.
type dbSalvage struct {
	configs  []salvagedConfig
	sessions []SessionRow
}

type salvagedConfig struct {
	tab, raw, name, host, network, security, protocol string
	idx, port                                         int
}

// dbRecovery describes the last recovery, for the notice after start; nil when
// the database opened normally.
var dbRecovery *struct {
	copy     string
	salvaged int
}

// quarantineCorruptDB moves a damaged database aside — the file and its rollback
// journal, renamed together so SQLite still pairs them — and returns the copy's
// path. Older copies are removed: one damaged database is worth keeping, a
// collection of them is not.
func quarantineCorruptDB(path string) string {
	if old, _ := filepath.Glob(path + ".corrupt-*"); old != nil {
		for _, f := range old {
			os.Remove(f) //nolint:errcheck
		}
	}
	dst := path + ".corrupt-" + time.Now().Format("20060102-150405")
	if err := os.Rename(path, dst); err != nil {
		return ""
	}
	if _, err := os.Stat(path + "-journal"); err == nil {
		os.Rename(path+"-journal", dst+"-journal") //nolint:errcheck
	}
	for _, sfx := range []string{"-wal", "-shm"} {
		os.Remove(path + sfx) //nolint:errcheck
	}
	return dst
}

// salvageDB reads what it can from a damaged database, row by row, so a bad
// page costs the rows on it and not the rest. Opened read-only and immutable:
// nothing is written to the copy, and a journal next to it is not replayed.
// Read in table order, without ORDER BY: a damaged index must not cost rows
// the table itself still holds.
func salvageDB(path string) *dbSalvage {
	out := readSalvage(path)
	if out != nil && len(out.configs) > 0 {
		return out
	}
	// Nothing readable. The commonest damage is to the first hundred bytes —
	// the header SQLite needs before it reads anything — with every page after
	// it intact. A copy with a header put back is tried at the page sizes a file
	// of this length can have; the original copy is not touched.
	for _, ps := range []int{4096, 1024, 2048, 8192, 16384, 32768, 65536} {
		fixed, err := withRepairedHeader(path, ps)
		if err != nil {
			continue
		}
		got := readSalvage(fixed)
		os.Remove(fixed) //nolint:errcheck
		if got != nil && len(got.configs) > 0 {
			return got
		}
	}
	return out
}

// withRepairedHeader writes a copy of a database file with a valid SQLite
// header for the given page size. Only what reading needs is set: the page
// size, the formats, UTF-8, the page count; the free list is dropped, which
// costs nothing to a reader.
func withRepairedHeader(path string, pageSize int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) < 2*pageSize || len(data)%pageSize != 0 {
		return "", fmt.Errorf("not a file of %d-byte pages", pageSize)
	}
	// Page 1's own b-tree header follows the file header: a table b-tree page,
	// interior (5) or leaf (13). Anything else means more than the header is gone.
	if data[100] != 0x05 && data[100] != 0x0D {
		return "", fmt.Errorf("page 1 is damaged too")
	}
	h := data[:100]
	for i := range h {
		h[i] = 0
	}
	copy(h, "SQLite format 3\x00")
	ps := pageSize
	if ps == 65536 {
		ps = 1
	}
	h[16], h[17] = byte(ps>>8), byte(ps)
	h[18], h[19] = 1, 1              // rollback-journal formats
	h[21], h[22], h[23] = 64, 32, 32 // the payload fractions SQLite requires
	be32 := func(off int, v uint32) {
		h[off], h[off+1], h[off+2], h[off+3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
	}
	be32(24, 1)                          // change counter
	be32(28, uint32(len(data)/pageSize)) // pages, valid while 92 matches 24
	be32(40, 1)                          // schema cookie
	be32(44, 4)                          // schema format
	be32(56, 1)                          // UTF-8
	be32(92, 1)
	be32(96, 3045000)
	dst := path + ".repair"
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "", err
	}
	return dst, nil
}

// readSalvage reads the rows of one database file, as far as they read.
func readSalvage(path string) *dbSalvage {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&immutable=1")
	if err != nil {
		return nil
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	out := &dbSalvage{}
	if rows, err := db.Query(`SELECT tab_id, idx, raw, name, host, port, network, security, protocol FROM configs`); err == nil {
		for rows.Next() {
			var c salvagedConfig
			var name, host, network, security, protocol sql.NullString
			var port sql.NullInt64
			if rows.Scan(&c.tab, &c.idx, &c.raw, &name, &host, &port, &network, &security, &protocol) != nil {
				continue
			}
			c.name, c.host, c.network, c.security, c.protocol = name.String, host.String, network.String, security.String, protocol.String
			c.port = int(port.Int64)
			if strings.TrimSpace(c.raw) != "" && c.tab != "" {
				out.configs = append(out.configs, c)
			}
		}
		rows.Close()
	}
	if rows, err := db.Query(`SELECT started_at, seconds, name, mode, reason, up, down FROM sessions ORDER BY id`); err == nil {
		for rows.Next() {
			var r SessionRow
			if rows.Scan(&r.StartedAt, &r.Seconds, &r.Name, &r.Mode, &r.Reason, &r.Up, &r.Down) == nil {
				out.sessions = append(out.sessions, r)
			}
		}
		rows.Close()
	}
	return out
}

// restoreSalvage writes what was read back into the new database: the
// configurations (their test results start again) and the connection log. The
// measurement history is rebuilt by the next tests.
func restoreSalvage(db *sql.DB, s *dbSalvage) int {
	if s == nil {
		return 0
	}
	tx, err := db.Begin()
	if err != nil {
		return 0
	}
	n := 0
	// The error columns are written empty, as every other write leaves them: the
	// loader reads them as text, and a NULL there made it skip the whole tab.
	if st, err := tx.Prepare(`INSERT INTO configs (tab_id, idx, raw, body_hash, name, host, port, network, security, protocol, ping_err, speed_err)
		VALUES (?,?,?,?,?,?,?,?,?,?,'','')`); err == nil {
		for _, c := range s.configs {
			if _, err := st.Exec(c.tab, c.idx, c.raw, hashBody(c.raw), c.name, c.host, c.port, c.network, c.security, c.protocol); err == nil {
				n++
			}
		}
		st.Close()
	}
	if st, err := tx.Prepare(`INSERT INTO sessions (started_at, seconds, name, mode, reason, up, down) VALUES (?,?,?,?,?,?,?)`); err == nil {
		for _, r := range s.sessions {
			st.Exec(r.StartedAt, r.Seconds, r.Name, r.Mode, r.Reason, r.Up, r.Down) //nolint:errcheck
		}
		st.Close()
	}
	if err := tx.Commit(); err != nil {
		return 0
	}
	return n
}

// announceDBRecovery tells the user, once the program is up: in the log, and in
// a notification, since a window may not be open yet.
func announceDBRecovery() {
	r := dbRecovery
	if r == nil {
		return
	}
	dbRecovery = nil
	vlog("warning", "the configuration database was damaged: %d configuration(s) were recovered; the damaged file is kept as %s", r.salvaged, r.copy)
	settingsMu.RLock()
	lang := appSettings.Language
	settingsMu.RUnlock()
	name := filepath.Base(r.copy)
	if lang == "ru" {
		desktopNotify("Vair", fmt.Sprintf("База конфигураций была повреждена. Восстановлено конфигураций: %d. Повреждённый файл сохранён как %s в папке данных Vair", r.salvaged, name))
		return
	}
	desktopNotify("Vair", fmt.Sprintf("The configuration database was damaged. Configurations recovered: %d. The damaged file is kept as %s in Vair's data folder", r.salvaged, name))
}

// compactIfMostlyFree rebuilds the file when most of it is free space. With
// auto_vacuum off the file never shrank: the pages of a big list deleted long
// ago stayed in it, and with secure_delete=fast so did much of their text —
// configurations, keys and passwords the user had removed. VACUUM is one
// transaction under the rollback journal, so an interrupted one leaves the old
// file as it was.
func compactIfMostlyFree(db *sql.DB) {
	var pages, free, size int64
	if db.QueryRow(`PRAGMA page_count`).Scan(&pages) != nil || db.QueryRow(`PRAGMA freelist_count`).Scan(&free) != nil ||
		db.QueryRow(`PRAGMA page_size`).Scan(&size) != nil {
		return
	}
	if free*size < compactMinFree || free*2 < pages {
		return
	}
	start := time.Now()
	if _, err := db.Exec(`VACUUM`); err != nil {
		vlog("warning", "configuration database: compaction failed: %v", err)
		return
	}
	vlog("info", "configuration database compacted: %d MB of free space returned in %s", free*size>>20, time.Since(start).Round(time.Millisecond))
}

// compactMinFree is the free space worth a rebuild (a variable for the tests).
var compactMinFree int64 = 20 << 20
