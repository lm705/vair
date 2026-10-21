package services

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

// Favorites is every starred configuration, whether or not a tab still holds
// it — a star outlives the configuration it was put on, and this is the only
// place the ones left behind can be seen.
func (c *ConfigService) Favorites() []core.FavoriteDTO { return core.Favorites() }

// RemoveFavorites unstars the given node bodies; returns how many went.
func (c *ConfigService) RemoveFavorites(bodies []string) int { return core.RemoveFavorites(bodies) }

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

// AddWARP enrols a free Cloudflare WARP account and adds it to the tab as a
// WireGuard configuration. Slow (a network round trip), so the frontend shows
// it working; returns the error text for the toast rather than swallowing it.
func (c *ConfigService) AddWARP(tabId string) string {
	// Same target rule as a paste: a config cannot live in the main "Sources"
	// tab, so an empty or main target becomes a user tab.
	if tabId == "" || tabId == "main" {
		tabId = core.EnsureUserTab()
		core.SwitchTab(tabId)
	}
	if _, err := core.AddWARPConfig(tabId); err != nil {
		return err.Error()
	}
	return ""
}

// DeleteDuplicatesOf removes the other copies of one configuration, keeping it.
// Returns how many were removed.
func (c *ConfigService) DeleteDuplicatesOf(idx int) int { return core.DeleteDuplicatesOf(idx) }

// History is everything measured about config idx of the displayed tab — the
// record behind the Stability column. Nil when the row is gone.
func (c *ConfigService) History(idx int) *core.HistoryDTO { return core.ConfigHistory(idx) }

// HistoryRaw is the same record for a configuration that has no row on screen:
// the AUTO panel lists candidates from every candidate tab. Nil when the
// configuration is no longer in any tab.
func (c *ConfigService) HistoryRaw(raw string) *core.HistoryDTO { return core.ConfigHistoryRaw(raw) }
