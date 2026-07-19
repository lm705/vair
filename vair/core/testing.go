package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// round2 trims a MB/s value to 2 decimals — the UI only shows 2, so storing more
// is just noise (a float64 is 8 bytes either way, so this is for tidiness, not
// size).
func round2(f float64) float64 { return math.Round(f*100) / 100 }

// ─────────────────────────── ping / speed ────────────────────────

// measurePing does a warm-up request then pingRounds timed GETs, returning the
// best (lowest) round-trip in ms. cancel (nil for single-config / auto callers)
// lets a bulk RELOAD abort promptly: it's checked between requests, AND attached
// to each request's context so even a request blocked mid-flight is torn down
// the instant cancel fires — without waiting out the warm-up / per-round timeout.
func measurePing(tr *http.Transport, cancel <-chan struct{}) (int64, error) {
	url := currentPingURL()
	pt := currentPingTimeout()
	// cancelled reports whether the bulk run was cancelled (nil cancel = never).
	cancelled := func() bool {
		if cancel == nil {
			return false
		}
		select {
		case <-cancel:
			return true
		default:
			return false
		}
	}
	// reqCtx returns a request whose context is cancelled when either the given
	// timeout elapses or the bulk `cancel` channel closes. The watcher goroutine
	// exits when the request finishes (done closed) so it never leaks.
	reqCtx := func(timeout time.Duration) (*http.Request, context.CancelFunc, chan struct{}) {
		ctx, cf := context.WithTimeout(context.Background(), timeout)
		done := make(chan struct{})
		if cancel != nil {
			go func() {
				select {
				case <-cancel:
					cf() // bulk cancelled → abort the in-flight request now
				case <-done:
				}
			}()
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		return req, cf, done
	}

	if cancelled() {
		return -1, errPingCancelled
	}
	wc := &http.Client{Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	wreq, wcf, wdone := reqCtx(currentWarmupTimeout())
	w, err := wc.Do(wreq)
	close(wdone)
	wcf()
	if err != nil {
		if cancelled() {
			return -1, errPingCancelled
		}
		return -1, fmt.Errorf("warmup: %w", err)
	}
	io.Copy(io.Discard, w.Body)
	w.Body.Close()
	mc := &http.Client{Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var best int64 = -1
	var lastErr error
	for i := 0; i < pingRounds; i++ {
		if cancelled() {
			return -1, errPingCancelled
		}
		start := time.Now()
		mreq, mcf, mdone := reqCtx(pt)
		resp, e := mc.Do(mreq)
		if e != nil {
			close(mdone)
			mcf()
			if cancelled() {
				return -1, errPingCancelled
			}
			lastErr = e
			if best > 0 {
				break
			}
			continue
		}
		ms := time.Since(start).Milliseconds()
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		close(mdone)
		mcf()
		if best < 0 || ms < best {
			best = ms
		}
	}
	if best < 0 {
		if lastErr != nil {
			return -1, lastErr
		}
		return -1, fmt.Errorf("all measurements failed")
	}
	return best, nil
}

// minSpeedBytes is the floor for a "real" measurement. SS configs (and
// occasionally others) can finish the request with a 200 OK that carries
// only a few KB before the upstream closes — typically when the SS server
// rejects the relay after the cipher handshake. Dividing those few KB by a
// near-zero elapsed-time produces fantasy mbps numbers (200+ MB/s on a
// 10 Mbps line). We require at least this much body data to call it valid.
const minSpeedBytes int64 = 256 * 1024

// withCacheBuster appends a unique query param so a CDN / proxy / ISP cache
// along the path can't answer with a cached or short-circuited body — the
// speed test must measure a real transfer. Unknown query params are ignored
// by the bundled presets (Cloudflare __down, cachefly, ovh), and this is the
// fix for the "response too fast — upstream cache or proxy short-circuit"
// rejections that used to drop otherwise-fine proxies.
func withCacheBuster(u string) string {
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "vcb=" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// err429 marks a measurement that ended because the upstream answered with
// HTTP 429. The outer measureSpeed checks for it via errors.Is to decide
// whether to fall back to a secondary URL.
var err429 = errors.New("HTTP 429")

// errSpeedCancelled is returned by measureSpeedOne when the bulk run is
// cancelled mid-download (e.g. RELOAD). Callers treat it as "stop", not a real
// failure — the row's result is left as-is rather than marked failed.
var errSpeedCancelled = errors.New("speed test cancelled")

// errPingCancelled is the ping-side equivalent: measurePing returns it when the
// bulk run is cancelled, so the runner leaves the row pending instead of failed.
var errPingCancelled = errors.New("ping cancelled")

// measureSpeed runs one speed test, with an optional fallback to a second
// URL ONLY when the primary returns HTTP 429. Each attempt is its own
// bounded request — same http.Client.Timeout and same defer-close — so a
// fallback can never extend the test indefinitely or leak the connection
// (the failure mode that caused tests to hang on "connecting…" in a
// previous attempt at this feature). cancel (nil for single-config tests)
// aborts the download promptly when the bulk run is cancelled.
func measureSpeed(tr *http.Transport, onProgress func(float64), cancel <-chan struct{}) (float64, error) {
	primary := currentSpeedURL()
	mbps, err := measureSpeedOne(tr, primary, onProgress, cancel)
	if err == nil {
		return mbps, nil
	}
	// Only retry on a clean 429 from the primary. Connect errors, slow
	// responses, cancellation, etc. stay as the user-visible/terminal result.
	if !errors.Is(err, err429) {
		return mbps, err
	}
	fb := currentSpeedFallbackURL()
	if fb == "" || fb == primary {
		return mbps, err
	}
	return measureSpeedOne(tr, fb, onProgress, cancel)
}

// measureSpeedOne is a single attempt: one HTTP request, bounded by
// Client.Timeout, body always closed via defer. Returned errors are
// terminal — there's no inner retry that could spin forever.
func measureSpeedOne(tr *http.Transport, urlStr string, onProgress func(float64), cancel <-chan struct{}) (float64, error) {
	sd := currentSpeedDuration()
	// Hard wall-clock cap on the whole download. Client.Timeout covers the
	// full request including body read, so a hung upstream can't pin the
	// goroutine past sd+5s — even on SS configs where xray's CONNECT
	// succeeds but the relayed stream stalls indefinitely.
	sc := &http.Client{Transport: tr, Timeout: sd + 5*time.Second}
	req, err := http.NewRequest("GET", withCacheBuster(urlStr), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", speedUserAgent)
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := sc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		// Wrap the sentinel so measureSpeed can detect 429 unambiguously
		// without string-matching on the error message.
		return 0, fmt.Errorf("%w", err429)
	}
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 64*1024)
	var total int64
	start := time.Now()
	deadline := start.Add(sd)
	lastReport := start
	for time.Now().Before(deadline) {
		// Abort mid-download the instant the bulk run is cancelled (e.g. RELOAD).
		// Without this the current config keeps reading its full ~4s window even
		// though the user already asked to stop — the source of the "RELOAD waits
		// for the in-flight speed test" lag. Checked each 64KB chunk, so it bails
		// within one read.
		if cancel != nil {
			select {
			case <-cancel:
				return 0, errSpeedCancelled
			default:
			}
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			total += int64(n)
		}
		now := time.Now()
		if onProgress != nil && now.Sub(lastReport) >= 400*time.Millisecond && total > 0 {
			onProgress(round2(float64(total) / now.Sub(start).Seconds() / 1024 / 1024))
			lastReport = now
		}
		if rerr != nil {
			break
		}
	}
	elapsed := time.Since(start)
	if total == 0 {
		return 0, fmt.Errorf("no data received")
	}
	// Only one floor now: too few bytes to measure. A proxy that relays
	// just a few KB before EOF is genuinely broken (the classic SS
	// "accept CONNECT, reset the stream" failure). The old "response too
	// fast" floor was dropped — with the cache-buster above defeating
	// cached/short-circuit replies and a large default file filling the
	// window, a short-but->=256KB sample now means a real (fast) proxy,
	// not a fake one, so we report it instead of rejecting it.
	if total < minSpeedBytes {
		return 0, fmt.Errorf("tiny response (%d B) — proxy relay closed early", total)
	}
	// Guard the divisor: a >=256KB burst can still arrive in well under a
	// millisecond over a fast local hop. Clamp so the ratio stays finite.
	secs := elapsed.Seconds()
	if secs < 0.001 {
		secs = 0.001
	}
	return round2(float64(total) / secs / 1024 / 1024), nil
}

// ─────────────────────────── entry runners ───────────────────────

// runPingForEntry pings one config. cancel (nil for single-config / auto
// callers) lets a bulk RELOAD abort the ping promptly — a cancelled ping leaves
// the row pending rather than marking it failed.
func runPingForEntry(entry *ConfigEntry, cancel <-chan struct{}) {
	n, err := parseNode(entry.Raw)
	if err != nil {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = err.Error()
		entry.mu.Unlock()
		return
	}
	entry.mu.Lock()
	entry.Protocol = string(n.Kind)
	entry.mu.Unlock()
	if reason := nodeUnsupportedReason(n); reason != "" {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = reason
		entry.mu.Unlock()
		return
	}
	ttl := startupTimeout + currentWarmupTimeout() + currentPingTimeout()*time.Duration(pingRounds) + 3*time.Second
	if err = withEngine(n, ttl, func(_ int, tr *http.Transport) error {
		delay, e := measurePing(tr, cancel)
		entry.mu.Lock()
		defer entry.mu.Unlock()
		if errors.Is(e, errPingCancelled) {
			// Cancelled (RELOAD). Leave the row pending — the reload replaces it.
			entry.PingStatus = StatusPending
			entry.Delay = -1
			entry.PingErr = ""
		} else if e != nil || delay < 0 {
			entry.PingStatus = StatusFailed
			entry.Delay = -1
			entry.PingErr = cleanPingErr(e)
		} else {
			entry.PingStatus = StatusOK
			entry.Delay = delay
			entry.PingErr = ""
		}
		return nil
	}); err != nil {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = shortErr(err.Error())
		entry.mu.Unlock()
	}
	entry.mu.Lock()
	ps, delay, perr, nm := entry.PingStatus, entry.Delay, entry.PingErr, entry.Name
	entry.mu.Unlock()
	if ps == StatusOK {
		tlog("ping ok: %s — %dms", nm, delay)
	} else {
		tlog("ping failed: %s — %s", nm, perr)
	}
}

// runSpeedForEntry always does ping→speed in a single xray session.
// Per-row ⬇ speed button should re-measure ping even if already tested,
// because conditions may have changed and speed result is meaningless without fresh ping.
// cancel (nil for single-config / auto callers) lets a bulk RELOAD abort the
// download promptly instead of waiting out the full speed window.
func runSpeedForEntry(entry *ConfigEntry, tabID string, cancel <-chan struct{}) {
	n, err := parseNode(entry.Raw)
	if err != nil {
		entry.mu.Lock()
		entry.SpeedStatus = StatusFailed
		entry.SpeedErr = err.Error()
		entry.mu.Unlock()
		return
	}
	entry.mu.Lock()
	entry.Protocol = string(n.Kind)
	entry.mu.Unlock()
	if reason := nodeUnsupportedReason(n); reason != "" {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = reason
		entry.SpeedStatus = StatusFailed
		entry.SpeedErr = reason
		entry.SpeedLive = 0
		entry.mu.Unlock()
		return
	}
	ttl := startupTimeout + currentWarmupTimeout() + currentPingTimeout()*time.Duration(pingRounds) + currentSpeedDuration() + 10*time.Second
	if err = withEngine(n, ttl, func(_ int, tr *http.Transport) error {
		// Always re-ping — gives fresh delay AND warms the tunnel for speed test
		delay, pingErr := measurePing(tr, cancel)
		entry.mu.Lock()
		if errors.Is(pingErr, errPingCancelled) {
			// Cancelled during the pre-speed ping (RELOAD). Leave the row pending.
			entry.PingStatus = StatusPending
			entry.Delay = -1
			entry.PingErr = ""
			entry.SpeedStatus = StatusPending
			entry.SpeedErr = ""
			entry.SpeedLive = 0
			entry.mu.Unlock()
			return nil
		}
		if pingErr != nil || delay < 0 {
			entry.PingStatus = StatusFailed
			entry.Delay = -1
			entry.PingErr = cleanPingErr(pingErr)
			entry.SpeedStatus = StatusSkipped
			entry.SpeedErr = "ping failed"
			entry.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
			return nil
		}
		entry.PingStatus = StatusOK
		entry.Delay = delay
		entry.PingErr = ""
		entry.SpeedStatus = StatusTestingSpeed
		entry.SpeedLive = 0
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
		mbps, e := measureSpeed(tr, func(live float64) {
			entry.mu.Lock()
			entry.SpeedLive = live
			entry.mu.Unlock()
			// Lossy: a later live callback (every ~250ms) supersedes this
			// one. Dropping under buffer pressure is harmless; the FINAL
			// terminal update below is reliable.
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID, Lossy: true})
		}, cancel)
		entry.mu.Lock()
		defer entry.mu.Unlock()
		entry.SpeedLive = 0
		if errors.Is(e, errSpeedCancelled) {
			// Cancelled mid-download (RELOAD). Don't mark the row failed — the
			// reload is about to replace this entry anyway. Leave it as-is.
			entry.SpeedStatus = StatusPending
			entry.SpeedErr = ""
		} else if e != nil {
			entry.SpeedStatus = StatusFailed
			entry.SpeedMBps = 0
			entry.SpeedErr = shortErr(e.Error())
		} else {
			entry.SpeedStatus = StatusOK
			entry.SpeedMBps = mbps
			entry.SpeedErr = ""
		}
		return nil
	}); err != nil {
		entry.mu.Lock()
		// withXray failed (e.g. xray exited with "exit status N" before
		// measurePing could even run). The bulk caller sets PingStatus=
		// TestingPing right before invoking us — so without this branch
		// the row sits on the blinking "ping" pill forever even though
		// the test is finished. Mirror the same reset for the speed side.
		if entry.PingStatus == StatusTestingPing {
			entry.PingStatus = StatusFailed
			entry.Delay = -1
			entry.PingErr = shortErr(err.Error())
		}
		entry.SpeedStatus = StatusFailed
		entry.SpeedMBps = 0
		entry.SpeedLive = 0
		entry.SpeedErr = shortErr(err.Error())
		entry.mu.Unlock()
	}
	logTestResult(entry)
}

// logTestResult emits one [test] line summarising the entry's current ping +
// speed outcome (gated by the LogTests setting, via tlog).
func logTestResult(entry *ConfigEntry) {
	entry.mu.Lock()
	ps, delay, perr := entry.PingStatus, entry.Delay, entry.PingErr
	ss, mbps, serr, nm := entry.SpeedStatus, entry.SpeedMBps, entry.SpeedErr, entry.Name
	entry.mu.Unlock()
	var b strings.Builder
	if ps == StatusOK {
		fmt.Fprintf(&b, "ping %dms", delay)
	} else {
		fmt.Fprintf(&b, "ping failed (%s)", perr)
	}
	switch ss {
	case StatusOK:
		fmt.Fprintf(&b, ", speed %.2f MB/s", mbps)
	case StatusSkipped:
		fmt.Fprintf(&b, ", speed skipped")
	case StatusFailed:
		fmt.Fprintf(&b, ", speed failed (%s)", serr)
	}
	tlog("%s — %s", nm, b.String())
}

func cleanPingErr(e error) string {
	if e == nil {
		return "timeout"
	}
	s := e.Error()
	if strings.Contains(s, "context deadline") || strings.Contains(s, "timeout") {
		return "timeout"
	}
	return shortErr(s)
}

// ─────────────────────────── bulk runners ────────────────────────

var (
	pingCancelCh  chan struct{}
	speedCancelCh chan struct{}
	testMu        sync.Mutex
	testingTab    string // which tab is being tested
)

// manualTests tracks the cancel channels of in-flight per-config (manual) ping
// /speed tests, keyed by the tab they run on. Unlike the bulk runs (one global
// run at a time), several manual tests can run at once and on different tabs, so
// each gets its own channel registered here. RELOAD on a tab cancels exactly the
// manual tests on THAT tab (see cancelTestsOnTab) without touching a bulk run or
// manual tests elsewhere. Guarded by manualMu.
var (
	manualMu    sync.Mutex
	manualTests = map[string]map[chan struct{}]struct{}{}
)

func registerManualTest(tabID string, ch chan struct{}) {
	manualMu.Lock()
	if manualTests[tabID] == nil {
		manualTests[tabID] = map[chan struct{}]struct{}{}
	}
	manualTests[tabID][ch] = struct{}{}
	manualMu.Unlock()
}

func unregisterManualTest(tabID string, ch chan struct{}) {
	manualMu.Lock()
	if m := manualTests[tabID]; m != nil {
		delete(m, ch)
		if len(m) == 0 {
			delete(manualTests, tabID)
		}
	}
	manualMu.Unlock()
}

// cancelManualTestsOnTab closes every registered manual-test cancel channel for
// the given tab. Safe to call when none exist. Returns true if any were closed.
func cancelManualTestsOnTab(tabID string) bool {
	manualMu.Lock()
	defer manualMu.Unlock()
	m := manualTests[tabID]
	if m == nil {
		return false
	}
	cancelled := false
	for ch := range m {
		select {
		case <-ch:
		default:
			close(ch)
			cancelled = true
		}
	}
	return cancelled
}

func cancelPingAll() {
	testMu.Lock()
	if pingCancelCh != nil {
		select {
		case <-pingCancelCh:
		default:
			close(pingCancelCh)
		}
	}
	testMu.Unlock()
}

func cancelSpeedAll() {
	testMu.Lock()
	if speedCancelCh != nil {
		select {
		case <-speedCancelCh:
		default:
			close(speedCancelCh)
		}
	}
	testMu.Unlock()
}

func isTestCancelled(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// cancelTestsOnTab cancels tests bound to the given tab — both per-config
// (manual) tests and a bulk PING ALL / SPEED ALL run, but the bulk one ONLY if
// it's testing THIS tab. RELOAD on a tab that isn't under test must not stop a
// bulk run elsewhere; it should only refresh its own configs. (The PING ALL /
// SPEED ALL buttons still cancel the bulk run globally via cancelPingAll/
// cancelSpeedAll — a deliberate "one bulk test at a time" toggle.)
// Returns true if it cancelled anything.
// testsBusyOnTab reports whether any test that could still write results into
// tabID is in flight: the background sweep (if it covers the tab), a bulk
// ping/speed run on the tab, or the tab's manual per-config tests. The running
// flags only drop after wg.Wait, so "not busy" means in-flight probes finished.
func testsBusyOnTab(tabID string) bool {
	if atomic.LoadInt32(&autoPingRunning) == 1 {
		autoSweepMu.Lock()
		covered := autoSweepTabs[tabID]
		autoSweepMu.Unlock()
		if covered {
			return true
		}
	}
	if atomic.LoadInt32(&state.pingRunning) == 1 || atomic.LoadInt32(&state.speedRunning) == 1 {
		testMu.Lock()
		tt := testingTab
		testMu.Unlock()
		if tt == tabID {
			return true
		}
	}
	manualMu.Lock()
	n := len(manualTests[tabID])
	manualMu.Unlock()
	return n > 0
}

// waitTestsDrained blocks until every test touching tabID has fully wound down
// (or the timeout passes). Reload calls it AFTER cancelling and BEFORE fetching:
// cancellation stops new probes, but the in-flight remainder (up to the
// concurrency limit, each bounded by its own timeouts) still finishes and writes
// results — without this wait those stale results stamp the freshly fetched
// list (same tab + idx, different configs).
func waitTestsDrained(tabID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for testsBusyOnTab(tabID) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

func cancelTestsOnTab(tabID string) bool {
	// Manual per-config tests on this tab — always safe to cancel; they're
	// independent and tab-scoped.
	cancelled := cancelManualTestsOnTab(tabID)
	// Background candidate sweep (AUTO / after-refresh) covering this tab: cancel
	// it too — a reload replaces the tab's entries, so the sweep is testing stale
	// objects, and its autoPingRunning guard would block the fresh after-refresh
	// test from ever starting (the reported reload-during-test conflict).
	if cancelAutoSweepOnTab(tabID) {
		cancelled = true
	}
	// Bulk run: only if it's running AND on this tab. testingTab can be stale
	// from a finished run, so gate on the running flags too.
	if atomic.LoadInt32(&state.pingRunning) == 0 && atomic.LoadInt32(&state.speedRunning) == 0 {
		return cancelled
	}
	testMu.Lock()
	defer testMu.Unlock()
	if testingTab != tabID {
		return cancelled
	}
	if pingCancelCh != nil {
		select {
		case <-pingCancelCh:
		default:
			close(pingCancelCh)
			cancelled = true
		}
	}
	if speedCancelCh != nil {
		select {
		case <-speedCancelCh:
		default:
			close(speedCancelCh)
			cancelled = true
		}
	}
	return cancelled
}

// mirrorPingResult / mirrorSpeedResult write an entry's terminal test result to
// the SQLite store (best-effort). Without this, a tab switched away from during a
// test reads stale (pre-test) rows from the store on return — the windowed read
// path no longer sees the in-memory live results. Called at the terminal
// entry_update broadcast so only final states (not live ticks) hit the DB.
func mirrorPingResult(tabID string, e *ConfigEntry) {
	if store == nil {
		return
	}
	e.mu.Lock()
	delay, status, errMsg := e.Delay, string(e.PingStatus), e.PingErr
	e.mu.Unlock()
	store.queuePing(tabID, e.Index, delay, status, errMsg) // batched flush
}
func mirrorSpeedResult(tabID string, e *ConfigEntry) {
	if store == nil {
		return
	}
	e.mu.Lock()
	delay, ps, perr := e.Delay, string(e.PingStatus), e.PingErr
	mbps, ss, serr, live := e.SpeedMBps, string(e.SpeedStatus), e.SpeedErr, e.SpeedLive
	e.mu.Unlock()
	store.queueSpeed(tabID, e.Index, delay, ps, perr, mbps, ss, serr, live) // batched flush
}

// loadTestEntries loads the entries a bulk test should run from the in-memory
// store: the onlyIndices subset (in the given order) when non-nil,
// else the whole tab in idx order. The returned *ConfigEntry are the LIVE working
// copies — the test mutates them in place (the window reads see it immediately);
// mirrorPing/SpeedResult persists to SQLite in batches.
func loadTestEntries(tabID string, onlyIndices []int) []*ConfigEntry {
	state.mu.RLock()
	src := state.tabEntries[tabID]
	if onlyIndices == nil {
		out := append([]*ConfigEntry(nil), src...)
		state.mu.RUnlock()
		return out
	}
	byIdx := make(map[int]*ConfigEntry, len(src))
	for _, e := range src {
		byIdx[e.Index] = e
	}
	state.mu.RUnlock()
	out := make([]*ConfigEntry, 0, len(onlyIndices))
	for _, i := range onlyIndices {
		if e, ok := byIdx[i]; ok {
			out = append(out, e)
		}
	}
	return out
}

// runPingAll runs ping tests against entries. If `onlyIndices` is non-nil,
// onlyIndices is an ordered list of entry indices to test, in the exact
// order they should be processed (typically the on-screen sortedList).
// Pass nil to test every entry of the active tab (loaded from the store).
func runPingAll(onlyIndices []int) {
	// If speed is running, cancel it only (don't start ping)
	if atomic.LoadInt32(&state.speedRunning) == 1 {
		cancelSpeedAll()
		return
	}
	// If ping is running, cancel it
	if atomic.LoadInt32(&state.pingRunning) == 1 {
		cancelPingAll()
		return
	}
	if !atomic.CompareAndSwapInt32(&state.pingRunning, 0, 1) {
		return
	}
	// Publish testingTab together with the run flag (before anything slow) so the
	// AUTO sweep's overlap check never sees pingRunning=1 with a stale testingTab.
	tabID := activeTabID()
	testMu.Lock()
	pingCancelCh = make(chan struct{})
	cancelCh := pingCancelCh
	testingTab = tabID
	testMu.Unlock()
	defer atomic.StoreInt32(&state.pingRunning, 0)
	// Load the entries to test from the store (transient *ConfigEntry, mutated
	// during the test and persisted back via mirrorPingResult). onlyIndices
	// preserves the client-supplied (on-screen) order; nil = the whole tab.
	entries := loadTestEntries(tabID, onlyIndices)
	state.broadcast(SSEEvent{Type: "bulk_ping_start", Payload: len(entries), Tab: tabID})
	sem := make(chan struct{}, currentPingConcurrency())
	var wg sync.WaitGroup
	var done int64
	// Acquire the semaphore in the main loop BEFORE spawning the goroutine.
	// This makes the loop block in input order, so the next entry that runs
	// is always the next one in the sortedList we received — the on-screen
	// order. (The previous design spawned every goroutine immediately and
	// let them race for sem slots, which made the visible test order look
	// random for any concurrency > 1.)
	for _, e := range entries {
		if isTestCancelled(cancelCh) {
			break
		}
		state.mu.RLock()
		cancelled := state.cancelledTabs[tabID]
		state.mu.RUnlock()
		if cancelled {
			break
		}
		sem <- struct{}{}
		if isTestCancelled(cancelCh) {
			<-sem
			break
		}
		wg.Add(1)
		go func(ent *ConfigEntry) {
			defer wg.Done()
			defer func() { <-sem }()
			if isTestCancelled(cancelCh) {
				atomic.AddInt64(&done, 1)
				return
			}
			ent.mu.Lock()
			ent.PingStatus = StatusTestingPing
			ent.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			runPingForEntry(ent, cancelCh)
			n := atomic.AddInt64(&done, 1)
			mirrorPingResult(tabID, ent) // persist final result so a tab switch-back reads it
			// Terminal entry update — reliable (this is the row's final
			// status; missing it leaves the UI on a stale "testing" pill).
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			// Bulk-progress tick is lossy: only the latest done/total matters
			// for the progress bar, and bulk_ping_done at the end is reliable.
			state.broadcast(SSEEvent{Type: "bulk_ping_progress", Payload: map[string]interface{}{"done": n, "total": int64(len(entries))}, Tab: tabID, Lossy: true})
		}(e)
	}
	wg.Wait()
	// Reconciliation pass: re-broadcast every tested entry's snapshot in
	// case any mid-flight reliable entry_update was dropped because a
	// slow SSE consumer hit the 2-second cap. Repeated updates are
	// idempotent on the client (onUpdate does an upsert by index), and
	// this guarantees the UI converges on the server's truth without
	// the user having to hit RELOAD. Sweep also sanity-checks for any
	// entry still in TestingPing — force-fails if so (defence in depth;
	// the per-entry watchdog should already have done this).
	// Skip the reconcile re-broadcast if the run was cancelled. On a cancel
	// triggered by RELOAD, the reload re-broadcasts a fresh "loaded" set;
	// a reconcile firing afterwards would re-assert every old result on top
	// of the reset table (the "results reappear a second later" bug). The
	// in-flight workers above already broadcast their own terminal status,
	// so a normal (uncancelled) finish still converges without this sweep.
	if !isTestCancelled(cancelCh) {
		reconcileBulkResults(entries, tabID, false)
	}
	store.flushResults() // persist the test's batched results immediately
	state.broadcast(SSEEvent{Type: "bulk_ping_done", Tab: tabID})
}

// runSpeedAll mirrors runPingAll: when onlyIndices is non-nil, only those
// entries are tested (for FILTER-aware testing).
func runSpeedAll(onlyIndices []int) {
	// If ping is running, cancel it only (don't start speed)
	if atomic.LoadInt32(&state.pingRunning) == 1 {
		cancelPingAll()
		return
	}
	// If speed is running, cancel it
	if atomic.LoadInt32(&state.speedRunning) == 1 {
		cancelSpeedAll()
		return
	}
	if !atomic.CompareAndSwapInt32(&state.speedRunning, 0, 1) {
		return
	}
	tabID := activeTabID()
	testMu.Lock()
	speedCancelCh = make(chan struct{})
	cancelCh := speedCancelCh
	testingTab = tabID
	testMu.Unlock()
	defer atomic.StoreInt32(&state.speedRunning, 0)
	entries := loadTestEntries(tabID, onlyIndices)
	state.broadcast(SSEEvent{Type: "bulk_speed_start", Payload: len(entries), Tab: tabID})
	sem := make(chan struct{}, currentSpeedConcurrency())
	var wg sync.WaitGroup
	var done int64
	// See runPingAll for the rationale on sem-before-spawn: tests fire in
	// the order the client sent (on-screen order) instead of randomly.
	for _, e := range entries {
		if isTestCancelled(cancelCh) {
			break
		}
		state.mu.RLock()
		cancelled := state.cancelledTabs[tabID]
		state.mu.RUnlock()
		if cancelled {
			break
		}
		sem <- struct{}{}
		if isTestCancelled(cancelCh) {
			<-sem
			break
		}
		wg.Add(1)
		go func(ent *ConfigEntry) {
			defer wg.Done()
			defer func() { <-sem }()
			if isTestCancelled(cancelCh) {
				atomic.AddInt64(&done, 1)
				return
			}
			ent.mu.Lock()
			ent.PingStatus = StatusTestingPing
			ent.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			runSpeedForEntry(ent, tabID, cancelCh)
			// Watchdog: runSpeedForEntry should have set a terminal status
			// (ok/failed/skipped). If for any reason it didn't, force-fail
			// the row so the UI doesn't sit on "connecting…" forever.
			// Mirrors the same guard at the end of /api/speed-one.
			ent.mu.Lock()
			if ent.SpeedStatus == StatusTestingSpeed {
				ent.SpeedStatus = StatusFailed
				if ent.SpeedErr == "" {
					ent.SpeedErr = "no result"
				}
				ent.SpeedLive = 0
			}
			// Sibling watchdog: PingStatus is set to TestingPing right before
			// the call. If xray exited early ("exit status N") and the inner
			// measurePing branch never ran, PingStatus stays stuck. Force-fail
			// so the row doesn't blink "ping" until the whole bulk completes.
			if ent.PingStatus == StatusTestingPing {
				ent.PingStatus = StatusFailed
				ent.Delay = -1
				if ent.PingErr == "" {
					ent.PingErr = "no result"
				}
			}
			ent.mu.Unlock()
			n := atomic.AddInt64(&done, 1)
			mirrorSpeedResult(tabID, ent) // persist final result so a tab switch-back reads it
			// Terminal entry update — reliable. This is the very fix point
			// for the "connecting…" / "ping" stuck-pill class of bugs.
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			// Progress tick is lossy — only the latest done/total matters,
			// and bulk_speed_done at the end is the reliable terminal.
			state.broadcast(SSEEvent{Type: "bulk_speed_progress", Payload: map[string]interface{}{"done": n, "total": int64(len(entries))}, Tab: tabID, Lossy: true})
		}(e)
	}
	wg.Wait()
	// See runPingAll for the rationale — covers the "stuck connecting…"
	// class of bugs where a slow SSE consumer drops a terminal event.
	// Skipped on cancel so a RELOAD-triggered stop doesn't re-assert old
	// speed results on top of the freshly-reset table.
	if !isTestCancelled(cancelCh) {
		reconcileBulkResults(entries, tabID, true)
	}
	store.flushResults() // persist the test's batched results immediately
	state.broadcast(SSEEvent{Type: "bulk_speed_done", Tab: tabID})
}

// reconcileBulkResults walks every entry tested in a bulk run and
// re-broadcasts its final snapshot (reliably). If `includeSpeed` is true
// the sweep also force-fails any entry stuck on SpeedStatus=TestingSpeed;
// without it only PingStatus stuck states are corrected (suits bulk ping
// which doesn't touch SpeedStatus). The function is cheap — one mu lock
// per entry plus a broadcast — and it's the safety net that lets us keep
// the high-frequency mid-flight progress events lossy without ever
// leaving a row stranded on a stale pill.
func reconcileBulkResults(entries []*ConfigEntry, tabID string, includeSpeed bool) {
	for _, e := range entries {
		e.mu.Lock()
		if e.PingStatus == StatusTestingPing {
			e.PingStatus = StatusFailed
			e.Delay = -1
			if e.PingErr == "" {
				e.PingErr = "no result"
			}
		}
		if includeSpeed && e.SpeedStatus == StatusTestingSpeed {
			e.SpeedStatus = StatusFailed
			e.SpeedLive = 0
			if e.SpeedErr == "" {
				e.SpeedErr = "no result"
			}
		}
		e.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: e.snap(), Tab: tabID})
	}
}
