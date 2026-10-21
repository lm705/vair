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

// installKind reports how an update may be applied to THIS copy.
//
//   - "inplace": the exe can be replaced where it stands. The portable download,
//     and a per-user install under %LOCALAPPDATA%\Programs, which is the default
//     precisely so this keeps working.
//   - "installer": the exe is somewhere the user cannot write — Program Files.
//     Rewriting it would need administrator rights the GUI deliberately does not
//     have, so the update is the setup program, run elevated.
//
// It was a constant until the installer existed, and the constant was "inplace"
// for everyone; an all-users installation would have gone on offering an update
// it could not perform.
func installKind() string {
	exe, err := os.Executable()
	if err != nil {
		return "inplace" // cannot tell; behave as every release before this one
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	// Writability is asked of the DIRECTORY, not the file: replacing an exe means
	// renaming the old one aside and creating a new one, both of which are
	// directory operations. A running exe is locked, so opening it for write says
	// nothing useful anyway.
	probe := filepath.Join(filepath.Dir(exe), ".vair-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "installer"
	}
	f.Close()
	os.Remove(probe)
	return "inplace"
}

// tunUnavailableReason is unreachable on Windows: TUN is always supported here.
func tunUnavailableReason() string { return "unsupported" }

// tunNotReadyReason — the one thing that can be missing is elevation.
func tunNotReadyReason() string {
	if !isPrivileged() {
		return "not_admin"
	}
	return ""
}
