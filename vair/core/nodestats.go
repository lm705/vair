package core

import (
	"database/sql"
	"math"
	"strconv"
	"strings"
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
	// Recent is the last few delays in order, newest last, for the list in the
	// history dialog. A failure is stored as -1. The averages above are what
	// ranking uses; this is only what a person sees, so a short window is enough.
	Recent []int16
	// RecentAt is when each of them was taken, in unix minutes, parallel to
	// Recent. Minutes rather than seconds because nothing here is answered to
	// the second, and because the text form stores differences: a bulk test
	// produces twenty samples inside one minute, which then costs one character
	// each.
	RecentAt []int32
	// SpeedAvg / SpeedVar are the same pair for the download speed, in Mbps, and
	// they are deliberately NOT symmetrical with the delay ones:
	//
	//   - More is better, so the variability is subtracted rather than added.
	//   - There is no success rate. A failed speed test is usually the test
	//     endpoint refusing (HTTP 429), not the server being bad, and charging
	//     the server for it would rank by which config happened to draw the
	//     rate limit. A failure records nothing at all here.
	//   - SpeedSeen counts only successful measurements, because that is what
	//     "enough history to trust" means when failures are not counted.
	//
	// A speed test costs seconds of real traffic where a ping costs
	// milliseconds, so this fills up far more slowly than the delay history.
	SpeedAvg  float64
	SpeedVar  float64
	SpeedSeen int64
	// SpeedRecent is the last few measurements, in hundredths of MB/s so they
	// fit the same int16 the delays use, with SpeedRecentAt their unix minutes.
	//
	// Kept for the eye, like the delay window, and kept SHORTER: a speed test
	// costs seconds of real traffic, so a server has a handful of these where it
	// has hundreds of pings — and the figure expires from the ranking after six
	// hours anyway, which is why each one carries its own time. A list of
	// speeds without times invites the question it cannot answer: why the
	// ranking says 0.6 when the list says 18.4.
	SpeedRecent   []int16
	SpeedRecentAt []int32
	// SpeedAt is when the last speed measurement landed. Speed ages badly — an
	// hour-old figure describes a network condition, not a server — so it is
	// kept separately from Updated and expires much sooner.
	SpeedAt time.Time
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

// speedMaxAge is how long a speed measurement is worth ranking on. Far shorter
// than statsMaxAge: a typical delay to a server barely moves over a month, and
// a download speed measured an hour ago describes the evening, the neighbours
// and whatever else was using the line.
const speedMaxAge = 6 * time.Hour

// SpeedTrusted reports whether there is enough RECENT speed history to rank on.
func (s nodeStat) SpeedTrusted() bool {
	return s.SpeedSeen >= statsMinSamples && !s.SpeedAt.IsZero() && time.Since(s.SpeedAt) < speedMaxAge
}

// SpeedSpread is the standard deviation of the measured speed, in Mbps.
func (s nodeStat) SpeedSpread() float64 { return math.Sqrt(math.Max(0, s.SpeedVar)) }

// SpeedScore is what a server can be RELIED ON to deliver, in Mbps: its typical
// speed less its variability, so a server that gives 50 Mbps every time ranks
// above one that averages the same by alternating 90 and 10. Higher is better,
// which is the other way round from the delay score — the two are ranked by
// different comparators for exactly that reason.
func (s nodeStat) SpeedScore() float64 {
	return math.Max(0, s.SpeedAvg-speedSpreadWeight*s.SpeedSpread())
}

// speedSpreadWeight is how much of the variability is charged against the mean.
// One standard deviation: enough that an erratic server loses to a steady one of
// the same average, not so much that a single slow sample buries a good server.
const speedSpreadWeight = 1.0

// recordSpeedStat folds one SUCCESSFUL speed measurement into the history.
//
// Only successes: see the comment on SpeedAvg. A test that failed tells us about
// the test endpoint far more often than about the server.
func recordSpeedStat(hash int64, mbps float64) {
	if hash == 0 || mbps <= 0 {
		return
	}
	nodeStats.Lock()
	defer nodeStats.Unlock()
	st := nodeStats.m[hash]
	st.SpeedSeen++
	st.SpeedAt = time.Now()
	// Hundredths of MB/s: 327 MB/s is beyond any link this measures, and the
	// second decimal is the last one that means anything at these speeds.
	v := mbps * 100
	if v > 32767 {
		v = 32767
	}
	st.SpeedRecent = append(st.SpeedRecent, int16(v))
	st.SpeedRecentAt = append(st.SpeedRecentAt, unixMinutes(time.Now()))
	if len(st.SpeedRecent) > speedRecentMax {
		st.SpeedRecent = append([]int16(nil), st.SpeedRecent[len(st.SpeedRecent)-speedRecentMax:]...)
		st.SpeedRecentAt = append([]int32(nil), st.SpeedRecentAt[len(st.SpeedRecentAt)-speedRecentMax:]...)
	}
	if st.SpeedSeen == 1 || st.SpeedAvg == 0 {
		// Seeded, not blended from zero — the same lesson as the delay mean.
		st.SpeedAvg, st.SpeedVar = mbps, 0
	} else {
		d := mbps - st.SpeedAvg
		st.SpeedAvg += statsAlpha * d
		st.SpeedVar = (1 - statsAlpha) * (st.SpeedVar + statsAlpha*d*d)
	}
	if st.Updated.IsZero() {
		st.Updated = time.Now()
	}
	nodeStats.m[hash] = st
	nodeStats.dirty[hash] = struct{}{}
}

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

// speedResultFresh reports whether a server's last speed measurement is recent
// enough to rank on.
//
// Speed ages badly — an hour-old figure describes the evening, the neighbours
// and whatever else was on the line, not a server — which is why the history
// mode expires it after speedMaxAge. The same clock applies to the row's own
// last measurement: it is persisted, so without this a restart brings back a
// number from days ago and ranks by it. No history row at all means nothing
// has been measured since the program last forgot, and that is not fresh
// either.
func speedResultFresh(hash int64) bool {
	s, ok := statsFor(hash)
	if !ok || s.SpeedAt.IsZero() {
		return false
	}
	return time.Since(s.SpeedAt) < speedMaxAge
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
	sample := int16(-1)
	if ok && delayMs > 0 {
		if delayMs > 30000 {
			delayMs = 30000 // an int16 holds a timeout, not a stopwatch
		}
		sample = int16(delayMs)
	}
	s.Recent = append(s.Recent, sample)
	s.RecentAt = append(s.RecentAt, unixMinutes(time.Now()))
	if len(s.Recent) > statsRecentMax {
		s.Recent = append([]int16(nil), s.Recent[len(s.Recent)-statsRecentMax:]...)
	}
	if len(s.RecentAt) > statsRecentMax {
		s.RecentAt = append([]int32(nil), s.RecentAt[len(s.RecentAt)-statsRecentMax:]...)
	}

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

// speedRecentMax is the same window for speed measurements, shorter because
// they are made far more rarely: a handful covers a day, where the same number
// of pings covers a minute.
const speedRecentMax = 8

// statsRecentMax is how many raw samples are kept for the eye — the list in the
// history dialog, and the sparkline the table can draw from it.
//
// Not what ranking reads: that is the running mean beside it, which every
// measurement folds into and none is dropped from. This is the only place raw
// values are kept at all, and they live in one text column on the server's row,
// so the number is a storage decision: twenty is a glance-able list at a few
// dozen bytes per server, where a real log of samples for a quarter-million
// servers would be millions of rows to write, index and prune.
const statsRecentMax = 20

// unknownSpreadFrac is what the ranking assumes it does not know about a figure
// resting on too few measurements, as a fraction of the figure itself.
//
// A server measured once has a mean equal to that measurement and a spread of
// zero, so it enters a "steady" ranking as if it were perfectly consistent —
// and beats a server whose long record has been charged for the spread it
// actually has. That is the mistake statsMinSamples exists to stop, and the
// threshold alone does not stop it: it only decides which of two rulers a
// candidate is measured with.
//
// A third of the figure, charged in proportion to how much of the history is
// missing: enough that one lucky measurement does not outrank a known-good
// server by a hair, not so much that a server measured once at twice the speed
// cannot win. It disappears entirely at statsMinSamples.
const unknownSpreadFrac = 0.35

// thinCharge is that fraction for a figure built from seen measurements: 0 once
// there are enough of them. Speed is discounted by it, delay inflated by it —
// same charge, and in both cases it makes the less-known figure the worse one.
func thinCharge(seen int64) float64 {
	if seen >= statsMinSamples {
		return 0
	}
	if seen < 0 {
		seen = 0
	}
	return unknownSpreadFrac * float64(statsMinSamples-seen) / statsMinSamples
}

// stableScore is the delay a server should be judged on when ranking by
// stability: its typical delay, plus what its inconsistency and its failures
// are worth.
//
// With too little history it is the raw delay, charged for what is not known
// about it — a single measurement is a guess, and it used to be compared at
// face value against figures that had been charged for their spread.
func stableScore(hash int64, rawDelay int64) float64 {
	s, ok := statsFor(hash)
	if !ok || !s.Trusted() {
		return float64(rawDelay) * (1 + thinCharge(s.Samples))
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
	updated_at INTEGER NOT NULL DEFAULT 0,
	recent     TEXT    NOT NULL DEFAULT ''
);
`

// migrateNodeStats adds what a database written by an older build has not got.
// A missing column is the ordinary case here, not an error.
func migrateNodeStats(db *sql.DB) {
	db.Exec(`ALTER TABLE node_stats ADD COLUMN recent TEXT NOT NULL DEFAULT ''`) //nolint:errcheck
	// The speed history. Added rather than rebuilt: an existing row keeps its
	// delay history and simply has no speed yet, which is the truth.
	db.Exec(`ALTER TABLE node_stats ADD COLUMN speed_avg REAL NOT NULL DEFAULT 0`)     //nolint:errcheck
	db.Exec(`ALTER TABLE node_stats ADD COLUMN speed_var REAL NOT NULL DEFAULT 0`)     //nolint:errcheck
	db.Exec(`ALTER TABLE node_stats ADD COLUMN speed_seen INTEGER NOT NULL DEFAULT 0`) //nolint:errcheck
	db.Exec(`ALTER TABLE node_stats ADD COLUMN speed_at INTEGER NOT NULL DEFAULT 0`)   //nolint:errcheck
	// When each kept sample was taken, and the speed window beside the delay
	// one. An older row has values without times: they read as unknown rather
	// than as "just now", which is what the dialog then says.
	db.Exec(`ALTER TABLE node_stats ADD COLUMN recent_at TEXT NOT NULL DEFAULT ''`)       //nolint:errcheck
	db.Exec(`ALTER TABLE node_stats ADD COLUMN speed_recent TEXT NOT NULL DEFAULT ''`)    //nolint:errcheck
	db.Exec(`ALTER TABLE node_stats ADD COLUMN speed_recent_at TEXT NOT NULL DEFAULT ''`) //nolint:errcheck
}

// unixMinutes is the clock these windows keep. Nothing here is answered to the
// second, and a minute keeps the stored differences to one or two characters.
func unixMinutes(t time.Time) int32 { return int32(t.Unix() / 60) }

// timesToText / timesFromText store a window's timestamps as the first one
// followed by the gaps after it.
//
// A bulk test writes twenty samples inside the same minute, so the gaps are
// almost all zero and the column costs about as much as the values beside it.
// Absolute minutes would be eight digits every time, for a number nobody reads
// on its own.
func timesToText(v []int32) string {
	if len(v) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(strconv.Itoa(int(v[0])))
	for i := 1; i < len(v); i++ {
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(int(v[i] - v[i-1])))
	}
	return b.String()
}

func timesFromText(s string) []int32 {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]int32, 0, len(parts))
	var acc int32
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out
		}
		if i == 0 {
			acc = int32(n)
		} else {
			acc += int32(n)
		}
		out = append(out, acc)
	}
	return out
}

// alignTimes makes a times slice the same length as the values it belongs to.
// A row written by an older build has values and no times at all; one written
// by a newer one and read back by this code always matches. Missing times read
// as "unknown" rather than shifting every value onto the wrong clock.
func alignTimes(times []int32, n int) []int32 {
	if len(times) == n {
		return times
	}
	out := make([]int32, n)
	// Right-align what there is: the newest values are the ones that have times.
	for i := 0; i < n; i++ {
		j := len(times) - n + i
		if j >= 0 && j < len(times) {
			out[i] = times[j]
		}
	}
	return out
}

// recentToText / recentFromText keep the window in one column: a dozen small
// numbers, comma separated. A blob would save a few bytes and cost anyone
// reading the database by hand.
func recentToText(v []int16) string {
	if len(v) == 0 {
		return ""
	}
	var b strings.Builder
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(int(x)))
	}
	return b.String()
}

func recentFromText(s string) []int16 {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]int16, 0, len(parts))
	for _, p := range parts {
		if n, err := strconv.Atoi(p); err == nil {
			out = append(out, int16(n))
		}
	}
	return out
}

// loadNodeStats reads the table into memory and drops what has gone stale.
func loadNodeStats(db *sql.DB) {
	if db == nil {
		return
	}
	cutoff := time.Now().Add(-statsMaxAge).Unix()
	db.Exec(`DELETE FROM node_stats WHERE updated_at < ?`, cutoff) //nolint:errcheck

	migrateNodeStats(db)
	rows, err := db.Query(`SELECT body_hash, ping_avg, ping_var, ok_rate, samples, updated_at, recent,
		speed_avg, speed_var, speed_seen, speed_at, recent_at, speed_recent, speed_recent_at FROM node_stats`)
	if err != nil {
		return
	}
	defer rows.Close()

	m := map[int64]nodeStat{}
	for rows.Next() {
		var h, samples, updated, speedSeen, speedAt int64
		var avg, variance, okRate, speedAvg, speedVar float64
		var recent, recentAt, speedRecent, speedRecentAt string
		if err := rows.Scan(&h, &avg, &variance, &okRate, &samples, &updated, &recent,
			&speedAvg, &speedVar, &speedSeen, &speedAt, &recentAt, &speedRecent, &speedRecentAt); err != nil {
			continue
		}
		st := nodeStat{PingAvg: avg, PingVar: variance, OK: okRate, Samples: samples,
			Updated: time.Unix(updated, 0), Recent: recentFromText(recent),
			SpeedAvg: speedAvg, SpeedVar: speedVar, SpeedSeen: speedSeen,
			SpeedRecent: recentFromText(speedRecent)}
		st.RecentAt = alignTimes(timesFromText(recentAt), len(st.Recent))
		st.SpeedRecentAt = alignTimes(timesFromText(speedRecentAt), len(st.SpeedRecent))
		if speedAt > 0 {
			st.SpeedAt = time.Unix(speedAt, 0)
		}
		m[h] = st
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
	stmt, err := tx.Prepare(`INSERT INTO node_stats (body_hash, ping_avg, ping_var, ok_rate, samples, updated_at, recent,
		speed_avg, speed_var, speed_seen, speed_at, recent_at, speed_recent, speed_recent_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(body_hash) DO UPDATE SET
			ping_avg=excluded.ping_avg, ping_var=excluded.ping_var,
			ok_rate=excluded.ok_rate, samples=excluded.samples, updated_at=excluded.updated_at,
			recent=excluded.recent, recent_at=excluded.recent_at,
			speed_avg=excluded.speed_avg, speed_var=excluded.speed_var,
			speed_seen=excluded.speed_seen, speed_at=excluded.speed_at,
			speed_recent=excluded.speed_recent, speed_recent_at=excluded.speed_recent_at`)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		return
	}
	for _, r := range pending {
		speedAt := int64(0)
		if !r.s.SpeedAt.IsZero() {
			speedAt = r.s.SpeedAt.Unix()
		}
		stmt.Exec(r.h, r.s.PingAvg, r.s.PingVar, r.s.OK, r.s.Samples, r.s.Updated.Unix(), recentToText(r.s.Recent),
			r.s.SpeedAvg, r.s.SpeedVar, r.s.SpeedSeen, speedAt,
			timesToText(r.s.RecentAt), recentToText(r.s.SpeedRecent), timesToText(r.s.SpeedRecentAt)) //nolint:errcheck
	}
	stmt.Close()
	tx.Commit() //nolint:errcheck
}

// StabilityDTO is one server's history as the table shows it.
type StabilityDTO struct {
	// Percent is the share of successful checks, 0..100, or -1 while there are
	// too few measurements to say anything.
	Percent int `json:"percent"`
	// Recent is the last delays, newest last; -1 marks a failed check.
	Recent []int16 `json:"recent"`
	// Avg and Jitter describe the same history in numbers, for the tooltip.
	Avg     int   `json:"avg"`
	Jitter  int   `json:"jitter"`
	Samples int64 `json:"samples"`
}

// stabilityFor returns the history for one config, or nil when there is none.
func stabilityFor(hash int64) *StabilityDTO {
	s, ok := statsFor(hash)
	if !ok || s.Samples == 0 {
		return nil
	}
	d := &StabilityDTO{
		Percent: -1,
		Recent:  s.Recent,
		Avg:     int(s.PingAvg + 0.5),
		Jitter:  int(s.Jitter() + 0.5),
		Samples: s.Samples,
	}
	if s.Trusted() {
		d.Percent = int(s.OK*100 + 0.5)
	}
	return d
}
