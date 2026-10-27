package core

import (
	"sync"
	"sync/atomic"
)

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
		setBlockGap(err.Error())
		vlog("warning", "kill-switch: %v", err)
		return
	}
	noteBlockGap()
	announceBlockState()
}

// blockGap is what is wrong with the block that was asked for: empty when it is
// standing and doing its job, a sentence for a person when it is not. Read by
// the interface, which is the only place it can do any good.
var blockGap struct {
	mu     sync.RWMutex
	reason string
}

func setBlockGap(reason string) {
	blockGap.mu.Lock()
	blockGap.reason = reason
	blockGap.mu.Unlock()
}

// noteBlockGap reads the firewall back and remembers what it found.
//
// Installing the block is not the same as traffic being blocked: Windows
// applies an outbound default only on profiles whose firewall is switched on,
// and only the profile of the network the machine is attached to decides
// anything. On a machine where that profile is off, every command succeeded and
// nothing was protected — a switch that said on, a program that said nothing.
func noteBlockGap() {
	reason := verifyBlockStanding()
	setBlockGap(reason)
	if reason != "" {
		vlog("error", "kill-switch: the block is not in force — %s", reason)
	}
}

// BlockGapReason is that sentence, for the interface. Empty when the block is
// either standing properly or was never asked for.
func BlockGapReason() string {
	if !persistentKillSwitchEnabled() && !proxyKillSwitchEnabled() {
		return ""
	}
	blockGap.mu.RLock()
	defer blockGap.mu.RUnlock()
	return blockGap.reason
}

// engageProxyKillSwitch fences in a proxy session: the same firewall block,
// with no tunnel interface to allow — what stays open is the engine's own
// connection to the server and loopback, which is the proxy.
//
// Without the persistent kill-switch it is the session's own block, in a
// dynamic WFP session like the tunnel's: it holds through a switch to another
// server, lets go tunnelHandoverGrace after a session that is not taken over,
// and goes with this process. It used to be the persistent set with a timer of
// its own, and a timer dies with the program: after a crash the machine stayed
// offline until Vair was started again — after a restart too, the set being
// persistent — while the same switch in TUN let go. Holding traffic after Vair
// is gone is the persistent kill-switch's job, in both modes.
func engageProxyKillSwitch() {
	if !proxyKillSwitchEnabled() {
		return
	}
	if !persistentKillSwitchEnabled() {
		if handled, err := engageProxySessionBlock(); handled {
			if err != nil {
				setBlockGap(err.Error())
				vlog("warning", "kill-switch (proxy): %v", err)
				return
			}
			setBlockGap("")
			announceBlockState()
			return
		}
	}
	if err := engageKillSwitch(""); err != nil {
		setBlockGap(err.Error())
		vlog("warning", "kill-switch (proxy): %v", err)
		return
	}
	noteBlockGap()
	announceBlockState()
}

// blockWillLapse reports whether the block holding traffic now will come down
// by itself: the session's own block, handed over by a session that ended, which
// waits tunnelHandoverGrace for the next one and no longer. The messages that
// tell the user traffic is blocked say so — "disconnect to release it" is wrong
// advice for a block that is about to go anyway.
func blockWillLapse() bool {
	return !persistentKillSwitchEnabled() && !blockStanding() && tunnelBlockHeld()
}

// trafficHeldNow is whether anything holds traffic in at this moment: the
// persistent set, or the session's own block, which outlives a lost session by
// tunnelHandoverGrace. The messages about a lost connection read it; they read
// the persistent set alone and said "connection closed" while the session's
// block still held everything for twenty seconds.
func trafficHeldNow() bool { return blockStanding() || tunnelBlockHeld() }

// releasePersistentKillSwitch lifts the block. Called where the user has said
// they are done — an explicit disconnect or a clean exit — and NOT when a
// connection merely dies, which is the whole reason the block exists.
func releasePersistentKillSwitch() {
	// Was there anything to release, or has anyone asked for one? If not, do not
	// go asking the firewall — an ordinary disconnect must not spawn PowerShell
	// for a block nobody ever engaged.
	interested := blockStanding() || persistentKillSwitchEnabled() || proxyKillSwitchEnabled()
	was := blockStanding()
	releaseKillSwitch()
	// And if it is somehow still standing — a marker that went missing, a
	// restore that was refused — open the firewall anyway. Someone asking for
	// their network back is not asking us to be careful.
	//
	// Only when something is still there after the release above. A switch
	// that is on says nothing about whether a block stands, and forcing a
	// release nothing needs asked Windows for administrator rights whenever
	// Vair ran without them — a UAC prompt on every disconnect — and after a
	// release that had worked it still read the firewall back through
	// PowerShell, seconds Windows does not give a program that is being closed
	// with the session.
	if interested && (killSwitchHeld() || blockStandingNoMarker.Load() || ourFiltersPresent()) {
		forceReleaseBlock()
	}
	// Said out loud, because it is the one moment where the protection someone
	// switched on ends, and it ends because they pressed disconnect. Without
	// this the log showed a tunnel going down and nothing about the block that
	// went with it.
	if was && !blockStanding() {
		vlog("info", "kill-switch: the block came down with the disconnect — this machine is back on its ordinary connection")
	}
	if was {
		announceBlockState()
	}
}

