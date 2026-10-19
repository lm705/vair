package core

import "strings"

// notifyServerGone is the message shown when the watch gives up on a node.
//
// Written here rather than passed in from the frontend: a notification is
// raised while nothing is on screen, and there may be no frontend listening at
// all. The language is the one the dialogs are set to — the same setting, so
// the notification does not arrive in a language the user did not choose.
func notifyServerGone(name string, blocked bool) string {
	settingsMu.RLock()
	lang := appSettings.Language
	settingsMu.RUnlock()
	name = strings.TrimSpace(name)
	if name != "" {
		name += ": "
	}
	if lang == "ru" {
		if blocked {
			return name + "сервер не отвечает. Трафик заблокирован — отключитесь, чтобы снять блокировку"
		}
		return name + "сервер не отвечает — соединение закрыто"
	}
	if blocked {
		return name + "the server stopped responding. Traffic is blocked — disconnect to release it"
	}
	return name + "the server stopped responding — connection closed"
}

// NotifyWatchTurnedOn confirms, in the place the real message will arrive, that
// notifications work at all.
//
// Worth a toast of its own: everything about this feature is invisible until
// something goes wrong, and a person who switches it on has no way of knowing
// whether their machine will actually show them anything. One message at the
// moment of switching answers that.
func NotifyWatchTurnedOn() {
	settingsMu.RLock()
	lang := appSettings.Language
	settingsMu.RUnlock()
	// A machine that will not show the notification should say so somewhere the
	// user is already looking, instead of staying silent about staying silent.
	if reason := NotificationsBlockedReason(lang); reason != "" {
		vlog("warning", "%s", reason)
		return
	}
	if lang == "ru" {
		desktopNotify("Vair", "Присмотр включён — сюда придёт сообщение, если сервер замолчит")
		return
	}
	desktopNotify("Vair", "Watching is on — this is where a server going quiet will be reported")
}
