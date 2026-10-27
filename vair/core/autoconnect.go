package core

import (
	"fmt"
	"sort"
	"strings"
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
	// score is what the ordering actually compares, in milliseconds. With
	// stability ranking off it is simply the last measured delay; with it on it
	// is the server's typical delay plus what its jitter and its failures are
	// worth. Kept on the candidate so the sort stays a comparison and the
	// history is consulted once per candidate rather than once per comparison.
	score float64
	// stable marks a candidate whose score came from history rather than from a
	// single measurement — the panel says so, because otherwise picking the
	// slower-looking node reads as a bug.
	stable bool
}

// autoHealthInterval is the probe spacing for the connected-health check.
func autoHealthInterval() time.Duration {
	settingsMu.RLock()
	s := appSettings.AutoHealthSec
	settingsMu.RUnlock()
	if s <= 0 {
		s = 15 // the default the panel and the wiki state
	} else if s < 5 {
		s = 5
	}
	return time.Duration(s) * time.Second
}

// autoRecheckSoon is how long to wait after a FAILED probe before trying again,
// instead of the full interval.
//
// A server that has died usually keeps accepting connections — the socket opens,
// the handshake completes, and nothing comes back. Only a real request notices,
// and only two failures in a row are trusted enough to act on. At a 30-second
// interval that is a minute and a half of a tunnel that swallows everything
// while the app says "connected".
//
// Re-checking quickly after the first failure cuts that to seconds without
// doubling the probe traffic in the normal case, where there is no first
// failure to react to.
const autoRecheckSoon = 5 * time.Second

// probeSpacing is the wait before the next probe: the normal interval, or the
// short one when the last probe failed.
func probeSpacing(fails int) time.Duration {
	if fails > 0 {
		if soon := autoRecheckSoon; soon < autoHealthInterval() {
			return soon
		}
	}
	return autoHealthInterval()
}

// noticeSilentServer is what is left of the health check while it is off. Not a
// probe every fifteen seconds and not a teardown — the switch is off because
// dropping a connection on a probe is a decision the user has not made — but
// the free evidence of the byte counters: bytes leaving with nothing coming
// back. Then one probe (at most one a minute) to be sure, and if the server is
// silent the connection bar says so, once with a notification, until bytes come
// back. A dead server used to read "connected" for as long as anyone looked.
func noticeSilentServer(cm *connManager, live *livenessWatch, lastProbe *time.Time) {
	cs := cm.snap()
	if cs.Status != ConnConnected {
		live.reset()
		return
	}
	up, down := cm.bytes()
	switch live.look(cs.ConnRaw, up, down, time.Now()) {
	case livenessAlive:
		markNoAnswer(cm, cs.ConnRaw, false)
	case livenessSuspect:
		if cs.NoAnswer || time.Since(*lastProbe) < silentServerRecheck {
			return
		}
		*lastProbe = time.Now()
		if alive, _ := probeLiveTunnel(cm); alive {
			live.answered()
			markNoAnswer(cm, cs.ConnRaw, false)
			return
		}
		if markNoAnswer(cm, cs.ConnRaw, true) {
			vlog("warning", "%s: the server is not answering — the connection is still up; \"Monitor the connected server\" closes such a connection, AUTO moves to another server", cs.EntryName)
			desktopNotify("Vair", notifyServerSilent(cs.EntryName))
		}
	}
}

// silentServerRecheck is how often a connection that looks silent is probed.
const silentServerRecheck = time.Minute

// markNoAnswer sets the session's NoAnswer, for the session it was measured on
// only, and reports whether it changed.
func markNoAnswer(cm *connManager, raw string, on bool) bool {
	cm.mu.Lock()
	if cm.state.Status != ConnConnected || cm.state.ConnRaw != raw || cm.state.NoAnswer == on {
		cm.mu.Unlock()
		return false
	}
	cm.state.NoAnswer = on
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	return true
}

// autoEffectiveMode resolves the mode for auto actions: explicit AutoMode, else
// the last-connected mode, else proxy. TUN downgrades to proxy without admin.
// watchIdleConnection is the health check for a connection AUTO is not managing.
//
// It answers the complaint that has no other answer in the app: the config
// stopped working, the engine stayed up, and the panel kept saying "connected"
// while a check-my-IP page showed the user's own address. Losing a connection
// silently is bad in proxy mode and worse in TUN mode, where the machine's
// whole route points at a tunnel that swallows traffic.
//
// It teams up with, rather than duplicates, the engine-death detection: a
// process that exits is noticed immediately and by other means. This covers the
// case where the process is alive and the far end is not.
//
// Deliberately conservative. It only ever acts on the same evidence AUTO uses to
// abandon a config — the same probe, the same interval, the same consecutive
// failure threshold — because the cost of being wrong is dropping a connection
// the user is happily using.
func watchIdleConnection(cm *connManager, fails *int, lastProbe *time.Time, live *livenessWatch) {
	settingsMu.RLock()
	watching := appSettings.WatchConnection
	settingsMu.RUnlock()
	if !watching {
		*fails = 0
		noticeSilentServer(cm, live, lastProbe)
		return
	}
	cs := cm.snap()
	if cs.Status != ConnConnected {
		*fails = 0
		live.reset()
		return
	}
	// A manual connect asks for an immediate probe so the panel can show the live
	// ping straight away; honour it here too, then fall back to the interval.
	probeNow := autoProbeNow.CompareAndSwap(true, false)
	up, down := cm.bytes()
	hint := live.look(cs.ConnRaw, up, down, time.Now())
	// After a failure the counters are not evidence: a failing probe's own
	// traffic is counted among them. See the supervisor for the whole story.
	if *fails > 0 {
		hint = livenessUnknown
	}
	switch hint {
	case livenessAlive:
		// The user's own traffic is crossing the tunnel and coming back, which
		// is a better answer than any probe. Still ask now and then, so the
		// live round-trip time on the panel does not go stale for a session.
		*fails = 0
		if !probeNow && time.Since(*lastProbe) < livenessQuietProbe {
			return
		}
	case livenessSuspect:
		// Bytes are leaving and nothing is coming back — exactly the failure
		// this watch exists for. Ask now instead of waiting out the interval
		// (but not at every tick: see livenessSuspectRecheck).
		if !probeNow && time.Since(*lastProbe) < livenessSuspectRecheck {
			return
		}
	default:
		if !probeNow && time.Since(*lastProbe) < probeSpacing(*fails) {
			return
		}
	}
	*lastProbe = time.Now()

	alive, rtt := probeLiveTunnel(cm)
	if alive {
		*fails = 0
		live.answered()
		autoLiveRtt.Store(rtt.Milliseconds())
		autoNoteLivePing(cs, rtt)
		broadcastAuto("health", cs.EntryName, cs.ConnRaw, "")
		return
	}
	autoLiveRtt.Store(0)
	*fails++

	settingsMu.RLock()
	threshold := appSettings.AutoFailThreshold
	settingsMu.RUnlock()
	if threshold < 1 {
		threshold = 2
	}
	vlog("warning", "health probe failed (%d/%d) for %s", *fails, threshold, cs.EntryName)
	if *fails < threshold {
		return
	}
	*fails = 0
	// No pool to move to — AUTO is off — so the only truthful thing left is to
	// stop claiming the connection exists. The window is often minimised to the
	// tray when this happens, so say it where it will be seen.
	// What the user is told depends on what is actually true now. With the
	// firewall block held, the connection is gone but the machine is NOT back on
	// its ordinary network — saying "connection closed" and leaving it there
	// would have them wondering why nothing loads.
	blocked := trafficHeldNow()
	desktopNotify("Vair", notifyServerGone(cs.EntryName, blocked))
	reason := "the server stopped responding — connection closed"
	if blocked && blockWillLapse() {
		reason = "the server stopped responding — traffic stays blocked for 20 s (kill-switch), then the normal connection returns"
	} else if blocked {
		reason = "the server stopped responding — traffic stays blocked (kill-switch); disconnect to release it"
	}
	connectionLost(reason, "")
}

