package core

// This file exposes the "add / edit / view config" flow to the shell bindings.
// A manual config is just a share-link URL built from the form (formToURL) and
// fed through the normal parse/store path, so it behaves exactly like an
// imported one. See serialize.go for the (tested) form↔URL conversion.

// ConfigURLResult carries a built share-link URL or a validation error string
// (errors are returned in-band, not as exceptions, so the modal can show a live
// preview / inline error while typing).
type ConfigURLResult struct {
	URL   string `json:"url"`
	Error string `json:"error,omitempty"`
}

// ConfigFormResult carries a form prefilled from an existing config, or an error.
type ConfigFormResult struct {
	Form  ConfigForm `json:"form"`
	Error string     `json:"error,omitempty"`
}

// BuildConfigURL serialises a form into its share-link URL (for the live preview
// and validation). It also round-trips through parseNode so an invalid
// combination is caught before the user hits Add.
func BuildConfigURL(f ConfigForm) ConfigURLResult {
	u, err := formToURL(f)
	if err != nil {
		return ConfigURLResult{Error: err.Error()}
	}
	if _, err := parseNode(u); err != nil {
		return ConfigURLResult{Error: "the config didn't validate: " + err.Error()}
	}
	return ConfigURLResult{URL: u}
}

// ParseConfigToForm parses an existing raw config into a form (edit / view
// prefill). Returns an error string for a config the form model can't express.
func ParseConfigToForm(raw string) ConfigFormResult {
	n, err := parseNode(raw)
	if err != nil {
		return ConfigFormResult{Error: err.Error()}
	}
	f := configToForm(n)
	if f.Protocol == "" {
		return ConfigFormResult{Error: "this config type can't be edited in the form"}
	}
	return ConfigFormResult{Form: f}
}

// AddManualConfig builds the URL and appends it to a user tab (never the Sources
// tab). Returns "" on success or an error string. The caller (frontend) ensures
// tabID is a user tab — Add is offered only there.
func AddManualConfig(tabID string, f ConfigForm) string {
	if tabID == "" || tabID == "main" {
		return "configs can't be added to the Sources tab"
	}
	u, err := formToURL(f)
	if err != nil {
		return err.Error()
	}
	if _, err := parseNode(u); err != nil {
		return "the config didn't validate: " + err.Error()
	}
	// Not Paste: that scans arbitrary text for things that look like node URLs,
	// and the scanner deliberately ignores http:// — every subscription link and
	// half the comments in a config file start with one, and treating those as
	// nodes would turn a pasted subscription into hundreds of imaginary proxies.
	// A URL this function built a line ago needs no scanning; adding it directly
	// is both correct and the only way an http proxy can be added at all.
	if addBuiltConfig(tabID, u) == 0 {
		return "the config was built but could not be stored — the tab may be missing"
	}
	return ""
}

// addBuiltConfig appends one already-validated URL to a tab, with the same
// bookkeeping a paste does: indices, the active-tab mirror, the window cache and
// the persist. Returns how many were added (0 or 1).
func addBuiltConfig(tabID, raw string) int {
	n, err := parseNode(raw)
	if err != nil || n == nil {
		return 0
	}
	e := &ConfigEntry{Raw: raw, PingStatus: StatusPending, Delay: -1, SpeedStatus: StatusPending}
	fillEntryFromNode(e, n)
	BeginPaste(tabID)
	state.mu.Lock()
	e.Index = nextEntryIndexLocked(tabID)
	state.tabEntries[tabID] = append(state.tabEntries[tabID], e)
	if state.activeTab == tabID {
		state.entries = state.tabEntries[tabID]
	}
	state.mu.Unlock()
	memInvalidate(tabID)
	EndPaste(tabID)
	return 1
}

// UpdateConfig replaces config idx in a user tab with a freshly serialised URL
// (the edit flow). Rebuilds the entry's display fields from the new raw, resets
// its stale test results, migrates raw-keyed references (last-connected / live
// connection), persists the whole tab, and emits entry_update. Returns "" on
// success or an error string.
func UpdateConfig(tabID string, idx int, f ConfigForm) string {
	if tabID == "" || tabID == "main" {
		return "Sources-tab configs can't be edited"
	}
	newRaw, err := formToURL(f)
	if err != nil {
		return err.Error()
	}
	// parseNode, not the list scanner: the scanner searches arbitrary text for
	// things that look like node URLs and deliberately ignores http://, so an
	// http proxy could never be saved no matter what was typed into the form —
	// and the message blamed the address, which was never the problem.
	n, err := parseNode(newRaw)
	if err != nil {
		return "the config didn't validate: " + err.Error()
	}
	ne := &ConfigEntry{Raw: newRaw}
	fillEntryFromNode(ne, n)
	entry, ok := memEntry(tabID, idx)
	if !ok {
		return "config not found"
	}
	entry.mu.Lock()
	oldRaw := entry.Raw
	entry.Raw = ne.Raw
	entry.Name = ne.Name
	entry.Host, entry.Port = ne.Host, ne.Port
	entry.Network, entry.Security, entry.Protocol = ne.Network, ne.Security, ne.Protocol
	// The server/params changed, so any prior ping/speed result is stale.
	entry.PingStatus, entry.Delay, entry.PingErr = StatusPending, 0, ""
	entry.SpeedStatus, entry.SpeedMBps, entry.SpeedErr, entry.SpeedLive = StatusPending, 0, "", 0
	entry.mu.Unlock()

	// Migrate raw-keyed references so an edit of the connected config doesn't
	// orphan them (mirrors RenameEntry).
	if oldRaw != ne.Raw {
		settingsMu.Lock()
		if appSettings.LastConnectedRaw == oldRaw {
			appSettings.LastConnectedRaw = ne.Raw
		}
		settingsMu.Unlock()
		cm := state.conn
		cm.mu.Lock()
		if cm.state.ConnRaw == oldRaw {
			cm.state.ConnRaw = ne.Raw
		}
		for i, cr := range cm.state.ChainRaws {
			if cr == oldRaw {
				cm.state.ChainRaws[i] = ne.Raw
			}
		}
		cm.mu.Unlock()
	}

	memInvalidate(tabID)
	dbPersist(tabID, loadTabEntries(tabID)) // whole-tab write (host/port/etc. columns)
	state.broadcast(SSEEvent{Type: "entry_update", Payload: entryPayload(entry.snap()), Tab: tabID})
	loadedSignal(tabID)
	saveTabs()
	saveSettings()
	return ""
}
