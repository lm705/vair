package core

// tunRunsInHelper: the tunnel is the helper service's when the service is
// installed — then the app never needs administrator rights. Without it (a
// portable copy on a machine with no installed Vair) the app runs the engine as
// its own child, and TUN needs the app to run as administrator.
func tunRunsInHelper() bool { return serviceMode() }

// tunPreconditionError says what stands between the user and a TUN session:
// the service's problem when there is a service, the missing rights when there
// is none.
func tunPreconditionError() string {
	if serviceMode() {
		_, msg := helperProblem()
		return msg
	}
	if !isPrivileged() {
		return "TUN mode requires administrator/root. Run the program as admin."
	}
	return ""
}

// helperIPv6Around: with IPv6 sent around the tunnel, the session block leaves
// IPv6 alone (but for DNS), as the app's own block does.
func helperIPv6Around() bool { return !ipv6InTunnel() }

// tunAvailable says whether a TUN session can be started at all right now —
// the service is there, or the app has the rights itself.
func tunAvailable() bool { return serviceMode() || isPrivileged() }

// autoTUNMissing is what AUTO says TUN lacks: the helper service for a copy
// the setup program installed, which comes with one; administrator rights for
// a portable copy.
func autoTUNMissing() string {
	if installedBySetupHere() {
		return "needs_service"
	}
	return "needs_admin"
}