// autoStillOn re-reads the toggle. Anything that loops for more than an instant
// has to ask again rather than trust the value it started with: the supervisor
// reads the setting once per tick and a single tick can span several connect
// attempts.
func autoStillOn() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.AutoConnect
}

func autoEffectiveMode() ConnMode {
	settingsMu.RLock()
	m, sel, last := appSettings.AutoMode, appSettings.SelectedMode, appSettings.LastConnectedMode
	settingsMu.RUnlock()
	// TUN without the rights it needs used to become proxy here, silently: no
	// TUN kill-switch, and every program that ignores the system proxy went out
	// directly while the setting said TUN. The supervisor now says so and does
	// not connect (autoNeedsAdmin).
	return resolveAutoMode(m, sel, last)
}

// autoNeedsAdmin is the mode auto-connect is set to — TUN — and the rights the
// program runs with — none to create a tunnel.
func autoNeedsAdmin(mode ConnMode) bool {
	return mode == ModeTUN && !tunAvailable()
}

// autoAdminNoted keeps the "TUN is not available" notice to once per time AUTO
// is switched on (SetAuto clears it) or the program starts.
var autoAdminNoted atomic.Bool

// autoNoteNeedsAdmin says why AUTO does not connect in TUN mode, with the way
// out that fits this copy (autoTUNMissing): administrator rights for a
// portable copy on Windows, the helper service for an installed one, the
// helper on Linux. Telling an installed copy to run as administrator sent
// people round the service the setup program put there for exactly this.
func autoNoteNeedsAdmin() {
	if autoAdminNoted.Swap(true) {
		return
	}
	state := autoTUNMissing()
	switch state {
	case "needs_service":
		vlog("warning", "auto: the connection mode is TUN and the Vair helper service is missing — auto-connect does not connect. Repair it in Settings → System, or set auto-connect's connection mode to proxy")
	case "needs_helper":
		vlog("warning", "auto: the connection mode is TUN and the helper is not installed — auto-connect does not connect. Install it from the TUN switch, or set auto-connect's connection mode to proxy")
	default:
		vlog("warning", "auto: the connection mode is TUN and Vair runs without administrator rights — auto-connect does not connect. Restart Vair as administrator, or set auto-connect's connection mode to proxy")
	}
	broadcastAuto(state, "", "", "")
	desktopNotify("Vair", notifyAutoNeedsAdmin(state))
}

func notifyAutoNeedsAdmin(state string) string {
	settingsMu.RLock()
	lang := appSettings.Language
	settingsMu.RUnlock()
	ru := lang == "ru"
	switch {
	case state == "needs_service" && ru:
		return "Авто-подключение не подключается: для режима TUN нужна служба-помощник Vair, а её нет. Восстановите её в «Настройки → Система» или выберите режим прокси в окне AUTO"
	case state == "needs_service":
		return "Auto-connect is not connecting: TUN mode needs the Vair helper service, and it is missing. Repair it in Settings → System, or choose proxy mode in the AUTO window"
	case state == "needs_helper" && ru:
		return "Авто-подключение не подключается: для режима TUN нужен служебный помощник. Установите его кнопкой у переключателя TUN или выберите режим прокси в окне AUTO"
	case state == "needs_helper":
		return "Auto-connect is not connecting: TUN mode needs the helper. Install it with the button next to the TUN switch, or choose proxy mode in the AUTO window"
	case ru:
		return "Авто-подключение не подключается: для режима TUN нужны права администратора. Перезапустите Vair от имени администратора или выберите режим прокси в окне AUTO"
	}
	return "Auto-connect is not connecting: TUN mode needs administrator rights. Restart Vair as administrator, or choose proxy mode in the AUTO window"
}

// resolveAutoMode is that order of preference, and nothing else.
//
// The AUTO setting first, because it is explicit. Then the proxy/tun pill on
// the main window — the one the user can see — because "follow the main window"
// means the control, not the history: the two disagree for as long as someone
// selects a mode without connecting in it, and AUTO used to bring servers up in
// TUN while every control on screen said proxy. The mode of the last session
// last, for a profile written before the pill was recorded.
func resolveAutoMode(auto, pill, last string) ConnMode {
	for _, m := range []string{auto, pill, last} {
		switch ConnMode(m) {
		case ModeProxy:
			return ModeProxy
		case ModeTUN:
			return ModeTUN
		}
	}
	return ModeProxy
}

