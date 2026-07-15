package core

import (
	"sync/atomic"
	"time"
)

// setTableActive aligns the active tab with the displayed table tab so the
// testers/connector (which read activeTabID) operate on the right configs.
func setTableActive() {
	tab := TableTab()
	state.mu.Lock()
	state.activeTab = tab
	state.entries = state.tabEntries[tab]
	state.mu.Unlock()
}

// PingAll tests latency of the configs CURRENTLY IN VIEW (toggles off if a test
// is already running). When a filter/proto is active it tests only the matching,
// on-screen subset — not the whole tab — in the displayed sort order; with no
// filter it falls back to the whole tab (nil, cheaper than materialising every
// index). Per-config results stream via "entry_update" events.
func PingAll(sort, filter string, proto []string) {
	setTableActive()
	go runPingAll(viewIndices(sort, filter, proto))
}

// SpeedAll tests download speed of the configs currently in view (same filtered/
// sorted scope as PingAll).
func SpeedAll(sort, filter string, proto []string) {
	setTableActive()
	go runSpeedAll(viewIndices(sort, filter, proto))
}

// viewIndices returns the entry indices the bulk testers should cover for the
// active tab's current view: the filtered+sorted set when a filter/proto is set,
// or nil (= whole tab) when the view is unfiltered.
func viewIndices(sort, filter string, proto []string) []int {
	tab := activeTabID()
	// A tab exclude filter narrows the view too, so it must scope the test even
	// when there's no text filter / proto pill — otherwise "ping all" would test
	// the excluded (hidden) configs as well. nil = the whole tab (cheaper).
	if filter == "" && len(proto) == 0 && len(tabExcludeFilter(tab)) == 0 {
		return nil
	}
	return Indices(tab, sort, filter, proto)
}

// PingSelected / SpeedSelected test a chosen set (the row context menu) via the
// bulk runner. A re-call cancels a run in progress.
func PingSelected(idxs []int)  { setTableActive(); go runPingAll(idxs) }
func SpeedSelected(idxs []int) { setTableActive(); go runSpeedAll(idxs) }

// PingOne tests a SINGLE config on its own manual-test channel (1.10
// handlePingOne) — NOT the bulk runner. So it emits only entry_update (no
// bulk_ping_* events → no table reload) and doesn't cancel a running bulk test.
func PingOne(idx int) {
	setTableActive()
	tabID := activeTabID()
	entry, ok := memEntry(tabID, idx)
	if !ok {
		return
	}
	cancelCh := make(chan struct{})
	registerManualTest(tabID, cancelCh)
	go func() {
		defer unregisterManualTest(tabID, cancelCh)
		entry.mu.Lock()
		entry.PingStatus = StatusTestingPing
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = recover() }()
			runPingForEntry(entry, cancelCh)
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
		entry.mu.Lock()
		if entry.PingStatus == StatusTestingPing && !isTestCancelled(cancelCh) {
			entry.PingStatus = StatusFailed
			entry.PingErr = "timeout"
			entry.Delay = -1
		}
		entry.mu.Unlock()
		mirrorPingResult(tabID, entry)
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	}()
}

// SpeedOne tests a SINGLE config's download speed on its own manual-test
// channel (1.10 handleSpeedOne) — no bulk events, no table reload.
func SpeedOne(idx int) {
	setTableActive()
	tabID := activeTabID()
	entry, ok := memEntry(tabID, idx)
	if !ok {
		return
	}
	cancelCh := make(chan struct{})
	registerManualTest(tabID, cancelCh)
	go func() {
		defer unregisterManualTest(tabID, cancelCh)
		entry.mu.Lock()
		entry.SpeedStatus = StatusTestingSpeed
		entry.SpeedMBps = 0
		entry.SpeedLive = 0
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = recover() }()
			runSpeedForEntry(entry, tabID, cancelCh)
		}()
		select {
		case <-done:
		case <-time.After(25 * time.Second):
		}
		cancelled := isTestCancelled(cancelCh)
		entry.mu.Lock()
		if entry.SpeedStatus == StatusTestingSpeed && !cancelled {
			entry.SpeedStatus = StatusFailed
			entry.SpeedErr = "timeout"
			entry.SpeedLive = 0
		}
		if entry.PingStatus == StatusTestingPing && !cancelled {
			entry.PingStatus = StatusFailed
			entry.Delay = -1
			if entry.PingErr == "" {
				entry.PingErr = "timeout"
			}
		}
		entry.mu.Unlock()
		mirrorSpeedResult(tabID, entry)
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	}()
}

// CancelTests stops any running ping/speed test.
func CancelTests() {
	if atomic.LoadInt32(&state.pingRunning) == 1 {
		cancelPingAll()
	}
	if atomic.LoadInt32(&state.speedRunning) == 1 {
		cancelSpeedAll()
	}
}

// TestsRunning reports whether a ping or speed test is in progress.
func TestsRunning() bool {
	return atomic.LoadInt32(&state.pingRunning) == 1 || atomic.LoadInt32(&state.speedRunning) == 1
}
