package core

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"vair/helper/wfp"
)

// The tunnel's own block: the kill-switch's filters, held for the length of one
// TUN session and no longer.
//
// sing-box's strict_route is not a kill-switch on Windows. Its filters let the
// engine and the TUN adapter through and stop DNS on port 53 everywhere else —
// and that is all. Everything else is kept in the tunnel by the routing table
// alone, and the routing table is only asked about packets nobody pinned: a
// program that binds to the physical adapter's address is sent out of that
// adapter, around the tunnel, with the machine's real address. WebRTC gathers
// candidates from every interface that way; games, downloaders and anything
// with an "interface" option do it on purpose. The leak check found exactly
// this in the test VM — TUN, kill-switch on, nothing else — and a ping bound to
// the physical address came back.
//
// So with the kill-switch on, the block goes up with the tunnel: the same
// policy as the persistent block, in a dynamic WFP session of this process.
// Dynamic hands the clean-up to the filtering engine — the filters go when the
// session is closed, or, if this process dies, when the process does. The
// promise is the setting's: nothing outside the tunnel while there is a tunnel,
// including the seconds of a switch from one tunnel to the next, which is why a
// teardown hands the block over instead of dropping it. Keeping the machine
// shut once the tunnels have stopped coming is the other setting's job, with
// filters of its own at fixed keys; these take whatever keys the engine gives
// them, so the two never meet.

// svcProxyBlock says the proxy kill-switch's block stands in the helper
// service, held by the control connection.
var svcProxyBlock atomic.Bool

var tunnelBlock struct {
	sync.Mutex
	close func() // closes the session that holds the filters; nil when none
	gen   uint64 // bumped whenever close changes hands, so a stale timer knows
	grace interface{ Stop() bool }
}

// tunnelHandoverGrace is how long a block handed over by a teardown waits for
// the next TUN session. A switch — the engine killed, the next one started,
// the tunnel checked — takes a few seconds; an AUTO sweep renews it at every
// candidate. Long enough for both, short enough that a switch which leads
// nowhere does not leave the machine offline for more than a moment past it.
const tunnelHandoverGrace = 20 * time.Second

// tunnelAfter is time.AfterFunc, behind a variable for the tests.
var tunnelAfter = func(d time.Duration, f func()) interface{ Stop() bool } { return time.AfterFunc(d, f) }

// wfpOpenTunnelBlock puts the filters up in a session of their own and returns
// what takes them down (wfp.OpenSession). Behind a variable for the tests.
var wfpOpenTunnelBlock = wfp.OpenSession

// engageTunnelBlock puts the block up for the TUN session that has just come
// up on iface. Never fatal, like the persistent block: a tunnel without it is
// the tunnel strict_route alone gave — and the log and the leak check both say
// what it lacks.
func engageTunnelBlock(iface string) {
	if serviceMode() {
		if !killSwitchEnabled() {
			releaseTunnelBlock()
			return
		}
		helperEngageTunnelBlock()
		return
	}
	if !killSwitchEnabled() {
		// A block handed over by the session before is not this one's to keep —
		// and it lets through only the tunnel it was put up for, so for its
		// grace period the new tunnel carried nothing and failed its check.
		releaseTunnelBlock()
		return
	}
	if err := engageSessionBlock(iface); err != nil {
		vlog("error", "kill-switch: the tunnel's block could not be put up — %v. Programs that bind to the physical adapter can go around the tunnel", err)
		return
	}
	vlog("info", "kill-switch: nothing leaves outside the tunnel while it is up — filters of our own, gone with the tunnel")
}

// proxyBlockIsSessions: the proxy kill-switch's block is a session block here
// (engageProxySessionBlock), not the persistent set.
const proxyBlockIsSessions = true

