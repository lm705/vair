package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─────────────────────────── ping / speed ────────────────────────

func measurePing(tr *http.Transport) (int64, error) {
	url := currentPingURL()
	pt := currentPingTimeout()
	wc := &http.Client{Transport: tr, Timeout: currentWarmupTimeout(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	w, err := wc.Get(url)
	if err != nil {
		return -1, fmt.Errorf("warmup: %w", err)
	}
	io.Copy(io.Discard, w.Body)
	w.Body.Close()
	mc := &http.Client{Transport: tr, Timeout: pt,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var best int64 = -1
	var lastErr error
	for i := 0; i < pingRounds; i++ {
		start := time.Now()
		resp, e := mc.Get(url)
		if e != nil {
			lastErr = e
			if best > 0 {
				break
			}
			continue
		}
		ms := time.Since(start).Milliseconds()
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
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

// measureSpeed runs one speed test, with an optional fallback to a second
// URL ONLY when the primary returns HTTP 429. Each attempt is its own
// bounded request — same http.Client.Timeout and same defer-close — so a
// fallback can never extend the test indefinitely or leak the connection
// (the failure mode that caused tests to hang on "connecting…" in a
// previous attempt at this feature).
func measureSpeed(tr *http.Transport, onProgress func(float64)) (float64, error) {
	primary := currentSpeedURL()
	mbps, err := measureSpeedOne(tr, primary, onProgress)
	if err == nil {
		return mbps, nil
	}
	// Only retry on a clean 429 from the primary. Connect errors, slow
	// responses, etc. stay as the user-visible error.
	if !errors.Is(err, err429) {
		return mbps, err
	}
	fb := currentSpeedFallbackURL()
	if fb == "" || fb == primary {
		return mbps, err
	}
	return measureSpeedOne(tr, fb, onProgress)
}

// measureSpeedOne is a single attempt: one HTTP request, bounded by
// Client.Timeout, body always closed via defer. Returned errors are
// terminal — there's no inner retry that could spin forever.
func measureSpeedOne(tr *http.Transport, urlStr string, onProgress func(float64)) (float64, error) {
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
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			total += int64(n)
		}
		now := time.Now()
		if onProgress != nil && now.Sub(lastReport) >= 400*time.Millisecond && total > 0 {
			onProgress(float64(total) / now.Sub(start).Seconds() / 1024 / 1024)
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
	return float64(total) / secs / 1024 / 1024, nil
}

// ─────────────────────────── entry runners ───────────────────────

func runPingForEntry(entry *ConfigEntry) {
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
		delay, e := measurePing(tr)
		entry.mu.Lock()
		defer entry.mu.Unlock()
		if e != nil || delay < 0 {
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
func runSpeedForEntry(entry *ConfigEntry, tabID string) {
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
		delay, pingErr := measurePing(tr)
		entry.mu.Lock()
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
		})
		entry.mu.Lock()
		defer entry.mu.Unlock()
		entry.SpeedLive = 0
		if e != nil {
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

func runPingAndSpeedForEntry(entry *ConfigEntry, tabID string) {
	n, err := parseNode(entry.Raw)
	if err != nil {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = err.Error()
		entry.SpeedStatus = StatusSkipped
		entry.SpeedErr = "parse error"
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
		entry.SpeedStatus = StatusSkipped
		entry.SpeedErr = reason
		entry.mu.Unlock()
		return
	}
	ttl := startupTimeout + currentWarmupTimeout() + currentPingTimeout()*time.Duration(pingRounds) + currentSpeedDuration() + 10*time.Second
	if err = withEngine(n, ttl, func(_ int, tr *http.Transport) error {
		delay, pingErr := measurePing(tr)
		entry.mu.Lock()
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
		mbps, sErr := measureSpeed(tr, func(live float64) {
			entry.mu.Lock()
			entry.SpeedLive = live
			entry.mu.Unlock()
			// Lossy — see runSpeedForEntry's identical callback for why.
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID, Lossy: true})
		})
		entry.mu.Lock()
		defer entry.mu.Unlock()
		entry.SpeedLive = 0
		if sErr != nil {
			entry.SpeedStatus = StatusFailed
			entry.SpeedMBps = 0
			entry.SpeedErr = shortErr(sErr.Error())
		} else {
			entry.SpeedStatus = StatusOK
			entry.SpeedMBps = mbps
			entry.SpeedErr = ""
		}
		return nil
	}); err != nil {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = shortErr(err.Error())
		entry.SpeedStatus = StatusSkipped
		entry.SpeedErr = "xray failed"
		entry.mu.Unlock()
	}
	logTestResult(entry)
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

// runPingAll runs ping tests against entries. If `onlyIndices` is non-nil,
// onlyIndices is an ordered list of entry indices to test, in the exact
// order they should be processed (typically the on-screen sortedList).
// Pass nil to test every entry in state.entries order.
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
	testMu.Lock()
	pingCancelCh = make(chan struct{})
	cancelCh := pingCancelCh
	testMu.Unlock()
	defer atomic.StoreInt32(&state.pingRunning, 0)
	state.mu.RLock()
	allEntries := make([]*ConfigEntry, len(state.entries))
	copy(allEntries, state.entries)
	tabID := state.activeTab
	state.mu.RUnlock()

	// Restrict to onlyIndices, preserving the client-supplied order so that
	// tests fire in the exact order the rows appear on screen.
	var entries []*ConfigEntry
	if onlyIndices != nil {
		byIdx := make(map[int]*ConfigEntry, len(allEntries))
		for _, e := range allEntries {
			byIdx[e.Index] = e
		}
		for _, idx := range onlyIndices {
			if e, ok := byIdx[idx]; ok {
				entries = append(entries, e)
			}
		}
	} else {
		entries = allEntries
	}

	testMu.Lock()
	testingTab = tabID
	testMu.Unlock()
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
			runPingForEntry(ent)
			n := atomic.AddInt64(&done, 1)
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
	testMu.Lock()
	speedCancelCh = make(chan struct{})
	cancelCh := speedCancelCh
	testMu.Unlock()
	defer atomic.StoreInt32(&state.speedRunning, 0)
	state.mu.RLock()
	allEntries := make([]*ConfigEntry, len(state.entries))
	copy(allEntries, state.entries)
	tabID := state.activeTab
	state.mu.RUnlock()

	var entries []*ConfigEntry
	if onlyIndices != nil {
		byIdx := make(map[int]*ConfigEntry, len(allEntries))
		for _, e := range allEntries {
			byIdx[e.Index] = e
		}
		for _, idx := range onlyIndices {
			if e, ok := byIdx[idx]; ok {
				entries = append(entries, e)
			}
		}
	} else {
		entries = allEntries
	}

	testMu.Lock()
	testingTab = tabID
	testMu.Unlock()
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
			runSpeedForEntry(ent, tabID)
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
