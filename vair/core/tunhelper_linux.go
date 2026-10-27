package core

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"vair/helper/api"
)

// The Linux helper: a unix socket systemd creates for the installing user's
// group.

// helperInstalled reports whether the service has been set up on this machine.
//
// The socket, not the binary: the package ships vair-helper whether or not the
// user has ever enabled it, and a binary on disk says nothing about whether a
// service is running. systemd removes the whole RuntimeDirectory on stop, so
// the socket's existence tracks the service's.
func helperInstalled() bool {
	_, err := os.Stat(api.SocketPath)
	return err == nil
}

// keptBlockClient is a connection to take down a block kept for a successor
// that is not coming: a fresh one, closed after.
func keptBlockClient() (*helperClient, func(), error) {
	h, err := dialHelper()
	if err != nil {
		return nil, nil, err
	}
	return h, h.Close, nil
}

// dialHelper connects and completes the handshake.
func dialHelper() (*helperClient, error) {
	nc, err := net.DialTimeout("unix", api.SocketPath, 3*time.Second)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("the Vair helper is not installed")
		}
		// EACCES here means the socket exists but this account is not in its
		// group — the helper was installed for someone else.
		return nil, fmt.Errorf("cannot reach the helper: %w", err)
	}
	return newHelperClient(nc)
}

// helperReachable answers the question Capabilities needs: is there a helper we
// can actually talk to, and does it speak our protocol?
//
// It connects rather than stat-ing, because the failures that matter are not
// visible from the filesystem — a socket owned by a group this account is not
// in, or a helper left over from an older app version.
func helperReachable() (bool, string) {
	code, _ := helperProblem()
	return code == "", code
}

// helperProblem is the same question with the answer kept whole: a reason code
// for Capabilities, and the sentence to put in front of the user.
//
// The two are separate because they serve different consumers, and collapsing
// them lost the only message worth reading. A version-skewed helper used to
// surface as "installed but not reachable — check systemctl status", which sends
// the user to a command that reports the service is active and running. What
// they need is the line CheckProtocol already writes: which side is old, and
// that reinstalling the helper fixes it.
func helperProblem() (code, message string) {
	if !helperInstalled() {
		return "no_helper", "TUN mode needs the Vair helper service. Click \"install helper\" next to the TUN switch."
	}
	h, err := dialHelper()
	if err != nil {
		vlog("warn", "helper: %v", err)
		return "no_helper", err.Error()
	}
	h.Close()
	return "", ""
}
