package core

import (
	"net"
	"strings"
	"sync/atomic"
	"time"
)

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
	chain := append([]string(nil), appSettings.LastConnectedChain...)
	settingsMu.RUnlock()

	entries, tabID, lost := restoreTargets(raw, chain)
	if lost != "" {
		// Nothing to come back to — or, for a chain, not all of it, and the
		// part that is left is not the same connection: the exit alone, dialled
		// directly, shows the network the one server the chain hid. The block
		// stays: it is protecting the machine from exactly the state it would
		// otherwise be in. Said out loud either way — on the connection bar and
		// in a notification — because a restore that silently does nothing
		// reads as the setting not working.
		held := killSwitchHeld()
		setConnError(state.conn, &ConfigEntry{Name: lost}, restoreLostMessage(len(chain) > 1, held))
		if held {
			desktopNotify("Vair", notifyRestoreFailed())
		} else {
			desktopNotify("Vair", notifyRestoreGone())
		}
		markSessionActive(false)
		return
	}
	labels := make([]string, len(entries))
	for i, e := range entries {
		labels[i] = autoLabel(e)
	}
	vlog("info", "restoring the connection to %s", strings.Join(labels, " → "))
	go restoreLoop(entries, tabID, ConnMode(mode), userConnGen.Load())
}

// restoreTargets finds the configurations to bring back: every hop of a chain,
// entry first, or the one configuration. lost names what could not be found,
// "" when everything was.
func restoreTargets(raw string, chain []string) (entries []*ConfigEntry, tabID, lost string) {
	if len(chain) < 2 {
		e, tab, ok := memEntryByRaw("", raw)
		if !ok {
			return nil, "", rawLabel(raw)
		}
		return []*ConfigEntry{e}, tab, ""
	}
	for i, r := range chain {
		e, tab, ok := memEntryByRaw(tabID, r)
		if !ok {
			return nil, "", rawLabel(r)
		}
		if i == 0 {
			tabID = tab
		}
		entries = append(entries, e)
	}
	return entries, tabID, ""
}

// rawLabel is a configuration's name from its link, for a message about a
// configuration that is no longer in any tab.
func rawLabel(raw string) string {
	if n, err := parseNode(raw); err == nil && n.Name != "" {
		return n.Name
	}
	return "the last configuration"
}

// restoreLostMessage is the connection bar's line when there is nothing to
// restore. Fixed phrases: the window translates them.
func restoreLostMessage(chain, held bool) string {
	what := "not restored: the configuration is no longer in any tab"
	if chain {
		what = "not restored: a configuration of the chain is no longer in any tab"
	}
	if held {
		return what + " — traffic stays blocked (kill-switch); disconnect to release it"
	}
	return what
}

// restoreBudget is how long a start keeps trying to bring the last session
// back. A start at sign-in often comes before the network does — Wi-Fi still
// associating, DHCP not yet answered — and the single attempt there used to
// fail and leave the machine disconnected (or, under the persistent
// kill-switch, offline) after the network came up seconds later.
const restoreBudget = 2 * time.Minute

// restoreRetry are the pauses after a failed attempt, one per retry.
var restoreRetry = []time.Duration{3 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second}

// restoreNetPoll is how often a restore waiting for the network looks again.
var restoreNetPoll = 2 * time.Second

