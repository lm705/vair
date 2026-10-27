package core

import "net"

// AppVersion is the running build version (Settings → Updates row).
func AppVersion() string { return appVersion }

// DataDir is the app data directory (Settings → Data → Open folder).
func DataDir() string { return dataDirPath() }

// ProxyOpenToLAN reports whether the proxy ports listen beyond this machine —
// "Allow LAN access" or a custom address — and so need an inbound firewall
// rule for other devices to reach them.
func ProxyOpenToLAN() bool {
	ip := net.ParseIP(proxyBindHost())
	return ip != nil && !ip.IsLoopback()
}

// ProxyPorts returns the user-facing HTTP and SOCKS proxy ports (for the shell's
// firewall rule covering the LAN-shared proxy).
func ProxyPorts() (int, int) { return currentProxyPorts() }

// ResetTotalStats zeroes the persisted lifetime traffic counters (the
// Settings → Statistics "reset total" button) and pushes fresh stats.
func ResetTotalStats() {
	settingsMu.Lock()
	appSettings.StatsTotalUp = 0
	appSettings.StatsTotalDown = 0
	settingsMu.Unlock()
	saveSettings()
	state.broadcast(SSEEvent{Type: "stats_update", Payload: statsSnapshot(nil)})
}

// GetSettings returns a snapshot of the current app settings.
func GetSettings() AppSettings {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings
}

// keepProgramOwned copies into a settings save the fields the program keeps
// itself, which no settings screen edits.
//
// A save carries the whole object, built from the copy a window loaded — and
// nothing tells a window when the program changes one of these: a star, the
// last connection, "a session was active", the mode picked on the main window,
// a new remote-access key, the service-check list. So a window opened before
// such a change sent the old value back with whatever setting was actually
// changed, and the program's own record went back in time: a star vanished,
// "restore the connection on start" restored nothing or the wrong server, AUTO
// came up in the mode the pill no longer showed, a freshly issued remote key
// was replaced by the one it had just revoked.
func keepProgramOwned(s *AppSettings, cur AppSettings) {
	s.StatsTotalUp, s.StatsTotalDown = cur.StatsTotalUp, cur.StatsTotalDown
	s.AutoConnect, s.AutoSwitch, s.AutoConnectOnStart = cur.AutoConnect, cur.AutoSwitch, cur.AutoConnectOnStart
	s.Favorites = cur.Favorites
	s.LastConnectedRaw, s.LastConnectedMode, s.LastSessionActive = cur.LastConnectedRaw, cur.LastConnectedMode, cur.LastSessionActive
	s.LastConnectedChain = cur.LastConnectedChain
	s.SelectedMode = cur.SelectedMode
	s.RemoteToken = cur.RemoteToken
	s.UnlockServices = cur.UnlockServices
	s.UpdateDismissedVersion = cur.UpdateDismissedVersion
	s.ConnTracking = cur.ConnTracking
	s.SettingsRev = cur.SettingsRev
}

// SetSettings applies a generic settings save and persists it. It never flips the
// AUTO master toggle (that's a dedicated AutoService action) and preserves
// server-authoritative traffic counters. Returns the applied settings; the shell
// layer reconciles autostart / deeplink (Windows-registry side effects) by
// comparing the result against the previous GetSettings().
// SetSelectedMode records the proxy/tun pill on the main window.
//
// One field, not a settings save. The pill used to write the whole settings
// object back, built from whatever the window had loaded — and a click before
// that load finished sent an object with every other field at its zero value.
// SetSettings replaces wholesale, so that was every setting reset at once, and
// the firewall block released along with them because the switch that asked for
// it had just gone from true to false. A setter that can only touch one field
// cannot do any of that.
func SetSelectedMode(mode string) {
	if mode != "proxy" && mode != "tun" {
		return
	}
	settingsMu.Lock()
	changed := appSettings.SelectedMode != mode
	appSettings.SelectedMode = mode
	settingsMu.Unlock()
	if changed {
		saveSettings()
	}
}

func SetSettings(s AppSettings) AppSettings {
	settingsMu.Lock()
	keepProgramOwned(&s, appSettings)
	sanitizeFragmentSettings(&s, appSettings)
	sanitizeTestURLs(&s, appSettings)
	sanitizeAutoSettings(&s)
	// The ranking mode is the setting; the two old booleans are kept in step so a
	// downgrade lands on the nearest equivalent instead of the default.
	syncAutoRankLegacy(&s)
	appSettings = s
	settingsMu.Unlock()
	saveSettings()
	// Opening/closing the log file is a side effect of the save, not of a
	// connection — apply it here so the toggle takes effect immediately.
	SetFileLogging(s.LogToFile)
	// Every window, not just the one that made the change.
	broadcastSettings(s)
	return s
}
