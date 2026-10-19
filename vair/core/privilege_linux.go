package core

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"vair/helper/api"
)

// installerPath is what the package puts down for this. A constant, because it
// is the target of a polkit action: the action names an exact path, and a
// caller-chosen one would defeat the point of authorising a specific program.
const installerPath = "/usr/lib/vair/vair-helper-install"

// EnsurePrivilege installs the helper service, asking for a password once.
//
// This is where Linux and Windows diverge most sharply, and why the function is
// named for the goal. Windows relaunches the whole app elevated. Here the app
// stays unprivileged forever — the browser engine, the user's configs and the
// subscription fetcher never run as root — and instead a small service is
// enabled that owns the tunnel and nothing else. One prompt, at the moment the
// user first asks for TUN mode, and none afterwards.
//
// Returns "" when the helper is up, or a reason to show the user.
func EnsurePrivilege() string {
	// Reachable, not merely present. A socket file proves a service exists, not
	// that it is one this app can talk to — after an app update the installed
	// helper can be a protocol behind, and answering "already there" to that
	// would leave the user pressing a button that does nothing about the only
	// problem they have. Reinstalling is exactly the fix, and the installer is
	// idempotent, so the same path serves both cases.
	if ok, _ := helperReachable(); ok {
		return ""
	}
	if _, err := os.Stat(installerPath); err != nil {
		return "TUN mode needs the Vair helper, which comes with the .deb/.rpm package — " +
			"this build was not installed from one"
	}
	if _, err := exec.LookPath("pkexec"); err != nil {
		// Rather than silently running nothing, hand over the exact command.
		// Someone on a system without polkit still has a terminal.
		return "no pkexec on this system — install the helper by hand: sudo " + installerPath
	}

	// pkexec shows the dialog described by com.vair.app.helper.policy and runs
	// the installer as root. It blocks until the user answers, which is why the
	// caller runs this off the UI thread.
	out, err := exec.Command("pkexec", installerPath).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			// 126 is pkexec's "dismissed or not authorised" — worth naming,
			// because it is the common case and it is not an error in the app.
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 126 {
				return "the installation was cancelled"
			}
			msg = err.Error()
		}
		vlog("error", "helper install: %s", msg)
		return msg
	}
	if !helperInstalled() {
		return fmt.Sprintf("the installer finished but %s never appeared — check: journalctl -u vair-helper", api.SocketPath)
	}
	vlog("info", "helper installed")
	return ""
}
