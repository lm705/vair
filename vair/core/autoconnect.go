package core

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// autoWant is the auto-supervisor's desired-state flag: true means "we
// should be connected" (set on any user/auto connect and at startup when
// auto-connect-on-start is on), false means the user deliberately
// disconnected (suppresses auto-reconnect until the next manual connect or
// restart). It is intentionally NOT part of ConnState — that struct is
// wholesale-replaced on every transition, while this flag must persist
// across an engine death so failover can fire. See startAutoSupervisor.
var autoWant atomic.Bool

// autoManaged is true when the *current* connection was established by the
// supervisor (auto-connect/failover) rather than a user click. It gates the
// "honor the candidate pool while connected" switch: we may move an
// auto-chosen config to match a pool change, but must never yank a config the
// user picked by hand. Set false on every user connect, true when the
// supervisor connects.
var autoManaged atomic.Bool

// autoLiveRtt is the most recent live-probe round-trip in milliseconds for the
// connected config (0 = unknown / not probing). Surfaced in the Auto panel
// status so the user can see how fast the current link actually is. Updated by
// the supervisor on each health probe; reset to 0 on disconnect.
var autoLiveRtt atomic.Int64

// autoForce, set via /api/auto/switch ("Switch now"), asks the supervisor to
// run an immediate failover/connect on its next tick, bypassing the health
// threshold and the min-dwell gate. autoWake nudges the supervisor so it reacts
// within ~instant rather than waiting out its sleep.
var autoForce atomic.Bool
var autoWake = make(chan struct{}, 1)

// autoProbeNow asks the supervisor to run a health probe on its next tick
// instead of waiting out the full health interval. Set after a manual connect
// while auto is on, so the panel shows the just-connected config's live ping
// within ~2s rather than after one probe interval. Self-clearing in the loop.
var autoProbeNow atomic.Bool

// autoKick wakes the supervisor loop without blocking (buffered, drop-if-full).
func autoKick() {
	select {
	case autoWake <- struct{}{}:
	default:
	}
}

// autoKickSoon re-wakes the supervisor after a short delay. Used when a forced
// "Switch now" couldn't run *this* tick (actionMu held by another action, or
// the connection was mid connect/disconnect): instead of waiting the full 2s
// loop interval, we retry in ~150ms so the switch feels immediate once the
// blocking action clears. autoForce stays armed, so the next wake acts on it.
func autoKickSoon() {
	go func() {
		time.Sleep(150 * time.Millisecond)
		autoKick()
	}()
}

// ─────────────────────────── auto-connect / failover ──────────────────────
//
// A single long-lived supervisor goroutine (startAutoSupervisor) drives both
// auto-connect-on-start and failover. Each tick it reads AppSettings + the live
// ConnState and acts. It takes cm.actionMu via TryLock, so a user
// connect/disconnect always wins and the supervisor simply skips that tick.

type autoCand struct {
	entry *ConfigEntry
	tabID string
	raw   string
	delay int64
	speed float64 // measured download MBps (0 = no valid speed result)
	rank  int     // 0 = ping OK (then by ascending delay), 1 = untested, 2 = ping failed
}

// autoHealthInterval is the probe spacing for the connected-health check.
func autoHealthInterval() time.Duration {
	settingsMu.RLock()
	s := appSettings.AutoHealthSec
	settingsMu.RUnlock()
	if s <= 0 {
		s = 30
	} else if s < 5 {
		s = 5
	}
	return time.Duration(s) * time.Second
}

// autoEffectiveMode resolves the mode for auto actions: explicit AutoMode, else
// the last-connected mode, else proxy. TUN downgrades to proxy without admin.
func autoEffectiveMode() ConnMode {
	settingsMu.RLock()
	m := appSettings.AutoMode
	last := appSettings.LastConnectedMode
	settingsMu.RUnlock()
	mode := ConnMode(m)
	if m == "" {
		mode = ConnMode(last)
	}
	if mode != ModeProxy && mode != ModeTUN {
		mode = ModeProxy
	}
	if mode == ModeTUN && !checkAdmin() {
		mode = ModeProxy
	}
	return mode
}

// autoPool returns the candidate tab IDs, defaulting to the main tab.
func autoPool() []string {
	settingsMu.RLock()
	pool := append([]string(nil), appSettings.AutoTabs...)
	settingsMu.RUnlock()
	if len(pool) == 0 {
		pool = []string{"main"}
	}
	return pool
}

// loadTabEntries returns a COPY of a tab's in-memory config slice, in
// idx order. A copy so callers that append / re-index / dedup then storeReplace
// don't mutate the shared backing array out from under a concurrent window read.
// (The entry pointers are shared; result fields are read via e.snap().)
func loadTabEntries(tabID string) []*ConfigEntry {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return append([]*ConfigEntry(nil), state.tabEntries[tabID]...)
}

// autoPoolHasEntries reports whether any pool tab currently has configs loaded.
func autoPoolHasEntries(pool []string) bool {
	state.mu.RLock()
	defer state.mu.RUnlock()
	for _, tid := range pool {
		if len(state.tabEntries[tid]) > 0 {
			return true
		}
	}
	return false
}

