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
func engageProxyKillSwitch() {
	if !proxyKillSwitchEnabled() {
		return
	}
	if err := engageKillSwitch(""); err != nil {
		setBlockGap(err.Error())
		vlog("warning", "kill-switch (proxy): %v", err)
		return
	}
	noteBlockGap()
	announceBlockState()
}

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
	if interested {
		// And if it is somehow still standing — a marker that went missing, a
		// restore that was refused — open the firewall anyway. Someone asking
		// for their network back is not asking us to be careful.
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
		if isPrivileged() {
			vlog("warning", "kill-switch: traffic is still blocked from the last run — release it from the connection bar, or connect")
		} else {
			vlog("warning", "kill-switch: traffic is still blocked from the last run — press the red button on the connection bar and allow the Windows prompt, or run  Set-NetFirewallProfile -All -DefaultOutboundAction Allow  as administrator")
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
