package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Autostart and the vair:// scheme, the XDG way. Windows keeps both in the
// registry; here each is a file in the user's config tree, which is why these
// need no privileges and leave nothing behind that an uninstall would miss.

// xdgConfigHome is $XDG_CONFIG_HOME, or ~/.config when unset — the spec's
// default, not a guess.
func xdgConfigHome() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config")
}

func xdgDataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

// desktopFileName is the basename of our entry everywhere it appears: the
// package's copy, the autostart copy, and the handler association.
const desktopFileName = AppID + ".desktop"

// autostartPath is where the XDG autostart spec says a per-user entry lives.
// Every desktop we target reads it — GNOME, KDE, XFCE, Cinnamon, MATE.
func autostartPath() string {
	c := xdgConfigHome()
	if c == "" {
		return ""
	}
	return filepath.Join(c, "autostart", desktopFileName)
}

// ApplyAutostart writes or removes the autostart entry.
//
// The Exec line points at THIS binary rather than at /usr/bin/vair, so a build
// run from a source tree autostarts itself rather than a packaged copy that may
// not exist — and a user who moves the binary gets a stale entry that fails
// visibly at login instead of silently launching the wrong thing.
func ApplyAutostart(enable bool) error {
	path := autostartPath()
	if path == "" {
		return fmt.Errorf("autostart: no config directory")
	}
	if !enable {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// --autostart is what tells main() to start hidden in the tray; the flag is
	// the same one the Windows Run-key entry carries.
	//
	// X-GNOME-Autostart-enabled exists because GNOME writes it as false when the
	// user disables the entry in Tweaks. Setting it true on every enable makes
	// the toggle authoritative rather than losing to a stale disable.
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Vair
Comment=Start Vair at login, hidden in the tray
Exec=%s --autostart
Icon=%s
Terminal=false
NoDisplay=true
X-GNOME-Autostart-enabled=true
`, exe, AppID)
	return os.WriteFile(path, []byte(entry), 0o644)
}

// ── vair:// scheme ───────────────────────────────────────────────
//
// Registration has two halves on Linux, and only the second is a runtime
// setting. A desktop entry declaring MimeType=x-scheme-handler/vair says the
// app CAN handle the scheme — the package ships one. mimeapps.list says which
// application the desktop should actually use, and that is what this toggle
// drives.

// deepLinkEntryPath returns the desktop entry that claims the scheme, and
// whether it is one we installed under the user's data dir (as opposed to the
// package's, which we must never touch).
func deepLinkEntryPath() (path string, userOwned bool) {
	user := filepath.Join(xdgDataHome(), "applications", desktopFileName)
	for _, p := range []string{
		"/usr/share/applications/" + desktopFileName,
		"/usr/local/share/applications/" + desktopFileName,
	} {
		if _, err := os.Stat(p); err == nil {
			return p, false
		}
	}
	return user, true
}

// RegisterDeepLink points the desktop's scheme handler at Vair, or unpoints it.
func RegisterDeepLink(enable bool) error {
	if !enable {
		return disableScheme()
	}
	path, userOwned := deepLinkEntryPath()
	if !userOwned {
		// A packaged entry exists, so any fallback we wrote earlier is not just
		// redundant — it SHADOWS the package's, because the user's data dir wins.
		//
		// This is not hypothetical: running a tarball or an AppImage first and
		// installing the package afterwards left an entry with NoDisplay=true
		// pointing at a path that no longer existed. The app vanished from the
		// application menu entirely and vair:// links resolved to nothing, with
		// nothing on screen to suggest why.
		removeUserDesktopEntry()
	}
	if userOwned {
		// No packaged entry — a tarball or a build tree. Write our own, because
		// an association can only name a desktop file that exists: without one
		// the toggle would report success and xdg-open would still find nothing.
		//
		// NoDisplay keeps it out of the application menu. It exists to make the
		// association resolvable, and a build run from a source tree has no
		// business adding itself to the user's launcher; MIME handling ignores
		// the flag.
		if err := writeUserDesktopEntry(path); err != nil {
			return err
		}
		// The database is what xdg-open consults; a new file it has not indexed
		// is invisible until this runs.
		exec.Command("update-desktop-database", filepath.Dir(path)).Run() //nolint:errcheck
	}
	return enableScheme()
}

func writeUserDesktopEntry(path string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Version=1.0
Name=Vair
GenericName=VPN client
Comment=Connect through VLESS/VMess/Trojan/Shadowsocks servers
Exec=%s %%u
Icon=%s
Terminal=false
Categories=Network;Security;
MimeType=x-scheme-handler/vair;
StartupWMClass=%s
NoDisplay=true
`, exe, AppID, WMClassID)
	return os.WriteFile(path, []byte(entry), 0o644)
}