// autoPoolHasPingData reports whether at least one pool config has a finished
// ping result (OK or Failed). autoPingTabs pings the whole pool in one
// synchronous sweep, so "any tested" reliably means "the sweep ran"; a fresh
// fetch resets entries to Pending, flipping this back to false so the
// supervisor re-pings before its next connect.
func autoPoolHasPingData(pool []string) bool {
	state.mu.RLock()
	defer state.mu.RUnlock()
	for _, tid := range pool {
		for _, e := range state.tabEntries[tid] {
			st := e.snap().PingStatus
			if st == StatusOK || st == StatusFailed {
				return true
			}
		}
	}
	return false
}

// autoConnInPool reports whether the currently-connected config belongs to the
// selected candidate pool. Match is by raw URL within a pool tab (not just
// ConnTab) so the same config living in multiple tabs still counts. Used to
// honor a pool change made from the panel while already connected.
func autoConnInPool(cs ConnState, pool []string) bool {
	if cs.ConnRaw == "" {
		return true // nothing to evaluate — don't force a switch
	}
	inPool := map[string]bool{}
	for _, tid := range pool {
		inPool[tid] = true
	}
	// Fast path: the connection's own tab is in the pool.
	if inPool[cs.ConnTab] {
		return true
	}
	// Otherwise, see if the connected raw appears in any pool tab.
	state.mu.RLock()
	defer state.mu.RUnlock()
	for _, tid := range pool {
		for _, e := range state.tabEntries[tid] {
			if e.snap().Raw == cs.ConnRaw {
				return true
			}
		}
	}
	return false
}

// autoConnRawPresent reports whether the connected config's raw URL still exists
// among the pool tabs' current entries. After a list refresh a previously-
// connected config can vanish from the pool while the tunnel stays up; in that
// case the over-budget guard must not insist on finding something "faster than
// the current config" (there's no in-list metric for a config that's gone) — it
// should just switch to the fastest available. Differs from autoConnInPool,
// which is satisfied as soon as the connection's TAB is in the pool, regardless
// of whether the specific config is still listed.
func autoConnRawPresent(pool []string, raw string) bool {
	if raw == "" {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	for _, tid := range pool {
		for _, e := range state.tabEntries[tid] {
			if e.snap().Raw == raw {
				return true
			}
		}
	}
	return false
}

// autoTabExcludeRules returns each pool tab's exclude-filter rules, keyed by
// tab ID. Caller must hold state.mu (R). Used to defensively re-apply the
// per-tab exclude filter when auto-connect picks candidates: the fetch paths
// already drop excluded configs, but if the user edits a tab's exclude rules
// without reloading, the in-memory entries are still the unfiltered set — so
// auto re-checks here rather than offering a config the tab is meant to hide.
func autoTabExcludeRulesLocked(pool []string) map[string][]string {
	inPool := map[string]bool{}
	for _, tid := range pool {
		inPool[tid] = true
	}
	out := map[string][]string{}
	for _, t := range state.tabs {
		if inPool[t.ID] && !t.ExcludeDisabled && len(t.ExcludeFilter) > 0 {
			out[t.ID] = t.ExcludeFilter
		}
	}
	return out
}

// autoEntryExcluded reports whether a config (already snapped) is hidden by its
// tab's exclude rules. rules is the slice for that tab (nil ⇒ never excluded).
func autoEntryExcluded(es ConfigEntry, rules []string) bool {
	if len(rules) == 0 {
		return false
	}
	return shouldSkip(es.Name, es.Protocol, es.Host, es.Network, es.Security, rules)
}

// autoCandidates collects connectable configs from the pool, skipping the
// excluded raw, configs still in cooldown, unsupported/unparseable ones, and
// any config hidden by its tab's exclude filter.
// Ordered: ping-OK first (by descending speed when AutoRankBySpeed is on, else
// by ascending delay), then untested, then ping-failed.
func autoCandidates(pool []string, excludeRaw string, cooldown map[string]time.Time) []autoCand {
	settingsMu.RLock()
	rankBySpeed := appSettings.AutoRankBySpeed
	settingsMu.RUnlock()
	now := time.Now()
	seen := map[string]bool{}
	var out []autoCand
	state.mu.RLock()
	excludeRules := autoTabExcludeRulesLocked(pool)
	for _, tid := range pool {
		rules := excludeRules[tid]
		for _, e := range state.tabEntries[tid] {
			es := e.snap()
			if es.Raw == "" || es.Raw == excludeRaw || seen[es.Raw] {
				continue
			}
			if autoEntryExcluded(es, rules) {
				continue
			}
			if t, ok := cooldown[es.Raw]; ok && now.Before(t) {
				continue
			}
			n, err := parseNode(es.Raw)
			if err != nil || nodeUnsupportedReason(n) != "" {
				continue
			}
			seen[es.Raw] = true
			rank := 1
			if es.PingStatus == StatusOK {
				rank = 0
			} else if es.PingStatus == StatusFailed {
				rank = 2
			}
			spd := 0.0
			if es.SpeedStatus == StatusOK {
				spd = es.SpeedMBps
			}
			out = append(out, autoCand{entry: e, tabID: tid, raw: es.Raw, delay: es.Delay, speed: spd, rank: rank})
		}
	}
	state.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].rank != out[j].rank {
			return out[i].rank < out[j].rank
		}
		if out[i].rank == 0 {
			if rankBySpeed {
				si, sj := out[i].speed, out[j].speed
				// Configs with a real speed result rank ahead of those without,
				// fastest first; ties / no-speed fall back to ping delay.
				if (si > 0) != (sj > 0) {
					return si > 0
				}
				if si > 0 && sj > 0 && si != sj {
					return si > sj
				}
			}
			return out[i].delay < out[j].delay
		}
		return false
	})
	return out
}

