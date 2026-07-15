package core

// Tab settings + source fetching — ported verbatim from the 1.10 handlers
// (handleTabSetURL / fetchTabURLs / handleReload), minus the HTTP layer.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// SourcesInfo returns the built-in SOURCES tab URLs (compiled in, read-only —
// shown in the Sources settings modal with copy/QR buttons).
func SourcesInfo() []string {
	urls := make([]string, 0, len(sourceDefs))
	for _, s := range sourceDefs {
		if s.URL != "" {
			urls = append(urls, s.URL)
		}
	}
	return urls
}

// startAutoRefresh periodically checks all tabs and refreshes those with
// RefreshMin > 0 (ported verbatim from 1.10 routes.go). Runs for the app's
// lifetime; started by StartBackground.
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
			// not on the next 1-minute tick.
			if _, seen := lastRefresh[t.ID]; !seen {
				lastRefresh[t.ID] = time.Now()
				continue
			}
			if time.Since(lastRefresh[t.ID]) < time.Duration(t.RefreshMin)*time.Minute {
				continue
			}
			lastRefresh[t.ID] = time.Now()
			// The per-tab "test after auto-refresh" (runAfterRefreshTest) runs ONLY
			// here — the auto-refresh path — so a manual RELOAD never triggers it.
			if t.IsMain {
				// Scheduled auto-refresh is a background refresh — silent (no spinner).
				go func() { fetchAndInitSilent(true); runAfterRefreshTest("main") }()
			} else if len(t.SourceURLs) > 0 || len(t.SourceFiles) > 0 || t.gitHubReady() {
				id, urls, files := t.ID, t.SourceURLs, t.SourceFiles
				go func() { fetchTabURLs(id, urls, files); runAfterRefreshTest(id) }()
			} else {
				// Pasted-only tab: no source to re-fetch, but the user set a refresh
				// interval. Honor it by resetting test results (a real reload would),
				// then re-test if it's an auto-connect candidate.
				tabID := t.ID
				go func() { refreshSourcelessTab(tabID); runAfterRefreshTest(tabID) }()
			}
		}
	}
}

// isSubscriptionURL reports whether s is a single bare http(s) link.
func isSubscriptionURL(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// AddTabSourceURL appends a subscription URL to a tab's persistent Source URLs
// and re-fetches (1.10 handleTabAddURL — used by QR scans / pasted links /
// vair:// deep links). Returns false when the URL was already there (or the
// tab is main / not found / not a URL).
func AddTabSourceURL(id, u string) bool {
	if id == "" || id == "main" {
		return false
	}
	u = strings.TrimSpace(u)
	if !isSubscriptionURL(u) {
		return false
	}
	var urls []string
	var files []TabFile
	found, added := false, false
	state.mu.Lock()
	for i := range state.tabs {
		if state.tabs[i].ID != id {
			continue
		}
		found = true
		if !strInSlice(u, state.tabs[i].SourceURLs) {
			state.tabs[i].SourceURLs = append(state.tabs[i].SourceURLs, u)
			added = true
		}
		urls = append([]string(nil), state.tabs[i].SourceURLs...)
		files = append([]TabFile(nil), state.tabs[i].SourceFiles...)
		break
	}
	state.mu.Unlock()
	if !found || !added {
		return false
	}
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()
	if state.activeTab == id {
		state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: id})
	}
	// Re-fetch with the full (possibly augmented) source set.
	go fetchTabURLs(id, urls, files)
	return true
}

// TabDetail returns the full tab (sources, subs, filters) for the settings modal.
func TabDetail(id string) Tab {
	state.mu.RLock()
	defer state.mu.RUnlock()
	for _, t := range state.tabs {
		if t.ID == id {
			return t
		}
	}
	return Tab{}
}

