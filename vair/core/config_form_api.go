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
	if Paste(tabID, u) == 0 {
		return "failed to add the config"
	}
	return ""
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
	built := parseConfigLines(newRaw)
	if len(built) == 0 {
		return "the config didn't validate"
	}
	ne := built[0]
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
	state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	loadedSignal(tabID)
	saveTabs()
	saveSettings()
	return ""
}