// autoPool returns the candidate tab IDs, defaulting to the main tab.
func autoPool() []string {
	settingsMu.RLock()
	chosen := append([]string(nil), appSettings.AutoTabs...)
	settingsMu.RUnlock()
	// Only tabs that still exist: a deleted tab stayed in the list, and a pool of
	// deleted tabs had nothing in it while the panel said to run a ping.
	// (DeleteTab also takes it out of the setting.)
	var pool []string
	state.mu.RLock()
	for _, id := range chosen {
		for _, t := range state.tabs {
			if t.ID == id {
				pool = append(pool, id)
				break
			}
		}
	}
	state.mu.RUnlock()
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
	state.mu.RLock()
	defer state.mu.RUnlock()
	// The connected config counts as a candidate where a pool tab holds it and
	// that tab's exclude filter does not hide it — the same test autoCandidates
	// applies. Checking only the tab left AUTO on a server the user had just
	// filtered out of the pool, for as long as that server kept working.
	rules := autoTabExcludeRulesLocked(pool)
	found := false
	for _, tid := range pool {
		for _, e := range state.tabEntries[tid] {
			es := e.snap()
			if es.Raw != cs.ConnRaw {
				continue
			}
			found = true
			if !autoEntryExcluded(es, rules[tid]) {
				return true
			}
		}
	}
	if found {
		return false // present, but hidden everywhere it is
	}
	// Not in any pool tab's list at all. A refresh can rewrite the raw of the
	// very config still connected (a renamed #fragment); its tab being in the
	// pool is then the best evidence left, and a switch on that alone would be
	// a switch for nothing.
	for _, tid := range pool {
		if tid == cs.ConnTab {
			return true
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
// by ascending score), then untested, then ping-failed.
//
// What the score MEANS depends on the ranking mode (see autoRankMode), but its
// direction never does: smaller is always better, so one comparator serves all
// four. The two speed modes negate their figure into it rather than sorting the
// other way, because a second comparator is a second thing to keep in step.
//
//	delay          the last measured delay
//	speed          the last measured speed, negated
//	history        typical delay, charged for jitter and for failed probes — a
//	               node that answers in 80 ms two tries out of three loses to one
//	               that answers in 100 ms every time, which is the right way round
//	               for anything interactive and the wrong way round for a
//	               leaderboard
//	speed_history  typical speed less its spread, negated: what the server can be
//	               relied on to deliver rather than its best moment. With no speed
//	               figure to go on it falls back to the delay one, which sorts
//	               behind everything that has a speed and keeps the rest in an
//	               order that means something
//
// unmeasuredScore is where a candidate nothing is known about sorts: behind
// every candidate something is known about, whatever the figure. Larger than any
// delay a probe can return plus the full unreliability penalty, and only used
// when there is no measurement of any kind to put in its place.
const unmeasuredScore = 1e6

func autoCandidates(pool []string, excludeRaw string, cooldown map[string]time.Time) []autoCand {
	mode := autoRankMode()
	rankBySpeed := mode == rankBySpeedLast
	rankStable := mode == rankByPingHistory
	rankSpeedHistory := mode == rankBySpeedHistory
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
			// A speed result counts while it still describes anything. The
			// history mode has always expired its figure after six hours
			// (SpeedTrusted); the last-measurement mode took whatever was in the
			// row, and rows are persisted — so after a restart it ranked on a
			// number from last Tuesday and called it the fastest server. Same
			// clock for both: the history row is where a measurement's time is
			// recorded, whichever mode reads it.
			spd := 0.0
			if es.SpeedStatus == StatusOK && speedResultFresh(hashBody(es.Raw)) {
				spd = es.SpeedMBps
			}
			c := autoCand{entry: e, tabID: tid, raw: es.Raw, delay: es.Delay, speed: spd, rank: rank}
			c.score = float64(es.Delay)
			switch {
			case rankStable:
				// One expression for both cases: the history score where there is
				// a history, the raw delay charged for what is unknown about it
				// where there is not.
				hash := hashBody(es.Raw)
				c.score = stableScore(hash, es.Delay)
				if st, ok := statsFor(hash); ok && st.Trusted() {
					c.stable = true
				}
			case rankSpeedHistory:
				// The speed a server can be relied on, not the best it ever did.
				// Ranked descending, so it is negated into the same "smaller is
				// better" score the comparator already uses — one comparator, one
				// direction, and no second sort to keep in step with the first.
				st, hasSt := statsFor(hashBody(es.Raw))
				if hasSt && st.SpeedTrusted() {
					c.score = -st.SpeedScore()
					c.stable = true
				} else if spd > 0 {
					// One or two measurements: the last one is a worse answer to
					// the same question, and better than no answer — but it is not
					// the same KIND of answer, and it used to be compared as if it
					// were. A server measured once has no spread to be charged for,
					// so it entered the order at face value while a server with a
					// record entered it at "average less its spread", and the
					// unmeasured one won on the strength of not being measured.
					// Charged for what is not known about it, the two are at least
					// judged by the same standard.
					c.score = -spd * (1 - thinCharge(st.SpeedSeen))
				} else {
					// No speed figure at all. Every candidate that has one has
					// already sorted ahead — those scores are negative — so this
					// only orders the rest among themselves, and the delay is the
					// only thing measured about them. Scoring them all zero, as
					// this used to, threw that away: after a bulk PING with no
					// speed test anywhere, every row scored the same and the
					// order fell back to where the configs happen to sit in the
					// tab, so a server nobody has ever measured could lead a
					// server with a month of history behind it.
					c.score = stableScore(hashBody(es.Raw), es.Delay)
					if c.score <= 0 {
						c.score = unmeasuredScore
					}
				}
			}
			out = append(out, c)
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
			return out[i].score < out[j].score
		}
		// Candidates with no fresh ping used to be left in whatever order they
		// happened to be in — which is the state EVERY config is in at the first
		// connection after a restart, so history ranking did nothing at the one
		// moment there is nothing else to go on. A server measured forty times
		// last week is a better guess than the row above it. Both history modes:
		// a speed history is as good an answer here as a delay history, and only
		// the delay one used to be consulted.
		byHistory := rankStable || rankSpeedHistory
		if byHistory && out[i].stable && out[j].stable {
			return out[i].score < out[j].score
		}
		if byHistory && out[i].stable != out[j].stable {
			return out[i].stable
		}
		return false
	})
	return out
}

