package core

import "sync/atomic"

// Capabilities describes what this build can actually do on this machine, so the
// UI can hide or explain a control instead of offering one that silently fails.
//
// Every field is filled on BOTH platforms. That is the point: the frontend gates
// on capabilities, never on `os === 'windows'`, so a feature that later becomes
// available on Linux (or unavailable on Windows) needs no TSX change.
//
// Zero values are deliberately the pessimistic ones — a field nobody sets reads
// as "not available" rather than as a promise the backend can't keep.
type Capabilities struct {
	// TUN reports whether TUN mode exists on this platform at all. TUNReady is
	// whether it can be started right now; when it can't, TUNBlockReason says
	// why in a token the UI maps to localised text:
	// "no_singbox" | "not_admin" | "unsupported".
	TUN            bool   `json:"tun"`
	TUNReady       bool   `json:"tun_ready"`
	TUNBlockReason string `json:"tun_block_reason,omitempty"`

	// SplitTunnel — per-application routing rules ("apps without VPN") are
	// honoured. ProcessPicker — the running-process list that feeds them can be
	// enumerated. They are separate because a platform can support the rules
	// while having no picker, and an empty picker with no explanation is worse
	// than no picker at all.
	SplitTunnel   bool `json:"split_tunnel"`
	ProcessPicker bool `json:"process_picker"`

	// SystemProxy — the "set system proxy" toggle does something.
	// SystemProxyBackend names what it drives, so the UI can be honest about
	// reach: "wininet" | "gsettings" | "kde" | "env" | "none". On Linux this
	// matters — gsettings and kioslaverc reach GIO apps and browsers set to
	// "use system settings", and nothing else. curl, apt and git read the
	// environment only.
	SystemProxy        bool   `json:"system_proxy"`
	SystemProxyBackend string `json:"system_proxy_backend"`

	// CustomTitlebar — the app draws its own titlebar and the window is
	// frameless. False where the compositor supplies decorations instead, in
	// which case the in-app titlebar must be hidden or the user gets two.
	CustomTitlebar bool `json:"custom_titlebar"`

	// NativeTitlebarGestures — the window manager already treats a drag region
	// as a real titlebar, so a double-click there maximises without the app
	// doing anything. Windows does (the drag is a caption hit-test, and
	// WM_NCLBUTTONDBLCLK is handled for us); GTK's move-drag does not, and the
	// gesture is simply lost. When false the frontend has to wire the
	// double-click itself — and when true it must NOT, or the two toggles
	// cancel out.
	NativeTitlebarGestures bool `json:"native_titlebar_gestures"`

	// Tray — a tray icon will actually be shown. False on a desktop with no
	// StatusNotifierItem host, where "minimize to tray on close" would hide the
	// window into nothing and leave the app unreachable while it still holds the
	// system proxy. The shell measures it and calls SetTrayAvailable.
	Tray bool `json:"tray"`

	Autostart      bool `json:"autostart"`
	DeepLinkToggle bool `json:"deep_link_toggle"`
	QRScreen       bool `json:"qr_screen"`
	OpenFolder     bool `json:"open_folder"`

	// KillSwitch — the "no traffic outside the tunnel" guarantee can be
	// enforced. True on Windows, where strict_route installs a WFP filter that
	// drops anything trying to leave outside the tunnel. False on Linux for now:
	// strict_route means something different there, sing-box warns against
	// pairing it with auto_redirect, and the nftables rule that would replace it
	// is not written yet. A safety switch that does nothing is worse than an
	// absent one, so it is reported rather than displayed.
	KillSwitch bool `json:"kill_switch"`

	// KillSwitchPersistent — the block can be held at the firewall, where it
	// outlives the engine that would otherwise enforce it. Windows only for now:
	// on Linux it belongs in nftables through the privileged helper, which is not
	// written yet, and offering a switch that does nothing is the one thing a
	// safety feature must never do.
	KillSwitchPersistent bool `json:"kill_switch_persistent"`

	// FirewallRules — the app can open its own inbound ports. When false the
	// LAN-remote and LAN-proxy panels must show the command to run by hand
	// rather than letting the user believe the port was opened.
	FirewallRules bool `json:"firewall_rules"`

	// SelfUpdate is how an update can be applied: "inplace" (replace the running
	// binary), "package" (hand a signed package to the package manager),
	// "appimage", or "none" (check and link only).
	SelfUpdate string `json:"self_update"`
}

// currentCaps composes the platform's fixed capabilities with the facts that
// depend on this machine's state right now — whether sing-box was found and
// whether we hold the privileges TUN needs.
func currentCaps() Capabilities {
	c := platformCaps()
	c.Tray = trayAvailable.Load()

	state.mu.RLock()
	haveSingbox := state.singboxBin != ""
	state.mu.RUnlock()

	switch {
	case !c.TUN:
		c.TUNReady, c.TUNBlockReason = false, tunUnavailableReason()
	case !haveSingbox:
		c.TUNReady, c.TUNBlockReason = false, "no_singbox"
	default:
		// What "ready" means is platform-specific: elevation on Windows, a
		// reachable helper on Linux. Asking each platform beats encoding both
		// chains here and getting one of them subtly wrong.
		if reason := tunNotReadyReason(); reason != "" {
			c.TUNReady, c.TUNBlockReason = false, reason
		} else {
			c.TUNReady, c.TUNBlockReason = true, ""
		}
	}
	return c
}

// trayAvailable is set once at startup by the shell, which is the layer that
// knows what the desktop provides. Atomic because AppInfo can be read from the
// frontend before the shell has finished its own startup.
var trayAvailable atomic.Bool

// SetTrayAvailable records whether a tray icon will be shown on this system.
func SetTrayAvailable(v bool) { trayAvailable.Store(v) }