// autoBumpCooldown puts a raw on an exponential cooldown so a flapping config
// drops out of the candidate list for a while (30s, doubling, capped 30 min).
func autoBumpCooldown(raw string, cooldown map[string]time.Time, backoff map[string]time.Duration) {
	const base = 30 * time.Second
	const max = 30 * time.Minute
	d := backoff[raw]
	if d == 0 {
		d = base
	} else {
		d *= 2
		if d > max {
			d = max
		}
	}
	backoff[raw] = d
	cooldown[raw] = time.Now().Add(d)
}

func autoLabel(e *ConfigEntry) string {
	es := e.snap()
	if es.Name != "" {
		return es.Name
	}
	return fmt.Sprintf("#%d", es.Index)
}

// autoHasFasterCandidate reports whether the candidate pool holds a config
// meaningfully faster than the currently-connected one, by the active ranking
// metric (ping delay, or download speed when AutoRankBySpeed). It gates the
// over-budget (slow-but-alive) failover so we never downgrade to a worse
// config — a dead probe bypasses this entirely. Returns false when the current
// config's own metric is unknown (can't prove an improvement → stay put), and
// compares like-for-like (clean best-of-3 ping vs ping, speed vs speed) so a
// spiky single live probe can't make a slower candidate look faster.
func autoHasFasterCandidate(pool []string, cs ConnState, cooldown map[string]time.Time) bool {
	settingsMu.RLock()
	bySpeed := appSettings.AutoRankBySpeed
	settingsMu.RUnlock()
	var curDelay int64 = -1
	var curSpeed float64 = -1
	state.mu.RLock()
	for _, tid := range pool {
		for _, e := range state.tabEntries[tid] {
			es := e.snap()
			if es.Raw == cs.ConnRaw {
				curDelay = es.Delay
				if es.SpeedStatus == StatusOK {
					curSpeed = es.SpeedMBps
				}
			}
		}
	}
	state.mu.RUnlock()
	// For the ping comparison, use the CURRENT LIVE latency (the live probe RTT
	// that just tripped the budget), not the config's stale clean list-ping.
	// They differ a lot: list-ping is a best-of-3 on a fresh test engine (e.g.
	// 90ms), while the live probe reflects the loaded tunnel right now (e.g.
	// 243ms). Comparing candidates against the stale 90ms made AUTO conclude "no
	// faster candidate" even when a 150ms candidate is clearly better than the
	// 243ms we're actually getting — the exact case where Switch now (which has
	// no such guard) does switch. Prefer the live RTT; fall back to list-ping.
	liveMs := autoLiveRtt.Load()
	if liveMs > 0 {
		curDelay = liveMs
	}
	// Unknown current metric ⇒ can't prove any candidate is better ⇒ don't move.
	if bySpeed && curSpeed <= 0 {
		return false
	}
	if !bySpeed && curDelay <= 0 {
		return false
	}
	for _, c := range autoCandidates(pool, cs.ConnRaw, cooldown) {
		es := c.entry.snap()
		if es.PingStatus != StatusOK {
			continue
		}
		if bySpeed {
			if c.speed > curSpeed*1.15 { // ≥15% faster download
				return true
			}
		} else if es.Delay > 0 && es.Delay < curDelay-20 { // ≥20ms lower ping than current live latency
			return true
		}
	}
	return false
}

// broadcastAuto pushes an auto_update SSE event so the panel + conn-bar badge
// can reflect the supervisor's activity. state ∈ {switching, connected,
// all_down}.
func broadcastAuto(stateStr, name, raw, reason string) {
	settingsMu.RLock()
	enabled := appSettings.AutoConnect
	settingsMu.RUnlock()
	state.broadcast(SSEEvent{Type: "auto_update", Payload: map[string]interface{}{
		"enabled":      enabled,
		"state":        stateStr,
		"current_name": name,
		"current_raw":  raw,
		"reason":       reason,
		"all_down":     stateStr == "all_down",
		"rtt_ms":       autoLiveRtt.Load(),
	}})
}

// autoTryConnect connects to one candidate and verifies the live tunnel.
// Caller MUST hold cm.actionMu. Returns true only on a verified connection.
func autoTryConnect(c autoCand, mode ConnMode) bool {
	if mode == ModeTUN {
		startTUNConnectionOnTab(c.entry, c.tabID)
	} else {
		startProxyConnectionOnTab(c.entry, c.tabID)
	}
	if state.conn.snap().Status != ConnConnected {
		return false
	}
	// Give the freshly-started tunnel a moment to settle, then verify it
	// actually passes traffic (a config can "connect" yet not route).
	for attempt := 0; attempt < 2; attempt++ {
		time.Sleep(800 * time.Millisecond)
		if state.conn.snap().Status != ConnConnected {
			return false
		}
		// Alive-only at connect time: never reject a slow-but-working config
		// here, or failover could get stuck when every candidate is slow.
		// The latency budget is enforced separately by the live monitor.
		if alive, rtt := probeLiveTunnel(state.conn); alive {
			// Publish THIS config's live RTT immediately so the panel shows the
			// new config's ping right after a switch, instead of lingering on
			// the previous (often slow) config's stale value.
			autoLiveRtt.Store(rtt.Milliseconds())
			return true
		}
	}
	return false
}

