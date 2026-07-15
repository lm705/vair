package core

// Settings export / import — ported verbatim from the 1.10 handlers
// (buildSettingsExport / handleSettingsExport / handleSettingsImport),
// minus the HTTP layer. The shell provides the file dialogs.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

type settingsExport struct {
	Version     int            `json:"version"`
	ExportedAt  string         `json:"exported_at"`
	AppName     string         `json:"app"`
	AppSettings AppSettings    `json:"app_settings"`
	Tabs        []persistedTab `json:"tabs"`
}

const settingsExportVersion = 1

// buildSettingsExport snapshots tabs + their current config entries + the app
// settings into a single document. Config entries (not just source URLs) are
// included so hand-pasted tabs survive the round-trip.
func buildSettingsExport() settingsExport {
	state.mu.RLock()
	var tabs []persistedTab
	for _, t := range state.tabs {
		pt := persistedTab{
			ID: t.ID, Name: t.Name,
			SourceURLs: t.SourceURLs, SourceDisabled: t.SourceDisabled, SourceFiles: t.SourceFiles,
			RefreshMin: t.RefreshMin, ExcludeFilter: t.ExcludeFilter,
			ExcludeDisabled: t.ExcludeDisabled, RefreshDisabled: t.RefreshDisabled,
			DedupMode:       t.DedupMode,
			AutoRefreshTest: t.AutoRefreshTest, Subs: t.Subs,
			GitHubEnabled: t.GitHubEnabled, GitHubOwner: t.GitHubOwner,
			GitHubRepo: t.GitHubRepo, GitHubFile: t.GitHubFile, GitHubPAT: t.GitHubPAT,
		}
		if !t.IsMain {
			// Snapshot the raw config strings (from memory; state.mu held) so the
			// import on another machine sees the same configs without needing the
			// original source URL to be reachable.
			for _, e := range state.tabEntries[t.ID] {
				if e.Raw != "" {
					pt.Configs = append(pt.Configs, e.Raw)
				}
			}
		}
		tabs = append(tabs, pt)
	}
	state.mu.RUnlock()
	settingsMu.RLock()
	settingsCopy := appSettings
	settingsMu.RUnlock()
	return settingsExport{
		Version:     settingsExportVersion,
		ExportedAt:  time.Now().UTC().Format(time.RFC3339),
		AppName:     "Vair",
		AppSettings: settingsCopy,
		Tabs:        tabs,
	}
}

// ExportSettingsJSON returns the export document (pretty JSON) plus the
// suggested filename ("vair_settings_YYYYMMDD_HHMMSS.json").
func ExportSettingsJSON() (data []byte, filename string, err error) {
	data, err = json.MarshalIndent(buildSettingsExport(), "", "  ")
	if err != nil {
		return nil, "", err
	}
	return data, fmt.Sprintf("vair_settings_%s.json", time.Now().Format("20060102_150405")), nil
}

// ImportSettings applies a settingsExport JSON document in place of the
// current state (1.10 handleSettingsImport). includeTabs=false imports only
// the app settings, leaving the user's tabs untouched. Returns "" on success
// or a human-readable error.
func ImportSettings(body []byte, includeTabs bool) string {
	var imp settingsExport
	if err := json.Unmarshal(body, &imp); err != nil {
		return "parse JSON: " + err.Error()
	}
	if imp.Version == 0 || imp.Version > settingsExportVersion {
		return fmt.Sprintf("unsupported export version %d (expected %d)", imp.Version, settingsExportVersion)
	}
	if includeTabs && len(imp.Tabs) == 0 {
		return "no tabs in export"
	}

	// Take ownership of the new state. Stop any running tests so they don't
	// reference about-to-be-replaced *ConfigEntry pointers.
	if atomic.LoadInt32(&state.pingRunning) == 1 {
		cancelPingAll()
	}
	if atomic.LoadInt32(&state.speedRunning) == 1 {
		cancelSpeedAll()
	}
	stopConnection()

	// Replace app settings (atomic on disk + in memory).
	settingsMu.Lock()
	appSettings = imp.AppSettings
	settingsMu.Unlock()
	saveSettings()

	// App-settings-only import: skip the tab rebuild entirely.
	if !includeTabs {
		return ""
	}

	// Rebuild tabs in memory from the imported document. Mirrors loadTabs but
	// on injected data instead of tabs.json.
	state.mu.Lock()
	state.tabs = []Tab{{ID: "main", Name: "Sources", IsMain: true, Closable: false}}
	state.tabEntries = make(map[string][]*ConfigEntry) // wipe in-memory configs (rebuilt below)
	state.entries = nil
	imported := map[string][]*ConfigEntry{} // tabID → parsed configs
	for _, pt := range imp.Tabs {
		if pt.ID == "main" {
			for i, t := range state.tabs {
				if t.ID == "main" {
					state.tabs[i].ExcludeFilter = pt.ExcludeFilter
					state.tabs[i].RefreshMin = pt.RefreshMin
					state.tabs[i].ExcludeDisabled = pt.ExcludeDisabled
					state.tabs[i].RefreshDisabled = pt.RefreshDisabled
					break
				}
			}
			continue
		}
		mode := pt.DedupMode
		if mode == "" && pt.Dedup {
			mode = "hide"
		}
		urls := pt.SourceURLs
		if len(urls) == 0 && pt.SourceURL != "" {
			urls = []string{pt.SourceURL}
		}
		tab := Tab{
			ID: pt.ID, Name: pt.Name, IsMain: false, Closable: true,
			SourceURLs: urls, SourceDisabled: pt.SourceDisabled, SourceFiles: pt.SourceFiles,
			RefreshMin: pt.RefreshMin, ExcludeFilter: pt.ExcludeFilter,
			ExcludeDisabled: pt.ExcludeDisabled, RefreshDisabled: pt.RefreshDisabled,
			DedupMode:       mode,
			AutoRefreshTest: pt.AutoRefreshTest, Subs: pt.subsOf(),
			GitHubEnabled: pt.GitHubEnabled, GitHubOwner: pt.GitHubOwner,
			GitHubRepo: pt.GitHubRepo, GitHubFile: pt.GitHubFile, GitHubPAT: pt.GitHubPAT,
		}
		state.tabs = append(state.tabs, tab)
		imported[tab.ID] = parseConfigLines(strings.Join(pt.Configs, "\n"))
	}
	// Make sure the active tab still exists; fall back to "main" otherwise.
	activeOK := false
	for _, t := range state.tabs {
		if t.ID == state.activeTab {
			activeOK = true
			break
		}
	}
	if !activeOK {
		state.activeTab = "main"
	}
	state.mu.Unlock()
	// Import rebuilds everything: wipe the store, then write each tab's configs.
	if store != nil {
		store.deleteAll()
		for tid, ents := range imported {
			storeReplace(tid, ents)
		}
	}
	saveTabs()

	// Push the new tab list + active tab; the client re-fetches the window.
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	state.broadcast(SSEEvent{Type: "active_tab", Payload: state.activeTab})
	loadedSignal(state.activeTab)
	return ""
}
