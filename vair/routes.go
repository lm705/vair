package main

import (
	"net/http"
	"os"
	"strings"
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
			// Auto-refresh toggled off for this tab → skip (interval is preserved).
			if t.RefreshDisabled || t.RefreshMin <= 0 {
				continue
			}
			if t.IsMain && !srcEnabled {
				continue
			}
			// First time we see this tab (app startup, or a just-added tab): start
			// its clock now so the first auto-refresh lands one full interval later,
			// not on the next 1-minute tick. (Bug: an empty map made time.Since(zero)
			// huge, so it refreshed almost immediately after launch.)
			if _, seen := lastRefresh[t.ID]; !seen {
				lastRefresh[t.ID] = time.Now()
				continue
			}
			if time.Since(lastRefresh[t.ID]) < time.Duration(t.RefreshMin)*time.Minute {
				continue
			}
			lastRefresh[t.ID] = time.Now()
			// fetchAndInit / fetchTabURLs trigger the candidate re-ping
			// themselves (covers manual refresh and startup too). The
			// per-tab "test after auto-refresh" (runAfterRefreshTest) runs
			// ONLY here — the auto-refresh path — so a manual RELOAD never
			// triggers it. Each fetch* call is synchronous, so we run the
			// test right after it returns (entries are loaded by then).
			if t.IsMain {
				go func() { fetchAndInit(); runAfterRefreshTest("main") }()
			} else if len(t.SourceURLs) > 0 || len(t.SourceFiles) > 0 || t.gitHubReady() {
				id, urls, files := t.ID, t.SourceURLs, t.SourceFiles
				go func() { fetchTabURLs(id, urls, files); runAfterRefreshTest(id) }()
			} else {
				// Pasted-only tab: no source to re-fetch, but the user set a refresh
				// interval. Honor it by resetting test results (a real reload would),
				// then re-test if it's an auto-connect candidate. Without this,
				// RefreshMin was silently ignored for sourceless tabs.
				tabID := t.ID
				go func() { refreshSourcelessTab(tabID); runAfterRefreshTest(tabID) }()
			}
		}
	}
}

// themedIndexHTML returns the UI with the persisted theme applied to the <body>
// tag up front. Without this the page first renders with the default dark
// palette and only switches to light once applyTheme() runs after the async
// settings fetch — a visible dark flash on launch for light-theme users. The
// body fills the viewport (height:100%), so seeding its class makes the very
// first paint use the right background. Dark is the default the static HTML
// already ships with, so only "light" needs the injection.
func themedIndexHTML() string {
	// Inject the running version so the UI can show it without a round-trip.
	html := strings.Replace(indexHTML, "__APP_VERSION__", appVersion, 1)
	settingsMu.RLock()
	light := appSettings.Theme == "light"
	settingsMu.RUnlock()
	if light {
		html = strings.Replace(html, "<body>", `<body class="theme-light">`, 1)
	}
	return html
}

func registerRoutes() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(themedIndexHTML()))
	})
	http.HandleFunc("/api/stream", handleSSE)
	http.HandleFunc("/api/connect", handleConnect)
	http.HandleFunc("/api/connect-chain", handleConnectChain)
	http.HandleFunc("/api/disconnect", handleDisconnect)
	http.HandleFunc("/api/conn/state", handleConnState)
	http.HandleFunc("/api/auto/switch", handleAutoSwitch)
	http.HandleFunc("/api/auto/candidates", handleAutoCandidates)
	http.HandleFunc("/api/auto/connect-cand", handleAutoConnectCand)
	http.HandleFunc("/api/ping/one", handlePingOne)
	http.HandleFunc("/api/ping/connected", handlePingConnected)
	http.HandleFunc("/api/check-exit", handleCheckExit)
	http.HandleFunc("/api/entry/rename", handleRenameEntry)
	http.HandleFunc("/api/sources-info", handleSourcesInfo)
	http.HandleFunc("/api/qr", handleQR)
	http.HandleFunc("/api/qr-text", handleQRText)
	http.HandleFunc("/api/ping/all", handlePingAll)
	http.HandleFunc("/api/speed/one", handleSpeedOne)
	http.HandleFunc("/api/speed/all", handleSpeedAll)
	http.HandleFunc("/api/tests/cancel", handleTestsCancel)
	http.HandleFunc("/api/reload", handleReload)
	http.HandleFunc("/api/tab/create", handleTabCreate)
	http.HandleFunc("/api/tab/delete", handleTabDelete)
	http.HandleFunc("/api/tab/switch", handleTabSwitch)
	http.HandleFunc("/api/tab/window", handleTabWindow)
	http.HandleFunc("/api/tab/indices", handleTabIndices)
	http.HandleFunc("/api/tab/raws", handleTabRaws)
	http.HandleFunc("/api/tab/delete-failed", handleTabDeleteFailed)
	http.HandleFunc("/api/tab/paste", handleTabPaste)
	http.HandleFunc("/api/tab/add-url", handleTabAddURL)
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
	http.HandleFunc("/api/update/check", handleUpdateCheck)
	http.HandleFunc("/api/update/apply", handleUpdateApply)
	http.HandleFunc("/api/update/dismiss", handleUpdateDismiss)
	http.HandleFunc("/api/deeplink", handleDeepLink)
	go logFlushLoop()
}
