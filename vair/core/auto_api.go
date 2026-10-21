package core

// AutoEnabled reports whether the auto-connect master toggle is on.
func AutoEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.AutoConnect
}

// SetAuto flips the auto-connect master toggle. Enabling arms the supervisor to
// connect to the fastest working config (and tests the candidate pool); disabling
// disconnects any live connection. Mirrors the 1.10 handleSettings?auto=1 path.
func SetAuto(enabled bool) {
	settingsMu.Lock()
	was := appSettings.AutoConnect
	appSettings.AutoConnect = enabled
	settingsMu.Unlock()
	// The ranking mode is the setting. The two pre-2.6 booleans are kept in step
	// with it for a downgrade and must not be read as if they were the source —
	// one write path that forgets to sync them and this silently stops measuring
	// speed in the modes that rank by it.
	withSpeed := rankModeUsesSpeed()

	if enabled && !was {
		autoWant.Store(true)
		if ecs := state.conn.snap(); ecs.Status == ConnConnected {
			autoProbeNow.Store(true)
			broadcastAuto("health", ecs.EntryName, ecs.ConnRaw, "")
		} else {
			broadcastAuto("idle", "", "", "")
		}
		go autoTestTabs(autoPool(), withSpeed)
	}
	if !enabled && was {
		autoWant.Store(false)
		cm := state.conn
		cm.mu.Lock()
		orig := cm.state.Status
		if orig != ConnIdle && orig != ConnDisconnecting {
			cm.state.Status = ConnDisconnecting
		}
		s := cm.state
		cm.mu.Unlock()
		if orig != ConnIdle && orig != ConnDisconnecting {
			state.broadcast(SSEEvent{Type: "conn_update", Payload: s})
			go stopConnection()
		}
	}
	autoKick()
	saveSettings()
}

// AutoSwitch forces an immediate failover/connect on the supervisor's next tick
// (the panel's "Switch now"). No-op (returns false) when auto is off.
func AutoSwitch() bool {
	settingsMu.RLock()
	on := appSettings.AutoConnect
	settingsMu.RUnlock()
	if !on {
		return false
	}
	autoWant.Store(true)
	autoForce.Store(true)
	autoKick()
	return true
}

// AutoCandDTO is one row of the AUTO panel's ranked candidate list.
type AutoCandDTO struct {
	Name    string  `json:"name"`
	Raw     string  `json:"raw"`
	Tab     string  `json:"tab"`
	Delay   int64   `json:"delay"`
	Status  Status  `json:"status"`
	Speed   float64 `json:"speed_mbps"`
	Current bool    `json:"current"`
	// Stable marks a row whose place in the order came from its measurement
	// history rather than from a single test. Without it, a ranking mode that
	// puts a 100 ms server above an 80 ms one — because the 80 answers two
	// times in three — reads as a bug.
	Stable bool `json:"stable"`
	// CooldownSec is how long AUTO will keep skipping this config after a
	// failure, or 0. The list showed cooled-down configs in their ranked place
	// with nothing to say they were being skipped, so the top of it could be a
	// config the supervisor would not touch for another twenty minutes.
	CooldownSec int `json:"cooldown_sec"`
}

// AutoCandidates returns the ranked candidate pool the supervisor would choose
// from (same ordering as autoCandidates) — ported from handleAutoCandidates.
func AutoCandidates() []AutoCandDTO {
	cs := state.conn.snap()
	connBody := ""
	if cs.Status == ConnConnected {
		// Match the connected config by node BODY, not the full raw: a source
		// refresh re-runs disambiguateNames, which rewrites the raw's #name
		// fragment (e.g. "USA - 2" → "USA - 3"), so raw equality silently loses
		// the highlight after every refresh. The body (uuid@host:port?...) is
		// what actually identifies the connection.
		connBody = nodeBody(cs.ConnRaw)
	}
	out := []AutoCandDTO{}
	for _, c := range autoCandidates(autoPool(), "", nil) {
		es := c.entry.snap()
		out = append(out, AutoCandDTO{
			Name:        autoLabel(c.entry),
			Raw:         es.Raw,
			Tab:         c.tabID,
			Delay:       es.Delay,
			Status:      es.PingStatus,
			Speed:       es.SpeedMBps,
			Current:     connBody != "" && nodeBody(es.Raw) == connBody,
			Stable:      c.stable,
			CooldownSec: int(cooldownLeft(es.Raw).Seconds()),
		})
	}
	return out
}

