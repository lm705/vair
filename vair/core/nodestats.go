package core

import (
	"database/sql"
	"math"
	"sync"
	"time"
)

// Per-server measurement history.
//
// A single ping is a coin toss. The node that answered in 80 ms once and times
// out every third attempt ranks above the one that answers in 100 ms every time,
// because the ranking only ever saw the last number. For anything interactive —
// a call, a page load — the second node is the better one, and no amount of
// re-testing tells you so if only the latest result is kept.
//
// What is kept is deliberately NOT a log of samples. A list of the last N
// measurements for a quarter of a million servers is millions of rows to write,
// index and prune. An exponentially weighted mean and variance carry the same
// two facts — how fast, how consistent — in one fixed-size row that updates in
// place and forgets old data by itself.
//
// The key is body_hash, the canonical identity of the server (see dedupKey), not
// the row. A subscription refresh rewrites every row and renumbers it; keying
// history to rows would throw it away exactly when it starts to be worth having.
// Keyed by server, the history survives refreshes, follows a config that appears
// in two tabs, and accumulates across the reloads that would otherwise reset it.

// statsAlpha is the weight of the newest measurement.
//
// 0.2 gives an effective memory of about five measurements: long enough for a
// variance estimate to mean something, short enough that a server which was bad
// yesterday and is good today is forgiven within a few sweeps. Higher would
// chase noise; lower would keep punishing a node that has recovered.
const statsAlpha = 0.2

// statsMaxAge is how long an unseen server's history is kept. A list churns —
// most of what was measured last month no longer exists — and the table should
// not grow forever because of it.
const statsMaxAge = 30 * 24 * time.Hour

// nodeStat is what is remembered about one server.
type nodeStat struct {
	// PingAvg is the exponentially weighted mean of successful delays, ms.
	PingAvg float64
	// PingVar is the exponentially weighted variance of the same. Jitter is its
	// square root — kept squared because that is the form the update is in.
	PingVar float64
	// OK is the exponentially weighted fraction of attempts that succeeded, 0..1.
	OK float64
	// Samples counts everything ever recorded, successes and failures alike. It
	// exists to answer "is this worth trusting yet": one measurement is not a
	// history, and scoring must not treat it as one.
	Samples int64
	// Updated is when this was last touched, for pruning.
	Updated time.Time
}

// Jitter is the standard deviation of the delay, in milliseconds.
func (s nodeStat) Jitter() float64 { return math.Sqrt(math.Max(0, s.PingVar)) }

// statsMinSamples is how many measurements a server needs before its history is
// allowed to influence ranking.
//
// Below it the mean is the last measurement and the variance is zero, which
// would rank a single lucky probe above a node with a long, slightly worse
// record — the exact mistake this file exists to stop.
const statsMinSamples = 3

// Trusted reports whether there is enough history to rank on.
func (s nodeStat) Trusted() bool { return s.Samples >= statsMinSamples }

// nodeStats is the in-memory copy of the table.
//
// All of it, deliberately. Ranking touches every candidate in the pool, and a
// query per candidate would turn one decision into thousands of round trips.
// The whole table is a few tens of bytes per distinct server ever measured, and
// pruning on load keeps that bounded by what has actually been seen recently.
var nodeStats = struct {
	sync.RWMutex
	m     map[int64]nodeStat
	dirty map[int64]struct{}
}{m: map[int64]nodeStat{}, dirty: map[int64]struct{}{}}

// statsFor returns what is known about a server, and whether anything is.
func statsFor(hash int64) (nodeStat, bool) {
	if hash == 0 {
		return nodeStat{}, false
	}
	nodeStats.RLock()
	defer nodeStats.RUnlock()
	s, ok := nodeStats.m[hash]
	return s, ok
}

// recordPingStat folds one measurement into a server's history.
//
// A failure updates the success rate and nothing else: a timeout carries no
// information about how fast the server is when it works, and letting it push
// the mean around would make an unreliable node look slow rather than
// unreliable — two different problems with two different answers.
func recordPingStat(hash int64, delayMs int64, ok bool) {
	if hash == 0 {
		return
	}
	nodeStats.Lock()
	defer nodeStats.Unlock()

	s := nodeStats.m[hash]
	s.Samples++
	s.Updated = time.Now()

	// Both averages are SEEDED by their first measurement rather than blended in
	// from zero. Blending was the obvious way to write it and it was wrong: a
	// brand-new server that answered four times out of four came out at 0.59
	// reliability — because the weighting was still climbing out of the zero it
	// started at — and collected a two-hundred-millisecond penalty for a perfect
	// record. Found in the database, not in the code.
	if s.Samples == 1 {
		if ok && delayMs > 0 {
			s.PingAvg, s.PingVar, s.OK = float64(delayMs), 0, 1
		} else {
			s.PingAvg, s.PingVar, s.OK = 0, 0, 0
		}
		nodeStats.m[hash] = s
		nodeStats.dirty[hash] = struct{}{}
		return
	}

	if ok && delayMs > 0 {
		x := float64(delayMs)
		if s.PingAvg == 0 {
			// First success after a run of failures: seed rather than drag the
			// mean up from zero, which would read as "very fast" for a while.
			s.PingAvg, s.PingVar = x, 0
		} else {
			d := x - s.PingAvg
			s.PingAvg += statsAlpha * d
			s.PingVar = (1 - statsAlpha) * (s.PingVar + statsAlpha*d*d)
		}
		s.OK += statsAlpha * (1 - s.OK)
	} else {
		s.OK += statsAlpha * (0 - s.OK)
	}

	nodeStats.m[hash] = s
	nodeStats.dirty[hash] = struct{}{}
}

