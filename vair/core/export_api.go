package core

// Settings export / import — ported verbatim from the 1.10 handlers
// (buildSettingsExport / handleSettingsExport / handleSettingsImport),
// minus the HTTP layer. The shell provides the file dialogs.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
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
		loaded := t.SourceLoaded
		pt.SourceLoaded = &loaded
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

// ExportSettingsFilename is the name the save dialog suggests
// ("vair_settings_YYYYMMDD_HHMMSS.json").
func ExportSettingsFilename() string {
	return fmt.Sprintf("vair_settings_%s.json", time.Now().Format("20060102_150405"))
}

// BackupProgress is how far an export or an import has got. With a few
// hundred thousand configurations either takes a while, and the buttons gave
// no sign of working or of having finished.
//
// Op is "export" or "import"; Phase is what is being done now — "read" (the
// file, which has no count), "parse" (the configurations, read back), "save"
// (written to the database) or "write" (the export file). Done and Total
// count configurations within the phase. Path is set once the work is over;
// Error, if it failed. Seq orders the updates, which can arrive out of order
// (see AutoSweepProgress).
type BackupProgress struct {
	Op      string `json:"op"`
	Phase   string `json:"phase,omitempty"`
	Running bool   `json:"running"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Path    string `json:"path,omitempty"`
	Error   string `json:"error,omitempty"`
	Seq     uint64 `json:"seq"`
}

var backupProg struct {
	sync.Mutex
	now BackupProgress
	seq uint64
}

// BackupProgressNow is the current state, for a settings window opened in the
// middle of an export or an import; every change after that arrives as a
// backup_progress event.
func BackupProgressNow() BackupProgress {
	backupProg.Lock()
	defer backupProg.Unlock()
	return backupProg.now
}

// backupProgressSeen, when set, sees every update as it is sent — for tests,
// which have no window to receive the events.
var backupProgressSeen func(BackupProgress)

func setBackupProgress(p BackupProgress) {
	backupProg.Lock()
	backupProg.seq++
	p.Seq = backupProg.seq
	backupProg.now = p
	seen := backupProgressSeen
	backupProg.Unlock()
	if seen != nil {
		seen(p)
	}
	state.broadcast(SSEEvent{Type: "backup_progress", Payload: p})
}

// backupStep is how many configurations pass between two progress updates:
// about one per cent — enough to move a bar smoothly, few enough events not to
// matter.
func backupStep(total int) int {
	if s := total / 100; s > 1 {
		return s
	}
	return 1
}

// backupRunning keeps an export and an import from running at once: an import
// replaces the very tabs an export is reading.
var backupRunning atomic.Bool

// ExportSettingsToFile writes the export document to path, configuration by
// configuration, reporting progress as it goes. The document is the one
// buildSettingsExport describes — the same file the import reads — but it is
// streamed to disk rather than built whole in memory first, and written next
// to the target and moved into place at the end, so a failure part-way never
// leaves a truncated export under the name the user chose.
func ExportSettingsToFile(path string) (err error) {
	if !backupRunning.CompareAndSwap(false, true) {
		return fmt.Errorf("an export or an import is already running")
	}
	defer backupRunning.Store(false)

	doc := buildSettingsExport()
	total := 0
	for _, t := range doc.Tabs {
		total += len(t.Configs)
	}
	setBackupProgress(BackupProgress{Op: "export", Phase: "write", Running: true, Total: total})
	defer func() {
		if err != nil {
			setBackupProgress(BackupProgress{Op: "export", Total: total, Error: err.Error()})
		} else {
			setBackupProgress(BackupProgress{Op: "export", Done: total, Total: total, Path: path})
		}
	}()

	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	step := backupStep(total)
	err = writeSettingsExport(w, doc, func(done int) {
		if done%step == 0 && done < total {
			setBackupProgress(BackupProgress{Op: "export", Phase: "write", Running: true, Done: done, Total: total})
		}
	})
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
	}
	return err
}

// writeSettingsExport writes doc as JSON, indented as before, calling progress
// after every configuration written. Each tab is encoded without its
// configurations, which are then written one at a time into the same object —
// a tab of a hundred thousand of them is otherwise one indivisible step.
func writeSettingsExport(w io.Writer, doc settingsExport, progress func(done int)) error {
	var err error
	put := func(b []byte) {
		if err == nil {
			_, err = w.Write(b)
		}
	}
	str := func(s string) []byte {
		b, _ := json.Marshal(s)
		return b
	}
	settings, merr := json.MarshalIndent(doc.AppSettings, "  ", "  ")
	if merr != nil {
		return merr
	}
	put([]byte(fmt.Sprintf("{\n  \"version\": %d,\n  \"exported_at\": %s,\n  \"app\": %s,\n  \"app_settings\": ",
		doc.Version, str(doc.ExportedAt), str(doc.AppName))))
	put(settings)
	put([]byte(",\n  \"tabs\": ["))
	done := 0
	for i, t := range doc.Tabs {
		configs := t.Configs
		t.Configs = nil // omitted (omitempty), written below
		tb, merr := json.MarshalIndent(t, "    ", "  ")
		if merr != nil {
			return merr
		}
		if i > 0 {
			put([]byte(","))
		}
		put([]byte("\n    "))
		if len(configs) == 0 {
			put(tb)
			continue
		}
		// The object without its closing brace, the configurations, the brace.
		body := bytes.TrimRight(bytes.TrimSuffix(bytes.TrimRight(tb, " \n"), []byte("}")), " \n")
		put(body)
		put([]byte(",\n      \"configs\": ["))
		for j, raw := range configs {
			if j > 0 {
				put([]byte(","))
			}
			put([]byte("\n        "))
			put(str(raw))
			if err != nil {
				return err
			}
			done++
			if progress != nil {
				progress(done)
			}
		}
		put([]byte("\n      ]\n    }"))
	}
	if len(doc.Tabs) > 0 {
		put([]byte("\n  "))
	}
	put([]byte("]\n}\n"))
	return err
}

// ImportSettings applies a settingsExport JSON document in place of the
// current state (1.10 handleSettingsImport). includeTabs=false imports only
// the app settings, leaving the user's tabs untouched. Returns "" on success
// or a human-readable error.
func ImportSettings(body []byte, includeTabs bool) string {
	return importSettings(body, includeTabs, "")
}

// parseImportedTabs reads back the configurations of every tab but the main
// one, reporting progress as it goes (once with done=0, then about every one
// per cent). A tab's configurations are parsed in slices, so the count moves
// along a tab of a hundred thousand rather than jumping once at its end; the
// raw strings are let go tab by tab as they are parsed. Returns the entries by
// tab and how many configurations there were.
func parseImportedTabs(tabs []persistedTab, progress func(done, total int)) (map[string][]*ConfigEntry, int) {
	total := 0
	for _, pt := range tabs {
		if pt.ID != "main" {
			total += len(pt.Configs)
		}
	}
	step := backupStep(total)
	if progress != nil {
		progress(0, total)
	}
	const slice = 2000
	out := map[string][]*ConfigEntry{}
	done := 0
	for ti := range tabs {
		pt := &tabs[ti]
		if pt.ID == "main" {
			continue
		}
		var ents []*ConfigEntry
		base := done
		for i := 0; i < len(pt.Configs); i += slice {
			end := i + slice
			if end > len(pt.Configs) {
				end = len(pt.Configs)
			}
			ents = append(ents, parseConfigLines(strings.Join(pt.Configs[i:end], "\n"))...)
			before := done
			done = base + end
			if progress != nil && done/step != before/step {
				progress(done, total)
			}
		}
		// Each slice numbers its own entries from zero; the tab numbers them.
		for i, e := range ents {
			e.Index = i
		}
		out[pt.ID] = ents
		pt.Configs = nil
	}
	// The last stretch may not have filled a step; the count ends at the total.
	if progress != nil && total > 0 && done/step == (done-1)/step {
		progress(total, total)
	}
	return out, total
}

// ImportSettingsFromFile is ImportSettings for a file on disk, with progress
// from the moment the file starts being read.
func ImportSettingsFromFile(path string, includeTabs bool) string {
	if !backupRunning.CompareAndSwap(false, true) {
		return "an export or an import is already running"
	}
	defer backupRunning.Store(false)
	setBackupProgress(BackupProgress{Op: "import", Phase: "read", Running: true})
	body, err := os.ReadFile(path)
	if err != nil {
		setBackupProgress(BackupProgress{Op: "import", Error: err.Error()})
		return err.Error()
	}
	return importSettings(body, includeTabs, path)
}

func importSettings(body []byte, includeTabs bool, path string) (res string) {
	setBackupProgress(BackupProgress{Op: "import", Phase: "read", Running: true})
	total := 0
	defer func() {
		if res != "" {
			setBackupProgress(BackupProgress{Op: "import", Total: total, Error: res})
		} else {
			setBackupProgress(BackupProgress{Op: "import", Done: total, Total: total, Path: path})
		}
	}()
	var imp settingsExport
	// The most hand-carried file of the three: an export is mailed, edited and
	// re-saved, and a mark picked up on the way would make it "not valid JSON".
	if err := json.Unmarshal(trimBOM(body), &imp); err != nil {
		return "parse JSON: " + err.Error()
	}
	if imp.Version == 0 || imp.Version > settingsExportVersion {
		return fmt.Sprintf("unsupported export version %d (expected %d)", imp.Version, settingsExportVersion)
	}
	if includeTabs && len(imp.Tabs) == 0 {
		return "no tabs in export"
	}

	// The configurations are read back before anything is replaced, outside
	// every lock: on a big export this is most of the work, and it used to be
	// done holding the state lock, which stopped the table for all of it.
	var imported map[string][]*ConfigEntry // tabID → parsed configs
	if includeTabs {
		imported, total = parseImportedTabs(imp.Tabs, func(done, total int) {
			setBackupProgress(BackupProgress{Op: "import", Phase: "parse", Running: true, Done: done, Total: total})
		})
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

	// Replace app settings (atomic on disk + in memory). The file goes through
	// the checks a settings save makes — it was taken whole, so a value the
	// window refuses (a fragment length of 0, a test address without a scheme)
	// arrived from a file unchecked. Two things stay this machine's own: its
	// traffic totals, and the session mark — the import has just ended the
	// connection, and the mark carried in the file was the other computer's,
	// which made the next start restore a session this one never had.
	settingsMu.Lock()
	next := imp.AppSettings
	sanitizeFragmentSettings(&next, appSettings)
	sanitizeTestURLs(&next, appSettings)
	next.StatsTotalUp, next.StatsTotalDown = appSettings.StatsTotalUp, appSettings.StatsTotalDown
	next.LastSessionActive = false
	// A file exported by an older version is brought up to date as one read
	// at a start would be.
	migrateSettings(&next)
	appSettings = next
	settingsMu.Unlock()
	saveSettings()
	// Every window, the detached AUTO panel included, draws from these.
	broadcastSettings(next)

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
		if pt.SourceLoaded != nil {
			tab.SourceLoaded = *pt.SourceLoaded
		} else {
			tab.SourceLoaded = len(urls) > 0 || len(pt.SourceFiles) > 0 || tab.gitHubReady()
		}
		state.tabs = append(state.tabs, tab)
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
	// Import rebuilds everything: wipe the store, then write each tab's configs,
	// counting them as they go into the database.
	if store != nil {
		store.deleteAll()
		step := backupStep(total)
		saved := 0
		lastStep := 0
		setBackupProgress(BackupProgress{Op: "import", Phase: "save", Running: true, Total: total})
		for tid, ents := range imported {
			memReplace(tid, ents)
			base := saved
			// The rows go in a batch at a time, so the count jumps; an update
			// goes out whenever it passes into the next step.
			if err := store.rewriteTab(tid, ents, false, func(n int) {
				if done := base + n; done/step != lastStep {
					lastStep = done / step
					setBackupProgress(BackupProgress{Op: "import", Phase: "save", Running: true, Done: done, Total: total})
				}
			}); err != nil {
				vlog("warning", "config store replace %s: %v", tid, err)
			}
			saved += len(ents)
		}
	} else {
		for tid, ents := range imported {
			memReplace(tid, ents)
		}
	}
	saveTabs()

	// Push the new tab list + active tab; the client re-fetches the window.
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	state.broadcast(SSEEvent{Type: "active_tab", Payload: state.activeTab})
	loadedSignal(state.activeTab)
	return ""
}
