package core

// engagePersistentKillSwitch installs the firewall-level block when the user
// asked for one, and says why it could not when it could not.
//
// Never fatal to the connection: a tunnel that is up without the extra block is
// still a working tunnel, and refusing to connect because a safety net could
// not be hung would trade a real thing for a precaution.
func engagePersistentKillSwitch(iface string) {
	if !persistentKillSwitchEnabled() {
		return
	}
	if err := engageKillSwitch(iface); err != nil {
		vlog("warning", "kill-switch: %v", err)
	}
}

// engageProxyKillSwitch fences in a proxy session: the same firewall block,
// with no tunnel interface to allow — what stays open is the engine's own
// connection to the server and loopback, which is the proxy.
func engageProxyKillSwitch() {
	if !proxyKillSwitchEnabled() {
		return
	}
	if err := engageKillSwitch(""); err != nil {
		vlog("warning", "kill-switch (proxy): %v", err)
	}
}

// releasePersistentKillSwitch lifts the block. Called where the user has said
// they are done — an explicit disconnect or a clean exit — and NOT when a
// connection merely dies, which is the whole reason the block exists.
func releasePersistentKillSwitch() {
	releaseKillSwitch()
}

// ReleasePersistentKillSwitch lifts the block from outside core — the settings
// screen, when the switch that asked for it is turned off.
func ReleasePersistentKillSwitch() { releasePersistentKillSwitch() }

// PersistentKillSwitchHeld reports whether traffic is currently blocked at the
// firewall, so the interface can say so rather than leaving someone to wonder
// why nothing loads.
func PersistentKillSwitchHeld() bool { return killSwitchHeld() }