// restoreLoop makes the attempts. It stops at the first that connects, when
// the user connects or disconnects anything themselves (gen moves), when the
// setting is turned off or AUTO on, and when the budget is spent.
func restoreLoop(entries []*ConfigEntry, tabID string, mode ConnMode, gen int64) {
	// TUN that this start cannot run at all — on Windows, a copy started
	// without administrator rights — is not a network that is late. One
	// attempt says why on the connection bar; retrying for two minutes only
	// repeated it.
	if mode == ModeTUN && tunPreconditionError() != "" {
		restoreOnce(entries, tabID, mode, gen)
		vlog("error", "the connection could not be restored: TUN mode needs administrator rights, and this copy of Vair was started without them")
		if killSwitchHeld() {
			desktopNotify("Vair", notifyRestoreFailed())
		} else {
			desktopNotify("Vair", notifyRestoreNotConnected())
		}
		return
	}
	deadline := time.Now().Add(restoreBudget)
	takenOver := func() bool { return userConnGen.Load() != gen || !restoreStillWanted() }
	waited := false
	for attempt := 0; ; attempt++ {
		// No network at all: wait for one rather than spend an attempt on it.
		for !networkLooksUp() && time.Now().Before(deadline) {
			if !waited {
				vlog("info", "restoring the connection: waiting for the network")
				waited = true
			}
			if takenOver() {
				return
			}
			time.Sleep(restoreNetPoll)
		}
		if takenOver() {
			return
		}
		if restoreOnce(entries, tabID, mode, gen) {
			return
		}
		if takenOver() {
			return
		}
		if attempt >= len(restoreRetry) || time.Now().Add(restoreRetry[attempt]).After(deadline) {
			break
		}
		vlog("warning", "restoring the connection: attempt %d did not connect — trying again in %s", attempt+1, restoreRetry[attempt])
		for until := time.Now().Add(restoreRetry[attempt]); time.Now().Before(until); time.Sleep(250 * time.Millisecond) {
			if takenOver() {
				return
			}
		}
	}
	// The connection did not come up. A block still standing stays standing:
	// the machine is without a network, which is the point — the alternative
	// is a machine on the open network with its real address. Both halves are
	// said out loud; the reason itself is on the connection bar.
	if killSwitchHeld() {
		vlog("error", "the connection could not be restored — the traffic block stays; release it from the connection bar, or connect something else")
		desktopNotify("Vair", notifyRestoreFailed())
		return
	}
	vlog("error", "the connection could not be restored in %s — the reason is on the connection bar", restoreBudget)
	desktopNotify("Vair", notifyRestoreNotConnected())
}

// restoreOnce makes one attempt, and reports whether it is connected. An
// attempt the user overtook while it waited for the connection lock is not
// made: theirs is the newer request.
func restoreOnce(entries []*ConfigEntry, tabID string, mode ConnMode, gen int64) bool {
	cm := state.conn
	cm.actionMu.Lock()
	defer cm.actionMu.Unlock()
	if userConnGen.Load() != gen {
		return true
	}
	switch {
	case len(entries) > 1:
		startChainOnTab(entries, tabID, mode)
	case mode == ModeTUN:
		startTUNConnectionOnTab(entries[0], tabID)
	default:
		startProxyConnectionOnTab(entries[0], tabID)
	}
	return cm.snap().Status == ConnConnected
}

// restoreStillWanted re-reads what restoreWanted read: a disconnect clears the
// session mark, and the setting or AUTO may be switched while a restore waits.
func restoreStillWanted() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.ReconnectOnStart && appSettings.LastSessionActive && !appSettings.AutoConnect
}

// userConnGen moves with every connect and disconnect the user asks for, so a
// restore still trying can tell that it has been overtaken.
var userConnGen atomic.Int64

// noteUserConnAction is called by every user-initiated connect and disconnect.
func noteUserConnAction() { userConnGen.Add(1) }

// networkLooksUp reports whether the machine has a network to connect over:
// an interface that is up, is not loopback or one of Vair's own tunnels, and
// has an address that is not link-local. Read from the system's interface
// table; nothing is sent. Virtual adapters (Hyper-V, VirtualBox) count as a
// network here — the retries cover what this cannot tell.
func networkLooksUp() bool {
	ifs, err := net.Interfaces()
	if err != nil {
		return true // cannot tell: attempt, as before
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if strings.HasPrefix(strings.ToLower(ifc.Name), "vair") {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.IsGlobalUnicast() {
				return true
			}
		}
	}
	return false
}
