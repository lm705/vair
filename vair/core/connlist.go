package core

import (
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The live list of connections.
//
// "Connected" is one bit, and it answers almost nothing: which program is using
// the tunnel, where it is going, which of your rules sent it there, and whether
// the 40 MB that just moved was a video or an update you did not ask for. Every
// other client in this class shows this; we could not.
//
// Two sources, because the two modes put us in different places:
//
//   - proxy — we are in the path. The counting relay already sees every
//     connection; reading the first bytes of it tells us the destination the
//     client asked for, and the byte counters are exact.
//   - TUN — we are not in the path; the engine is. What we have is its log,
//     which we already read for the rule statistics, and which names the
//     destination, the rule and the outbound. There are no per-connection byte
//     counts there, and the list says so rather than showing zeros.
//
// Nothing is written to disk and nothing leaves the machine: the list lives in
// memory, capped, and is dropped when the connection ends.

// connMax is how many live connections are kept. A busy browser opens dozens; a
// thousand would be a memory leak with a scrollbar.
const connMax = 512

// connEntry is one connection as the panel shows it.
type connEntry struct {
	ID      int64
	Process string // "chrome.exe", or "" when we could not tell
	Dest    string // "youtube.com:443"
	Rule    string // which of the user's lists sent it there, when known
	Via     string // "proxy" | "direct" | "blocked"
	Up      *atomic.Int64
	Down    *atomic.Int64
	Started time.Time
	Exact   bool // false when the byte counts are unknown (TUN)
	// taken marks a row the counting relay has adopted, and closed a row whose
	// end has already been recorded. Both exist because in hybrid TUN two
	// watchers see the same connection — see adoptEngineRow.
	taken  atomic.Bool
	closed atomic.Bool
	// What it had carried when the history was last cleared, for a connection
	// that was open at that moment: "since start" counts only what came after.
	cleared          atomic.Bool
	baseUp, baseDown atomic.Int64
}

// sinceClear is what the connection has carried since the history was last
// cleared, and whether it belongs in the history at all: one that was open at
// the clear and has carried nothing since does not. It used to be folded back
// in whole, so the button emptied the table and the next second put every open
// connection back — in TUN, where the engine's rows end only when it says so,
// that was most of the table.
func (e *connEntry) sinceClear() (up, down int64, counts bool) {
	up, down = e.Up.Load()-e.baseUp.Load(), e.Down.Load()-e.baseDown.Load()
	return up, down, !e.cleared.Load() || up > 0 || down > 0
}

// ConnRow is one row of the live list, for the frontend.
type ConnRow struct {
	ID      int64  `json:"id"`
	Process string `json:"process"`
	Dest    string `json:"dest"`
	Rule    string `json:"rule"`
	Via     string `json:"via"`
	Up      int64  `json:"up"`
	Down    int64  `json:"down"`
	Seconds int    `json:"seconds"`
	Exact   bool   `json:"exact"`
}

var conns = struct {
	sync.Mutex
	m      map[int64]*connEntry
	nextID int64
}{m: map[int64]*connEntry{}}

// connectionsWatched reports whether to record anything at all.
//
// Off until the user turns it on with the switch in the panel. What it collects
// is a list of the sites visited, with the program that visited each and the
// traffic, kept for the whole run and readable through the web interface; that
// is not something to keep for someone who never asked for it.
//
// Once on, it runs for the whole session, not only while the panel is open: it
// used to start the first time the panel opened, which made the totals useless
// for the question they exist for. Off means off: no map entry per connection,
// no reading of the client's request, no parsing of the engine's log. What has
// already been collected stays until it is cleared.
//
// On costs, per connection: one map entry (512 live at most), a parse of the
// first bytes as they pass, and a process lookup with a two-second cache.
// Everything is in memory and nothing is written to disk.
func connectionsWatched() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.ConnTracking
}

// connBegin registers a connection and returns its handle, or nil when tracking
// is switched off.
func connBegin(dest, process, via, rule string, exact bool) *connEntry {
	if !connectionsWatched() {
		return nil
	}
	// No lookup of the destination here: the name is what the client asked
	// for, and resolving it again — once, through the system's resolver —
	// sent every site visited through the proxy to the provider (see revname.go).
	conns.Lock()
	defer conns.Unlock()
	if len(conns.m) >= connMax {
		// Drop the oldest rather than refuse the newest: the interesting
		// connection is the one that just started.
		var oldest *connEntry
		for _, e := range conns.m {
			if oldest == nil || e.Started.Before(oldest.Started) {
				oldest = e
			}
		}
		if oldest != nil {
			delete(conns.m, oldest.ID)
		}
	}
	conns.nextID++
	e := &connEntry{
		ID: conns.nextID, Process: process, Dest: dest, Rule: rule, Via: via,
		Up: &atomic.Int64{}, Down: &atomic.Int64{}, Started: time.Now(), Exact: exact,
	}
	conns.m[e.ID] = e
	return e
}