// unreliablePenaltyMs is what a server that never answers costs itself, in the
// same units as the delay it is being compared on.
//
// Half a second: decisive against anything measured in tens of milliseconds,
// but not so large that a node failing one probe in ten is written off. The
// penalty is proportional — a node that answers 90% of the time pays a tenth of
// it.
const unreliablePenaltyMs = 500

// jitterWeight is how many milliseconds of jitter count as one millisecond of
// delay. One: a node that varies by 50 ms is treated as 50 ms slower, which is
// roughly what it feels like to use.
const jitterWeight = 1.0

// stableScore is the delay a server should be judged on when ranking by
// stability: its typical delay, plus what its inconsistency and its failures
// are worth.
//
// Returns the raw delay unchanged when there is not enough history — a node
// nobody has measured properly must not be pushed up or down by a guess.
func stableScore(hash int64, rawDelay int64) float64 {
	s, ok := statsFor(hash)
	if !ok || !s.Trusted() {
		return float64(rawDelay)
	}
	return s.PingAvg + jitterWeight*s.Jitter() + (1-s.OK)*unreliablePenaltyMs
}

// ── persistence ────────────────────────────────────────────────────────────

const nodeStatsSchema = `
CREATE TABLE IF NOT EXISTS node_stats (
	body_hash  INTEGER PRIMARY KEY,
	ping_avg   REAL    NOT NULL DEFAULT 0,
	ping_var   REAL    NOT NULL DEFAULT 0,
	ok_rate    REAL    NOT NULL DEFAULT 0,
	samples    INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);
`

// loadNodeStats reads the table into memory and drops what has gone stale.
func loadNodeStats(db *sql.DB) {
	if db == nil {
		return
	}
	cutoff := time.Now().Add(-statsMaxAge).Unix()
	db.Exec(`DELETE FROM node_stats WHERE updated_at < ?`, cutoff) //nolint:errcheck

	rows, err := db.Query(`SELECT body_hash, ping_avg, ping_var, ok_rate, samples, updated_at FROM node_stats`)
	if err != nil {
		return
	}
	defer rows.Close()

	m := map[int64]nodeStat{}
	for rows.Next() {
		var h, samples, updated int64
		var avg, variance, okRate float64
		if err := rows.Scan(&h, &avg, &variance, &okRate, &samples, &updated); err != nil {
			continue
		}
		m[h] = nodeStat{PingAvg: avg, PingVar: variance, OK: okRate, Samples: samples, Updated: time.Unix(updated, 0)}
	}
	nodeStats.Lock()
	nodeStats.m = m
	nodeStats.Unlock()
	vlog("info", "measurement history: %d server(s)", len(m))
}

// flushNodeStats writes the servers touched since the last flush.
//
// Called from the same place the test results are flushed, so a sweep costs one
// transaction rather than one per measurement.
func flushNodeStats(db *sql.DB) {
	if db == nil {
		return
	}
	nodeStats.Lock()
	if len(nodeStats.dirty) == 0 {
		nodeStats.Unlock()
		return
	}
	type row struct {
		h int64
		s nodeStat
	}
	pending := make([]row, 0, len(nodeStats.dirty))
	for h := range nodeStats.dirty {
		pending = append(pending, row{h, nodeStats.m[h]})
	}
	nodeStats.dirty = map[int64]struct{}{}
	nodeStats.Unlock()

	tx, err := db.Begin()
	if err != nil {
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO node_stats (body_hash, ping_avg, ping_var, ok_rate, samples, updated_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(body_hash) DO UPDATE SET
			ping_avg=excluded.ping_avg, ping_var=excluded.ping_var,
			ok_rate=excluded.ok_rate, samples=excluded.samples, updated_at=excluded.updated_at`)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		return
	}
	for _, r := range pending {
		stmt.Exec(r.h, r.s.PingAvg, r.s.PingVar, r.s.OK, r.s.Samples, r.s.Updated.Unix()) //nolint:errcheck
	}
	stmt.Close()
	tx.Commit() //nolint:errcheck
}