// autoConnectFromPool tries pool candidates in order until one connects and
// verifies. preferRaw (if set & present) is tried first; excludeRaw is skipped.
// At most autoMaxAttempts are tried per sweep — cooled-down failures fall out
// so subsequent sweeps advance through the rest of the pool. Caller MUST hold
// cm.actionMu. Returns true on success.
func autoConnectFromPool(pool []string, preferRaw, excludeRaw string, mode ConnMode, reason string, cooldown map[string]time.Time, backoff map[string]time.Duration) bool {
	const autoMaxAttempts = 8
	cands := autoCandidates(pool, excludeRaw, cooldown)
	if len(cands) == 0 {
		broadcastAuto("all_down", "", "", "no candidates")
		vlog("warning", "auto: no eligible config in pool")
		return false
	}
	if preferRaw != "" {
		for i := range cands {
			if cands[i].raw == preferRaw {
				// Only jump the last-connected config to the front if it actually
				// passed its ping (rank 0). If its fresh ping failed/timed out
				// (rank 2) or it's untested (rank 1), DON'T prefer it — leave it in
				// sorted position so AUTO connects to the fastest working config
				// instead of stubbornly retrying a dead "last" config.
				if cands[i].rank == 0 {
					c := cands[i]
					cands = append(cands[:i], cands[i+1:]...)
					cands = append([]autoCand{c}, cands...)
				}
				break
			}
		}
	}
	// Clear the previous config's live RTT so the "switching" broadcasts (and any
	// failed attempt) don't keep showing the old config's ping. autoTryConnect
	// republishes the real value once the new config verifies.
	autoLiveRtt.Store(0)
	for i, c := range cands {
		if i >= autoMaxAttempts {
			vlog("warning", "auto: tried %d candidates without success; will retry", autoMaxAttempts)
			return false
		}
		name := autoLabel(c.entry)
		broadcastAuto("switching", name, c.raw, reason)
		vlog("info", "auto: trying %s (%s)", name, mode)
		if autoTryConnect(c, mode) {
			delete(cooldown, c.raw)
			delete(backoff, c.raw)
			autoManaged.Store(true) // supervisor owns this connection
			broadcastAuto("connected", name, c.raw, reason)
			vlog("info", "auto: connected → %s (%s)", name, reason)
			return true
		}
		autoBumpCooldown(c.raw, cooldown, backoff)
		vlog("warning", "auto: %s did not work, trying next", name)
		// TUN settle gap: each failed TUN attempt tore down a WinTUN adapter and
		// rewrote the default route. removeTUNAdapter() returns before Windows
		// finishes releasing the device, so firing the next TUN create immediately
		// can race the kernel (transient "adapter busy") and churns system routing
		// in a burst. A brief pause between attempts lets the adapter/route settle.
		// Proxy mode has no adapter, so it needs no delay and stays fast.
		if mode == ModeTUN && i < len(cands)-1 {
			time.Sleep(1200 * time.Millisecond)
		}
	}
	// Every candidate failed. That almost always means a local/network-wide
	// outage rather than each config being individually bad — so the cooldowns
	// we just stamped are misleading and would make the *next* sweep skip the
	// genuinely-fastest configs and land on a slower one. Clear them so the next
	// attempt starts clean and re-tries the fastest config first.
	for k := range cooldown {
		delete(cooldown, k)
	}
	for k := range backoff {
		delete(backoff, k)
	}
	broadcastAuto("all_down", "", "", "all candidates failed")
	vlog("warning", "auto: all candidates in pool failed")
	return false
}

// autoPingRunning guards autoPingTabs so two background sweeps can't overlap.
var autoPingRunning int32

// The running sweep's cancellation state: RELOAD on a tab the sweep covers
// cancels it (cancelAutoSweepOnTab via cancelTestsOnTab), so a re-reload stops
// the stale test instead of colliding with it — the fresh after-refresh test
// then waits for the drain (waitAutoSweepIdle) and starts cleanly.
var (
	autoSweepMu     sync.Mutex
	autoSweepCancel chan struct{}
	autoSweepTabs   map[string]bool
)

// cancelAutoSweepOnTab cancels the background candidate sweep if it covers
// tabID (or any sweep when tabID is ""). Returns whether a cancel was issued.
func cancelAutoSweepOnTab(tabID string) bool {
	if atomic.LoadInt32(&autoPingRunning) == 0 {
		return false
	}
	autoSweepMu.Lock()
	defer autoSweepMu.Unlock()
	if autoSweepCancel == nil || (tabID != "" && !autoSweepTabs[tabID]) {
		return false
	}
	select {
	case <-autoSweepCancel:
		return false // already cancelled
	default:
		close(autoSweepCancel)
		return true
	}
}

// waitAutoSweepIdle blocks until no background sweep is running (or the timeout
// passes). Used by the after-refresh tests so a freshly-cancelled sweep drains
// before the new one starts — without this the new test's CAS fails and it is
// silently skipped.
func waitAutoSweepIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for atomic.LoadInt32(&autoPingRunning) == 1 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