// connEnd forgets a finished connection.
//
// Idempotent, because a row can have two owners: in hybrid TUN the engine
// reports the connection closing and the relay's copy of it finishes, and the
// second of the two would otherwise count the same bytes into the session
// totals a second time.
func connEnd(e *connEntry) {
	if e == nil || e.closed.Swap(true) {
		return
	}
	// Read under the list's lock: the relay points a row at its own counters
	// when it adopts one (openRelayRow), and the engine can end that row from
	// its log at the same moment.
	conns.Lock()
	snap := connEntry{ID: e.ID, Process: e.Process, Dest: e.Dest, Via: e.Via}
	up, down, counts := e.sinceClear()
	delete(conns.m, e.ID)
	conns.Unlock()
	if counts {
		addConnTotal(&snap, up, down)
	}
}

// adoptEngineRow hands the counting relay the row the engine has just opened
// for the same connection, instead of listing it twice.
//
// In hybrid TUN both of us see everything the tunnel carries: sing-box writes
// "inbound connection to X" in its log and then dials our relay, which reads
// the same X out of the SOCKS greeting a moment later. The panel used to show
// both — one row with the verdict and no bytes, one with the bytes and no
// verdict, for one connection the user made once.
//
// Neither half is redundant, which is why this joins them rather than dropping
// one: only the engine knows which rule sent it and which way it went, and only
// the relay can count what crossed. The engine is always first (it dials us),
// so the relay looks for a row of its own making with this destination that
// nothing has taken yet.
//
// Two connections to the same destination in the same instant can be matched
// the wrong way round. What that costs is which of two rows to the same host
// carries which byte count — the panel is still right about every field it
// shows for both.
func adoptEngineRow(dest string) *connEntry {
	if !connectionsWatched() {
		return nil
	}
	conns.Lock()
	defer conns.Unlock()
	var best *connEntry
	for _, e := range conns.m {
		if e.Dest != dest || e.Exact || e.taken.Load() {
			continue
		}
		if best == nil || e.Started.After(best.Started) {
			best = e
		}
	}
	if best == nil || best.taken.Swap(true) {
		return nil
	}
	// It has byte counts now, and the panel stops marking it as an estimate.
	best.Exact = true
	return best
}

// isFakeIPDest reports whether a destination is one of FakeIP's stand-ins
// (engine.go: inet4_range, ipv6FakeRange).
func isFakeIPDest(dest string) bool {
	host, _, err := net.SplitHostPort(dest)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return fakeIPv4.Contains(ip) || fakeIPv6.Contains(ip)
}

var (
	fakeIPv4 = netip.MustParsePrefix("198.18.0.0/15")
	fakeIPv6 = netip.MustParsePrefix(ipv6FakeRange)
)

// mergeIntoRelayRow joins an engine row whose real destination has just become
// known to the relay's row for the same connection, if the relay got there
// first: it looked for an engine row by the real name while the engine row
// still carried the stand-in, found none and opened its own. The relay's row
// keeps the bytes, takes the program and the verdict, and the engine's goes —
// without being counted, since it is the same connection. Nil when there is
// nothing to join.
func mergeIntoRelayRow(e *connEntry) *connEntry {
	conns.Lock()
	defer conns.Unlock()
	var best *connEntry
	for _, r := range conns.m {
		if r == e || r.Dest != e.Dest || !r.Exact || r.Process != "" || r.taken.Load() {
			continue
		}
		if d := r.Started.Sub(e.Started); d < -2*time.Second || d > 5*time.Second {
			continue
		}
		if best == nil || r.Started.Before(best.Started) {
			best = r
		}
	}
	if best == nil || e.taken.Load() {
		return nil
	}
	best.taken.Store(true)
	best.Process = e.Process
	if e.Rule != "" {
		best.Rule = e.Rule
	}
	if e.Via != "" {
		best.Via = e.Via
	}
	e.closed.Store(true)
	delete(conns.m, e.ID)
	return best
}