// TabSettingsReq mirrors the 1.10 /api/tab/set-url payload.
type TabSettingsReq struct {
	URLs            []string  `json:"urls"`
	DisabledURLs    []string  `json:"disabled_urls"`
	Files           []TabFile `json:"files"`
	RefreshMin      int       `json:"refresh_min"`
	ExcludeFilter   []string  `json:"exclude_filter"`
	ExcludeDisabled bool      `json:"exclude_disabled"`
	RefreshDisabled bool      `json:"refresh_disabled"`
	DedupMode       string    `json:"dedup_mode"` // "" | "hide" | "delete"
	// "" / "ping" / "speed" — test to run after a scheduled auto-refresh.
	AutoRefreshTest string `json:"auto_refresh_test"`
	// Per-tab GitHub private-repo import via PAT.
	GitHubEnabled bool   `json:"github_enabled"`
	GitHubOwner   string `json:"github_owner"`
	GitHubRepo    string `json:"github_repo"`
	GitHubFile    string `json:"github_file"`
	GitHubPAT     string `json:"github_pat"`
}

func strSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// tabFilesKey produces a deterministic string for change-detection on a list
// of files. Two file lists are "the same set of sources" if their (name,path)
// pairs match in order. Size/mtime are intentionally NOT part of the key —
// content changes on disk are picked up by RELOAD, not by save.
func tabFilesKey(files []TabFile) string {
	var sb strings.Builder
	for _, f := range files {
		sb.WriteString(f.Name)
		sb.WriteByte('|')
		sb.WriteString(f.Path)
		if f.Disabled {
			sb.WriteString("|off") // toggling on/off changes what gets fetched
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// githubKey produces a deterministic string for change-detection on a tab's
// GitHub import config. A disabled import collapses to "" so toggling it off
// (or editing fields while disabled) is treated as "no GitHub source".
func githubKey(enabled bool, owner, repo, file, pat string) string {
	if !enabled {
		return ""
	}
	return owner + "\x00" + repo + "\x00" + file + "\x00" + pat
}

// SetTabSettings applies the tab-settings modal state (1.10 handleTabSetURL):
// updates the tab, re-fetches sources when they changed, applies delete-dedup
// on transition, or clears the store when all sources were removed.
func SetTabSettings(id string, req TabSettingsReq) {
	// Normalize the mode. Empty or unrecognized → "" (off).
	newMode := req.DedupMode
	switch newMode {
	case "off":
		newMode = ""
	case "hide", "delete", "":
		// recognized
	default:
		newMode = ""
	}

	// Clean URLs
	var cleanURLs []string
	for _, u := range req.URLs {
		u = strings.TrimSpace(u)
		if u != "" {
			cleanURLs = append(cleanURLs, u)
		}
	}
	// Disabled URLs: keep only those still present in the URL list (drop stale
	// entries for sources the user removed) so SourceDisabled never lingers.
	var cleanDisabled []string
	for _, u := range req.DisabledURLs {
		u = strings.TrimSpace(u)
		if u != "" && strInSlice(u, cleanURLs) && !strInSlice(u, cleanDisabled) {
			cleanDisabled = append(cleanDisabled, u)
		}
	}
	// Clean files: every file needs a Path (the native picker always provides
	// one). Stat each path to refresh size/mtime for display; content is read
	// lazily by fetchTabURLs.
	var cleanFiles []TabFile
	for _, f := range req.Files {
		f.Name = strings.TrimSpace(f.Name)
		f.Path = strings.TrimSpace(f.Path)
		if f.Path == "" {
			continue
		}
		if f.Name == "" {
			f.Name = filepath.Base(f.Path)
		}
		if info, err := os.Stat(f.Path); err == nil {
			f.Size = info.Size()
			f.Mtime = info.ModTime().Unix()
		}
		cleanFiles = append(cleanFiles, f)
	}

	// GitHub import fields: trim, strip a leading "/" off the file path.
	ghEnabled := req.GitHubEnabled
	ghOwner := strings.TrimSpace(req.GitHubOwner)
	ghRepo := strings.TrimSpace(req.GitHubRepo)
	ghFile := strings.TrimLeft(strings.TrimSpace(req.GitHubFile), "/")
	ghPAT := strings.TrimSpace(req.GitHubPAT)
	ghReady := ghEnabled && ghOwner != "" && ghRepo != "" && ghFile != "" && ghPAT != ""
	hasSource := len(cleanURLs) > 0 || len(cleanFiles) > 0 || ghReady

	var sourcesChanged bool
	var excludeChanged bool
	var oldMode string
	state.mu.Lock()
	for i, t := range state.tabs {
		if t.ID == id {
			// The exclude filter is applied at FETCH time, so a change to it —
			// including toggling it off — needs a rebuild to re-show / re-hide.
			excludeChanged = t.ExcludeDisabled != req.ExcludeDisabled ||
				!strSlicesEqual(t.ExcludeFilter, req.ExcludeFilter)
			if !t.IsMain {
				oldURLs := strings.Join(t.SourceURLs, "|")
				newURLs := strings.Join(cleanURLs, "|")
				oldDisabled := strings.Join(t.SourceDisabled, "|")
				newDisabled := strings.Join(cleanDisabled, "|")
				oldFilesKey := tabFilesKey(t.SourceFiles)
				newFilesKey := tabFilesKey(cleanFiles)
				oldGH := githubKey(t.GitHubEnabled, t.GitHubOwner, t.GitHubRepo, t.GitHubFile, t.GitHubPAT)
				newGH := githubKey(ghEnabled, ghOwner, ghRepo, ghFile, ghPAT)
				sourcesChanged = (oldURLs != newURLs) || (oldDisabled != newDisabled) ||
					(oldFilesKey != newFilesKey) || (oldGH != newGH)
				oldMode = t.DedupMode
				state.tabs[i].SourceURLs = cleanURLs
				state.tabs[i].SourceDisabled = cleanDisabled
				state.tabs[i].SourceFiles = cleanFiles
				state.tabs[i].DedupMode = newMode
				state.tabs[i].GitHubEnabled = ghEnabled
				state.tabs[i].GitHubOwner = ghOwner
				state.tabs[i].GitHubRepo = ghRepo
				state.tabs[i].GitHubFile = ghFile
				state.tabs[i].GitHubPAT = ghPAT
			}
			state.tabs[i].RefreshMin = req.RefreshMin
			state.tabs[i].ExcludeFilter = req.ExcludeFilter
			state.tabs[i].ExcludeDisabled = req.ExcludeDisabled
			state.tabs[i].RefreshDisabled = req.RefreshDisabled
			switch req.AutoRefreshTest {
			case "ping", "speed":
				state.tabs[i].AutoRefreshTest = req.AutoRefreshTest
			default:
				state.tabs[i].AutoRefreshTest = ""
			}
			break
		}
	}
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()

	// Instant, in-memory updates that must NOT re-download the sources:
	//   • switching to "delete" dedup drops body-duplicates from the current list;
	//   • an exclude-filter change is now a pure VIEW filter (memWindow hides
	//     matching configs on read), so a re-signal re-filters the visible list
	//     instantly — both when enabling AND disabling — with no fetch, no data
	//     loss. (This is what 1.10 achieved by re-fetching; the view filter is
	//     strictly better — no network.)
	if newMode == "delete" && oldMode != "delete" {
		applyDeleteDedupInPlace(id)
	}
	if excludeChanged {
		loadedSignal(id)
	}

	// The ONLY thing that re-fetches from the network is an actual source /
	// file / GitHub change (main's sources are managed via the Sources modal and
	// never reach here with sourcesChanged set).
	if !sourcesChanged || id == "main" {
		return
	}
	if hasSource {
		if state.activeTab == id {
			state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: id})
		}
		go fetchTabURLs(id, cleanURLs, cleanFiles)
	} else {
		// All sources removed — clear the subscription info too.
		state.mu.Lock()
		for i := range state.tabs {
			if state.tabs[i].ID == id {
				state.tabs[i].Subs = nil
				break
			}
		}
		state.mu.Unlock()
		storeReplace(id, nil) // all sources removed, no re-fetch → clear the store now
		state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
		loadedSignal(id)
		saveTabs()
	}
}

// Reload re-fetches the ACTIVE tab's sources (1.10 handleReload): main →
// fetchAndInit; a tab with sources → fetchTabURLs; a paste-only tab → reset
// stale ping/speed results so they get re-tested.
func Reload(tabID string) {
	state.mu.RLock()
	if tabID == "" {
		tabID = state.activeTab
	}
	var sourceURLs []string
	var sourceFiles []TabFile
	var ghReady bool
	for _, t := range state.tabs {
		if t.ID == tabID {
			sourceURLs = t.SourceURLs
			sourceFiles = t.SourceFiles
			ghReady = t.gitHubReady()
			break
		}
	}
	state.mu.RUnlock()

	// Stop an in-flight ping/speed run IMMEDIATELY — but ONLY if it's testing
	// THIS tab (tests are global, one at a time across all tabs).
	cancelTestsOnTab(tabID)
	// Briefly mark the tab cancelled so any bulk loop bound to THIS tab bails
	// before we re-broadcast the fresh list.
	state.mu.Lock()
	state.cancelledTabs[tabID] = true
	state.mu.Unlock()
	go func() {
		time.Sleep(300 * time.Millisecond)
		state.mu.Lock()
		delete(state.cancelledTabs, tabID)
		state.mu.Unlock()
	}()

	if tabID == "main" {
		go fetchAndInit()
	} else if len(sourceURLs) > 0 || len(sourceFiles) > 0 || ghReady {
		go func() {
			state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: tabID})
			fetchTabURLs(tabID, sourceURLs, sourceFiles)
		}()
	} else {
		// No URL: reset all test results for this tab (memory + store).
		go func() {
			resetTabResultsMem(tabID)
			loadedSignal(tabID)
		}()
	}
}

