//go:build windows

package core

import "vair/helper/winsys"

// removeTUNAdapter reaps leftover TUN adapters by name so they don't linger in
// the system and cause "file already exists" on the next connect.
//
// Windows keeps the adapter registered after the engine that created it dies,
// so a crash leaves one behind and the reconnect fails on a name clash. The
// interface name carries a timestamp, hence the wildcards; xc-tun-* is from the
// 1.10 releases and is swept too, for users upgrading.
func removeTUNAdapter() {
	if serviceMode() {
		return // the service sweeps after its own engine
	}
	runHidden("powershell", "-NonInteractive", "-Command", winsys.ReapScript(liveTUNIface())).Run() //nolint:errcheck
}

// sweepGhostTunnelAtStart clears what a run that never got to tear anything
// down left on the machine: an engine still running with nobody driving it, and
// the adapter it made, which still holds the default route.
//
// The teardown path already sweeps adapters, but it only runs when there IS a
// teardown. Kill Vair from Task Manager, lose power, or have the shell die on
// its own, and neither half is cleaned up: the engine goes on running, its
// wintun adapter stays registered with 0.0.0.0/0 pointed into it, and the
// machine has no network at all — nothing forwards those packets any more. The
// user's own account of it was a computer with no internet after a crash and no
// obvious reason for it.
//
// Two things, in this order, because the adapter cannot go while its owner is
// alive: kill the engine, then remove the adapter.
//
// This does not open anything that was closed. A firewall block left standing
// is held by keepBlockAtStart, which has already run by the time this starts;
// what comes back is the ordinary route table of a machine that is not
// connected to anything — which is the true state of it.
func sweepGhostTunnelAtStart() {
	if serviceMode() {
		return // the service sweeps when it starts
	}
	state.mu.RLock()
	engines := []string{state.xrayBin, state.singboxBin}
	state.mu.RUnlock()
	script := winsys.GhostSweepScript(engines)
	// The lock is taken HERE, on the startup goroutine, and released by the
	// sweep when it finishes. That ordering is the whole point: a connect waits
	// on this same lock before it starts an engine, so from the first moment of
	// the start nothing can bring a tunnel up until the sweep is done with the
	// old one.
	//
	// Taking it in the goroutine instead was tried and is wrong — and visibly
	// so. Restoring the session on start beat the PowerShell launch to it by a
	// few hundred milliseconds, the sweep then found the engine it had just
	// started (same path as the ghost it was looking for) and killed it, and
	// the restored connection died with "the engine stopped".
	//
	// The work itself still runs off this goroutine: it is half a second of
	// PowerShell and the window must not wait for it.
	tunReapMu.Lock()
	go func() {
		defer tunReapMu.Unlock()
		runHidden("powershell", "-NonInteractive", "-Command", script).Run() //nolint:errcheck
		tunReapDone++
	}()
}