// LiveConnections returns the current list, busiest first.
func LiveConnections() []ConnRow {
	// Rows whose ending was never logged would otherwise pile up: a line can be
	// lost to the log's rate gate, and a list that only grows is worse than one
	// that forgets. Done on read, so there is no ticker to stop — and not at all
	// while recording is off, when the list is a frozen picture rather than a
	// live one and has nothing to expire towards.
	if connectionsWatched() {
		expireTunConns(5 * time.Minute)
	}
	conns.Lock()
	defer conns.Unlock()
	out := make([]ConnRow, 0, len(conns.m))
	now := time.Now()
	for _, e := range conns.m {
		out = append(out, ConnRow{
			ID: e.ID, Process: e.Process, Dest: e.Dest, Rule: e.Rule, Via: e.Via,
			Up: e.Up.Load(), Down: e.Down.Load(),
			Seconds: int(now.Sub(e.Started).Seconds()), Exact: e.Exact,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		bi, bj := out[i].Up+out[i].Down, out[j].Up+out[j].Down
		if bi != bj {
			return bi > bj
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// ─────────────────── where the TUN path reads its connections ───────────────

// The engine's own log, at info or debug, is the only view we have of a tunnel
// we are not standing in. It names the destination of every connection and,
// at debug, the rule that placed it. There are no byte counts in it, so those
// rows say so instead of showing zeros.

var tunConns = struct {
	sync.Mutex
	m map[string]*connEntry // engine connection id → row
	// procs holds the program the engine named for a connection whose row is
	// not open yet: sing-box logs "found process path" before "inbound
	// connection to" for the same id. Bounded like the rows are.
	procs map[string]string
}{m: map[string]*connEntry{}, procs: map[string]string{}}

// tunProcsMax caps the programs waiting for their row; past it the map is
// emptied — a few rows without a name beat a map that only grows.
const tunProcsMax = 512

// noteEngineConnLine turns one line of engine output into a row, or updates one.
func noteEngineConnLine(src, line string) {
	if src == "xray" {
		// xray has no connection id to close a row on, so it never opens one —
		// the relay already did that. What its access line adds is the verdict:
		// "accepted //example.com:443 [http-in -> direct]" says which way the
		// engine sent a connection the relay could only see arrive.
		if m := xrayAcceptedRe.FindStringSubmatch(line); m != nil {
			setConnVia(xrayDestHost(m[1]), m[2])
		}
		return
	}
	if src != "singbox" {
		return
	}
	id := singboxConnID(line)
	if id == "" {
		return
	}
	switch {
	case strings.Contains(line, "found process path:"):
		// The only line where the engine names the program. It comes before
		// the row exists, so it waits for it; if the row is already open, it
		// is filled in directly.
		m := singboxProcRe.FindStringSubmatch(line)
		if m == nil {
			return
		}
		proc := baseName(m[1])
		tunConns.Lock()
		e := tunConns.m[id]
		if e == nil {
			if len(tunConns.procs) >= tunProcsMax {
				tunConns.procs = map[string]string{}
			}
			tunConns.procs[id] = proc
		}
		tunConns.Unlock()
		if e != nil {
			conns.Lock()
			e.Process = proc
			conns.Unlock()
		}
	case strings.Contains(line, "inbound connection to "):
		m := singboxDestRe.FindStringSubmatch(line)
		if m == nil {
			return
		}
		tunConns.Lock()
		proc := tunConns.procs[id]
		delete(tunConns.procs, id)
		tunConns.Unlock()
		e := connBegin(m[1], proc, "", "", false)
		if e == nil {
			return
		}
		tunConns.Lock()
		tunConns.m[id] = e
		tunConns.Unlock()
	case strings.Contains(line, "outbound connection to "):
		// With FakeIP the program connected to a stand-in address (198.18.x.x)
		// and the inbound line names that; the outbound line, a moment later,
		// names what it stands for. The panel showed the stand-ins.
		m := singboxOutDestRe.FindStringSubmatch(line)
		if m == nil {
			return
		}
		tunConns.Lock()
		e := tunConns.m[id]
		tunConns.Unlock()
		if e == nil {
			return
		}
		conns.Lock()
		fake := isFakeIPDest(e.Dest)
		if fake {
			e.Dest = m[1]
		}
		conns.Unlock()
		if fake {
			if r := mergeIntoRelayRow(e); r != nil {
				tunConns.Lock()
				if tunConns.m[id] == e {
					tunConns.m[id] = r
				}
				tunConns.Unlock()
			}
		}
	case strings.Contains(line, "router: match["):
		m := singboxMatchRe.FindStringSubmatch(line)
		if m == nil {
			return
		}
		tunConns.Lock()
		e := tunConns.m[id]
		tunConns.Unlock()
		if e == nil {
			return
		}
		conns.Lock()
		e.Via = m[4]
		if e.Via == "" {
			e.Via = "blocked"
		}
		e.Rule = m[1] + "=" + m[2]
		conns.Unlock()
	case strings.Contains(line, "connection closed"):
		tunConns.Lock()
		e := tunConns.m[id]
		delete(tunConns.m, id)
		tunConns.Unlock()
		connEnd(e)
	}
}

// expireTunConns drops rows the engine never reported the end of — a log line
// can be lost to the rate gate, and a list that only grows is worse than one
// that forgets.
func expireTunConns(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	tunConns.Lock()
	defer tunConns.Unlock()
	for id, e := range tunConns.m {
		if e.Started.Before(cutoff) {
			delete(tunConns.m, id)
			connEnd(e)
		}
	}
}

// ─────────────────────────── totals for the session ─────────────────────────
//
// The live list answers "what is happening"; it cannot answer "what has been
// happening", and closing the panel used to throw away everything it had seen.
// So finished connections are added up here, by program and destination, and
// the panel shows both: what is moving now, and what has moved since tracking
// began.
//
// In memory, capped, never written down, and cleared by the button next to it
// or by closing the program. Tracking still begins only when the panel is first
// opened — nothing is collected before someone asks to see it — but it no
// longer stops when the panel closes, because a total with holes in it is worse
// than no total.

// connTotalsMax bounds the table. A busy day is a few hundred pairs; the cap is
// there so a machine that talks to thousands of hosts cannot grow it without
// limit.
const connTotalsMax = 2000

type connTotal struct {
	Process string
	Dest    string
	// Via is the engine's verdict, kept from the last connection of this pair.
	// A pair normally has one — the routing rules do not change between two
	// connections to the same host — and when they do, the newer one is the
	// truth about what happens now.
	Via   string
	Count int
	Up    int64
	Down  int64
	Last  time.Time
}

// ConnTotalRow is one row of the totals table.
type ConnTotalRow struct {
	Process string `json:"process"`
	Dest    string `json:"dest"`
	Via     string `json:"via"`
	Count   int    `json:"count"`
	Up      int64  `json:"up"`
	Down    int64  `json:"down"`
	Seconds int    `json:"seconds"` // since it was last seen
}

var connTotals = struct {
	sync.Mutex
	m map[string]*connTotal
}{m: map[string]*connTotal{}}

func totalsKey(process, dest string) string { return process + "\x00" + dest }

// addConnTotal folds a finished connection into the totals: up and down are
// what it carried since the history was last cleared.
func addConnTotal(e *connEntry, up, down int64) {
	if e == nil || e.Dest == "" {
		return
	}
	// A connection open when recording was switched off would otherwise land in
	// the session totals when it finally ends, minutes later — the table would
	// go on growing after the switch said it had stopped.
	if !connectionsWatched() {
		return
	}
	connTotals.Lock()
	defer connTotals.Unlock()
	if len(connTotals.m) >= connTotalsMax {
		// Forget whatever has been quiet longest.
		var oldestKey string
		var oldest time.Time
		for k, v := range connTotals.m {
			if oldestKey == "" || v.Last.Before(oldest) {
				oldestKey, oldest = k, v.Last
			}
		}
		delete(connTotals.m, oldestKey)
	}
	k := totalsKey(e.Process, e.Dest)
	t := connTotals.m[k]
	if t == nil {
		t = &connTotal{Process: e.Process, Dest: e.Dest}
		connTotals.m[k] = t
	}
	t.Count++
	t.Up += up
	t.Down += down
	if e.Via != "" {
		t.Via = e.Via
	}
	t.Last = time.Now()
}

// ConnectionTotals returns the session's totals, busiest first, with the
// connections that are still open folded in so the numbers do not jump when one
// finishes.
func ConnectionTotals() []ConnTotalRow {
	merged := map[string]*connTotal{}
	connTotals.Lock()
	for k, v := range connTotals.m {
		c := *v
		merged[k] = &c
	}
	connTotals.Unlock()

	now := time.Now()
	conns.Lock()
	for _, e := range conns.m {
		if e.Dest == "" {
			continue
		}
		up, down, counts := e.sinceClear()
		if !counts {
			continue
		}
		k := totalsKey(e.Process, e.Dest)
		t := merged[k]
		if t == nil {
			t = &connTotal{Process: e.Process, Dest: e.Dest}
			merged[k] = t
		}
		t.Count++
		t.Up += up
		t.Down += down
		if e.Via != "" {
			t.Via = e.Via
		}
		t.Last = now
	}
	conns.Unlock()

	out := make([]ConnTotalRow, 0, len(merged))
	for _, t := range merged {
		out = append(out, ConnTotalRow{
			Process: t.Process, Dest: t.Dest, Via: t.Via, Count: t.Count,
			Up: t.Up, Down: t.Down, Seconds: int(now.Sub(t.Last).Seconds()),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		bi, bj := out[i].Up+out[i].Down, out[j].Up+out[j].Down
		if bi != bj {
			return bi > bj
		}
		return out[i].Dest < out[j].Dest
	})
	return out
}

// ClearConnectionTotals empties the history behind the button that says so.
// Connections that are still open keep their rows: they are not history.
func ClearConnectionTotals() {
	connTotals.Lock()
	connTotals.m = map[string]*connTotal{}
	connTotals.Unlock()
	// The button says "clear" and the panel has three tables; emptying one of
	// them was a puzzle rather than a feature.
	//
	// The live table is the exception, and only while recording: its rows are
	// connections that are OPEN, not history, and dropping them would hide
	// traffic that is still moving — the row would never come back, because a row
	// is created when a connection starts and these have already started. With
	// recording off the same rows are a frozen picture of a moment that has
	// passed, and then clearing is exactly what the button should do.
	if !connectionsWatched() {
		conns.Lock()
		conns.m = map[int64]*connEntry{}
		conns.Unlock()
	} else {
		// The open ones stay in "right now", and "since start" counts only what
		// they carry from here on (sinceClear).
		conns.Lock()
		for _, e := range conns.m {
			e.baseUp.Store(e.Up.Load())
			e.baseDown.Store(e.Down.Load())
			e.cleared.Store(true)
		}
		conns.Unlock()
	}
	clearBypassSnapshot()
}

// endTunConns closes every row the engine opened: the session is over, and the
// engine that would have reported each connection's end is gone. The rows used
// to stay "open" for up to five minutes after a disconnect — in "right now",
// and in "since start" as just seen — and a clear could not remove them.
func endTunConns() {
	tunConns.Lock()
	rows := make([]*connEntry, 0, len(tunConns.m))
	for id, e := range tunConns.m {
		rows = append(rows, e)
		delete(tunConns.m, id)
	}
	tunConns.procs = map[string]string{}
	tunConns.Unlock()
	for _, e := range rows {
		connEnd(e)
	}
}

// setConnVia records the engine's routing decision on the rows that are waiting
// for one.
//
// Matched by destination, because that is all the two sides share: the relay
// knows the address the client asked for, the engine's log knows the address it
// dialed and what it decided. Several connections to the same host get the same
// verdict, which is correct — the routing rules do not distinguish them either.
func setConnVia(host, via string) {
	if host == "" || via == "" {
		return
	}
	if via == "block" || via == "blocked" {
		via = "blocked"
	}
	conns.Lock()
	defer conns.Unlock()
	for _, e := range conns.m {
		if e.Via != "" {
			continue
		}
		if e.Dest == host || strings.HasPrefix(e.Dest, host+":") {
			e.Via = via
		}
	}
}

// ConnectionTracking / SetConnectionTracking are the panel's switch.
func ConnectionTracking() bool { return connectionsWatched() }

// recordingOnSince is when recording was last turned on in this run, in unix
// nanoseconds; zero when it was already on when the program started.
var recordingOnSince atomic.Int64

// ConnectionsMissedEarlier reports whether the running session has connections
// the panel cannot show: recording was turned on after the session started, and
// a connection is recorded when it opens — its request is read then, and only
// then. A browser keeps its connections open for minutes, so the sites it was
// already talking to stay missing until they are reopened; reconnecting closes
// them all.
func ConnectionsMissedEarlier() bool {
	since := recordingOnSince.Load()
	if since == 0 || !connectionsWatched() {
		return false
	}
	s := ConnSnapshot()
	return s.Status == ConnConnected && !s.StartedAt.IsZero() && s.StartedAt.UnixNano() < since
}

func SetConnectionTracking(on bool) {
	settingsMu.Lock()
	was := appSettings.ConnTracking
	appSettings.ConnTracking = on
	settingsMu.Unlock()
	saveSettings()
	if on && !was {
		recordingOnSince.Store(time.Now().UnixNano())
	}
	// Nothing is cleared here. Switching recording off answers "stop collecting",
	// not "throw away what you collected" — and a panel that emptied itself when
	// the switch was flipped destroyed the very thing the user had been reading.
	// The live rows freeze as a last picture of the moment it was switched off;
	// the clear button empties both tables when that is what is wanted.
}
