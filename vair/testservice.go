package main

import "vair/core"

// TestService runs ping/speed tests over the table tab. Per-config results arrive
// via the "entry_update" Wails event.
type TestService struct{}

// PingAll tests latency of the configs in the current view — filtered/sorted as
// on screen (call again to cancel). An empty filter + no proto = the whole tab.
func (t *TestService) PingAll(sort, filter string, proto []string) { core.PingAll(sort, filter, proto) }

// SpeedAll tests download speed of the configs in the current view.
func (t *TestService) SpeedAll(sort, filter string, proto []string) {
	core.SpeedAll(sort, filter, proto)
}

// PingOne / SpeedOne test a single config (the per-row buttons).
func (t *TestService) PingOne(idx int)  { core.PingOne(idx) }
func (t *TestService) SpeedOne(idx int) { core.SpeedOne(idx) }

// PingSelected / SpeedSelected test a chosen set (the row context menu).
func (t *TestService) PingSelected(idxs []int)  { core.PingSelected(idxs) }
func (t *TestService) SpeedSelected(idxs []int) { core.SpeedSelected(idxs) }

// PingConnected re-pings the currently connected config regardless of which
// tab the UI is showing (the conn-bar ping chip).
func (t *TestService) PingConnected() { core.PingConnected() }

// Cancel stops any running ping/speed test.
func (t *TestService) Cancel() { core.CancelTests() }

// Running reports whether a test is in progress.
func (t *TestService) Running() bool { return core.TestsRunning() }
