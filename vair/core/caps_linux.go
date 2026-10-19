package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// isPrivileged reports whether this process can create a TUN device and rewrite
// routes. Under the planned architecture the GUI deliberately runs unprivileged
// and hands that work to a helper daemon, so this normally answers false — and
// that is correct, not a defect. It stays because running the whole app as root
// is a thing users do, and the answer should be truthful when they do.
func isPrivileged() bool { return os.Geteuid() == 0 }

// platformCaps is the Linux baseline. Several fields are false today because the
// feature genuinely isn't implemented yet, not because Linux can't do it; each
// flips as its piece lands, and the UI follows without a frontend change.
//
//   - TUN: needs the privileged helper, and "needs" is measured — the capability
//     is true when we can actually talk to its socket. Not installed reads as
//     tun_block_reason "no_helper", which the UI turns into an install button
//     rather than the "requires admin" it used to claim, because no amount of
//     elevation makes an absent service appear.
//   - SplitTunnel: measured, not assumed. sing-box matches process_name on
//     Linux by reading /proc, which needs CAP_SYS_PTRACE + CAP_DAC_READ_SEARCH
//     — those arrive with the helper, and so does TUN mode, which is the only
//     mode the rules apply in. Verified end to end rather than reasoned about:
//     with "curl" in the list, curl exits on the local address while wget and
//     python go through the tunnel. ProcessPicker is separate and always true —
//     reading /proc needs no privilege at all.
//   - Autostart / DeepLinkToggle: real, and both are plain files under the
//     user's config tree rather than anything privileged.
//   - SystemProxy: detected at runtime, not fixed — a GNOME box drives
//     gsettings, a Plasma box kioslaverc, and a bare window manager gets
//     neither. Even where it works it reaches far less than the Windows switch
//     does (GIO/KIO apps and browsers set to "use system settings", and nothing
//     that reads http_proxy from its environment), which is why the backend
//     NAME is reported and not just a boolean.
//   - QRScreen: Wayland forbids arbitrary screen capture; it needs the
//     xdg-desktop-portal ScreenCast path. Scanning a QR from a FILE is
//     unaffected and stays available.
//   - OpenFolder: true — xdg-open handles both the data folder and outbound
//     links, and xdg-utils is a package dependency so it is always there.
//   - FirewallRules: opening a port needs root, which the GUI does not have. The
//     UI shows the command instead of pretending.
//   - SelfUpdate: names how this copy was INSTALLED, which decides what an
//     update could ever do to it. Applying one is a separate matter — today
//     only "inplace" is implemented, and Linux is never that.
func platformCaps() Capabilities {
	b := sysProxy()
	return Capabilities{
		TUN:            helperInstalled(),
		CustomTitlebar: true, // frameless windows map fine; see window_chrome_linux.go
		// gtk_window_begin_move_drag has no double-click gesture, so the
		// frontend supplies one.
		NativeTitlebarGestures: false,
		SplitTunnel:            helperInstalled(),
		ProcessPicker:          true,
		SystemProxy:            b.available(),
		SystemProxyBackend:     b.name(),
		// Writing ~/.config/autostart/<id>.desktop needs no privileges and every
		// desktop we target reads it.
		Autostart: true,
		// The toggle drives the handler association in mimeapps.list, which is
		// xdg-mime's job. Without xdg-utils there is nothing to drive — the
		// scheme still works if a packaged desktop entry claims it, but the
		// switch would be a lie.
		DeepLinkToggle:       deepLinkToggleAvailable(),
		QRScreen:             false,
		OpenFolder:           true,
		KillSwitch:           false,
		KillSwitchPersistent: false,
		FirewallRules:        false,
		SelfUpdate:           installKind(),
	}
}

// deepLinkToggleAvailable reports whether the scheme association can be changed.
func deepLinkToggleAvailable() bool {
	_, err := exec.LookPath("xdg-mime")
	return err == nil
}

// installKind reports how this binary got here, which decides what an update is
// allowed to do to it.
//
//   - "appimage": the runtime sets $APPIMAGE to the bundle's own path, and
//     updating means replacing that one file.
//   - "package": the binary lives in a system prefix, so it is root-owned and
//     belongs to dpkg/rpm. Rewriting it in place would leave the package
//     database describing a file that no longer matches, and would need root
//     the GUI deliberately does not have.
//   - "none": a tarball or a build tree. Writable, but no bare-binary asset is
//     published for Linux to write there, so claiming otherwise would be a
//     promise the manifest cannot keep.
func installKind() string {
	if os.Getenv("APPIMAGE") != "" {
		return "appimage"
	}
	exe, err := os.Executable()
	if err != nil {
		return "none"
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	for _, prefix := range []string{"/usr/bin/", "/usr/local/bin/", "/opt/", "/usr/lib/", "/snap/"} {
		if strings.HasPrefix(exe, prefix) {
			return "package"
		}
	}
	return "none"
}

// tunUnavailableReason distinguishes "this platform has no TUN" from "the
// helper that provides it is not installed" — the second is fixable by the
// user, in one click, and telling them so is the entire point.
func tunUnavailableReason() string { return "no_helper" }

// tunNotReadyReason: the helper is installed, so the remaining question is
// whether we can actually talk to it. Elevation is deliberately not part of the
// answer — the app never becomes root.
func tunNotReadyReason() string {
	if ok, reason := helperReachable(); !ok {
		return reason
	}
	return ""
}
