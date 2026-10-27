package core

// tunRunsInHelper — the app never becomes root here, so the TUN engine belongs
// to the privileged helper.
func tunRunsInHelper() bool { return true }

// tunPreconditionError — the app is never root here, so the question is whether
// the privileged helper is installed and reachable. The message names the fix
// the user can actually perform.
func tunPreconditionError() string {
	_, msg := helperProblem()
	return msg
}

// helperIPv6Around is Windows' question — whether IPv6 goes around the
// tunnel's block. The Linux helper builds its block from the tunnel itself.
func helperIPv6Around() bool { return false }

// tunAvailable says whether a TUN session can be started at all: the helper is
// installed. The app itself is never root here — asking that, as AUTO used to,
// answered "no" on every Linux machine.
func tunAvailable() bool { return helperInstalled() }

// autoTUNMissing is what AUTO says TUN lacks: on Linux, the helper.
func autoTUNMissing() string { return "needs_helper" }