// engageProxySessionBlock puts the proxy kill-switch's block up as the
// session's own: the tunnel's policy with no tunnel to let through — the engine
// and this program, loopback (the proxy itself), DHCP and, unless blocked, the
// local network. Handed over and let go exactly like the tunnel's.
func engageProxySessionBlock() (bool, error) {
	if serviceMode() {
		h, err := controlHelper()
		if err == nil {
			err = h.SetSessionBlock(true, allowLANTraffic(), false)
		}
		if err != nil {
			return true, fmt.Errorf("the block could not be put up — %w", err)
		}
		svcProxyBlock.Store(true)
		vlog("info", "kill-switch (proxy): nothing leaves outside the proxy while the session is up — held by the Vair helper service")
		return true, nil
	}
	if err := engageSessionBlock(""); err != nil {
		return true, fmt.Errorf("the block could not be put up — %w", err)
	}
	vlog("info", "kill-switch (proxy): nothing leaves outside the proxy while the session is up — filters of our own, gone with the session")
	return true, nil
}

// engageSessionBlock puts the block for the session on iface up — iface ""
// for a proxy session, which has no tunnel — replacing the one standing, if
// any, in a single step.
func engageSessionBlock(iface string) error {
	o, err := ksBlockOptions(iface)
	if err != nil {
		return err
	}
	o.V6Around = iface != "" && !ipv6InTunnel()
	closeFn, err := wfpOpenTunnelBlock(o)
	if err != nil {
		return err
	}
	tunnelBlock.Lock()
	prev := tunnelBlock.close
	tunnelBlock.close = closeFn
	tunnelBlock.gen++
	stopTunnelGraceLocked()
	tunnelBlock.Unlock()
	// Only now the old one, if a session somehow left it: two blocks for a
	// moment is traffic stopped, a gap between them is not.
	if prev != nil {
		prev()
	}
	return nil
}

// releaseTunnelBlock takes it down now: the user's disconnect, or a session
// that is not a tunnel taking over.
func releaseTunnelBlock() {
	if serviceMode() {
		helperReleaseTunnelBlock()
		if svcProxyBlock.Swap(false) {
			if h, err := controlHelper(); err == nil {
				if err := h.SetSessionBlock(false, false, false); err != nil {
					vlog("warn", "kill-switch (proxy): could not take the block down: %v", err)
				}
			}
		}
		return
	}
	tunnelBlock.Lock()
	c := tunnelBlock.close
	tunnelBlock.close = nil
	tunnelBlock.gen++
	stopTunnelGraceLocked()
	tunnelBlock.Unlock()
	if c != nil {
		c()
	}
}

// handOverTunnelBlock keeps the block standing past the end of its session,
// for the next one to take over, and takes it down if none has by the end of
// the grace period. Every teardown renews the period, so a string of
// candidates keeps it up for as long as they keep coming.
func handOverTunnelBlock() {
	if serviceMode() {
		// The tunnel's block was handed over by the stop itself
		// (StopForHandover); the proxy's is the control connection's to hand.
		if svcProxyBlock.Swap(false) {
			if h, err := controlHelper(); err == nil {
				if h.SetSessionBlock(false, false, true) == nil {
					noteBlockKept()
				}
			}
		}
		return
	}
	tunnelBlock.Lock()
	defer tunnelBlock.Unlock()
	if tunnelBlock.close == nil {
		return
	}
	stopTunnelGraceLocked()
	gen := tunnelBlock.gen
	tunnelBlock.grace = tunnelAfter(tunnelHandoverGrace, func() {
		tunnelBlock.Lock()
		if tunnelBlock.gen != gen || tunnelBlock.close == nil {
			tunnelBlock.Unlock()
			return
		}
		c := tunnelBlock.close
		tunnelBlock.close = nil
		tunnelBlock.gen++
		tunnelBlock.grace = nil
		tunnelBlock.Unlock()
		c()
		vlog("info", "kill-switch: no tunnel came back within %s — the tunnel's block is down, this machine is on its ordinary connection", tunnelHandoverGrace)
	})
}

func stopTunnelGraceLocked() {
	if tunnelBlock.grace != nil {
		tunnelBlock.grace.Stop()
		tunnelBlock.grace = nil
	}
}

// tunnelBlockHeld reports whether the tunnel's block is standing.
func tunnelBlockHeld() bool {
	if serviceMode() {
		return helperTunnelBlockHeld() || svcProxyBlock.Load() || blockKeptRecently()
	}
	tunnelBlock.Lock()
	defer tunnelBlock.Unlock()
	return tunnelBlock.close != nil
}
