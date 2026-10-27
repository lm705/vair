package core

import (
	"os"
	"path/filepath"
)

// isPrivileged reports whether we can do the things TUN mode needs — create the
// adapter, rewrite the routing table, install the WFP filter.
//
// Opening \\.\PHYSICALDRIVE0 is the cheap standard probe: it needs
// administrator, it touches nothing, and it needs no extra imports. A proper
// token check would be more precise and no more useful.
func isPrivileged() bool {
	f, err := os.Open(`\\.\PHYSICALDRIVE0`)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// platformCaps — everything Vair does on Windows, it does. This is the baseline
// the Linux side is measured against.
func platformCaps() Capabilities {
	return Capabilities{
		TUN:            true,
		CustomTitlebar: true,
		// The caption hit-test gives us double-click-to-maximise for free.
		NativeTitlebarGestures: true,
		SplitTunnel:            true,
		ProcessPicker:          true,
		SystemProxy:            true,
		SystemProxyBackend:     "wininet",
		Autostart:              true,
		DeepLinkToggle:         true,
		QRScreen:               true,
		OpenFolder:             true,
		KillSwitch:             true,
		KillSwitchPersistent:   true,
		FirewallRules:          true,
		SelfUpdate:             installKind(),
	}
}

// helperServiceCap is the service's state for the settings. Asked after the
// TUN question, which starts a stopped service: asked before it, the same
// answer said "stopped" and "TUN ready" at once.
func helperServiceCap() string { return helperServiceState() }

// installKind reports how an update may be applied to THIS copy.
//
//   - "inplace": the exe can be replaced where it stands — the portable
//     download, which carries its engines inside itself.
//   - "installer": the setup program put this copy here, and the setup program
//     updates it.
//
// Installed means installed, whoever is running it. The update for this kind
// of copy is not the exe alone: the engines sit beside it in bin\, and the
// program prefers that copy to the one a portable exe carries, so replacing
// only the exe left the engines at whatever version was first installed — for
// good. And writability was not the test either: an all-users copy in Program
// Files is writable to an elevated Vair (TUN mode runs elevated), which then
// offered to overwrite the installed program with the portable one.
//
// The setup program leaves its uninstaller beside the exe; that is what is
// looked for. A folder this program cannot write to is the other sign — no
// in-place update could work there anyway.
func installKind() string {
	exe, err := os.Executable()
	if err != nil {
		return "inplace" // cannot tell; behave as every release before this one
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	dir := filepath.Dir(exe)
	if installedBySetup(dir) {
		return "installer"
	}
	// Writability is asked of the DIRECTORY, not the file: replacing an exe means
	// renaming the old one aside and creating a new one, both of which are
	// directory operations. A running exe is locked, so opening it for write says
	// nothing useful anyway.
	probe := filepath.Join(dir, ".vair-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "installer"
	}
	f.Close()
	os.Remove(probe)
	return "inplace"
}

// installedBySetup reports whether the setup program's uninstaller is in dir:
// Inno Setup writes it there as unins000.exe with its log beside it (a higher
// number when the folder already held one).
func installedBySetup(dir string) bool {
	m, _ := filepath.Glob(filepath.Join(dir, "unins[0-9][0-9][0-9].dat"))
	return len(m) > 0
}

// tunUnavailableReason is unreachable on Windows: TUN is always supported here.
func tunUnavailableReason() string { return "unsupported" }

// tunNotReadyReason — with the helper service installed, whether it can be
// reached; without it, elevation. A copy the setup program installed always
// comes with the service, so for one without it the answer is the service, not
// administrator rights.
func tunNotReadyReason() string {
	if serviceMode() {
		code, _ := helperProblem()
		return code
	}
	if installedBySetupHere() {
		return "no_service"
	}
	if !isPrivileged() {
		return "not_admin"
	}
	return ""
}

// installedBySetupHere: this copy was put here by the setup program.
func installedBySetupHere() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return installedBySetup(filepath.Dir(exe))
}
