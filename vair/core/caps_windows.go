package core

import "os"

// isPrivileged reports whether we can do the things TUN mode needs — create the
// adapter, rewrite the routing table, install the WFP filter.
//
// Opening \\.\PHYSICALDRIVE0 is the cheap standard probe: it needs
// administrator, it touches nothing, and it needs no extra imports. A proper
// token check would be more precise and no more useful.
func isPrivileged() bool {
	f, err := os.Open(`\\.\PHYSICALDRIVE0`)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// platformCaps — everything Vair does on Windows, it does. This is the baseline
// the Linux side is measured against.
func platformCaps() Capabilities {
	return Capabilities{
		TUN:            true,
		CustomTitlebar: true,
		// The caption hit-test gives us double-click-to-maximise for free.
		NativeTitlebarGestures: true,
		SplitTunnel:            true,
		ProcessPicker:          true,
		SystemProxy:            true,
		SystemProxyBackend:     "wininet",
		Autostart:              true,
		DeepLinkToggle:         true,
		QRScreen:               true,
		OpenFolder:             true,
		KillSwitch:             true,
		KillSwitchPersistent:   true,
		FirewallRules:          true,
		SelfUpdate:             "inplace",
	}
}

// tunUnavailableReason is unreachable on Windows: TUN is always supported here.
func tunUnavailableReason() string { return "unsupported" }

// tunNotReadyReason — the one thing that can be missing is elevation.
func tunNotReadyReason() string {
	if !isPrivileged() {
		return "not_admin"
	}
	return ""
}