// fetchTabURLs fetches configs from multiple URLs and replaces tab entries.
// Files (re-read from disk) are appended after URL contents in order, then the
// per-tab GitHub import. Ported verbatim from 1.10.
func fetchTabURLs(tabID string, urls []string, files []TabFile) {
	// Mark this tab as fetching so switching to it shows a spinner (not a stale/
	// empty list), and clear it however we return.
	state.mu.Lock()
	state.fetching[tabID] = true
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		delete(state.fetching, tabID)
		state.mu.Unlock()
	}()

	// Snapshot which URL sources the user switched off — skipped on fetch.
	state.mu.RLock()
	var disabled []string
	for _, t := range state.tabs {
		if t.ID == tabID {
			disabled = t.SourceDisabled
			break
		}
	}
	state.mu.RUnlock()

	var allEntries []*ConfigEntry
	// enabledSources counts sources that actually participate in this fetch.
	// 0 enabled + 0 entries means the user explicitly switched everything off —
	// that CLEARS the tab (the keep-existing guard is only for failed fetches).
	enabledSources := 0
	// One record per ENABLED URL source: its metadata on success, or
	// {URL, Error} on failure — rebuilt every fetch (reconciliation).
	var subs []subMeta
	for _, u := range urls {
		if strInSlice(u, disabled) {
			continue
		}
		enabledSources++
		lines, meta, err := fetchURLMeta(u)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ tab %s fetch %s: %v\n", tabID, u, err)
			subs = append(subs, subMeta{URL: u, Error: err.Error()})
			continue
		}
		if len(lines) == 0 {
			subs = append(subs, subMeta{URL: u, Error: "no configs found"})
			continue
		}
		rec := subMeta{URL: u}
		if meta != nil {
			rec = *meta // already carries URL + Count
		} else {
			rec.Count = len(lines)
		}
		subs = append(subs, rec)
		entries := parseConfigLines(strings.Join(lines, "\n"))
		allEntries = append(allEntries, entries...)
	}
	// Reconcile subscription info NOW (before any early return).
	state.mu.Lock()
	for i := range state.tabs {
		if state.tabs[i].ID == tabID {
			state.tabs[i].Subs = subs
			break
		}
	}
	state.mu.Unlock()

	// Read each file fresh from its on-disk path (streamed line by line, so
	// peak memory is one line even for multi-hundred-MB dumps).
	updatedFiles := make([]TabFile, len(files))
	copy(updatedFiles, files)
	for i := range updatedFiles {
		f := &updatedFiles[i]
		if f.Path == "" {
			fmt.Fprintf(os.Stderr, "⚠ tab %s: file %q has no path, skipping\n", tabID, f.Name)
			continue
		}
		if f.Disabled {
			continue // user switched this file source off
		}
		enabledSources++
		if info, statErr := os.Stat(f.Path); statErr == nil {
			f.Size = info.Size()
			f.Mtime = info.ModTime().Unix()
		}
		entries, err := parseConfigFile(f.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ tab %s read %s: %v\n", tabID, f.Path, err)
			continue
		}
		allEntries = append(allEntries, entries...)
	}

	// GitHub private-repo import (per-tab, via PAT). Appended after URL + file
	// sources; config is read from the live tab.
	state.mu.RLock()
	var ghOwner, ghRepo, ghFile, ghPAT string
	var ghReady bool
	for _, t := range state.tabs {
		if t.ID == tabID {
			ghReady = t.gitHubReady()
			ghOwner, ghRepo, ghFile, ghPAT = t.GitHubOwner, t.GitHubRepo, t.GitHubFile, t.GitHubPAT
			break
		}
	}
	state.mu.RUnlock()
	if ghReady {
		enabledSources++
		ghLines, err := fetchGitHubPATContent(ghOwner, ghRepo, ghFile, ghPAT)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ tab %s GitHub import: %v\n", tabID, err)
			vlog("warning", "tab: GitHub import failed (%s/%s): %v", ghOwner, ghRepo, err)
		} else {
			entries := parseConfigLines(strings.Join(ghLines, "\n"))
			allEntries = append(allEntries, entries...)
			vlog("info", "tab: imported %d config(s) from GitHub %s/%s", len(ghLines), ghOwner, ghRepo)
		}
	}

	if len(allEntries) == 0 {
		if enabledSources == 0 {
			// Every source was explicitly switched off/removed — clear the tab.
			// (Deliberate 2.0 change from 1.10, which kept the old configs here.)
			storeReplace(tabID, nil)
			state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
			loadedSignal(tabID)
			saveTabs()
			return
		}
		// Sources were enabled but yielded nothing (network failure, bad link) —
		// keep existing entries; the reconciled subscription info shows the error.
		fmt.Fprintf(os.Stderr, "⚠ tab %s: no configs fetched, keeping existing\n", tabID)
		state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
		loadedSignal(tabID)
		return
	}

	// Read the dedup mode. The exclude filter is NO LONGER applied here — it's a
	// view filter (memWindow drops matching configs on read), so every fetched
	// config is kept and toggling the filter re-filters instantly without a
	// re-fetch. Only "delete" dedup still removes rows server-side.
	state.mu.RLock()
	var dedupMode string
	for _, t := range state.tabs {
		if t.ID == tabID {
			dedupMode = t.DedupMode
			break
		}
	}
	state.mu.RUnlock()

	if dedupMode == "delete" {
		allEntries = dedupByBody(allEntries)
	}

	// Rename duplicate display names so each entry is uniquely identifiable.
	disambiguateNames(allEntries)

	// Re-index
	for i, e := range allEntries {
		e.Index = i
	}
	state.mu.Lock()
	// Persist updated file metadata back to the tab (size/mtime refresh).
	for i := range state.tabs {
		if state.tabs[i].ID == tabID {
			state.tabs[i].SourceFiles = updatedFiles
			break
		}
	}
	state.mu.Unlock()
	addedN, removedN, addedIdx := reloadDelta(tabID, allEntries) // reads the OLD DB set first
	// Serve the UI from memory immediately, THEN write to SQLite. Crucially clear
	// the "fetching" flag BEFORE the (slow, 200k-row) write: the configs are
	// already in memory + on screen, so the tab is not "loading" from the user's
	// POV — switching away and back mustn't show a phantom spinner while the disk
	// write finishes in the background. (The defer's delete becomes a no-op.)
	memReplace(tabID, allEntries)
	state.mu.Lock()
	delete(state.fetching, tabID)
	state.mu.Unlock()
	loadedSignal(tabID)
	broadcastReloadDelta(tabID, addedN, removedN, addedIdx)
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	dbPersist(tabID, allEntries)
	saveTabs()
	// Re-ping this tab if it's in the auto-connect pool (entries were rebuilt
	// with no ping data). No-ops when auto-connect is off.
	go autoPingAfterRefresh(tabID)
	// Hint at returning the parse spike's memory to the OS right away.
	debug.FreeOSMemory()
}
