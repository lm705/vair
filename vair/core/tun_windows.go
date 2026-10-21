//go:build windows

package core

import (
	"path/filepath"
	"strings"
)

// removeTUNAdapter reaps leftover TUN adapters by name so they don't linger in
// the system and cause "file already exists" on the next connect.
//
// Windows keeps the adapter registered after the engine that created it dies,
// so a crash leaves one behind and the reconnect fails on a name clash. The
// interface name carries a timestamp, hence the wildcards; xc-tun-* is from the
// 1.10 releases and is swept too, for users upgrading.
func removeTUNAdapter() {
	runHidden("powershell", "-NonInteractive", "-Command", reapScript(liveTUNIface())).Run() //nolint:errcheck
}

// reapScript is the sweep, with the adapter of a live session left alone.
//
// The sweep matches every vair-* adapter, and it runs in the background so a
// disconnect does not wait for it — which used to mean this: switch from one
// configuration to another, the teardown starts a sweep, the connect right
// behind it creates vair-<now>, and the sweep (a PowerShell launch, 300-700ms
// later) removes the adapter that was just made. sing-box carried on running
// with no interface, the window still said connected, and every packet went out
// the physical one. It survived further switches — each spawned another sweep —
// and only an explicit disconnect cleared it, because there nothing was racing.
//
// Excluding the live name is the belt to the reordering's braces: even a sweep
// that overlaps a connect now cannot touch the adapter that connect is using.
func reapScript(except string) string {
	const base = `Get-NetAdapter -Name 'vair-*','xc-tun-*' -ErrorAction SilentlyContinue`
	tail := ` | Remove-NetAdapter -Confirm:$false`
	if except == "" {
		return base + tail
	}
	// Single-quoted PowerShell string: the only character to escape is the
	// quote itself, and the names we generate never contain one.
	return base + ` | Where-Object { $_.Name -ne '` + psQuote(except) + `' }` + tail
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
	state.mu.RLock()
	engines := []string{state.xrayBin, state.singboxBin}
	state.mu.RUnlock()
	script := ghostSweepScript(engines)
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

// ghostSweepScript builds that sweep.
//
// Engines are matched by their full path, never by image name. The user runs
// the 1.10 release beside this one, and a sweep by name would reach into it and
// kill a working tunnel; the path is what tells the two apart. For the same
// reason the adapter pattern here is vair-* alone — the teardown sweep also
// takes xc-tun-*, the 1.10 name, which is right when this program has just torn
// down a tunnel and wrong at a start, where the only thing that owns an
// xc-tun-* adapter is a copy of 1.10 that is running now.
func ghostSweepScript(engines []string) string {
	var b strings.Builder
	for _, p := range engines {
		if p == "" {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(p), ".exe")
		// -Name narrows the enumeration to the two candidates; the Path test is
		// what actually decides. A process we may not open reads back as an
		// empty path and fails the test, which is the safe way to fail.
		b.WriteString("Get-Process -Name '" + psQuote(name) + "' -ErrorAction SilentlyContinue | " +
			"Where-Object { $_.Path -eq '" + psQuote(p) + "' } | " +
			"Stop-Process -Force -ErrorAction SilentlyContinue; ")
	}
	b.WriteString("Get-NetAdapter -Name 'vair-*' -ErrorAction SilentlyContinue | Remove-NetAdapter -Confirm:$false")
	return b.String()
}