// Turning the scheme OFF takes two edits, not one.
//
// Dropping our line from [Default Applications] only removes an explicit
// choice. The packaged desktop entry still declares
// MimeType=x-scheme-handler/vair, so it remains a registered handler and, being
// the only one, is picked again by default — measured: after the "off" edit
// alone, `xdg-mime query default x-scheme-handler/vair` still answered
// com.vair.app.desktop. [Removed Associations] is the spec's way to say "this
// application is NOT a handler for this type", and it is what actually
// unregisters us.
func disableScheme() error {
	if err := setMimeappsKey("Default Applications", schemeKey, ""); err != nil {
		return err
	}
	return setMimeappsKey("Removed Associations", schemeKey, desktopFileName+";")
}

// enableScheme clears the removal first: xdg-mime writes the default without
// looking at [Removed Associations], so leaving it would produce a file that
// both names us as the default and denies we can handle the scheme, and the
// denial wins.
func enableScheme() error {
	if err := setMimeappsKey("Removed Associations", schemeKey, ""); err != nil {
		return err
	}
	return exec.Command("xdg-mime", "default", desktopFileName, schemeKey).Run()
}

const schemeKey = "x-scheme-handler/vair"

// setMimeappsKey sets (or, with an empty value, deletes) one key in one section
// of mimeapps.list, leaving every other line exactly as it was.
//
// Written by hand rather than with an INI library because the file holds the
// user's handler choice for every type they have ever changed, and a
// reformatting round-trip through a parser that drops comments or reorders
// sections would be a memorable way to lose them.
func setMimeappsKey(section, key, value string) error {
	path := filepath.Join(xdgConfigHome(), "mimeapps.list")
	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	} else if !os.IsNotExist(err) {
		return err
	}

	var out []string
	cur := ""
	done, inSection := false, false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			// Leaving the target section without having written the key: append
			// it here, so it lands under its own heading rather than at the end
			// of the file under someone else's.
			if inSection && !done && value != "" {
				out = append(out, key+"="+value)
				done = true
			}
			cur = strings.Trim(t, "[]")
			inSection = cur == section
			out = append(out, line)
			continue
		}
		if inSection && strings.HasPrefix(t, key+"=") {
			if value != "" && !done {
				out = append(out, key+"="+value)
				done = true
			}
			continue // drop the old line either way
		}
		out = append(out, line)
	}
	if !done && value != "" {
		if !inSection {
			out = append(out, "["+section+"]")
		}
		out = append(out, key+"="+value)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

// OpenPath shows a folder in the file manager, OpenURL hands a link to the
// default browser. The WebView must never navigate away from the app, so every
// outbound link goes through here.
//
// xdg-open is the portable entry point on every desktop we target; it lives in
// xdg-utils, which the package depends on. It backgrounds itself, so Start
// without Wait leaks nothing.
func OpenPath(path string) {
	_ = exec.Command("xdg-open", path).Start()
}

func OpenURL(url string) {
	_ = exec.Command("xdg-open", url).Start()
}

// removeUserDesktopEntry deletes the fallback entry we may have written when no
// packaged one existed. Named after AppID, so there is no ambiguity about whose
// file it is.
func removeUserDesktopEntry() {
	p := filepath.Join(xdgDataHome(), "applications", desktopFileName)
	if err := os.Remove(p); err == nil {
		// The database still lists it until told otherwise.
		exec.Command("update-desktop-database", filepath.Dir(p)).Run() //nolint:errcheck
	}
}