// autoPingTabs pings every config across the given tabs (deduped) so the
// supervisor has fresh Delay/PingStatus for its "fastest by ping" ordering.
// A config-list refresh rebuilds entries with no ping data, so this is what
// keeps ordering meaningful. It yields to any user-initiated bulk test
// (state.pingRunning / speedRunning) and never overlaps another auto-ping.
// The currently-connected config is skipped (we already know it works via the
// live probe, and a parallel test connection could trip per-account limits).
// Results broadcast as entry_update; it deliberately avoids the bulk_ping_*
// progress machinery, which is single-tab UI state.
// autoTestTabs pings (and, when withSpeed, speed-tests) every config across the
// given tabs. withSpeed runs the full ping→speed measurement so the supervisor
// can rank by real download speed (AutoRankBySpeed); otherwise it's ping-only
// (fast, for connect ordering). See autoPingTabs / the refresh path for callers.
// testTabs is the shared background tester: it pings (and, when withSpeed,
// speed-tests) every config across the given tabs, deduped and respecting each
// tab's exclude filter. Results broadcast as entry_update (no bulk-progress UI).
// It yields to any user-initiated bulk test and never overlaps another
// background sweep (autoPingRunning guard). keepGoing, when non-nil, is checked
// before each config and stops the sweep early when it returns false — AUTO uses
// it to bail if auto-connect is switched off mid-sweep; the per-tab
// "test after auto-refresh" passes nil (always run to completion).
func testTabs(tabIDs []string, withSpeed bool, keepGoing func() bool) {
	// manualBulkOverlaps reports whether a manual PING/SPEED-ALL run is testing
	// one of THIS sweep's tabs. A manual bulk on a DIFFERENT tab must NOT stop the
	// sweep (each probe runs its own isolated engine, so they coexist fine) — the
	// old unconditional check let a ping/speed test on any tab (and its Stop) kill
	// the AUTO candidate sweep on another tab.
	sweepSet := map[string]bool{}
	for _, tid := range tabIDs {
		sweepSet[tid] = true
	}
	manualBulkOverlaps := func() bool {
		if atomic.LoadInt32(&state.pingRunning) == 0 && atomic.LoadInt32(&state.speedRunning) == 0 {
			return false
		}
		testMu.Lock()
		tt := testingTab
		testMu.Unlock()
		return sweepSet[tt]
	}
	if manualBulkOverlaps() {
		return
	}
	if !atomic.CompareAndSwapInt32(&autoPingRunning, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&autoPingRunning, 0)

	// Register this sweep's cancel channel + tab set (RELOAD on a covered tab
	// closes the channel via cancelAutoSweepOnTab).
	autoSweepMu.Lock()
	autoSweepCancel = make(chan struct{})
	sweepCancel := autoSweepCancel
	autoSweepTabs = map[string]bool{}
	for _, tid := range tabIDs {
		autoSweepTabs[tid] = true
	}
	autoSweepMu.Unlock()
	defer func() {
		autoSweepMu.Lock()
		autoSweepTabs = nil
		autoSweepMu.Unlock()
	}()

	type pingItem struct {
		e   *ConfigEntry
		tab string
	}
	var list []pingItem
	seen := map[string]bool{}
	state.mu.RLock()
	excludeRules := autoTabExcludeRulesLocked(tabIDs)
	for _, tid := range tabIDs {
		rules := excludeRules[tid]
		for _, e := range state.tabEntries[tid] {
			es := e.snap()
			// NB: the currently-connected config is intentionally NOT skipped — it
			// gets re-tested too. Each test spins up its own isolated engine on a
			// separate port (see runPingForEntry/withEngine), so probing the live
			// config doesn't disturb the active tunnel, and the candidate list then
			// shows fresh ping/speed for it like every other config.
			if es.Raw == "" || seen[es.Raw] {
				continue
			}
			// Don't waste test traffic on configs the tab's exclude filter hides.
			if autoEntryExcluded(es, rules) {
				continue
			}
			seen[es.Raw] = true
			list = append(list, pingItem{e: e, tab: tid})
		}
	}
	state.mu.RUnlock()
	if len(list) == 0 {
		return
	}

	sem := make(chan struct{}, currentPingConcurrency())
	var wg sync.WaitGroup
	for _, item := range list {
		// Cancelled (a RELOAD on a covered tab) → stop dispatching; the refresh
		// replaced the entries this sweep holds, so testing them is stale work.
		if isTestCancelled(sweepCancel) {
			break
		}
		// Step aside only when a manual bulk test targets a tab THIS sweep covers
		// (same tab → don't double-test / fight over its rows). A manual test on
		// another tab is left to run alongside.
		if manualBulkOverlaps() {
			break
		}
		// Bail early when the caller's predicate says so. For AUTO this fires when
		// auto-connect was turned off mid-sweep — otherwise a stale sweep keeps
		// autoPingRunning=1 to completion, so a quick disable→enable can't start a
		// fresh sweep (the CAS fails) and the supervisor waits on the old one.
		if keepGoing != nil && !keepGoing() {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(ent *ConfigEntry, tab string) {
			defer wg.Done()
			defer func() { <-sem }()
			ent.mu.Lock()
			ent.PingStatus = StatusTestingPing
			if withSpeed {
				ent.SpeedStatus = StatusTestingSpeed
			}
			ent.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tab})
			if withSpeed {
				// runSpeedForEntry does ping→speed in one engine session.
				runSpeedForEntry(ent, tab, nil)
				mirrorSpeedResult(tab, ent)
			} else {
				runPingForEntry(ent, nil)
				mirrorPingResult(tab, ent)
			}
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tab})
		}(item.e, item.tab)
	}
	wg.Wait()
	if withSpeed {
		vlog("info", "refreshed ping+speed for %d config(s)", len(list))
	} else {
		vlog("info", "refreshed ping for %d config(s)", len(list))
	}
}

