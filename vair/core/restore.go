package core

// Bringing a connection back after the program was closed.
//
// Two problems, and one answer to both.
//
// The first is the plain one: a program that was connected when it closed is a
// program that should be connected when it opens again, if that is what the
// user asked for. Auto-connect already does something like it, but it chooses
// its own server; this restores the one that was actually in use, in the mode
// it was in.
//
// The second is what to do about the kill switch that holds after the tunnel is
// gone. That block is never lifted on the program's own initiative — closing
// the program is the plainest case of the tunnel being gone, and a machine put
// back on the open network with its real address is exactly what the switch was
// turned on to prevent. What restoring adds is the pleasant way out of it: the
// tunnel comes back under the block, and nothing is unprotected in between.
//
// Without restoring, the block is still there on the next start and nothing is
// connected: it is the user who ends that, from the red chip on the connection
// bar or by connecting something. The one thing the program will not do is
// decide on their behalf — neither that protection they asked for is
// inconvenient, nor that a start is a request to send their traffic somewhere.
//
// A restore that fails — the configuration is gone, the server is dead —
// leaves the block exactly where it is. Lifting it would put the machine on
// the open network with its real address, which is what the switch was turned
// on to prevent; a failure to reconnect is not consent. What it does instead is
// say so: in the log, in a desktop notification, and as the red chip on the
// connection bar that lifts the block in one click when that is what the user
// wants.

// markSessionActive records that a connection is up, so a start that follows a
// close (or a crash) knows there was something to come back to. Cleared by an
// explicit disconnect, and deliberately NOT by shutdown.
func markSessionActive(active bool) {
	settingsMu.Lock()
	changed := appSettings.LastSessionActive != active
	appSettings.LastSessionActive = active
	settingsMu.Unlock()
	if changed {
		saveSettings()
	}
}

// restoreWanted reports whether this start should try to bring the last session
// back. Read before the configuration store is open, so it can only consult the
// settings and the firewall — whether the configuration still exists is decided
// later, by restoreLastSession.
func restoreWanted() bool {
	settingsMu.RLock()
	raw := appSettings.LastConnectedRaw
	active := appSettings.LastSessionActive
	want := appSettings.ReconnectOnStart
	auto := appSettings.AutoConnect
	settingsMu.RUnlock()
	if raw == "" || !active || auto {
		// Auto-connect makes its own choice, and a better one: it picks the
		// fastest working server rather than the last one used.
		return false
	}
	// The setting decides, and nothing else. A standing block used to be taken
	// as reason enough on its own — the machine has no network, so bring the
	// tunnel back — and that is the program deciding to send someone's traffic
	// out at a start they did not ask it to. The block stays either way; what
	// changes is who ends it. With this off, the red chip on the connection bar
	// and the connect button are both one click away.
	return want
}

// restoreLastSession reconnects what was connected when the program last
// closed. Called once, after the tabs and configurations are in memory.
func restoreLastSession() {
	if !restoreWanted() {
		return
	}
	settingsMu.RLock()
	raw, mode := appSettings.LastConnectedRaw, appSettings.LastConnectedMode
	settingsMu.RUnlock()

	entry, tabID, ok := memEntryByRaw("", raw)
	if !ok {
		// Nothing to come back to. The block stays: it is protecting the
		// machine from exactly the state it would otherwise be in. Said out
		// loud, because a machine with no network needs a reason on screen.
		if killSwitchHeld() {
			vlog("error", "the configuration this session was using is no longer in any tab — the traffic block stays; release it from the connection bar or by connecting something")
			desktopNotify("Vair", notifyRestoreFailed())
		}
		markSessionActive(false)
		return
	}
	vlog("info", "restoring the connection to %s", autoLabel(entry))
	go func() {
		cm := state.conn
		cm.actionMu.Lock()
		defer cm.actionMu.Unlock()
		if ConnMode(mode) == ModeTUN {
			startTUNConnectionOnTab(entry, tabID)
		} else {
			startProxyConnectionOnTab(entry, tabID)
		}
		// The connection did not come up and the block is still standing. It
		// stays standing: the machine is without a network, which is the point
		// — the alternative is a machine on the open network with its real
		// address. Both halves are said out loud.
		if state.conn.snap().Status != ConnConnected && killSwitchHeld() {
			vlog("error", "the connection could not be restored — the traffic block stays; release it from the connection bar, or connect something else")
			desktopNotify("Vair", notifyRestoreFailed())
		}
	}()
}
