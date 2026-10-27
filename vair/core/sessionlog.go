package core

import (
	"context"
	"sync"
	"time"
)

// What was connected, for how long, and how it ended.
//
// The question this answers is the one nobody could answer the next morning:
// "it was working, why did it move me off it?" Everything that knew was
// transient — the connections panel lives until the program closes, the log
// panel is a ring buffer, and logging to a file is off by default and would
// record every visited host to answer a question about six lines.
//
// So the six lines are kept instead. One row per session, a hundred of them,
// nothing about traffic contents: when it started, what it was, in which mode,
// how long it held, and the reason the program itself gave for the end.
//
// It is on this computer and clearable from the panel that shows it — a record
// of when you were connected and to what is worth being able to erase.

// sessionsKept is how many sessions the log holds. A hundred is weeks of
// ordinary use and a bad evening of a flapping server, and it is bounded either
// way.
const sessionsKept = 100

// The table the log lives in. Six small columns and no index: a hundred rows
// are read whole, once, at the first look.
const sessionsSchema = `
CREATE TABLE IF NOT EXISTS sessions (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	started_at INTEGER NOT NULL,
	seconds    INTEGER NOT NULL,
	name       TEXT    NOT NULL,
	mode       TEXT    NOT NULL,
	reason     TEXT    NOT NULL,
	up         INTEGER NOT NULL DEFAULT 0,
	down       INTEGER NOT NULL DEFAULT 0
);
`

// SessionRow is one connection, from the moment it came up to the moment it
// ended.
type SessionRow struct {
	StartedAt int64  `json:"started_at"` // unix seconds
	Seconds   int    `json:"seconds"`    // how long it held
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	// Reason is what ended it, in the program's own words ("disconnected",
	// "the server stopped responding", "auto: switched to a faster server").
	Reason string `json:"reason"`
	Up     int64  `json:"up"`
	Down   int64  `json:"down"`
}

// sessionEnd carries the reason for the teardown that is about to happen.
//
// The teardown itself is one funnel (stopConnectionLocked) and knows only that
// it is tearing down; the caller is the one that knows why. Every caller that
// sets this does so while holding actionMu, which the teardown also holds, so
// the value cannot belong to another teardown by the time it is read.
var sessionEnd = struct {
	sync.Mutex
	why string
}{}

// noteSessionEndReason tells the next teardown what to write down. Call it while
// holding actionMu, immediately before tearing the connection down.
func noteSessionEndReason(why string) {
	sessionEnd.Lock()
	sessionEnd.why = why
	sessionEnd.Unlock()
}

// takeSessionEndReason reads and clears it. An empty one means nobody said, and
// a plain disconnect is the honest default.
func takeSessionEndReason() string {
	sessionEnd.Lock()
	why := sessionEnd.why
	sessionEnd.why = ""
	sessionEnd.Unlock()
	if why == "" {
		return "disconnected"
	}
	return why
}

// noteSessionEnded records a finished session. Called from the teardown with the
// state it is about to clear, so nothing has to be remembered while a connection
// runs.
func noteSessionEnded(cs ConnState, counter *trafficCounter) {
	// A start time is what says this was a session: it is set when the state
	// becomes connected and nowhere else, so a connect that failed on the way up
	// has none and leaves no row. The status is NOT the test — a disconnect by
	// hand marks the state "disconnecting" before it tears anything down, and
	// requiring "connected" here dropped exactly the sessions the user ended
	// themselves. The teardown funnel refuses an idle state before this is
	// reached, so one session cannot be recorded twice.
	if cs.StartedAt.IsZero() {
		return
	}
	row := SessionRow{
		StartedAt: cs.StartedAt.Unix(),
		Seconds:   int(time.Since(cs.StartedAt).Seconds()),
		Name:      cs.EntryName,
		Mode:      string(cs.Mode),
		Reason:    takeSessionEndReason(),
	}
	if counter != nil {
		row.Up, row.Down = counter.Up.Load(), counter.Down.Load()
	}
	sessions.Lock()
	sessions.rows = append(sessions.rows, row)
	if len(sessions.rows) > sessionsKept {
		sessions.rows = append([]SessionRow(nil), sessions.rows[len(sessions.rows)-sessionsKept:]...)
	}
	sessions.Unlock()
	if store != nil {
		store.appendSession(row)
	}
}

// ── the store side ─────────────────────────────────────────────────────────

// appendSession writes one row and drops whatever is over the cap.
func (s *configStore) appendSession(r SessionRow) {
	if s == nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`INSERT INTO sessions (started_at, seconds, name, mode, reason, up, down) VALUES (?,?,?,?,?,?,?)`,
		r.StartedAt, r.Seconds, r.Name, r.Mode, r.Reason, r.Up, r.Down); err != nil {
		return
	}
	// Trim by id, which is insertion order: the log is a tail, not an archive.
	s.db.Exec(`DELETE FROM sessions WHERE id <= (SELECT MAX(id) - ? FROM sessions)`, sessionsKept) //nolint:errcheck
}

// loadSessions reads the log back, oldest first.
func (s *configStore) loadSessions() []SessionRow {
	if s == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT started_at, seconds, name, mode, reason, up, down FROM sessions ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(&r.StartedAt, &r.Seconds, &r.Name, &r.Mode, &r.Reason, &r.Up, &r.Down); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// clearSessions empties the table — and the file: with secure_delete on, the
// pages the rows were on are overwritten as they are freed. Otherwise a cleared
// record of when you were connected, and to what, stayed readable in the
// database file until something happened to reuse the space.
func (s *configStore) clearSessions() {
	if s == nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.ExecContext(ctx, `PRAGMA secure_delete = ON`)   //nolint:errcheck
	conn.ExecContext(ctx, `DELETE FROM sessions`)        //nolint:errcheck
	conn.ExecContext(ctx, `PRAGMA secure_delete = FAST`) //nolint:errcheck
}

// sessions is the log in memory, oldest first. Loaded from the store at
// startup, so the answer survives a restart — which is most of the point.
var sessions = struct {
	sync.Mutex
	rows   []SessionRow
	loaded bool
}{}

// SessionLog returns the log, newest first.
func SessionLog() []SessionRow {
	sessions.Lock()
	defer sessions.Unlock()
	if !sessions.loaded && store != nil {
		sessions.rows = store.loadSessions()
		sessions.loaded = true
	}
	out := make([]SessionRow, 0, len(sessions.rows))
	for i := len(sessions.rows) - 1; i >= 0; i-- {
		out = append(out, sessions.rows[i])
	}
	return out
}

// ClearSessionLog forgets every recorded session.
func ClearSessionLog() {
	sessions.Lock()
	sessions.rows = nil
	sessions.loaded = true
	sessions.Unlock()
	if store != nil {
		store.clearSessions()
	}
}
