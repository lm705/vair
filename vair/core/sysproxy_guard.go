package core

import "time"

// Keeping the system proxy pointed at us for as long as we are connected.
//
// Setting the OS proxy once is not the same as it staying set. Another VPN
// client, a group policy refresh, a "cleaner", a browser extension with the
// proxy permission — all of them write the same setting, and when they do,
// traffic leaves outside the tunnel while the app still says "connected".
// Nothing in the program notices, because nothing looks.
//
// The complement to this is what disconnecting does: restore the user's own
// settings rather than clearing them. Both halves are about the same idea —
// the OS proxy belongs to whoever set it, and we should be honest about which
// of us that currently is.

// proxyGuardEvery is how often the setting is examined. Long enough not to
// matter (reading two registry values or two GSettings keys), short enough that
// a hijack is measured in seconds rather than in "when the user noticed".
const proxyGuardEvery = 20 * time.Second

// A burst of corrections means something is writing the setting as fast as we
// are: at that point the two programs are in a tug-of-war that neither wins,
// and the user's proxy setting flickers between two values for as long as both
// run. The guard stands down and says so once.
//
// Measured over a window rather than as "consecutive corrections", because a
// clean reading between two hijacks is normal — the check lands after we have
// already put the setting back. Counting consecutive events made the tally
// reset every time and the safety valve could never trip.
const (
	proxyGuardBurst  = 3
	proxyGuardWindow = 5 * time.Minute
)

// proxyGuardState is the tally kept between ticks.
type proxyGuardState struct {
	lastCheck   time.Time
	burstStart  time.Time
	corrections int
	stoodDown   bool
}

// guardSystemProxy re-points the OS at us when something else has taken the
// setting over. Called from the supervisor tick; keeps its own schedule.
func guardSystemProxy(g *proxyGuardState) {
	if time.Since(g.lastCheck) < proxyGuardEvery {
		return
	}
	g.lastCheck = time.Now()

	settingsMu.RLock()
	off := appSettings.ProxyGuardDisabled
	settingsMu.RUnlock()
	if off {
		return
	}

	cm := state.conn
	// The connect and disconnect paths own appliedProxyPort and the OS setting;
	// stepping between them would race a re-apply against a revert and could
	// leave the user's proxy pointing at a port nothing listens on. Skipping a
	// round costs twenty seconds.
	if !cm.actionMu.TryLock() {
		return
	}
	defer cm.actionMu.Unlock()

	port := appliedProxyPort
	if port == 0 {
		// Nothing of ours is applied. A fresh connection starts with a clean
		// slate rather than inheriting the tally from the last one.
		*g = proxyGuardState{lastCheck: g.lastCheck}
		return
	}

	b := sysProxy()
	ok, err := b.pointsAt("127.0.0.1", port)
	if err != nil || ok {
		return
	}

	// A burst that has gone quiet is forgotten, so an app that misbehaves once
	// an hour is corrected every time.
	if time.Since(g.burstStart) > proxyGuardWindow {
		g.burstStart, g.corrections, g.stoodDown = time.Now(), 0, false
	}
	if g.stoodDown {
		return
	}

	g.corrections++
	if err := reapplySystemProxy(port); err != nil {
		vlog("warn", "system proxy: could not restore the setting: %v", err)
		return
	}
	if g.corrections >= proxyGuardBurst {
		g.stoodDown = true
		vlog("warn", "system proxy: it keeps being changed — restored %d times in %s, now leaving it alone. "+
			"Another VPN or proxy program is probably running.", g.corrections, proxyGuardWindow)
		return
	}
	vlog("warn", "system proxy: it was changed by something else — pointed back at Vair")
}
