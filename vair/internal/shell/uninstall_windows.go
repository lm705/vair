//go:build windows

package shell

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"

	"vair/core"
)

// notifyAppIDKey is where core registers the app id that lets Windows show our
// toasts. Named again here rather than exported from core: this is the only
// other place that needs it, and it needs it to delete the key, not to use it.
const notifyAppIDKey = `Software\Classes\AppUserModelId\Vair`

// UninstallCleanup gives the machine back, then removes the marks Vair left in
// the registry. Called by the uninstaller through `Vair.exe --uninstall-cleanup`
// before a single file is deleted; see build/windows/installer/vair.iss.
//
// Ordering is the point: the system-level state (firewall, proxy, adapters) goes
// first, because that is what leaves a machine unusable if it survives, and the
// registry entries are only untidiness by comparison.
//
// Per-user by nature. An all-users installation puts one copy of the program on
// the machine and a set of these keys under every account that ran it; the
// uninstaller runs as one account and can only speak for that one. The rest are
// three values that point at a path that no longer exists, which Windows
// ignores.
func UninstallCleanup() []string {
	out := core.UninstallCleanup()

	if err := ApplyAutostart(false); err != nil {
		out = append(out, "autostart: "+err.Error())
	} else {
		out = append(out, "autostart: removed")
	}

	if err := RegisterDeepLink(false); err != nil {
		out = append(out, "vair:// links: "+err.Error())
	} else {
		out = append(out, "vair:// links: unregistered")
	}

	switch err := registry.DeleteKey(registry.CURRENT_USER, notifyAppIDKey); {
	case err == nil:
		out = append(out, "notifications: app id removed")
	case os.IsNotExist(err):
		out = append(out, "notifications: nothing registered")
	default:
		out = append(out, "notifications: "+err.Error())
	}

	// The two firewall rules the app opens on request: the web interface port and
	// the LAN proxy. Removing a rule that was never added is not an error, and
	// netsh says so quietly.
	RemoveFirewallRule()
	RemoveProxyFirewallRule()
	out = append(out, "firewall rules: removed")

	return out
}

// RunUninstallCleanup performs the cleanup, records what it did, and exits
// non-zero if anything important could not be undone. It is the whole of
// `--uninstall-cleanup`: no window, no engine, no store.
//
// The log goes to the DATA directory, not beside the program: the program's own
// folder is being deleted around us, and a file written into it after the
// uninstaller took inventory is a file the uninstaller leaves behind — an empty
// Vair folder with one log in it, which is exactly the litter this whole path
// exists to avoid. The data directory survives the uninstall by default and is
// where someone would look anyway.
func RunUninstallCleanup() {
	lines := UninstallCleanup()
	report := "Vair uninstall cleanup\n"
	failed := false
	for _, l := range lines {
		report += "  " + l + "\n"
		if strings.Contains(l, "COULD NOT") {
			failed = true
		}
	}
	writeUninstallLog(report)
	if failed {
		// The uninstaller reads this and tells the user what is still switched on.
		os.Exit(1)
	}
}

func writeUninstallLog(report string) {
	for _, path := range []string{
		filepath.Join(core.AppRootDir(), "uninstall.log"),
		filepath.Join(os.TempDir(), "vair-uninstall.log"),
	} {
		if f, err := os.Create(path); err == nil {
			fmt.Fprint(f, report)
			f.Close()
			return
		}
	}
}

// HasUninstallCleanupFlag reports whether we were started by the uninstaller.
func HasUninstallCleanupFlag(args []string) bool {
	return hasFlag(args, "--uninstall-cleanup")
}

// HasEnableAutostartFlag reports whether the installer is telling us the user
// ticked "start with Windows".
func HasEnableAutostartFlag(args []string) bool {
	return hasFlag(args, "--enable-autostart")
}

func hasFlag(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// RunEnableAutostart records the installer's checkbox and acts on it, then
// exits. Two steps, and both are needed: the registry entry is what Windows
// reads at logon, and the setting is what the program's own switch shows. An
// installer that wrote only the registry would leave the switch saying "off"
// about a program that starts by itself — and the first time the user touched
// that switch, it would turn autostart off by "turning it on".
func RunEnableAutostart() {
	s := core.GetSettings()
	s.AutostartEnabled = true
	core.SetSettings(s)
	if err := ApplyAutostart(true); err != nil {
		log.Printf("autostart: %v", err)
	}
}