// The block is one set of filters with two settings behind it: "keep blocking
// after the tunnel is gone", wanted in every mode (a proxy session has no
// tunnel either), and the proxy kill-switch, wanted in proxy sessions only.
// Everything that takes the block down for a reason other than the user's
// disconnect asks this first — otherwise one setting's block was lifted on
// the other setting's say-so, or left standing where nothing wanted it.
func blockWantedFor(mode ConnMode) bool {
	if persistentKillSwitchEnabled() {
		return true
	}
	// Where the proxy kill-switch is the session's own block, this set is not
	// what it asks for.
	return mode == ModeProxy && proxyKillSwitchEnabled() && !proxyBlockIsSessions
}

// dropBlockNobodyWants takes down a block the session now starting does not
// ask for. Called once the new session's own protection is in place, so there
// is no moment with neither.
//
// The case: a proxy session with its kill-switch, then TUN without the
// persistent block. The proxy's filters let no tunnel through — they were put
// up where there was none — so the new tunnel carried nothing for any program
// but this one, and the session failed as "no traffic", blaming the server.
func dropBlockNobodyWants(mode ConnMode) {
	if !killSwitchHeld() || blockWantedFor(mode) {
		return
	}
	releaseKillSwitch()
	if !killSwitchHeld() {
		vlog("info", "kill-switch: the block the previous session left is down — this %s session does not ask for one", mode)
	}
	announceBlockState()
}

// AfterBlockSettingsChange takes the block down when a switch that asked for
// it has just been turned off — "off" meaning off now, not at the next
// disconnect — and nothing else still asks for it. Turning the proxy
// kill-switch off in the middle of a TUN session used to lift the persistent
// block with it.
func AfterBlockSettingsChange(old, now AppSettings) {
	releaseBlockNoLongerWanted(old, now)
	if killSwitchesTurned(old, now) {
		go applyKillSwitchesToLiveSession(old, now) // re-checks for leaks itself
	} else if (old.ProxyKillSwitch && !now.ProxyKillSwitch) || (persistentOf(old) && !persistentOf(now)) {
		// A block switched off: the verdict it earned no longer describes the
		// session.
		recheckLeak()
	}
}

func tunKillSwitchOf(s AppSettings) bool { return s.KillSwitch && s.DNSLeakProtection }
func persistentOf(s AppSettings) bool    { return s.KillSwitchPersistent && tunKillSwitchOf(s) }

// killSwitchesTurned reports whether a save changed anything the live session's
// protection depends on.
func killSwitchesTurned(old, now AppSettings) bool {
	return tunKillSwitchOf(old) != tunKillSwitchOf(now) ||
		(persistentOf(now) && !persistentOf(old)) ||
		(now.ProxyKillSwitch && !old.ProxyKillSwitch)
}

// applyKillSwitchesToLiveSession makes a switch turned on — or the TUN
// kill-switch turned off — take effect in the session that is running, not at
// the next one. Before, a kill-switch switched on mid-session protected
// nothing until a reconnect: the persistent block, turned on and followed by
// closing Vair, left no block at all, and nothing on screen said the switch
// was waiting for a connection.
//
// Under the connection's action lock, so it cannot interleave with a connect
// or a disconnect; the session is read again once the lock is held.
func applyKillSwitchesToLiveSession(old, now AppSettings) {
	state.conn.actionMu.Lock()
	defer state.conn.actionMu.Unlock()
	state.conn.mu.Lock()
	st := state.conn.state
	state.conn.mu.Unlock()
	if st.Status != ConnConnected {
		return
	}
	switch st.Mode {
	case ModeTUN:
		switch {
		case tunKillSwitchOf(now) && !tunKillSwitchOf(old):
			engageTunnelBlock(st.TUNIface)
		case !tunKillSwitchOf(now) && tunKillSwitchOf(old):
			releaseTunnelBlock()
			vlog("info", "kill-switch: turned off — traffic outside the tunnel is no longer blocked for this session")
		}
		if persistentOf(now) && !persistentOf(old) {
			engagePersistentKillSwitch(st.TUNIface)
		}
	case ModeProxy:
		if now.ProxyKillSwitch && !old.ProxyKillSwitch {
			engageProxyKillSwitch()
		}
	}
	recheckLeak()
}

