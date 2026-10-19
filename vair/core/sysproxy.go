package core

import (
	"encoding/json"
	"fmt"
	"os"
)

// ─────────────────────────── system proxy ────────────────────────
//
// Pointing the OS at our local listener is one idea with two very different
// implementations. Windows has a single registry switch that nearly everything
// honours. Linux has no such switch at all: GNOME and KDE each keep their own
// setting, they reach GIO apps and browsers set to "use system settings", and
// command-line tools read the environment instead. So the operation lives
// behind a backend interface, and Capabilities.SystemProxyBackend tells the UI
// which one is driving — the difference in reach is something users must be
// told, not something to paper over.

// sysProxyBackend applies and reverts the OS-level proxy setting.
type sysProxyBackend interface {
	// name identifies the backend for Capabilities / the UI: "wininet",
	// "gsettings", "kde" or "none".
	name() string
	// available reports whether this machine can actually be driven.
	available() bool
	// set points the OS at host:port and returns a snapshot of the settings it
	// overwrote. Each backend renders proxyBypass in its own syntax.
	set(host string, port int) (proxySnapshot, error)
	// revert puts back what set found. The snapshot may be empty (an older lock
	// file, or a backend with nothing to remember), which must mean "turn the
	// proxy off" rather than "do nothing".
	revert(snap proxySnapshot) error
	// pointsAt answers whether the OS proxy currently points at host:port. It is
	// asked rather than "read me the settings" so each backend can compare in
	// its own terms — the same address is spelled differently in the registry,
	// in GSettings and in kioslaverc, and normalising three syntaxes in one
	// place is how a guard ends up fighting a proxy that was never wrong.
	//
	// An error means "could not tell", which callers must treat as "leave it
	// alone" rather than as a mismatch.
	pointsAt(host string, port int) (bool, error)
}

// proxySnapshot is opaque backend-specific state: whatever revert needs to put
// the machine back exactly as it was.
//
// It exists because the obvious implementation is wrong in a way users notice
// weeks later. Reverting by DISABLING the proxy silently discards a setting the
// user may have had before Vair ever ran — a corporate proxy, a PAC URL — and
// they have no reason to connect its loss to a VPN client they disconnected. So
// every backend records what it overwrote and puts that back.
type proxySnapshot map[string]string

// proxyBypass is what must never go through the proxy. Each rule carries both
// renderings on one line rather than being translated at runtime, because the
// two syntaxes are not mechanically convertible: WinINET matches textual
// wildcards and has no idea what a CIDR block is, while GNOME wants real
// prefixes and would treat "10.*" as a hostname. A translator would have to
// guess the mask from the number of octets, and get 172.16.0.0/12 wrong.
var proxyBypass = []bypassRule{
	{win: "localhost", unix: "localhost"},
	{win: "127.*", unix: "127.0.0.0/8"},
	{win: "10.*", unix: "10.0.0.0/8"},
	{win: "172.16.*", unix: "172.16.0.0/12"},
	{win: "192.168.*", unix: "192.168.0.0/16"},
	{win: "*.local", unix: "*.local"},
}

type bypassRule struct{ win, unix string }

//lint:ignore U1000 the Windows backend uses this; the Linux one uses bypassUnix
func bypassWin() []string { return renderBypass(func(r bypassRule) string { return r.win }) }

//lint:ignore U1000 the Linux backends use this; Windows uses bypassWin
func bypassUnix() []string { return renderBypass(func(r bypassRule) string { return r.unix }) }

func renderBypass(pick func(bypassRule) string) []string {
	out := make([]string, 0, len(proxyBypass))
	for _, r := range proxyBypass {
		out = append(out, pick(r))
	}
	return out
}

// proxyLockPath marks that WE currently have the system proxy enabled. Written
// when we set it, removed when we unset it. If it survives to the next startup
// the app was killed or the machine went down without a clean disconnect — see
// clearStaleProxy.
func proxyLockPath() string { return runtimePath("proxy.active") }

// proxyLock is the content of that file. It carries the snapshot because the
// case it exists for — a crash — is exactly the case where the snapshot is no
// longer in memory. The next launch reads it and restores the user's settings
// instead of leaving them on whatever Vair last wrote.
type proxyLock struct {
	Backend  string        `json:"backend"`
	Addr     string        `json:"addr"`
	Snapshot proxySnapshot `json:"snapshot,omitempty"`
}

