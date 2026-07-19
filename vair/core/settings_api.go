package core

// AppVersion is the running build version (Settings → Updates row).
func AppVersion() string { return appVersion }

// DataDir is the app data directory (Settings → Data → Open folder).
func DataDir() string { return dataDirPath() }

// TunShareProxyEnabled reports whether the "share proxy over LAN in TUN mode"
// toggle is on (the shell uses it to reconcile the inbound firewall rule).
func TunShareProxyEnabled() bool { return tunShareProxyEnabled() }

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

// SetSettings applies a generic settings save and persists it. It never flips the
// AUTO master toggle (that's a dedicated AutoService action) and preserves
// server-authoritative traffic counters. Returns the applied settings; the shell
// layer reconciles autostart / deeplink (Windows-registry side effects) by
// comparing the result against the previous GetSettings().
func SetSettings(s AppSettings) AppSettings {
	settingsMu.Lock()
	s.StatsTotalUp = appSettings.StatsTotalUp
	s.StatsTotalDown = appSettings.StatsTotalDown
	s.AutoConnect = appSettings.AutoConnect
	appSettings = s
	settingsMu.Unlock()
	saveSettings()
	return s
}
