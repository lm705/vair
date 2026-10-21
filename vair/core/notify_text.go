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

// notifyChainGone is the message for a chain that stopped answering.
//
// Its own wording, because its ending is different: for a single config AUTO
// moves to another one and the user need not know, but a chain is a route the
// user built — through this country, then that one — and AUTO has no way to
// build another. Replacing it with one hop would quietly hand back a weaker
// connection than the one that was asked for, so the session ends and this
// says so.
func notifyChainGone(name string, blocked bool) string {
	settingsMu.RLock()
	lang := appSettings.Language
	settingsMu.RUnlock()
	name = strings.TrimSpace(name)
	if name != "" {
		name += ": "
	}
	if lang == "ru" {
		if blocked {
			return name + "цепочка перестала отвечать. Трафик заблокирован — отключитесь, чтобы снять блокировку"
		}
		return name + "цепочка перестала отвечать — соединение закрыто, автопереключение цепочку не пересобирает"
	}
	if blocked {
		return name + "the chain stopped responding. Traffic is blocked — disconnect to release it"
	}
	return name + "the chain stopped responding — connection closed; auto-connect does not rebuild a chain"
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

// notifyRestoreFailed is raised when the connection a start tried to bring back
// did not come up and a firewall block had to be lifted for it. Both halves
// matter: the tunnel is not there, and the protection that was standing in for
// it is not there either.
func notifyRestoreFailed() string {
	settingsMu.RLock()
	lang := appSettings.Language
	settingsMu.RUnlock()
	if lang == "ru" {
		return "Не удалось восстановить подключение. Трафик остаётся заблокированным — снимите блокировку в окне программы или подключитесь заново"
	}
	return "The connection could not be restored. Traffic stays blocked — release it in the app, or connect something else"
}

// notifyBlockedAfterStart is raised when the program starts on a machine the
// block is still holding — after a crash, and after the reboot a Windows update
// makes, which is where most people will meet it. Nothing is wrong: the setting
// is doing exactly what it says. But a computer with no internet and no
// explanation is a computer somebody is about to start repairing.
func notifyBlockedAfterStart() string {
	settingsMu.RLock()
	lang := appSettings.Language
	settingsMu.RUnlock()
	if lang == "ru" {
		return "Трафик заблокирован — так задано настройкой «не снимать блокировку после исчезновения туннеля». Интернета нет, пока вы не подключитесь или не нажмёте «трафик заблокирован ✕» в окне программы"
	}
	return "Traffic is blocked — that is \"keep blocking after the tunnel is gone\" doing its job. There is no internet until you connect, or press \"traffic blocked ✕\" in the app"
}
