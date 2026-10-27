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

// UninstallCleanup gives the machine back, then removes the marks this copy of
// Vair left in the registry and the firewall. Called by the uninstaller through
// `Vair.exe --uninstall-cleanup` before a single file is deleted; see
// build/windows/installer/vair.iss.
//
// Ordering is the point: the running copy is closed first, then the
// system-level state (firewall, proxy, adapters) goes, because that is what
// leaves a machine unusable if it survives, and the registry entries are only
// untidiness by comparison.
//
// Only what belongs to THIS copy. A second copy of Vair on the same machine —
// the portable one beside an installed one — registers the same vair:// scheme,
// the same Run entry and firewall rules under the same names, and removing one
// copy used to take the other's with it. Each entry is checked for the path it
// points at, and the firewall rules are removed by program.
//
// Per-user by nature. An all-users installation puts one copy of the program on
// the machine and a set of these keys under every account that ran it; the
// uninstaller runs as one account and can only speak for that one. The rest are
// three values that point at a path that no longer exists, which Windows
// ignores.
func UninstallCleanup() []string {
	self, err := ownExe()
	if err != nil {
		return []string{"this program: COULD NOT find its own path (" + err.Error() + ")"}
	}

	line, others := closeRunningCopy(self)
	out := []string{line}
	if strings.Contains(line, "COULD NOT") {
		// Still running, and still holding everything below: releasing the kill
		// switch under it would leave a connected client with no protection.
		return out
	}

	// The kill switch, the system proxy and the tunnel adapters are system-wide,
	// with no owner written on them. With another copy of Vair running they are
	// that copy's — its block is what keeps it from leaking — and are left alone.
	if len(others) == 0 {
		out = append(out, core.UninstallCleanup()...)
	} else {
		out = append(out, "another copy of Vair is running ("+others[0]+"): its kill switch, system proxy and adapters were left as they are")
	}

	out = append(out, removeOwnRunEntry(self))
	out = append(out, removeOwnDeepLink(self))
	out = append(out, removeOwnNotifyID(self))
	out = append(out, removeOwnFirewallRules(self, len(others) == 0)...)
	return out
}

// removeOwnRunEntry removes the autostart entry when it starts this copy.
func removeOwnRunEntry(self string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return "autostart: nothing registered"
	}
	defer k.Close()
	v, _, err := k.GetStringValue(autostartValueName)
	if err != nil {
		return "autostart: nothing registered"
	}
	if exe := runEntryExe(v); !samePath(exe, self) {
		return "autostart: left as it is (it starts " + exe + ")"
	}
	if err := k.DeleteValue(autostartValueName); err != nil {
		return "autostart: " + err.Error()
	}
	return "autostart: removed"
}

// removeOwnDeepLink unregisters vair:// when the links open this copy.
func removeOwnDeepLink(self string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, deepLinkClassBase+`\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		return "vair:// links: nothing registered"
	}
	cmd, _, err := k.GetStringValue("")
	k.Close()
	if err != nil {
		return "vair:// links: nothing registered"
	}
	if exe := runEntryExe(cmd); !samePath(exe, self) {
		return "vair:// links: left as they are (they open " + exe + ")"
	}
	if err := RegisterDeepLink(false); err != nil {
		return "vair:// links: " + err.Error()
	}
	return "vair:// links: unregistered"
}

// removeOwnNotifyID removes the notification identity unless it names another
// copy that is still on disk: every copy writes the same key, naming itself.
func removeOwnNotifyID(self string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, notifyAppIDKey, registry.QUERY_VALUE)
	if err != nil {
		return "notifications: nothing registered"
	}
	icon, _, _ := k.GetStringValue("IconUri")
	k.Close()
	if icon != "" && !samePath(icon, self) && fileExists(icon) {
		return "notifications: left as they are (they belong to " + icon + ")"
	}
	if err := registry.DeleteKey(registry.CURRENT_USER, notifyAppIDKey); err != nil && !os.IsNotExist(err) {
		return "notifications: " + err.Error()
	}
	return "notifications: app id removed"
}

// removeOwnFirewallRules removes every firewall rule that names this copy's
// program or its engines: the remote-access rule, the LAN-proxy rules for UDP,
// the rule sing-box registers for itself — and the ones Windows makes on its
// own the first time a program listens, which the uninstaller never knew about
// and which outlived every uninstall until now.
//
// The LAN proxy's TCP rule names ports rather than a program, so it cannot be
// told apart from another copy's; it goes only when no other copy is running.
// A copy started later puts it back at startup when it needs it.
func removeOwnFirewallRules(self string, alone bool) []string {
	programs := []string{self}
	for _, engine := range []string{"sing-box.exe", "xray.exe"} {
		if p := filepath.Join(filepath.Dir(self), "bin", engine); fileExists(p) {
			programs = append(programs, p)
		}
	}
	out := core.RemoveProgramFirewallRules(programs...)
	if alone {
		RemoveProxyFirewallRule()
		out = append(out, "firewall: the LAN proxy rule is gone")
	}
	return out
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
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

// HasReleaseBlockFlag reports `--release-block`: take the kill-switch block
// down and exit.
func HasReleaseBlockFlag(args []string) bool {
	return hasFlag(args, "--release-block")
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