// releaseBlockNoLongerWanted is the other half: a switch turned off.
func releaseBlockNoLongerWanted(old, now AppSettings) {
	if !(persistentOf(old) && !persistentOf(now)) && !(old.ProxyKillSwitch && !now.ProxyKillSwitch) {
		return
	}
	// The proxy kill-switch turned off in a proxy session: its block is the
	// session's own (engageProxyKillSwitch), not the persistent set.
	if old.ProxyKillSwitch && !now.ProxyKillSwitch && tunnelBlockHeld() {
		state.conn.mu.Lock()
		mode := state.conn.state.Mode
		state.conn.mu.Unlock()
		if mode == ModeProxy {
			releaseTunnelBlock()
			vlog("info", "kill-switch (proxy): turned off — traffic outside the proxy is no longer blocked")
			announceBlockState()
		}
	}
	if !blockStanding() {
		return
	}
	// Which session the block serves: the one running, or, with none running,
	// the one it was put up for.
	state.conn.mu.Lock()
	mode, status := state.conn.state.Mode, state.conn.state.Status
	state.conn.mu.Unlock()
	if status != ConnConnected && status != ConnConnecting {
		mode, _ = heldBlockMode()
	}
	if blockWantedFor(mode) {
		return
	}
	// A proxy session whose kill-switch is still on, the persistent one just
	// turned off: its block becomes the session's own first, then the
	// persistent set comes down — no moment with neither.
	if status == ConnConnected && mode == ModeProxy && now.ProxyKillSwitch && proxyBlockIsSessions {
		engageProxyKillSwitch()
	}
	releasePersistentKillSwitch()
}

// ForceReleaseBlock is the red button on the connection bar: put this machine
// back on its network, whatever the marker says. Also clears the "there was a
// session" flag, since pressing it is a decision to be disconnected.
func ForceReleaseBlock() {
	forceReleaseBlock()
	markSessionActive(false)
	announceBlockState()
}

// announceBlockState pushes the connection state out again after the block has
// changed under it.
//
// The chip on the connection bar is drawn from that state, and the state is
// only ever sent when a connection starts, stops or fails — none of which is
// what happens when a block goes up or comes down. So the chip showed whatever
// had been true at the last event: still on the bar after a disconnect had
// already opened the firewall, and still on the bar after the red button
// itself had worked. Both times the machine was online and the interface said
// it was not, which is the one thing this chip must never say.
func announceBlockState() {
	state.broadcast(SSEEvent{Type: "conn_update", Payload: state.conn.snap()})
}

// ReleasePersistentKillSwitch lifts the block from outside core — the settings
// screen, when the switch that asked for it is turned off.
func ReleasePersistentKillSwitch() { releasePersistentKillSwitch() }

// PersistentKillSwitchHeld reports whether traffic is currently blocked at the
// firewall, so the interface can say so rather than leaving someone to wonder
// why nothing loads.
func PersistentKillSwitchHeld() bool { return blockStanding() }

// blockStandingNoMarker is the case the marker cannot describe: the firewall is
// blocking and the file that knows how to undo it is gone. Older builds deleted
// it on a failed restore, which left the machine shut with nothing on screen to
// press — the red chip is drawn from the marker, and there was no marker.
//
// Set by a reading of the actual firewall, never by guesswork, and only where
// the block can be attributed to us (see noteStrayBlockAtStart).
var blockStandingNoMarker atomic.Bool

// blockStanding answers "is traffic blocked right now" for everything that has
// to draw or decide from it — the chip, the session panel, the message on a
// tunnel that carries nothing.
func blockStanding() bool { return killSwitchHeld() || blockStandingNoMarker.Load() }

// keepBlockAtStart decides what to do with a block found standing at startup.
//
// It is only stale when nobody wants it any more. While "keep blocking after
// the tunnel is gone" is still on, that block is doing its job — the machine
// was left protected on purpose and lifting it here would put it on the open
// network with its real address for however long it takes someone to notice.
// It comes down when the user says so: the red chip on the connection bar,
// disconnecting, or turning the setting off.
func keepBlockAtStart() {
	if !killSwitchHeld() {
		// No marker — which is not the same as no block. Look at the firewall
		// itself, off this goroutine, for the machine that was left shut by a
		// build that threw its marker away.
		go noteStrayBlockAtStart()
		return
	}
	if persistentKillSwitchEnabled() {
		// Said differently without administrator rights, because "or connect"
		// is not available there either — TUN needs the same rights — and the
		// button is about to ask Windows for them rather than fail.
		if blocksAvailable() {
			vlog("warning", "kill-switch: traffic is still blocked from the last run — release it from the connection bar, or connect")
		} else {
			vlog("warning", "kill-switch: traffic is still blocked from the last run — press the red button on the connection bar and allow the Windows prompt, or run  %s", releaseHint())
		}
		announceBlockState()
		// And then check what is actually true, off this goroutine: the marker
		// is a record of what we did, not a reading of the firewall, and a
		// restart can have moved one without the other.
		go checkBlockAfterRestart()
		return
	}
	releaseStaleKillSwitch()
	announceBlockState()
}

// releaseBlockOnExit is the other half of keepBlockAtStart: what a clean exit
// does with a block that is standing.
//
// Nothing, while the user still wants it. Closing the program is the plainest
// case of "the tunnel is gone", and releasing it here handed the machine back
// to the open network with its real address — the exact thing the switch was
// turned on to prevent. It is lifted only where somebody said so: disconnect,
// the setting turned off, the red chip on the connection bar, or the line the
// settings screen prints for a machine with no Vair on it at all.
func releaseBlockOnExit() {
	if persistentKillSwitchEnabled() {
		if killSwitchHeld() {
			vlog("warning", "kill-switch: traffic stays blocked after this exit, as asked — start Vair again, or release it from the firewall")
		}
		return
	}
	releasePersistentKillSwitch()
}
