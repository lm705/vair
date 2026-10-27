package core

// AutoEnabled reports whether the auto-connect master toggle is on.
func AutoEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.AutoConnect
}

// SetAuto flips the auto-connect master toggle. Enabling arms the supervisor to
// connect to the fastest working config (and tests the candidate pool);
// disabling stops the supervision and leaves the connection as it is.
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
		autoAdminNoted.Store(false) // say it again if the rights are still missing
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
		// The connection stays. Switching AUTO off used to disconnect whatever was
		// up — a server connected by hand included — which neither the switch's
		// description nor the wiki said. Off means no longer supervised; the
		// connection is ended by Disconnect, as any other.
		autoWant.Store(false)
		autoManaged.Store(false)
		// Turning it on is announced above; turning it off has to be as well.
		// The switch lives in the AUTO window, and without this the main
		// window's AUTO button went on showing the mode as on.
		broadcastAuto("off", "", "", "")
	}
	autoKick()
	saveSettings()
}

// AutoSwitch forces an immediate failover/connect on the supervisor's next tick
// (the panel's "Switch now"). Returns why it cannot, or "": a request with
// nothing to act on used to stay armed and fire whenever the pool next had
// configs.
func AutoSwitch() string {
	settingsMu.RLock()
	on := appSettings.AutoConnect
	settingsMu.RUnlock()
	if !on {
		return switchAutoOff
	}
	if !autoPoolHasEntries(autoPool()) {
		return switchNoCandidates
	}
	autoWant.Store(true)
	autoForce.Store(true)
	autoKick()
	return ""
}

// Why "Switch now" did nothing, for the panel.
const (
	switchAutoOff      = "auto-connect is off"
	switchNoCandidates = "there is nothing to switch to: the candidate tabs have no configurations"
)

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
		// Speed is the figure the ranking used: one older than six hours is not
		// counted, and showing it ranked a 12 MB/s row below a 3 MB/s one.
		out = append(out, AutoCandDTO{
			Name:        autoLabel(c.entry),
			Raw:         es.Raw,
			Tab:         c.tabID,
			Delay:       es.Delay,
			Status:      es.PingStatus,
			Speed:       c.speed,
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
	noteUserConnAction()
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
	announceDBRecovery()   // a damaged database found at start: say so
	go startAutoSupervisor()
	go logFlushLoop()     // flush batched engine/app logs to the "log" event
	go startAutoRefresh() // per-tab RefreshMin source refresh (1.10 routes.go)
	refreshSourcesAtStart()
}

// refreshSourcesAtStart: only an EMPTY SOURCES list is fetched at launch —
// directly, since there is nothing to connect with otherwise, and with the
// spinner. A list that has configurations keeps them, and the first session
// refreshes it, silently, through the tunnel (afterSessionUp): the fetch at
// every start went out before any tunnel, from the real address.
func refreshSourcesAtStart() {
	if memTabCount("main") > 0 {
		sourcesDeferred.Store(true)
		loadedSignal("main")
		return
	}
	go fetchAndInitSilent(false)
}