func readProxyLock() (proxyLock, bool) {
	data, err := os.ReadFile(proxyLockPath())
	if err != nil {
		return proxyLock{}, false
	}
	var l proxyLock
	// A 2.2.0-or-earlier lock file is the bare "127.0.0.1:8080" string, not
	// JSON. It has no snapshot, so reverting from it can only mean "off" — the
	// same behaviour that version had. Recognising it still matters: it means we
	// clean up after an upgrade instead of leaving the machine pointed at a
	// listener that no longer exists.
	if json.Unmarshal(data, &l) != nil {
		return proxyLock{Addr: string(data)}, true
	}
	return l, true
}

// appliedProxyPort is the local port the system proxy currently points at, or 0
// when we haven't set it. Skipping a redundant re-apply is what makes switching
// between configs on the same port feel instant: on Windows it avoids three
// registry writes plus a settings-changed broadcast to every top-level window.
//
// Only touched from the connect/disconnect paths, which are serialised by
// cm.actionMu (plus clearStaleProxy at startup, before any connection), so it
// needs no lock of its own.
var appliedProxyPort int

func setSystemProxy(port int) error {
	if appliedProxyPort == port {
		return nil // already pointed here
	}
	b := sysProxy()
	if !b.available() {
		return fmt.Errorf("system proxy: no supported backend on this desktop")
	}
	// A lock file already here means a previous set is still in force, so what
	// b.set is about to read as "the current settings" is OUR OWN configuration,
	// not the user's. Keep the first snapshot: it is the only one that describes
	// the machine as we found it.
	prev, hadPrev := readProxyLock()
	snap, err := b.set("127.0.0.1", port)
	if err != nil {
		return err
	}
	if hadPrev && prev.Backend == b.name() && len(prev.Snapshot) > 0 {
		snap = prev.Snapshot
	}
	// Record that the proxy is ours and active, so a dirty shutdown can be
	// detected and recovered from on the next launch.
	os.MkdirAll(tabsDir(), 0o755) //nolint:errcheck
	if data, err := json.Marshal(proxyLock{
		Backend:  b.name(),
		Addr:     fmt.Sprintf("127.0.0.1:%d", port),
		Snapshot: snap,
	}); err == nil {
		os.WriteFile(proxyLockPath(), data, 0o600) //nolint:errcheck
	}
	appliedProxyPort = port
	return nil
}

// reapplySystemProxy writes our address back into the OS setting after somebody
// else overwrote it.
//
// Deliberately not setSystemProxy: that one snapshots what it finds so the
// user's settings can be restored later, and what it would find here is the
// hijacker's value. The original snapshot — the machine as we first found it —
// stays in the lock file untouched, which is the whole point.
func reapplySystemProxy(port int) error {
	b := sysProxy()
	if !b.available() {
		return nil
	}
	_, err := b.set("127.0.0.1", port)
	return err
}

// unsetSystemProxy undoes only a proxy WE set — this session, or a stale one
// from a dirty shutdown, which the lock file records. In non-system-proxy mode
// we never touched the OS setting, and clearing it would clobber the user's own.
func unsetSystemProxy() {
	lock, had := readProxyLock()
	if appliedProxyPort == 0 && !had {
		return
	}
	b := sysProxy()
	// A lock written by a different backend is not ours to undo: the user has
	// switched desktop session since (GNOME to KDE, say), and applying a GNOME
	// snapshot through kwriteconfig would write nonsense. Drop the stale file
	// and leave that desktop's settings alone.
	if lock.Backend == "" || lock.Backend == b.name() {
		b.revert(lock.Snapshot) //nolint:errcheck
	}
	os.Remove(proxyLockPath()) //nolint:errcheck
	appliedProxyPort = 0
}

// SystemProxyActive reports whether the OS proxy currently points at us. The UI
// reads it to label a live connection honestly: "system proxy" only when the
// system really was pointed here, and a plain local proxy when it wasn't —
// because the user turned it off, or because this desktop has no backend.
func SystemProxyActive() bool { return appliedProxyPort > 0 }

// ReapplySystemProxy reconciles the OS proxy with the NoSystemProxy setting
// while a proxy-mode connection is live, so the toggle applies instantly:
// off→on unsets it, on→off points it back at the running listener.
func ReapplySystemProxy() {
	cs := state.conn.snap()
	if cs.Status != ConnConnected || cs.Mode != ModeProxy || cs.HTTPPort <= 0 {
		return
	}
	if noSystemProxyEnabled() {
		unsetSystemProxy()
	} else if err := setSystemProxy(cs.HTTPPort); err != nil {
		fmt.Fprintf(os.Stderr, "⚠  setSystemProxy: %v\n", err)
	}
}

// clearStaleProxy runs at startup: if the lock file outlived the process that
// wrote it, the machine is still pointed at a listener that no longer exists.
func clearStaleProxy() {
	if _, err := os.Stat(proxyLockPath()); err == nil {
		unsetSystemProxy()
	}
}
