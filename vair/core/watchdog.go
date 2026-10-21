package core

import "time"

// What the supervisor may conclude without asking the network, and when it must
// stop trusting what it measured earlier.
//
// Two questions, both about the same loop.
//
// The first is whether a probe is needed at all. Every fifteen seconds the
// supervisor sends a request through the tunnel to find out whether it still
// carries anything — while the user's own traffic is going through it and
// answering that question far better than any probe could. Vair copies every
// byte of that traffic through its own relay in both modes, so the counters are
// already in hand: bytes coming back are proof of life, bytes going out with
// nothing coming back are the first sign of the failure the probe exists to
// catch, and neither costs a packet to observe.
//
// The second is whether the machine was asleep. A laptop that is shut and
// opened an hour later comes back to a supervisor whose timers all fired at
// once and whose last measurement is an hour old — which used to read as a dead
// tunnel and cost the user their connection for no reason.

// livenessHint is what the byte counters say about a connection nobody has
// probed yet.
type livenessHint int

const (
	// livenessUnknown: nothing moved, or nothing here counts bytes (a tunnel
	// carried by the engine alone has no relay in the middle). Ask the network.
	livenessUnknown livenessHint = iota
	// livenessAlive: bytes came back through the tunnel since the last look.
	livenessAlive
	// livenessSuspect: bytes went out and none came back, for long enough that
	// an ordinary pause in a conversation no longer explains it.
	livenessSuspect
)

const (
	// How long traffic may leave with nothing returning before the tunnel is
	// worth asking about. Long enough to cover a slow request, short enough to
	// beat the fifteen-second interval it replaces.
	livenessSuspectAfter = 5 * time.Second
	// A connection that is visibly carrying traffic is still probed, just
	// rarely: the panel shows a live round-trip time and the "too slow" check
	// reads it, so it must not go stale for the whole session.
	livenessQuietProbe = 2 * time.Minute
)

// livenessWatch remembers the counters between ticks. Zero value is ready.
type livenessWatch struct {
	raw      string // the configuration these counters belong to
	up, down int64
	seen     bool
	sentAt   time.Time // when bytes first went out with nothing coming back
}

// look reports what the counters have to say. raw identifies the session, so a
// reconnect — whose counters start again at zero — is never read as traffic
// that stopped.
func (w *livenessWatch) look(raw string, up, down int64, now time.Time) livenessHint {
	if !w.seen || w.raw != raw {
		*w = livenessWatch{raw: raw, up: up, down: down, seen: true}
		return livenessUnknown
	}
	prevUp, prevDown := w.up, w.down
	w.up, w.down = up, down
	switch {
	case down > prevDown:
		w.sentAt = time.Time{}
		return livenessAlive
	case up > prevUp:
		if w.sentAt.IsZero() {
			w.sentAt = now
		}
		if now.Sub(w.sentAt) >= livenessSuspectAfter {
			return livenessSuspect
		}
		return livenessUnknown
	default:
		// An idle connection. Neither alive nor suspect: nobody asked it for
		// anything, so it has not failed to answer.
		w.sentAt = time.Time{}
		return livenessUnknown
	}
}

// reset forgets the counters, for a caller that knows the evidence is stale.
func (w *livenessWatch) reset() { *w = livenessWatch{} }

// clockJumped reports whether the world moved on without this loop.
//
// wallGap is how much time passed on the clock on the wall, monoGap how much
// passed for the process; tick is how long the loop meant to wait. The two
// disagree when the machine slept — every operating system stops the monotonic
// clock while it is suspended, and the wall clock comes back with the hours
// that went by — and the loop alone falls behind when the process was frozen
// for some other reason: a paused virtual machine, a host swapping heavily.
//
// Both mean the same thing to a supervisor: what was measured before it is not
// evidence about now.
func clockJumped(wallGap, monoGap, tick time.Duration) bool {
	const slack = 4 * time.Second
	if wallGap-monoGap > slack {
		return true // the clock ran while we did not
	}
	return monoGap > tick+slack // we did not run
}
