package core

import (
	"net"
	"sort"
	"strconv"
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
// It used to answer "is the panel open", and tracking began the first time it
// was. That made the totals useless for the question they exist for: opening the
// panel to find out what had been using the connection showed an empty table and
// started counting from that moment.
//
// So it runs for the whole session — unless it is switched off, which is now a
// deliberate choice with a switch of its own in the panel rather than a side
// effect of closing a window. Off means off: no map entry per connection, no
// peek at the first bytes to learn the destination, no parsing of the engine's
// log. What has already been collected stays until it is cleared.
//
// On costs, per connection: one map entry (512 live at most), one read of the
// first kilobyte, and a process lookup with a two-second cache. Everything is in
// memory and nothing is written to disk.
func connectionsWatched() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return !appSettings.ConnTrackingOff
}

// connBegin registers a connection and returns its handle, or nil when tracking
// is switched off.
func connBegin(dest, process, via, rule string, exact bool) *connEntry {
	if !connectionsWatched() {
		return nil
	}
	// A name asked for here is a name that can be put to an address seen bare
	// somewhere else — see revname.go.
	if h, _, err := net.SplitHostPort(dest); err == nil {
		noteResolvedHost(h)
	}
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
	addConnTotal(e)
	conns.Lock()
	delete(conns.m, e.ID)
	conns.Unlock()
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

// ─────────────────── where the proxy path reads the destination ─────────────

// peekDestination reads the first bytes a client sends and returns the address
// it asked for, without consuming them: the returned prefix is replayed to the
// engine.
//
// Two protocols, both trivial at this depth. HTTP: either "CONNECT host:port"
// or a request line with an absolute URL. SOCKS5: the address sits at a fixed
// offset of the connect request, after the greeting. Anything unrecognised
// gives an empty destination and the bytes are passed through untouched — this
// must never be able to break a connection it merely wanted to describe.
func peekDestination(prefix []byte) string {
	if len(prefix) < 5 {
		return ""
	}
	// SOCKS5 greeting: 05 NMETHODS ...
	if prefix[0] == 0x05 {
		return socks5Dest(prefix)
	}
	line := string(prefix)
	if i := strings.IndexAny(line, "\r\n"); i > 0 {
		line = line[:i]
	}
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return ""
	}
	if strings.EqualFold(parts[0], "CONNECT") {
		return parts[1]
	}
	// GET http://host/path HTTP/1.1
	u := parts[1]
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	} else {
		return ""
	}
	if i := strings.IndexAny(u, "/?"); i >= 0 {
		u = u[:i]
	}
	if u == "" {
		return ""
	}
	if !strings.Contains(u, ":") {
		u += ":80"
	}
	return u
}

// socks5Dest pulls the address out of a SOCKS5 connect request that follows the
// greeting in the same read. When it does not (the client waits for the
// method reply first, which most do), the destination stays unknown and the row
// shows the address the engine reports instead.
func socks5Dest(b []byte) string {
	n := int(b[1])
	off := 2 + n // greeting
	if len(b) < off+7 || b[off] != 0x05 || b[off+1] != 0x01 {
		return ""
	}
	atyp := b[off+3]
	p := off + 4
	var host string
	switch atyp {
	case 0x01: // IPv4
		if len(b) < p+6 {
			return ""
		}
		host = strconv.Itoa(int(b[p])) + "." + strconv.Itoa(int(b[p+1])) + "." +
			strconv.Itoa(int(b[p+2])) + "." + strconv.Itoa(int(b[p+3]))
		p += 4
	case 0x03: // hostname
		if len(b) < p+1 {
			return ""
		}
		l := int(b[p])
		p++
		if len(b) < p+l+2 {
			return ""
		}
		host = string(b[p : p+l])
		p += l
	default:
		return "" // IPv6 arrives rarely here and adds nothing but formatting
	}
	port := int(b[p])<<8 | int(b[p+1])
	return host + ":" + strconv.Itoa(port)
}

// ─────────────────── where the TUN path reads its connections ───────────────

// The engine's own log, at info or debug, is the only view we have of a tunnel
// we are not standing in. It names the destination of every connection and,
// at debug, the rule that placed it. There are no byte counts in it, so those
// rows say so instead of showing zeros.

var tunConns = struct {
	sync.Mutex
	m map[string]*connEntry // engine connection id → row
}{m: map[string]*connEntry{}}

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
	case strings.Contains(line, "inbound connection to "):
		m := singboxDestRe.FindStringSubmatch(line)
		if m == nil {
			return
		}
		e := connBegin(m[1], "", "", "", false)
		if e == nil {
			return
		}
		tunConns.Lock()
		tunConns.m[id] = e
		tunConns.Unlock()
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

// addConnTotal folds a finished connection into the totals.
func addConnTotal(e *connEntry) {
	if e == nil || e.Dest == "" {
		return
	}
	// A connection open when recording was switched off would otherwise land in
	// the session totals when it finally ends, minutes later — the table would
	// go on growing after the switch said it had stopped.
	if !connectionsWatched() {
		return
	}
	up, down := e.Up.Load(), e.Down.Load()
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
		k := totalsKey(e.Process, e.Dest)
		t := merged[k]
		if t == nil {
			t = &connTotal{Process: e.Process, Dest: e.Dest}
			merged[k] = t
		}
		t.Count++
		t.Up += e.Up.Load()
		t.Down += e.Down.Load()
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
	}
	clearBypassSnapshot()
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

func SetConnectionTracking(on bool) {
	settingsMu.Lock()
	appSettings.ConnTrackingOff = !on
	settingsMu.Unlock()
	saveSettings()
	// Nothing is cleared here. Switching recording off answers "stop collecting",
	// not "throw away what you collected" — and a panel that emptied itself when
	// the switch was flipped destroyed the very thing the user had been reading.
	// The live rows freeze as a last picture of the moment it was switched off;
	// the clear button empties both tables when that is what is wanted.
}
