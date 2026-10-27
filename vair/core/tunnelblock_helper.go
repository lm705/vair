package core

import (
	"sync"
	"time"

	"vair/helper/api"
)

// The tunnel's block when the helper runs the tunnel: the Linux helper's
// nftables table, the Windows service's WFP filters — asked for over the
// connection that owns the tunnel.
//
// It went up with start_tun and down with stop, and that was all. So a
// kill-switch turned on mid-session protected nothing until a reconnect, one
// turned off kept blocking, and a switch to another server — a stop and a
// start — left the machine with no tunnel and no block while the next engine
// came up. These are the Windows functions of the same names, done through the
// helper (protocol 4): the block follows the setting at once, and a teardown
// hands it to the next tunnel instead of taking it down (see teardown in
// connection.go, which stops with keep_block).

// liveHelper is the helper connection of the session that is up, if any.
func liveHelper() *helperClient {
	state.conn.mu.Lock()
	defer state.conn.mu.Unlock()
	h, _ := state.conn.tunRemote.(*helperClient)
	return h
}

// keptBlock notes a block the last teardown left for its successor, so a
// disconnect knows there is one to end — the helper would take it down by
// itself after BlockHandoverSec, but a user who pressed Disconnect is not a
// successor, and waiting leaves their machine offline for that long.
var keptBlock struct {
	sync.Mutex
	at time.Time
}

func noteBlockKept() {
	keptBlock.Lock()
	keptBlock.at = time.Now()
	keptBlock.Unlock()
}

func helperEngageTunnelBlock() {
	h := liveHelper()
	if h == nil || h.HoldsBlock() {
		return
	}
	if err := h.SetKillSwitch(true); err != nil {
		vlog("error", "kill-switch: could not put the block up for this session: %v", err)
		return
	}
	vlog("info", "kill-switch: on for this session — nothing leaves outside the tunnel")
}

func helperReleaseTunnelBlock() {
	if h := liveHelper(); h != nil {
		if h.HoldsBlock() {
			if err := h.SetKillSwitch(false); err != nil {
				vlog("warn", "kill-switch: could not take the block down: %v", err)
			}
		}
		return
	}
	keptBlock.Lock()
	kept := !keptBlock.at.IsZero() && time.Since(keptBlock.at) < time.Duration(api.BlockHandoverSec)*time.Second
	keptBlock.at = time.Time{}
	keptBlock.Unlock()
	if !kept {
		return
	}
	h, done, err := keptBlockClient()
	if err != nil {
		return // the helper takes it down itself when the grace runs out
	}
	defer done()
	if err := h.SetKillSwitch(false); err != nil {
		vlog("warn", "kill-switch: could not take down the block kept for the next tunnel: %v", err)
	}
}

func helperTunnelBlockHeld() bool {
	h := liveHelper()
	return h != nil && h.HoldsBlock()
}

// blockKeptRecently says a block was handed over less than BlockHandoverSec
// ago — still standing in the helper, waiting for a successor.
func blockKeptRecently() bool {
	keptBlock.Lock()
	defer keptBlock.Unlock()
	return !keptBlock.at.IsZero() && time.Since(keptBlock.at) < time.Duration(api.BlockHandoverSec)*time.Second
}