// autoTestTabs is the AUTO-supervisor entry point: same as testTabs but bails if
// auto-connect is switched off mid-sweep.
func autoTestTabs(tabIDs []string, withSpeed bool) {
	testTabs(tabIDs, withSpeed, func() bool {
		settingsMu.RLock()
		on := appSettings.AutoConnect
		settingsMu.RUnlock()
		return on
	})
}

// runAfterRefreshTest runs the per-tab "test after auto-refresh" (Tab.
// AutoRefreshTest = "ping" or "speed") if configured. Called only from the
// auto-refresh path (NOT manual RELOAD), independent of AUTO mode. Shares the
// background-test guard, so it skips if AUTO is already testing this cycle.
func runAfterRefreshTest(tabID string) {
	state.mu.RLock()
	var mode string
	for _, t := range state.tabs {
		if t.ID == tabID {
			mode = t.AutoRefreshTest
			break
		}
	}
	state.mu.RUnlock()
	switch mode {
	case "ping", "speed":
		// A just-cancelled sweep (reload during a test) may still be draining its
		// in-flight pings; wait it out so the fresh test actually starts instead
		// of being skipped by the autoPingRunning guard.
		waitAutoSweepIdle(30 * time.Second)
		testTabs([]string{tabID}, mode == "speed", nil)
	}
}

// autoPingTabs is the ping-only entry point (connect ordering). Kept as a thin
// wrapper so existing call sites read clearly.
func autoPingTabs(tabIDs []string) { autoTestTabs(tabIDs, false) }

// autoPingAfterRefresh re-pings a freshly auto-refreshed tab, but only when
// auto-connect is on and the tab is part of the candidate pool. Called right
// after a pool tab's auto-refresh completes (the refresh wipes ping data).
func autoPingAfterRefresh(tabID string) {
	settingsMu.RLock()
	on := appSettings.AutoConnect && appSettings.AutoPingRefresh
	withSpeed := appSettings.AutoRankBySpeed
	settingsMu.RUnlock()
	if !on {
		return
	}
	for _, p := range autoPool() {
		if p == tabID {
			// A just-cancelled sweep (reload during a candidate test) may still be
			// draining; wait so the fresh sweep starts instead of CAS-skipping.
			waitAutoSweepIdle(30 * time.Second)
			// When ranking by speed, run the full ping→speed test so the
			// candidate list has fresh Mbps to rank by — same trigger as ping.
			autoTestTabs([]string{tabID}, withSpeed)
			return
		}
	}
}

// refreshSourcelessTab handles the auto-refresh tick for a user tab that has no
// source URL/file (only pasted configs). There's nothing to re-fetch, but the
// user set a refresh interval, so we honor it by resetting each config's test
// results back to "pending" — exactly what a real reload does to a sourced tab.
// This makes auto-refresh a meaningful feature for sourceless tabs (stale ping/
// speed numbers get cleared on the cadence) independent of the auto-connect
// feature. If the tab is also in the auto-connect pool, autoPingAfterRefresh
// then re-tests it so the cleared rows get fresh data.
func refreshSourcelessTab(tabID string) {
	n := resetTabResultsMem(tabID) // reset ping/speed in memory + SQLite
	loadedSignal(tabID)
	vlog("info", "auto-refresh: reset test results for %d config(s) in sourceless tab", n)
	// If this tab feeds auto-connect, re-test the cleared rows so they're ranked.
	autoPingAfterRefresh(tabID)
}

