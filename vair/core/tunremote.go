package core

import (
	"context"
	"os"
	"os/exec"
)

// tunStopper is a TUN engine we do not own the process of.
//
// It exists so teardown has one thing to call regardless of where the engine
// runs: as our child (Windows) or inside the privileged helper (Linux). The
// interface is deliberately tiny — starting is platform-specific enough to be
// worth its own code, stopping is not.
type tunStopper interface {
	// Stop tears the tunnel down and returns when it is gone.
	Stop() error
	// Close releases the transport. Safe to call after Stop.
	Close()
}

// hybridParts carries the unprivileged half of a hybrid TUN session — the xray
// child and the accounting around it — to whichever function finishes the
// connect. Bundled rather than passed as eight parameters, and shared between
// platforms so the two paths cannot disagree about what a session owns.
type hybridParts struct {
	xrayCmd     *exec.Cmd
	xrayCancel  context.CancelFunc
	xrayTmp     string
	counter     *trafficCounter
	fwdCancel   context.CancelFunc
	isChain     bool
	chainLabels []string
	chainRaws   []string
}

// stopHybridParts tears down that half. Used when the front-end fails to start
// and the session never happens.
//
//lint:ignore U1000 called from tunstart_linux.go; on Windows the app runs the engine itself
func (h hybridParts) stop() {
	if h.fwdCancel != nil {
		h.fwdCancel()
	}
	if h.xrayCmd != nil && h.xrayCmd.Process != nil {
		h.xrayCmd.Process.Kill() //nolint:errcheck
	}
	if h.xrayCancel != nil {
		h.xrayCancel()
	}
	if h.xrayTmp != "" {
		os.Remove(h.xrayTmp) //nolint:errcheck
	}
}
