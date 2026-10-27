package core

import "github.com/godbus/dbus/v5"

// Desktop notifications, Linux.
//
// The same job as the Windows toast, through the interface every desktop here
// already implements: org.freedesktop.Notifications on the session bus. GNOME,
// KDE, XFCE and Cinnamon all provide it; a session with no notification daemon
// simply has no owner for the name, the call fails, and nothing happens — which
// is the right outcome for a best-effort message.
//
// Sent over D-Bus directly rather than through notify-send, which is a separate
// package (libnotify-bin) that is not installed everywhere.

// desktopNotify shows a system notification. Never blocks the caller and never
// reports failure.
func desktopNotify(title, body string) {
	if notifyMuted.Load() {
		return
	}
	go func() {
		conn, err := dbus.SessionBus()
		if err != nil {
			return
		}
		obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
		// app_name, replaces_id, icon, summary, body, actions, hints, timeout.
		// replaces_id 0 means "a new one"; the icon name is the desktop entry's,
		// and a desktop that cannot find it falls back to its own default.
		call := obj.Call("org.freedesktop.Notifications.Notify", 0,
			"Vair", uint32(0), "vair", title, body,
			[]string{}, map[string]dbus.Variant{}, int32(10000))
		_ = call.Err // a session without a notification daemon is not a fault
	}()
}