// startAutoSupervisor is the single goroutine that drives auto-connect and
// failover. Launched once at startup (both startup paths). It does nothing
// while AutoConnect is off (no probing overhead).
func startAutoSupervisor() {
	cm := state.conn
	failCount := 0
	var lastSwitch, lastProbe, nextRetry, lastTUNSwitch, lastSlowRetest time.Time
	cooldown := map[string]time.Time{}
	backoff := map[string]time.Duration{}
	const minDwell = 60 * time.Second
	// When over budget but already on the fastest available config, re-test the
	// pool no more often than this — enough to promote a faster config once one
	// appears, without spamming test traffic while stuck on a uniformly-slow net.
	const slowRetestEvery = 30 * time.Second
	// Hard floor between *TUN* switches, regardless of which path triggers them
	// (forced "Switch now", pool change, or failover). Recreating the WinTUN
	// adapter + flipping the default route in a tight burst can disrupt
	// system-wide networking; this bounds the cadence. Proxy switches are cheap
	// and are not gated. tunSwitchTooSoon() reports when we're inside the floor.
	const tunSwitchFloor = 4 * time.Second
	tunSwitchTooSoon := func(mode ConnMode) bool {
		return mode == ModeTUN && !lastTUNSwitch.IsZero() && time.Since(lastTUNSwitch) < tunSwitchFloor
	}

	for {
		select {
		case <-time.After(2 * time.Second):
		case <-autoWake: // "Switch now" or similar — react without waiting
		}

		settingsMu.RLock()
		on := appSettings.AutoConnect
		// Auto-switch (failover while connected) is no longer a separate toggle:
		// it's an intrinsic part of auto-connect, always on when the feature is on.
		sw := on
		threshold := appSettings.AutoFailThreshold
		maxLat := appSettings.AutoMaxLatencyMs
		lastRaw := appSettings.LastConnectedRaw
		settingsMu.RUnlock()
		if !on {
			failCount = 0
			autoForce.Store(false) // don't carry a stale force into re-enable
			continue
		}
		// A pending "Switch now" request. Consumed by whichever branch acts on
		// it; left armed while a transient connect/disconnect settles.
		forced := autoForce.Load()
		if threshold < 1 {
			threshold = 2
		}
		pool := autoPool()
		mode := autoEffectiveMode()
		probeEvery := autoHealthInterval()

		cs := cm.snap()
		switch cs.Status {
		case ConnConnecting, ConnDisconnecting:
			// Transient — wait for it to settle. But if a "Switch now" is pending,
			// retry soon rather than waiting the full 2s tick, so it fires the
			// instant the connect/disconnect finishes.
			if forced {
				autoKickSoon()
			}
		case ConnConnected:
			// "Switch now" — leave the current config (cooled down so we don't
			// bounce straight back) and grab the next-best, ignoring threshold
			// and min-dwell. Works for user- and auto-owned connections alike.
			if forced && autoPoolHasEntries(pool) {
				// Respect the TUN switch floor even for a forced switch — but keep
				// autoForce armed and retry soon so it fires the moment the floor
				// clears (the user still gets their switch, just not a churn burst).
				if tunSwitchTooSoon(mode) {
					autoKickSoon()
					continue
				}
				if cm.actionMu.TryLock() {
					autoForce.Store(false)
					autoBumpCooldown(cs.ConnRaw, cooldown, backoff)
					vlog("info", "auto: manual switch from %s", cs.EntryName)
					ok := autoConnectFromPool(pool, "", cs.ConnRaw, mode, "manual switch", cooldown, backoff)
					cm.actionMu.Unlock()
					failCount = 0
					lastProbe = time.Now()
					if ok {
						lastSwitch = time.Now()
						if mode == ModeTUN {
							lastTUNSwitch = time.Now()
						}
					} else {
						nextRetry = time.Now().Add(probeEvery)
					}
				} else {
					// actionMu held by a user connect/disconnect or a probe — retry
					// shortly instead of waiting out the 2s loop. autoForce stays set.
					autoKickSoon()
				}
				continue
			}
			// Honor the candidate pool. If the user changed the selected tabs
			// while *auto* is keeping us connected to a config that's no longer
			// in the pool, switch to one that is. Applies whether or not
			// auto-switch-on-failure is on (picking tabs must do something while
			// connected), but only for supervisor-owned connections — never
			// yank a config the user connected to by hand.
			if autoManaged.Load() && !autoConnInPool(cs, pool) && autoPoolHasEntries(pool) {
				if time.Since(lastSwitch) < minDwell {
					continue
				}
				if !cm.actionMu.TryLock() {
					continue
				}
				vlog("info", "auto: %s is outside the selected tabs — switching", cs.EntryName)
				ok := autoConnectFromPool(pool, "", cs.ConnRaw, mode, "tabs changed", cooldown, backoff)
				cm.actionMu.Unlock()
				failCount = 0
				lastProbe = time.Now()
				if ok {
					lastSwitch = time.Now()
					if mode == ModeTUN {
						lastTUNSwitch = time.Now()
					}
				} else {
					nextRetry = time.Now().Add(probeEvery)
				}
				continue
			}
			// A manual connect requests an immediate probe so the panel shows the
			// just-connected config's live ping within ~2s. Honor it even when
			// auto-switch is off (it's display-only there) and regardless of the
			// probe interval.
			probeNow := autoProbeNow.CompareAndSwap(true, false)
			if !sw {
				failCount = 0
				// Still probe once for display if the user just connected manually.
				if probeNow {
					if alive, rtt := probeLiveTunnel(cm); alive {
						autoLiveRtt.Store(rtt.Milliseconds())
						broadcastAuto("health", cs.EntryName, cs.ConnRaw, "")
					} else {
						autoLiveRtt.Store(0)
					}
				}
				continue
			}
			if !probeNow && time.Since(lastProbe) < probeEvery {
				continue
			}
			lastProbe = time.Now()
			alive, rtt := probeLiveTunnel(cm)
			if alive {
				autoLiveRtt.Store(rtt.Milliseconds())
			} else {
				autoLiveRtt.Store(0)
			}
			overBudget := alive && maxLat > 0 && rtt > time.Duration(maxLat)*time.Millisecond
			if alive && !overBudget {
				failCount = 0
				// Refresh the panel with the live latency of a healthy link.
				broadcastAuto("health", cs.EntryName, cs.ConnRaw, "")
				continue
			}
			// A dead probe counts as a failure (any working config beats a broken
			// one). An over-budget-but-alive probe also counts, but the guard below
			// keeps us from downgrading to an equal/worse config on a latency spike.
			failCount++
			reason := "probe failed"
			if overBudget {
				reason = fmt.Sprintf("too slow %dms", rtt.Milliseconds())
				vlog("warning", "auto: %s too slow (%dms > %dms) (%d/%d)", cs.EntryName, rtt.Milliseconds(), maxLat, failCount, threshold)
			} else {
				vlog("warning", "auto: health probe failed (%d/%d) for %s", failCount, threshold, cs.EntryName)
			}
			if failCount < threshold {
				continue
			}
			if overBudget {
				// Slow-but-alive path. The min-dwell only gates THIS case: it stops a
				// transient latency spike from flapping us off a working config. A DEAD
				// probe skips this whole block and switches the moment the threshold is
				// hit, so a broken link never has to wait out the dwell (the
				// "health probe failed 3/2, 4/2 … no switch" the user reported).
				if time.Since(lastSwitch) < minDwell {
					continue
				}
				// When the connected config is still in the refreshed pool, only switch
				// if the pool holds a genuinely FASTER candidate than the current live
				// latency — never flap to an equal/worse one; stay put and kick a
				// throttled background re-test so the next tick re-ranks on fresh data.
				// But if the connected config has DROPPED OUT of the pool (a list
				// refresh removed it while the tunnel stayed up), there's no in-list
				// metric to compare against, so fall through and switch to the fastest
				// available rather than re-testing forever waiting for something faster
				// than a config that's gone (the "France vanished, tests run nonstop"
				// report).
				if autoConnRawPresent(pool, cs.ConnRaw) && !autoHasFasterCandidate(pool, cs, cooldown) {
					failCount = 0
					broadcastAuto("health", cs.EntryName, cs.ConnRaw, "slow but best")
					if atomic.LoadInt32(&autoPingRunning) == 0 && time.Since(lastSlowRetest) > slowRetestEvery {
						lastSlowRetest = time.Now()
						settingsMu.RLock()
						bySpeed := appSettings.AutoRankBySpeed
						settingsMu.RUnlock()
						go autoTestTabs(pool, bySpeed)
					}
					continue
				}
			}
			// A faster candidate exists → switch to the fastest available below.
			// Pick the failover target by *fresh* ping ranking. If the pool has no
			// ping data (e.g. an auto-refresh just reset entries to "pending"),
			// autoCandidates falls back to tab order — which is the "switched to a
			// random config, not the fastest" the user reported. Re-ping first so
			// the sweep ranks by real delay, exactly like the manual "Switch now".
			// The connected config is skipped inside autoPingTabs, so this is cheap
			// and safe while still connected.
			if !autoPoolHasPingData(pool) && atomic.LoadInt32(&autoPingRunning) == 0 {
				autoPingTabs(pool)
			}
			if !cm.actionMu.TryLock() {
				continue // user action in progress — yield
			}
			// Cool down the failing config so we don't immediately flap back.
			autoBumpCooldown(cs.ConnRaw, cooldown, backoff)
			ok := autoConnectFromPool(pool, "", cs.ConnRaw, mode, reason, cooldown, backoff)
			cm.actionMu.Unlock()
			failCount = 0
			lastProbe = time.Now()
			if ok {
				lastSwitch = time.Now()
				if mode == ModeTUN {
					lastTUNSwitch = time.Now()
				}
			} else {
				nextRetry = time.Now().Add(probeEvery)
			}
		case ConnIdle, ConnError:
			// "Switch now" while disconnected = connect now: it arms intent and
			// skips the back-off timer so the next-best config comes up at once.
			if forced {
				autoForce.Store(false)
				autoWant.Store(true)
				nextRetry = time.Time{}
			}
			if !autoWant.Load() || time.Now().Before(nextRetry) {
				continue
			}
			if !autoPoolHasEntries(pool) {
				continue // entries not loaded yet (startup) — retry next tick
			}
			// Never connect mid-ping: if a candidate ping (enable / refresh /
			// startup) is in flight, wait for it so we pick the fastest config,
			// not whichever happens to come up first.
			if atomic.LoadInt32(&autoPingRunning) == 1 {
				continue
			}
			// First connect with no ping data yet → ping the whole pool now
			// (blocking) so ordering is by real delay. A config-list refresh
			// resets entries to "pending", which re-triggers this next pass.
			if !autoPoolHasPingData(pool) {
				autoPingTabs(pool)
				if !autoWant.Load() {
					continue // user disconnected while we were pinging
				}
				if !autoPoolHasPingData(pool) {
					// Ping couldn't run (e.g. user bulk test busy) — retry
					// rather than connect to an unranked config.
					nextRetry = time.Now().Add(probeEvery)
					continue
				}
			}
			if !cm.actionMu.TryLock() {
				continue
			}
			// Re-check under the lock: the ping above can take several seconds,
			// during which the user may have connected by hand. Never connect
			// over a live/in-progress session, and respect a mid-ping manual
			// disconnect (autoWant cleared).
			if s := cm.snap().Status; (s != ConnIdle && s != ConnError) || !autoWant.Load() {
				cm.actionMu.Unlock()
				continue
			}
			ok := autoConnectFromPool(pool, lastRaw, "", mode, "auto-connect", cooldown, backoff)
			cm.actionMu.Unlock()
			if ok {
				lastSwitch = time.Now()
				lastProbe = time.Now()
			} else {
				nextRetry = time.Now().Add(probeEvery)
			}
		}
	}
}
