package main

import (
	"os"
	"path/filepath"

	"vair/core"
)

// TabService backs the tab bar. Live changes also arrive via the "tabs_update"
// and "active_tab" Wails events.
type TabService struct{}

// List returns all tabs with their config counts.
func (t *TabService) List() []core.TabDTO { return core.Tabs() }

// Active returns the active tab id.
func (t *TabService) Active() string { return core.ActiveTab() }

// Switch makes id the active tab; returns whether a fetch is in flight for it
// (the UI shows the spinner from this truth — no stale-cache race).
func (t *TabService) Switch(id string) bool { return core.SwitchTab(id) }

// Create adds a new user tab and returns it.
func (t *TabService) Create() core.TabDTO { return core.CreateTab() }

// Delete removes a user tab (and its configs).
func (t *TabService) Delete(id string) { core.DeleteTab(id) }

// Rename renames a tab.
func (t *TabService) Rename(id string, name string) { core.RenameTab(id, name) }

// Detail returns the full tab (sources, subscription info, filters) for the
// tab-settings modal.
func (t *TabService) Detail(id string) core.Tab { return core.TabDetail(id) }

// SourcesInfo returns the built-in SOURCES tab URLs (read-only list in the
// Sources settings modal).
func (t *TabService) SourcesInfo() []string { return core.SourcesInfo() }

// Reorder applies a new tab order (drag-reorder in the tab bar).
func (t *TabService) Reorder(ids []string) { core.ReorderTabs(ids) }

// AddSourceURL appends a subscription URL to a tab's sources and re-fetches
// (QR scans / pasted subscription links / vair:// deep links). Returns false
// when it was already present.
func (t *TabService) AddSourceURL(id, url string) bool { return core.AddTabSourceURL(id, url) }

// SetSettings applies the tab-settings modal state (live apply — re-fetches
// sources when they changed, etc.; the 1.10 /api/tab/set-url).
func (t *TabService) SetSettings(id string, req core.TabSettingsReq) {
	core.SetTabSettings(id, req)
}

// PickFiles opens the native multi-select file dialog and returns the picked
// files' metadata (name/path/size/mtime) for the tab-settings Files list.
func (t *TabService) PickFiles() []core.TabFile {
	dlg := theApp.Dialog.OpenFile().
		SetTitle("Vair — add config files").
		CanChooseFiles(true).
		AddFilter("Config lists (*.txt)", "*.txt").
		AddFilter("All files (*.*)", "*.*")
	paths, err := dlg.PromptForMultipleSelection()
	if err != nil || len(paths) == 0 {
		return nil
	}
	out := make([]core.TabFile, 0, len(paths))
	for _, p := range paths {
		f := core.TabFile{Name: filepath.Base(p), Path: p}
		if info, err := os.Stat(p); err == nil {
			f.Size = info.Size()
			f.Mtime = info.ModTime().Unix()
		}
		out = append(out, f)
	}
	return out
}