// AutoConnectCand connects to a specific candidate chosen in the AUTO panel
// (by raw URL, across tabs). Arms auto-want but marks the connection user-owned
// so the supervisor won't switch away on its own. Ported from
// handleAutoConnectCand; returns false when the candidate isn't in the pool.
func AutoConnectCand(raw, mode string) bool {
	if raw == "" {
		return false
	}
	var found *autoCand
	for _, c := range autoCandidates(autoPool(), "", nil) {
		if c.entry.snap().Raw == raw {
			cc := c
			found = &cc
			break
		}
	}
	if found == nil {
		return false
	}
	// An empty mode means "whatever AUTO would use" — which is the only answer
	// that makes sense from AUTO's own panel, and the one the panel now asks
	// for. It used to pass the proxy/tun pill from the main window instead, so
	// a panel whose Connection mode said TUN brought servers up in proxy: two
	// controls on screen, one of them ignored, and the one being ignored was
	// the one in the same window as the click.
	if mode != string(ModeProxy) && mode != string(ModeTUN) {
		mode = string(autoEffectiveMode())
	}
	autoWant.Store(true)
	autoManaged.Store(false)
	autoLiveRtt.Store(0)
	autoProbeNow.Store(true)
	// The *OnTab connect functions require the caller to hold cm.actionMu (they
	// don't lock themselves — only the public startProxyConnection wrapper does).
	// Take it here so this user-driven connect serialises against the supervisor
	// and other connect/disconnect flows, exactly like Connect.
	go func() {
		cm := state.conn
		cm.actionMu.Lock()
		defer cm.actionMu.Unlock()
		if mode == "tun" {
			startTUNConnectionOnTab(found.entry, found.tabID)
		} else {
			startProxyConnectionOnTab(found.entry, found.tabID)
		}
	}()
	return true
}

// ReloadPool re-fetches the sources of every tab in the auto-connect candidate
// pool (the AUTO panel's reload button) — the same semantics as the header
// RELOAD, applied to each pool tab.
func ReloadPool() {
	for _, id := range autoPool() {
		Reload(id)
	}
}

// StartBackground launches the auto-connect supervisor, the log flusher, the
// per-tab source auto-refresh loop, and the startup fetch of the SOURCES tab
// (1.10 main.go did `go fetchAndInit()` on every launch — without it the
// Sources list stays empty until a manual RELOAD). The shell calls it once
// the engines are extracted.
func StartBackground() {
	// Auto-connect implies connect-on-startup (1.10 main.go): if the feature is
	// on, arm intent NOW so the supervisor actually connects once entries load
	// and the startup ping ranks them. Without this the startup test ran (via
	// test-after-refresh) but the supervisor stayed idle — autoWant is otherwise
	// only set by the SetAuto toggle / "Switch now".
	settingsMu.RLock()
	autoOn := appSettings.AutoConnect
	toFile := appSettings.LogToFile
	settingsMu.RUnlock()
	if autoOn {
		autoWant.Store(true)
	}
	SetFileLogging(toFile) // re-open the log file if the user left it on
	go startAutoSupervisor()
	go logFlushLoop()     // flush batched engine/app logs to the "log" event
	go startAutoRefresh() // per-tab RefreshMin source refresh (1.10 routes.go)
	// Refresh SOURCES at launch, but SILENTLY when it already has persisted
	// configs (loaded from the store) — a background re-download shouldn't blank
	// the list with a spinner. A first-ever launch (empty) shows the spinner.
	go fetchAndInitSilent(memTabCount("main") > 0)
}
