package services

import (
	"os"

	"vair/core"
	"vair/internal/remote"
	"vair/internal/shell"
)

// SettingsService exposes app settings to the frontend. Set reconciles the
// shell-side toggles (autostart / vair:// scheme) that touch the Windows registry.
type SettingsService struct{}

// Get returns the current settings.
func (s *SettingsService) Get() core.AppSettings {
	return core.GetSettings()
}

// Set applies a settings save and returns the applied settings.
func (s *SettingsService) Set(ns core.AppSettings) core.AppSettings {
	old := core.GetSettings()
	applied := core.SetSettings(ns)
	if applied.AutostartEnabled != old.AutostartEnabled {
		_ = shell.ApplyAutostart(applied.AutostartEnabled)
	}
	if applied.DeepLinkEnabled != old.DeepLinkEnabled {
		_ = shell.RegisterDeepLink(applied.DeepLinkEnabled)
	}
	// Switching the watch on sends one notification straight away, so whether
	// this machine shows them is answered now rather than during an outage.
	if applied.WatchConnection && !old.WatchConnection {
		core.NotifyWatchTurnedOn()
	}
	// Turning the firewall block off means off now, not at the next disconnect —
	// otherwise the switch says one thing and the machine does another.
	if (old.KillSwitchPersistent && !applied.KillSwitchPersistent) ||
		(old.ProxyKillSwitch && !applied.ProxyKillSwitch) {
		core.ReleasePersistentKillSwitch()
	}
	// Theme changed → re-sync the native window/webview background, else a live
	// resize flashes the previous theme's colour at the lagging edges.
	if applied.Theme != old.Theme {
		shell.ApplyWindowTheme()
	}
	// LAN remote-control toggle / port change → restart the server to match
	// (the listener binds the old port, so a port change needs stop+start).
	if applied.RemoteEnabled != old.RemoteEnabled || applied.RemotePort != old.RemotePort {
		remote.Restart()
	}
	// Language change → relabel the tray menu.
	if applied.Language != old.Language {
		shell.RefreshTrayMenu()
	}
	// "Set system proxy" toggle applies instantly to a live proxy connection.
	if applied.NoSystemProxy != old.NoSystemProxy {
		core.ReapplySystemProxy()
	}
	// TUN-mode LAN proxy sharing: reconcile the inbound firewall rule when the
	// toggle, the ports, or the bind address change (so another device can reach
	// the shared proxy). The config-side inbound applies on the next connection.
	if applied.TunShareProxy != old.TunShareProxy || applied.HttpPort != old.HttpPort ||
		applied.SocksPort != old.SocksPort || applied.ProxyBind != old.ProxyBind {
		shell.ApplyTunProxyFirewall()
	}
	return applied
}

// RemoteInfo tells the Settings UI whether remote control is on, its token, the
// port, and the machine's LAN IPs (to build the phone URL + QR).
type RemoteInfo struct {
	Enabled bool     `json:"enabled"`
	Token   string   `json:"token"`
	Port    int      `json:"port"`
	IPs     []string `json:"ips"`
}

// Remote returns the current remote-access info (ensuring a token exists so the
// URL/QR are ready the moment the user flips the toggle on).
func (s *SettingsService) Remote() RemoteInfo {
	enabled, token := core.RemoteConfig()
	if token == "" {
		token = core.EnsureRemoteToken()
	}
	return RemoteInfo{Enabled: enabled, Token: token, Port: remote.Port(), IPs: remote.LocalIPv4s()}
}

// RegenerateRemoteToken replaces the access key (old links/QRs/cookies stop
// working immediately — the server restarts with the new token) and returns the
// fresh info for the UI.
func (s *SettingsService) RegenerateRemoteToken() RemoteInfo {
	core.RegenerateRemoteToken()
	remote.Restart()
	return s.Remote()
}

// Version is the running build version (Settings → Updates).
func (s *SettingsService) Version() string { return core.AppVersion() }

// ResetStats zeroes the lifetime traffic counters.
func (s *SettingsService) ResetStats() { core.ResetTotalStats() }

// NotificationsBlocked returns a sentence when this machine will not show the
// watch's notification (Windows notifications turned off), or "" when it will.
// The settings screen puts it under the switch, where the promise was made.
func (s *SettingsService) NotificationsBlocked() string {
	return core.NotificationsBlockedReason(core.GetSettings().Language)
}

// RuleStats reports how often each line of the user's rule lists has fired.
// Polled by the settings screen while the rules section is open — cheap enough
// (a walk of four short lists) and never needed when it is closed.
func (s *SettingsService) RuleStats() core.RuleStatsDTO { return core.RuleStats() }

// OpenDataFolder shows the app's data directory in the file manager.
func (s *SettingsService) OpenDataFolder() {
	shell.OpenPath(core.DataDir())
}

// Export saves tabs + tab settings + app settings to a JSON file picked in the
// native Save dialog. Returns "" on success, "cancelled", or an error text.
func (s *SettingsService) Export() string {
	data, filename, err := core.ExportSettingsJSON()
	if err != nil {
		return err.Error()
	}
	path, err := shell.App().Dialog.SaveFile().
		SetMessage("Vair — export settings").
		SetFilename(filename).
		AddFilter("JSON (*.json)", "*.json").
		PromptForSingleSelection()
	if err != nil || path == "" {
		return "cancelled"
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err.Error()
	}
	return ""
}

// Import replaces the current state from an exported JSON file picked in the
// native Open dialog. includeTabs=false imports only the app settings.
// Returns "" on success, "cancelled", or an error text.
func (s *SettingsService) Import(includeTabs bool) string {
	paths, err := shell.App().Dialog.OpenFile().
		SetTitle("Vair — import settings").
		CanChooseFiles(true).
		AddFilter("JSON (*.json)", "*.json").
		PromptForMultipleSelection()
	if err != nil || len(paths) == 0 {
		return "cancelled"
	}
	body, err := os.ReadFile(paths[0])
	if err != nil {
		return err.Error()
	}
	res := core.ImportSettings(body, includeTabs)
	if res == "" {
		// Registry-backed toggles may have changed with the imported settings.
		applied := core.GetSettings()
		_ = shell.ApplyAutostart(applied.AutostartEnabled)
		_ = shell.RegisterDeepLink(applied.DeepLinkEnabled)
	}
	return res
}

// ListProcesses returns running process names for the Settings picker.
func (s *SettingsService) ListProcesses() []string {
	return shell.ListRunningProcessNames()
}

// Quit exits the app for real (the titlebar ✕ when minimize-to-tray is off —
// 1.10 semantics). Tears the connection down first so the system proxy is
// never left broken.
func (s *SettingsService) Quit() {
	core.Shutdown()
	shell.App().Quit()
}

// OpenURL opens a link in the user's default browser (the WebView must never
// navigate away from the app).
func (s *SettingsService) OpenURL(url string) {
	if url == "" {
		return
	}
	shell.OpenURL(url)
}

// TakePendingDeepLink returns (and clears) the vair:// URL the app was
// launched with. The frontend pulls it once mounted — race-free startup
// delivery; second-instance links still arrive via the "deeplink" event.
func (s *SettingsService) TakePendingDeepLink() string { return shell.TakePendingDeepLink() }