// autoNoteDeadConfig records that a config AUTO was connected to stopped
// carrying traffic.
//
// The health probe used to leave no trace anywhere: the row kept the "ok" from
// its last bulk ping, the measurement history never heard about the failure,
// and so the ranking went on offering the same dead server as its best
// candidate — pick it, watch it fail, switch away, come back a minute later.
// Writing the failure where a failed ping test would write it fixes both at
// once: the row drops out of the "answered last time" group until something
// re-tests it, and the history's success rate falls the way it would for any
// other failed check.
//
// Once per failover, not once per probe: the probe runs every fifteen seconds
// and folding all of that in would swamp a history built from occasional tests.
func autoNoteDeadConfig(cs ConnState) {
	autoNoteFailure(cs.ConnTab, cs.ConnRaw, "no traffic")
}

// autoNoteFailedAttempt records that AUTO tried this config and it did not come
// up — the engine never started, the tunnel never appeared, or it appeared and
// carried nothing.
//
// A failed attempt used to be written down nowhere the user could see: the
// candidate went on a cooldown inside the supervisor and its row kept the ping
// from its last bulk test. Opening the window then showed a healthy 80 ms next
// to a server the program had just refused to use — the two disagreed, and the
// row was the one being read.
func autoNoteFailedAttempt(c autoCand) {
	if c.entry == nil {
		autoNoteFailure("", c.raw, "did not connect")
		return
	}
	autoNoteFailureOn(c.entry, c.tabID, c.raw, "did not connect")
}

// autoNoteFailure marks the config identified by raw, looking the row up first.
func autoNoteFailure(tabHint, raw, why string) {
	if raw == "" {
		return
	}
	entry, tabID, _ := memEntryByRaw(tabHint, raw)
	if entry == nil {
		// The config is gone from every tab — no row to mark, but the history is
		// keyed by the config itself and still worth telling.
		recordPingStat(hashBody(raw), 0, false)
		return
	}
	autoNoteFailureOn(entry, tabID, raw, why)
}

// autoNoteFailureOn writes the failure where a failed manual test writes it:
// the row, the persisted copy of it, and the measurement history.
func autoNoteFailureOn(entry *ConfigEntry, tabID, raw, why string) {
	entry.mu.Lock()
	entry.PingStatus = StatusFailed
	entry.Delay = -1
	entry.PingErr = why
	entry.mu.Unlock()
	if store != nil {
		// The same path a failed manual test takes: it persists the row AND folds
		// the failure into the history, once.
		mirrorPingResult(tabID, entry)
	} else {
		recordPingStat(hashBody(raw), 0, false)
	}
	state.broadcast(SSEEvent{Type: "entry_update", Payload: entryPayload(entry.snap()), Tab: tabID})
}

// autoNoteLivePing puts the live probe's round trip on the connected config's
// row, so the table shows what the link is doing now rather than what a bulk
// test measured an hour ago.
//
// The ROW only — never the measurement history, which is a set of comparable
// numbers and must stay one. A list ping is a best-of-three on a test engine of
// its own over an idle tunnel; this is one request through the tunnel the user
// is working over, and the two differ by tens of milliseconds for the same
// server (autoHasFasterCandidate keeps them apart for exactly that reason, and
// takes the live figure where the live figure is the question).
//
// Mixing them would have cost more than it gave: in the "steady" modes the
// connected config's average would climb towards its loaded-link figure, so the
// switch check would find a "faster candidate" more often than it should — and
// the inflated average would follow the config after the session ended. Nothing
// is lost by leaving it out: candidate sweeps do not skip the connected config,
// so its history keeps growing from measurements taken the same way as
// everyone else's.
func autoNoteLivePing(cs ConnState, rtt time.Duration) {
	if cs.ConnRaw == "" || rtt <= 0 {
		return
	}
	// A chain's round trip crosses every server in it. Written on the row of
	// the first, it replaced that server's own figure, and the ranking then
	// read the whole chain's delay as the first server's.
	if len(cs.ChainRaws) > 1 {
		return
	}
	ms := rtt.Milliseconds()
	if ms < 1 {
		ms = 1 // a sub-millisecond link still answered; 0 means "no result"
	}
	entry, tabID, _ := memEntryByRaw(cs.ConnTab, cs.ConnRaw)
	if entry == nil {
		return
	}
	entry.mu.Lock()
	entry.PingStatus = StatusOK
	entry.Delay = ms
	entry.PingErr = ""
	idx := entry.Index
	entry.mu.Unlock()

	if store != nil {
		store.queuePingRow(tabID, idx, ms, string(StatusOK), "") // the row, not the history
	}
	state.broadcast(SSEEvent{Type: "entry_update", Payload: entryPayload(entry.snap()), Tab: tabID})
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
	publishCooldowns(cooldown)
}

// The supervisor's pauses, copied out for the panel.
//
// The map itself belongs to the supervisor goroutine and is not shared — which
// is why the panel could not see it, and why its candidate list showed a config
// at the top that AUTO would not touch for another twenty minutes. A copy taken
// where the map changes costs nothing and lets the list say so.
var autoCooldownView atomic.Value // map[string]time.Time

func publishCooldowns(cooldown map[string]time.Time) {
	cp := make(map[string]time.Time, len(cooldown))
	now := time.Now()
	for raw, until := range cooldown {
		if until.After(now) {
			cp[raw] = until
		}
	}
	autoCooldownView.Store(cp)
}

