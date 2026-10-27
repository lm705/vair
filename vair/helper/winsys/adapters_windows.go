package winsys

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// Leftover TUN adapters.
//
// Windows keeps a wintun adapter registered after the engine that created it
// dies, so a crash leaves one behind — holding the default route — and the
// reconnect fails on a name clash. The interface name carries a timestamp,
// hence the wildcards; xc-tun-* is from the 1.10 releases.

// ReapScript is the sweep after a teardown, with the adapter of a live session
// left alone.
//
// The sweep matches every vair-* adapter, and it runs in the background so a
// disconnect does not wait for it — which used to mean this: switch from one
// configuration to another, the teardown starts a sweep, the connect right
// behind it creates vair-<now>, and the sweep (a PowerShell launch, 300-700ms
// later) removes the adapter that was just made. sing-box carried on running
// with no interface, the window still said connected, and every packet went out
// the physical one. Excluding the live name means even a sweep that overlaps a
// connect cannot touch the adapter that connect is using.
func ReapScript(except string) string {
	const base = `Get-NetAdapter -Name 'vair-*','xc-tun-*' -ErrorAction SilentlyContinue`
	tail := ` | Remove-NetAdapter -Confirm:$false`
	if except == "" {
		return base + tail
	}
	return base + ` | Where-Object { $_.Name -ne '` + PSQuote(except) + `' }` + tail
}

// GhostSweepScript is the sweep at a start: an engine still running with
// nobody driving it, then the adapter it made — in that order, because the
// adapter cannot go while its owner is alive.
//
// Engines are matched by their full path, never by image name. Vair 1.10 may be
// running beside this program, and a sweep by name would reach into it and kill
// a working tunnel; the path is what tells the two apart. For the same reason
// the adapter pattern here is vair-* alone — at a start, the only thing that
// owns an xc-tun-* adapter is a copy of 1.10 that is running now.
func GhostSweepScript(engines []string) string {
	var b strings.Builder
	for _, p := range engines {
		if p == "" {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(p), ".exe")
		// -Name narrows the enumeration to the two candidates; the Path test is
		// what actually decides. A process we may not open reads back as an
		// empty path and fails the test, which is the safe way to fail.
		b.WriteString("Get-Process -Name '" + PSQuote(name) + "' -ErrorAction SilentlyContinue | " +
			"Where-Object { $_.Path -eq '" + PSQuote(p) + "' } | " +
			"Stop-Process -Force -ErrorAction SilentlyContinue; ")
	}
	b.WriteString("Get-NetAdapter -Name 'vair-*' -ErrorAction SilentlyContinue | Remove-NetAdapter -Confirm:$false")
	return b.String()
}

// RunSweep runs one of those scripts and waits for it. They are plain commands
// rather than encoded ones: they are built from names we generate.
func RunSweep(script string) {
	cmd := exec.Command("powershell", "-NonInteractive", "-Command", script)
	Hide(cmd)
	cmd.Run() //nolint:errcheck
}
