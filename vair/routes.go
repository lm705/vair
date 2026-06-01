package main

import (
	"net/http"
	"os"
	"sync"
	"time"
)

// ── Track spawned test processes for cleanup ─────────────────────
var (
	spawnedPIDs   []int
	spawnedPIDsMu sync.Mutex
)

func trackPID(pid int) {
	spawnedPIDsMu.Lock()
	spawnedPIDs = append(spawnedPIDs, pid)
	spawnedPIDsMu.Unlock()
}

func untrackPID(pid int) {
	spawnedPIDsMu.Lock()
	for i, p := range spawnedPIDs {
		if p == pid {
			spawnedPIDs = append(spawnedPIDs[:i], spawnedPIDs[i+1:]...)
			break
		}
	}
	spawnedPIDsMu.Unlock()
}

// killOrphanedXray kills any test xray processes left behind.
func killOrphanedXray() {
	spawnedPIDsMu.Lock()
	pids := make([]int, len(spawnedPIDs))
	copy(pids, spawnedPIDs)
	spawnedPIDsMu.Unlock()
	for _, pid := range pids {
		if p, err := os.FindProcess(pid); err == nil {
			p.Kill() //nolint:errcheck
		}
	}
}

// startAutoRefresh periodically checks all tabs and refreshes those with RefreshMin > 0.
// For Sources tab, it uses the same interval logic.
func startAutoRefresh() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	lastRefresh := make(map[string]time.Time)
	for range ticker.C {
		state.mu.RLock()
		tabs := make([]Tab, len(state.tabs))
		copy(tabs, state.tabs)
		state.mu.RUnlock()

		settingsMu.RLock()
		srcEnabled := appSettings.SourcesEnabled
		settingsMu.RUnlock()

		for _, t := range tabs {
			if t.RefreshMin <= 0 {
				continue
			}
			if t.IsMain && !srcEnabled {
				continue
			}
			if time.Since(lastRefresh[t.ID]) < time.Duration(t.RefreshMin)*time.Minute {
				continue
			}
			lastRefresh[t.ID] = time.Now()
			// fetchAndInit / fetchTabURLs trigger the candidate re-ping
			// themselves (covers manual refresh and startup too).
			if t.IsMain {
				go fetchAndInit()
			} else if len(t.SourceURLs) > 0 || len(t.SourceFiles) > 0 {
				go fetchTabURLs(t.ID, t.SourceURLs, t.SourceFiles)
			} else {
				// Pasted-only tab: no source to re-fetch, but the user set a refresh
				// interval. Honor it by resetting test results (a real reload would),
				// then re-test if it's an auto-connect candidate. Without this,
				// RefreshMin was silently ignored for sourceless tabs.
				tabID := t.ID
				go refreshSourcelessTab(tabID)
			}
		}
	}
}

func registerRoutes() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(indexHTML))
	})
	http.HandleFunc("/api/stream", handleSSE)
	http.HandleFunc("/api/connect", handleConnect)
	http.HandleFunc("/api/connect-chain", handleConnectChain)
	http.HandleFunc("/api/disconnect", handleDisconnect)
	http.HandleFunc("/api/conn/state", handleConnState)
	http.HandleFunc("/api/auto/switch", handleAutoSwitch)
	http.HandleFunc("/api/auto/candidates", handleAutoCandidates)
	http.HandleFunc("/api/ping/one", handlePingOne)
	http.HandleFunc("/api/ping/connected", handlePingConnected)
	http.HandleFunc("/api/check-exit", handleCheckExit)
	http.HandleFunc("/api/entry/rename", handleRenameEntry)
	http.HandleFunc("/api/ping/all", handlePingAll)
	http.HandleFunc("/api/speed/one", handleSpeedOne)
	http.HandleFunc("/api/speed/all", handleSpeedAll)
	http.HandleFunc("/api/tests/cancel", handleTestsCancel)
	http.HandleFunc("/api/reload", handleReload)
	http.HandleFunc("/api/tab/create", handleTabCreate)
	http.HandleFunc("/api/tab/delete", handleTabDelete)
	http.HandleFunc("/api/tab/switch", handleTabSwitch)
	http.HandleFunc("/api/tab/paste", handleTabPaste)
	http.HandleFunc("/api/tab/rename", handleTabRename)
	http.HandleFunc("/api/tab/set-url", handleTabSetURL)
	http.HandleFunc("/api/tab/delete-entries", handleTabDeleteEntries)
	http.HandleFunc("/api/tab/reorder", handleTabReorder)
	http.HandleFunc("/api/settings", handleSettings)
	http.HandleFunc("/api/stats/reset", handleStatsReset)
	http.HandleFunc("/api/restart-admin", handleRestartAdmin)
	http.HandleFunc("/api/storage/open", handleStorageOpen)
	http.HandleFunc("/api/export", handleSettingsExport)
	http.HandleFunc("/api/import", handleSettingsImport)
	http.HandleFunc("/api/logs", handleLogs)
	http.HandleFunc("/api/logs/clear", handleLogsClear)
	go logFlushLoop()
}