// cooldownLeft is how long a config is still paused for, or zero.
func cooldownLeft(raw string) time.Duration {
	m, _ := autoCooldownView.Load().(map[string]time.Time)
	if m == nil {
		return 0
	}
	if until, ok := m[raw]; ok {
		if d := time.Until(until); d > 0 {
			return d
		}
	}
	return 0
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
	mode := autoRankMode()
	bySpeed := strings.HasPrefix(mode, "speed")
	byHistory := mode == rankByPingHistory || mode == rankBySpeedHistory
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
	// Unknown current metric ⇒ fall back to the one that IS known rather than
	// refuse to answer.
	//
	// A speed mode with no speed measurement for the connected config used to
	// return false here, and the whole latency budget went with it: the probe
	// said 900 ms, the guard said "nothing better exists", and AUTO sat on it
	// until something else re-measured the pool. The delay comparison is a
	// coarser answer to the same question and it is available — the live probe
	// just produced it.
	if bySpeed && curSpeed <= 0 {
		if curDelay <= 0 {
			return false // neither figure known: nothing to prove an improvement against
		}
		bySpeed, byHistory = false, false
	}
	if !bySpeed && curDelay <= 0 {
		return false
	}
	// In a history mode the same margin is applied to the history score, not to
	// the last measurement: ranking by what a server has done over time and then
	// deciding whether to move by what it did once is two different questions
	// answered about the same switch, and the second one undoes the first.
	var curScore float64
	if byHistory {
		if st, ok := statsFor(hashBody(cs.ConnRaw)); ok {
			switch {
			case mode == rankByPingHistory && st.Trusted():
				curScore = st.PingAvg + jitterWeight*st.Jitter() + (1-st.OK)*unreliablePenaltyMs
			case mode == rankBySpeedHistory && st.SpeedTrusted():
				curScore = -st.SpeedScore()
			}
		}
		if curScore == 0 {
			byHistory = false // nothing to compare against; fall back to the raw figures
		}
	}
	for _, c := range autoCandidates(pool, cs.ConnRaw, cooldown) {
		es := c.entry.snap()
		if es.PingStatus != StatusOK {
			continue
		}
		// A candidate with no history of its own is judged on what it does have.
		//
		// Requiring c.stable here meant a candidate measured twice could not win,
		// however good it looked — so a slow-but-alive connection with nothing but
		// freshly-added candidates around it never moved, and the setting the user
		// asked for ("rank on history") quietly became "never switch". The
		// comparison degrades one step instead: history against history where both
		// have it, last measurement against the live figure where they do not.
		byHist := byHistory && c.stable
		switch {
		case byHist && mode == rankBySpeedHistory:
			// Both scores are negated speeds, so "enough better" is the same
			// percentage margin the last-measurement mode uses.
			if -c.score > -curScore*autoSwitchSpeedFactor() {
				return true
			}
		case byHist:
			if c.score < curScore-float64(autoSwitchToleranceMs()) {
				return true
			}
		case bySpeed:
			if c.speed > 0 && c.speed > curSpeed*autoSwitchSpeedFactor() { // enough faster to be worth the switch
				return true
			}
		default:
			if es.Delay > 0 && es.Delay < curDelay-autoSwitchToleranceMs() { // enough lower than the live latency
				return true
			}
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

// autoSweepInFlight is true while autoConnectFromPool is working through its
// candidates.
//
// It exists for one decision made far from here: a session that fails its
// traffic check is torn down, and in proxy mode a teardown normally takes the
// system proxy with it. Between two candidates that would put the machine on
// its ordinary connection with its own address for a second or two — so while
// the sweep is still going the setting is left pointing at the local port,
// where applications get a refused connection instead of a direct route out.
// The sweep clears it itself once every candidate is spent.
var autoSweepInFlight atomic.Bool

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
			// the previous (often slow) config's stale value — and put it on the
			// row too, which is where the user looks after a switch.
			autoLiveRtt.Store(rtt.Milliseconds())
			autoNoteLivePing(state.conn.snap(), rtt)
			return true
		}
	}
	return false
}

// autoCandidatesForConnect is the ranked list to actually try, with the
// cooldowns treated as advice rather than as a lock.
//
// A pause is stamped on a config that just failed and doubles each time, up to
// half an hour. That is right when one server is flapping and wrong when the
// network was down: every candidate failed, every candidate is now paused, and
// AUTO would sit doing nothing for as long as the longest pause — after the
// connection came back. "Switch now" walked the same list, so it became a
// button that did nothing at all.
//
// So when the pauses exclude everything, they are ignored for this attempt and
// the same ranked order is tried anyway. Nothing else changes: the pauses still
// keep a flapping server out of the way while there is anything else to pick.
func autoCandidatesForConnect(pool []string, excludeRaw string, cooldown map[string]time.Time) []autoCand {
	cands := autoCandidates(pool, excludeRaw, cooldown)
	if len(cands) > 0 || len(cooldown) == 0 {
		return cands
	}
	if cands = autoCandidates(pool, excludeRaw, nil); len(cands) > 0 {
		vlog("info", "auto: every candidate is cooling down — trying them anyway")
	}
	return cands
}

