package main

import "vair/core"

// ConfigService backs the config table — the REAL windowed store (core): rows are
// parsed by the engine, windowed from SQLite/memstore, sorted/filtered server-side.
//
// The read methods take an explicit tabId (the frontend's active tab) instead of
// reading the backend's activeTab: SwitchTab is async, so relying on the backend
// value races the frontend and returns the wrong tab's rows on a switch. The
// per-tab "hide duplicates" dedup mode is applied by core automatically, so the
// frontend never passes a dedup flag.
type ConfigService struct{}

// Count is how many configs in tabId match the filter + type pills (the
// virtualizer sizes to it).
func (c *ConfigService) Count(tabId, filter string, proto []string) int {
	return core.WindowCount(tabId, filter, proto)
}

// Window returns rows [offset, offset+limit) of tabId for the given sort
// ("idx"|"ping"|"speed"), filter and type pills (proto; empty = all).
func (c *ConfigService) Window(tabId, sort, filter string, proto []string, offset, limit int) []core.Row {
	rows, _ := core.Window(tabId, sort, filter, proto, offset, limit)
	return rows
}

// Stats returns the header counters for tabId under the current filter/proto.
func (c *ConfigService) Stats(tabId, filter string, proto []string) core.StatsDTO {
	return core.Stats(tabId, filter, proto)
}

// Reload re-fetches tabId's sources (the header RELOAD button — 1.10
// /api/reload). Paste-only tabs just reset stale ping/speed results.
func (c *ConfigService) Reload(tabId string) {
	core.Reload(tabId)
}

// BuildConfigURL serialises the add/edit form to a share-link URL (live preview
// + validation). Errors come back in-band via ConfigURLResult.Error.
func (c *ConfigService) BuildConfigURL(form core.ConfigForm) core.ConfigURLResult {
	return core.BuildConfigURL(form)
}

// ConfigToForm prefills the form from an existing config (edit / view flow).
func (c *ConfigService) ConfigToForm(raw string) core.ConfigFormResult {
	return core.ParseConfigToForm(raw)
}

// AddConfig appends a manually-built config to a user tab. Returns "" on success
// or an error string.
func (c *ConfigService) AddConfig(tabId string, form core.ConfigForm) string {
	return core.AddManualConfig(tabId, form)
}

// UpdateConfig replaces config idx in a user tab from the edited form. Returns ""
// on success or an error string.
func (c *ConfigService) UpdateConfig(tabId string, idx int, form core.ConfigForm) string {
	return core.UpdateConfig(tabId, idx, form)
}

// Indices returns the FULL ordered index list of tabId's current view (every
// matching row, not just loaded windows) — select-all / shift-range need it.
func (c *ConfigService) Indices(tabId, sort, filter string, proto []string) []int {
	return core.Indices(tabId, sort, filter, proto)
}

// RawsAll returns every matching row's index + raw in screen order
// (select-all → copy the whole filtered set).
func (c *ConfigService) RawsAll(tabId, sort, filter string, proto []string) core.OrderedRaws {
	return core.RawsAll(tabId, sort, filter, proto)
}

// RawsFor returns raws for the given entry indices (shift-range copy over
// rows the windowed client never loaded).
func (c *ConfigService) RawsFor(tabId string, idxs []int) []string {
	return core.RawsForIndices(tabId, idxs)
}

// DeleteFailed removes every config in tabId whose ping OR speed test failed
// (whole tab, server-side). Returns the number of remaining configs.
func (c *ConfigService) DeleteFailed(tabId string) int {
	return core.DeleteFailed(tabId)
}

// ToggleFavorite stars/unstars a config; returns the new state.
func (c *ConfigService) ToggleFavorite(idx int) bool {
	return core.ToggleFavorite(idx)
}

// RenameEntry renames config idx in the active tab; returns false if not allowed.
func (c *ConfigService) RenameEntry(idx int, name string) bool {
	return core.RenameEntry(idx, name)
}

// DeleteEntries removes the given config indices from the active tab.
func (c *ConfigService) DeleteEntries(indices []int) {
	core.DeleteEntries(indices)
}

// BeginPaste resolves the paste target ONCE (configs can't go into the main
// "Sources" tab — ensure/switch to a user tab first), marks it loading, and
// returns its id. Subsequent PasteChunk/EndPaste calls carry this id, so
// switching tabs mid-paste can't reroute the data.
func (c *ConfigService) BeginPaste() string {
	tab := core.ActiveTab()
	if tab == "main" {
		tab = core.EnsureUserTab()
		core.SwitchTab(tab)
	}
	core.BeginPaste(tab)
	return tab
}

// PasteChunk parses one ≤40MB chunk into tabId (in memory); EndPaste persists
// the whole tab with a single write and clears the spinner.
func (c *ConfigService) PasteChunk(tabId, raw string) int { return core.PasteChunk(tabId, raw) }
func (c *ConfigService) EndPaste(tabId string)            { core.EndPaste(tabId) }
