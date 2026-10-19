package shell

import (
	"time"

	"github.com/godbus/dbus/v5"
)

// TrayHostAvailable reports whether anything on this desktop will actually show
// a tray icon.
//
// This is not a nicety. A tray icon on Linux is a StatusNotifierItem, and it is
// rendered only if some process owns org.kde.StatusNotifierWatcher on the
// session bus. Stock GNOME owns nothing of the sort — the AppIndicator
// extension does, and the package lists it as a Recommends, which can be
// removed.
//
// Without this check the failure is silent and bad: the app registers an icon
// that never appears, "minimize to tray on close" hides the window into
// nothing, and the user is left with a running VPN client they cannot reach
// while it holds their system proxy. Wails logs the registration error and
// carries on, so nothing in the API reports it.
func TrayHostAvailable(atLogin bool) bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	probe := func() bool {
		var has bool
		err := conn.BusObject().
			Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").
			Store(&has)
		return err == nil && has
	}
	if probe() {
		return true
	}
	// At LOGIN the answer can be a false negative: an autostarted app races
	// gnome-shell finishing its extension load, and the watcher appears a
	// second or two later. Retry only on that path — waiting five seconds on
	// every manual launch to confirm an absence would be a five-second delay
	// for the desktops that genuinely have no tray.
	if !atLogin {
		return false
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if probe() {
			return true
		}
	}
	return false
}