// autoConnectFromPool tries pool candidates in order until one connects and
// verifies. preferRaw (if set & present) is tried first; excludeRaw is skipped.
// At most autoMaxAttempts are tried per sweep — cooled-down failures fall out
// so subsequent sweeps advance through the rest of the pool. Caller MUST hold
// cm.actionMu. Returns true on success.
func autoConnectFromPool(pool []string, preferRaw, excludeRaw string, mode ConnMode, reason string, cooldown map[string]time.Time, backoff map[string]time.Duration) bool {
	const autoMaxAttempts = 8
	// While this runs, a failed candidate's teardown leaves the system proxy
	// pointed at the local port (see autoSweepInFlight); when it ends without a
	// connection, there is nothing left to point at and it comes off.
	autoSweepInFlight.Store(true)
	defer func() {
		autoSweepInFlight.Store(false)
		if state.conn.snap().Status != ConnConnected {
			unsetSystemProxy()
		}
	}()
	cands := autoCandidatesForConnect(pool, excludeRaw, cooldown)
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
		// The user turned AUTO off while we were working through the list. Each
		// attempt is a full connect, so without this check switching off could be
		// followed by up to eight more connections — the switch kept flipping
		// configs for the better part of a minute after being turned off, which
		// reads as the toggle not working.
		if !autoStillOn() {
			vlog("info", "auto: switched off mid-attempt — stopping")
			return false
		}
		name := autoLabel(c.entry)
		broadcastAuto("switching", name, c.raw, reason)
		vlog("info", "auto: trying %s (%s)", name, mode)
		clearFailureBlame()
		if autoTryConnect(c, mode) {
			// The cooldown is over — it connected. The BACKOFF is not cleared here
			// on purpose: "connected" means one probe passed a second after the
			// tunnel came up, and a server that comes up and dies twenty seconds
			// later used to have its escalation wiped on every attempt, so its
			// pause never grew past the first thirty seconds and AUTO returned to
			// it every couple of minutes for as long as it kept half-working. The
			// supervisor clears it once the connection has actually lasted.
			delete(cooldown, c.raw)
			autoManaged.Store(true) // supervisor owns this connection
			broadcastAuto("connected", name, c.raw, reason)
			vlog("info", "auto: connected → %s (%s)", name, reason)
			return true
		}
		autoBumpCooldown(c.raw, cooldown, backoff)
		// Say so on the row as well as in the cooldown map, which only the
		// supervisor can see. Not when AUTO has just been switched off, though:
		// an attempt cut short by the user says nothing about the config, and a
		// row marked failed there is a lie the ranking would then act on.
		// Nor when the failure was ours: a session that came up and carried
		// nothing while our own firewall block was standing says nothing about
		// the server, and the ranking must not act on it either.
		if autoStillOn() && !failureWasOurs() {
			autoNoteFailedAttempt(c)
		}
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
	publishCooldowns(cooldown)
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

// AutoSweepProgress is how far AUTO's test of its pool has got. A big pool is
// tested before the first connection, and without a count the panel said
// "waiting" for minutes with nothing to show that it was getting anywhere.
//
// Seq orders the updates. The events that carry them are delivered each on a
// goroutine of its own, so they can arrive out of order — and the last count
// of a sweep, reported from a test goroutine a moment before the sweep's end,
// could land after "finished" and leave the panel showing a full bar that
// never went away. The panel keeps the highest Seq it has seen and drops
// anything older.
type AutoSweepProgress struct {
	Running bool   `json:"running"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Speed   bool   `json:"speed"`
	Seq     uint64 `json:"seq"`
}

var (
	autoSweepMuP sync.Mutex
	autoSweepNow AutoSweepProgress
	autoSweepSeq uint64
)

// AutoSweep is the current state, for a panel opened in the middle of a sweep;
// every change after that arrives as an auto_sweep event.
func AutoSweep() AutoSweepProgress {
	autoSweepMuP.Lock()
	defer autoSweepMuP.Unlock()
	return autoSweepNow
}

func setAutoSweep(p AutoSweepProgress) {
	autoSweepMuP.Lock()
	autoSweepSeq++
	p.Seq = autoSweepSeq
	autoSweepNow = p
	autoSweepMuP.Unlock()
	state.broadcast(SSEEvent{Type: "auto_sweep", Payload: p})
}

// testTabs is the shared background tester: it pings (and, when withSpeed,
// speed-tests) every config across the given tabs, deduped and respecting each
// tab's exclude filter. Results broadcast as entry_update; progress, when the
// caller asks for it, goes to progress(done, total) — once with done=0 when the
// queue is known, then after every config.
// It yields to any user-initiated bulk test and never overlaps another
// background sweep (autoPingRunning guard). keepGoing, when non-nil, is checked
// before each config and stops the sweep early when it returns false — AUTO uses
// it to bail if auto-connect is switched off mid-sweep; the per-tab
// "test after auto-refresh" passes nil (always run to completion).
// It reports whether it ran at all: a sweep that found another one in progress,
// or nothing to test, returns false.
func testTabs(tabIDs []string, withSpeed bool, keepGoing func() bool, progress func(done, total int)) bool {
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
		return false
	}
	if !atomic.CompareAndSwapInt32(&autoPingRunning, 0, 1) {
		return false
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
		return false
	}
	if progress != nil {
		progress(0, len(list))
	}

	// The setting that matches the work actually being done. A speed sweep holds
	// a full download for several seconds per config, which is why its default is
	// 5 and ping's is 10 — running the speed pass at the ping width made AUTO
	// ignore the user's speed setting entirely and measure every config while the
	// link was saturated by nine others.
	conc := currentPingConcurrency()
	if withSpeed {
		conc = currentSpeedConcurrency()
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	// Counted, not assumed: the loop below breaks on a cancel, on a manual bulk
	// taking over, and on AUTO being switched off mid-sweep, and reporting the
	// length of the queue in those cases claims work that never happened.
	var tested int32
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
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entryPayload(ent.snap()), Tab: tab})
			if withSpeed {
				// runSpeedForEntry does ping→speed in one engine session.
				runSpeedForEntry(ent, tab, nil)
				mirrorSpeedResult(tab, ent)
			} else {
				runPingForEntry(ent, nil)
				mirrorPingResult(tab, ent)
			}
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entryPayload(ent.snap()), Tab: tab})
			n := atomic.AddInt32(&tested, 1)
			if progress != nil {
				progress(int(n), len(list))
			}
		}(item.e, item.tab)
	}
	wg.Wait()
	what := "ping"
	if withSpeed {
		what = "ping+speed"
	}
	if n := int(atomic.LoadInt32(&tested)); n < len(list) {
		vlog("info", "refreshed %s for %d of %d config(s) — stopped early", what, n, len(list))
	} else {
		vlog("info", "refreshed %s for %d config(s)", what, n)
	}
	return true
}

// autoTestTabs is the AUTO-supervisor entry point: same as testTabs but bails if
// auto-connect is switched off mid-sweep.
func autoTestTabs(tabIDs []string, withSpeed bool) {
	// done arrives from goroutines finishing in any order; under the lock a
	// count older than the one already shown is dropped, so it only ever moves
	// forward on screen.
	var mu sync.Mutex
	last, total := 0, 0
	ran := testTabs(tabIDs, withSpeed, func() bool {
		settingsMu.RLock()
		on := appSettings.AutoConnect
		settingsMu.RUnlock()
		return on
	}, func(done, n int) {
		mu.Lock()
		defer mu.Unlock()
		if done != 0 && done < last {
			return
		}
		last, total = done, n
		setAutoSweep(AutoSweepProgress{Running: true, Done: done, Total: n, Speed: withSpeed})
	})
	if ran {
		mu.Lock()
		p := AutoSweepProgress{Running: false, Done: last, Total: total, Speed: withSpeed}
		mu.Unlock()
		setAutoSweep(p)
	}
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
		testTabs([]string{tabID}, mode == "speed", nil, nil)
	}
}

// autoPingTabs is the ping-only entry point (connect ordering). Kept as a thin
// wrapper so existing call sites read clearly.
//
// It does NOT skip the connected config, and an older comment here said it did:
// every test runs its own engine on its own port, so measuring the live one
// disturbs nothing and the list then shows a fresh figure for it like every
// other row.
func autoPingTabs(tabIDs []string) { autoTestTabs(tabIDs, false) }

// autoPingAfterRefresh re-pings a freshly auto-refreshed tab, but only when
// auto-connect is on and the tab is part of the candidate pool. Called right
// after a pool tab's auto-refresh completes (the refresh wipes ping data).
func autoPingAfterRefresh(tabID string) {
	settingsMu.RLock()
	on := appSettings.AutoConnect && appSettings.AutoPingRefresh
	settingsMu.RUnlock()
	// Either speed mode needs fresh Mbps to rank by, so the re-test runs the
	// full ping→speed pass rather than ping alone.
	withSpeed := strings.HasPrefix(autoRankMode(), "speed")
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
	// The watchdog's own counters, kept apart from failCount so switching AUTO on
	// and off does not carry evidence from one regime into the other.
	idleFails := 0
	// The system-proxy guard rides this loop because it already runs whether
	// AUTO is on or off, and its own schedule is far slower than the tick.
	var proxyGuard proxyGuardState
	var lastIdleProbe time.Time
	// What the byte counters have already shown about the connection, kept
	// apart for the same reason the failure counters are: the two regimes must
	// not hand each other evidence.
	var live, idleLive livenessWatch
	// When the last wait began, for noticing that the machine slept through it.
	var lastTick time.Time
	var lastSwitch, lastProbe, nextRetry, lastTUNSwitch, lastSlowRetest time.Time
	cooldown := map[string]time.Time{}
	backoff := map[string]time.Duration{}
	// Read again on every pass below, like the threshold and the latency
	// budget: read once here, a changed "Minimum time on a server" waited for
	// the next start of the program.
	minDwell := autoMinDwell()
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
		// Measured across the WAIT, not across the whole round. The round also
		// contains a health probe, and a probe against a server that has
		// stopped answering takes as long as its timeouts allow — eight to ten
		// seconds. Measured end to end, that read as "the machine was away for
		// 10s", and the counters were wiped every time: the log filled with
		// "health probe failed (1/2)" over and over, the count never reached
		// two, and the dead server was never swapped out. The one thing the
		// supervisor exists for was undone by its own slowness.
		lastTick = time.Now()
		select {
		case <-time.After(2 * time.Second):
		case <-autoWake: // "Switch now" or similar — react without waiting
		}

		// A shut laptop opened an hour later arrives here with every timer
		// fired at once and a measurement an hour old. Nothing from before the
		// gap describes the connection now, and treating it as evidence cost
		// the user a working tunnel: two probes that "failed" while the machine
		// was away, and a switch to another server on waking.
		now := time.Now()
		wallGap := now.Round(0).Sub(lastTick.Round(0))
		if clockJumped(wallGap, now.Sub(lastTick), 2*time.Second) {
			failCount, idleFails = 0, 0
			lastProbe, lastIdleProbe = time.Time{}, time.Time{}
			live.reset()
			idleLive.reset()
			vlog("info", "the machine was away for %s — health counters start again", wallGap.Round(time.Second))
			continue
		}

		settingsMu.RLock()
		on := appSettings.AutoConnect
		// Auto-switch (failover while connected) is no longer a separate toggle:
		// it's an intrinsic part of auto-connect, always on when the feature is on.
		sw := on
		threshold := appSettings.AutoFailThreshold
		maxLat := appSettings.AutoMaxLatencyMs
		// Tried first only when the user asked for it (AutoPreferLast); by
		// default the first connection goes to the top of the ranking.
		lastRaw := ""
		// Not after a chain: its last raw is the exit server, and connecting that
		// one alone put a single hop in place of the route that was built —
		// the substitution auto-connect refuses everywhere else. The ranking
		// decides instead.
		if appSettings.AutoPreferLast && len(appSettings.LastConnectedChain) <= 1 {
			lastRaw = appSettings.LastConnectedRaw
		}
		settingsMu.RUnlock()
		minDwell = autoMinDwell()
		guardSystemProxy(&proxyGuard)

		if !on {
			failCount = 0
			autoForce.Store(false) // don't carry a stale force into re-enable
			// AUTO being off does not make a dead tunnel less dead. Nothing else
			// in the app looks: the engine process can be perfectly healthy while
			// the server behind it has stopped answering, and the connection panel
			// went on saying "connected" while every request left through the
			// user's real address. The probe is the same one AUTO trusts enough to
			// abandon a config over, and it runs on the same interval; the only
			// difference is what happens at the end, since there is no pool to
			// switch to.
			watchIdleConnection(cm, &idleFails, &lastIdleProbe, &idleLive)
			continue
		}
		// Set to TUN with no rights to create a tunnel: auto-connect cannot do its
		// job, says so once, and the connection is watched as with AUTO off.
		if autoNeedsAdmin(autoEffectiveMode()) {
			failCount = 0
			autoForce.Store(false)
			autoNoteNeedsAdmin()
			watchIdleConnection(cm, &idleFails, &lastIdleProbe, &idleLive)
			continue
		}
		// The mode was changed to proxy (or the rights arrived): take the notice
		// off the panel rather than leave it until the next connection.
		if autoAdminNoted.Swap(false) {
			broadcastAuto("idle", "", "", "")
		}
		// Back under AUTO's own supervision: forget what the off-mode watchdog saw.
		idleFails, lastIdleProbe = 0, time.Time{}
		idleLive.reset()
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
			// "Switch now" with nothing in the pool is spent here, not kept: left
			// armed, it fired whenever the pool next had configs — minutes or
			// hours later — and took a connection the user had made by hand.
			if forced && !autoPoolHasEntries(pool) {
				autoForce.Store(false)
			}
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
					// The teardown happens inside the connect below, so the reason
					// has to be left here or the log records the default and every
					// "Switch now" reads as an ordinary disconnect.
					noteSessionEndReason("switched by hand")
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
				vlog("info", "auto: %s is no longer among the candidates (its tab was deselected, or its exclude filter hides it) — switching", cs.EntryName)
				noteSessionEndReason("auto: no longer among the candidates")
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
						autoNoteLivePing(cs, rtt)
						broadcastAuto("health", cs.EntryName, cs.ConnRaw, "")
					} else {
						autoLiveRtt.Store(0)
					}
				}
				continue
			}
			// What the traffic already says, before spending a probe on the
			// question. Bytes coming back are proof; bytes going out with
			// nothing coming back are the failure itself, caught sooner than
			// the interval would have caught it.
			up, down := cm.bytes()
			hint := live.look(cs.ConnRaw, up, down, time.Now())
			// Once a probe has failed, the counters stop being evidence.
			//
			// They were, and it cost the feature its point. A failed probe is
			// not a silent one: it opens a connection, and a server that accepts
			// and then answers nothing useful — a REALITY handshake, a tiny
			// response, an error page — still sends bytes back through the
			// counting relay. The next tick read those bytes as "traffic is
			// flowing", cleared the failure count and put the next probe two
			// minutes away: the log repeated "health probe failed (1/2)", the
			// count never reached two, and a dead server was never left.
			//
			// Bytes are excellent evidence that a link works; they are no
			// evidence at all once something has said it does not. From the
			// first failure until a probe succeeds, only probes count.
			if failCount > 0 {
				hint = livenessUnknown
			}
			switch hint {
			case livenessAlive:
				failCount = 0
				if !probeNow && time.Since(lastProbe) < livenessQuietProbe {
					continue
				}
			case livenessSuspect:
				if !probeNow && time.Since(lastProbe) < livenessSuspectRecheck {
					continue
				}
			default:
				// Same reasoning as the idle watchdog: after a failed probe,
				// look again in seconds rather than waiting out the interval.
				if !probeNow && time.Since(lastProbe) < probeSpacing(failCount) {
					continue
				}
			}
			lastProbe = time.Now()
			alive, rtt := probeLiveTunnel(cm)
			if alive {
				autoLiveRtt.Store(rtt.Milliseconds())
				autoNoteLivePing(cs, rtt)
			} else {
				autoLiveRtt.Store(0)
			}
			// Not for a chain. Two servers in a row are slower than one, and the
			// only thing auto-connect does with a chain is end it: a working chain
			// over the budget was closed as "stopped responding" whenever any
			// single server in the pool was faster. A chain is left only when it
			// stops answering.
			overBudget := alive && maxLat > 0 && rtt > time.Duration(maxLat)*time.Millisecond && len(cs.ChainRaws) <= 1
			if alive && !overBudget {
				failCount = 0
				live.answered()
				// It has held for longer than the minimum stay, so whatever was
				// wrong with it before is over: forget the escalating pause. This
				// is the only place it is forgiven — connecting is not enough,
				// staying up is.
				if time.Since(lastSwitch) > minDwell {
					delete(backoff, cs.ConnRaw)
				}
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
				// Past the threshold but inside the minimum stay, the count keeps
				// climbing (3/2, 4/2 …) with no switch; say why, or it reads as
				// AUTO ignoring its own threshold.
				hold := ""
				if failCount >= threshold && time.Since(lastSwitch) < minDwell {
					hold = fmt.Sprintf(" — staying for the minimum time on a server, %ds left", int((minDwell-time.Since(lastSwitch)).Seconds())+1)
				}
				vlog("warning", "auto: %s too slow (%dms > %dms) (%d/%d)%s", cs.EntryName, rtt.Milliseconds(), maxLat, failCount, threshold, hold)
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
						go autoTestTabs(pool, rankModeUsesSpeed())
					}
					continue
				}
			}
			// A chain is not replaced, it is ended.
			//
			// AUTO picks one config out of a pool; a chain is a route the user
			// built by hand — through this country, then that one — and there is
			// nothing in the pool to rebuild it from. Switching would hand back a
			// single hop in its place: still "connected", quietly weaker than what
			// was asked for, and easy not to notice. So the session ends and says
			// why, the same ending the watch gives when there is nowhere to move.
			if len(cs.ChainRaws) > 1 {
				failCount = 0
				blocked := trafficHeldNow()
				desktopNotify("Vair", notifyChainGone(cs.EntryName, blocked))
				why := "the chain stopped responding — connection closed (auto-connect does not rebuild a chain)"
				if blocked && blockWillLapse() {
					why = "the chain stopped responding — traffic stays blocked for 20 s (kill-switch), then the normal connection returns"
				} else if blocked {
					why = "the chain stopped responding — traffic stays blocked (kill-switch); disconnect to release it"
				}
				noteSessionEndReason("auto: " + reason)
				connectionLost(why, "")
				// And it stays ended: without this the next tick would find an idle
				// connection with the intent still armed and connect a single config
				// anyway — the same substitution by a longer route.
				autoWant.Store(false)
				vlog("warning", "auto: %s was a chain — ended rather than replaced with one hop", cs.EntryName)
				continue
			}
			// A faster candidate exists → switch to the fastest available below.
			// Pick the failover target by *fresh* ping ranking. If the pool has no
			// ping data (e.g. an auto-refresh just reset entries to "pending"),
			// autoCandidates falls back to tab order — which is the "switched to a
			// random config, not the fastest" the user reported. Re-ping first so
			// the sweep ranks by real delay, exactly like the manual "Switch now".
			// Safe to run while connected: each test spins up its own engine on
			// its own port, so it neither touches nor is touched by the live one.
			// Ping only, even in a speed mode: something has just gone down and
			// the pool has no order at all. A full speed pass would leave the
			// machine off the air for as long as it takes to measure every
			// candidate, and the slow-but-best branch above already kicks a
			// background sweep that measures what the mode ranks on.
			if !autoPoolHasPingData(pool) && atomic.LoadInt32(&autoPingRunning) == 0 {
				autoPingTabs(pool)
			}
			if !cm.actionMu.TryLock() {
				continue // user action in progress — yield
			}
			// Cool down the failing config so we don't immediately flap back.
			autoBumpCooldown(cs.ConnRaw, cooldown, backoff)
			// A dead link is a fact about the config; a slow one is not. Only the
			// first is written down — an over-budget probe means the server works
			// and something faster exists, which is not a failure to remember.
			if !alive {
				autoNoteDeadConfig(cs)
			}
			// The session log's row for the connection we are about to replace:
			// "auto: probe failed" / "auto: too slow 243ms", the same words the
			// panel and the log already use for the switch.
			noteSessionEndReason("auto: " + reason)
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
			// First connect with no test data yet → test the whole pool now
			// (blocking) so the order is a real one. A config-list refresh
			// resets entries to "pending", which re-triggers this next pass.
			//
			// What is measured follows the ranking mode, as it does everywhere
			// else. After a restart in a speed mode this pinged and stopped, so
			// the first connection of the session — the one the mode exists to
			// decide — was made without a single speed measurement behind it.
			// Switching AUTO on has always run the full pass; a restart now does
			// the same. The failover path above deliberately does not: getting
			// back online beats measuring throughput first.
			if !autoPoolHasPingData(pool) {
				autoTestTabs(pool, rankModeUsesSpeed())
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
