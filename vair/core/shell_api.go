package core

// Shell-facing odds and ends ported from the 1.10 handlers: app capability
// info (TUN gating), and re-pinging the connected config across tabs.

import (
	"fmt"
	"os"
	"runtime"
	"time"
)

// AppInfo mirrors the 1.10 app_info payload — the frontend gates the TUN mode
// pill on singbox availability + admin rights.
type AppInfo struct {
	SingboxAvailable bool   `json:"singbox_available"`
	IsAdmin          bool   `json:"is_admin"`
	OS               string `json:"os"`
}

// GetAppInfo reports engine availability and elevation for the UI.
func GetAppInfo() AppInfo {
	state.mu.RLock()
	sb := state.singboxBin != ""
	state.mu.RUnlock()
	return AppInfo{SingboxAvailable: sb, IsAdmin: checkAdmin(), OS: runtime.GOOS}
}

// PingConnected re-pings the currently connected config regardless of which
// tab the UI is showing (1.10 handlePingConnected). The conn-bar ping chip
// calls this when the connected config isn't in the active tab; the entry is
// located by its raw URL (its connection tab first, then any tab) and the
// result broadcasts under that entry's tab.
func PingConnected() {
	cs := state.conn.snap()
	if cs.Status != ConnConnected || cs.ConnRaw == "" {
		return
	}
	entry, tabID, _ := memEntryByRaw(cs.ConnTab, cs.ConnRaw)
	synthetic := entry == nil
	if synthetic {
		// The connected config is gone from every tab (its source refresh dropped
		// it). Returning silently here left the conn-bar chip stuck on "testing"
		// forever — the frontend waits for an entry_update that never comes. Ping
		// a synthesized entry instead: the result still broadcasts (Tab = the
		// connection's tab, raw = the connected raw the chip matches on). Index -1
		// so nothing row-addressed can act on it.
		entry = &ConfigEntry{Index: -1, Raw: cs.ConnRaw, Name: cs.EntryName, Delay: -1}
		tabID = cs.ConnTab
	}
	go func() {
		entry.mu.Lock()
		entry.PingStatus = StatusTestingPing
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "⚠ ping connected panic: %v\n", r)
				}
			}()
			runPingForEntry(entry, nil)
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
		entry.mu.Lock()
		if entry.PingStatus == StatusTestingPing {
			entry.PingStatus = StatusFailed
			entry.PingErr = "timeout"
			entry.Delay = -1
		}
		entry.mu.Unlock()
		if !synthetic {
			// Persist so a tab switch-back reads it. NEVER for a synthesized entry:
			// queuePing writes by (tab, idx) and would stamp the result onto
			// whatever row occupies that index now.
			mirrorPingResult(tabID, entry)
		}
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	}()
}
