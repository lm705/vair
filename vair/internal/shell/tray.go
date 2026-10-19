package shell

import (
	"fmt"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"

	"vair/core"
)

// ── System tray menu (state-aware, ported from 1.10 showTrayMenu) ───────────
// The menu reflects the live connection / auto state: the connected config +
// Disconnect when connected, an "auto on" line + Switch now when auto is enabled,
// always Show + Exit. Rebuilt on conn/auto changes (refreshTrayMenu via the
// emitter). Labels follow the app language.
var (
	theTray     *application.SystemTray
	trayMenuSig string
)

// trayT picks the label for the current app language (Russian when settings say
// so, English otherwise — matching the rest of the UI's i18n default).
func trayT(en, ru string) string {
	if core.GetSettings().Language == "ru" {
		return ru
	}
	return en
}

func buildTrayMenu() *application.Menu {
	m := theApp.NewMenu()
	cs := core.ConnSnapshot()
	connected := cs.Status == core.ConnConnected
	autoOn := core.AutoEnabled()

	if connected {
		mode := "Proxy"
		if cs.Mode == core.ModeTUN {
			mode = "TUN"
		}
		m.Add(fmt.Sprintf("● %s [%s]", cs.EntryName, mode)).SetEnabled(false) // info line
	}
	if autoOn {
		m.Add("⟳ " + trayT("Auto-connect: ON", "Авто-подключение: ВКЛ")).SetEnabled(false)
	}
	if connected || autoOn {
		m.AddSeparator()
	}
	m.Add(trayT("Show Vair", "Показать Vair")).OnClick(func(_ *application.Context) { ShowMain() })
	if autoOn {
		m.Add(trayT("Switch now", "Переключить сейчас")).OnClick(func(_ *application.Context) { core.AutoSwitch() })
	}
	if connected {
		m.Add(trayT("Disconnect", "Отключить")).OnClick(func(_ *application.Context) { core.Disconnect() })
	}
	m.AddSeparator()
	m.Add(trayT("Exit", "Выход")).OnClick(func(_ *application.Context) {
		core.Shutdown() // stop any connection + clear system proxy before exit
		theApp.Quit()
	})
	return m
}

// RefreshTrayMenu rebuilds the tray menu when the connection / auto / language
// state that shapes it actually changed (cheap signature guard, since auto_update
// fires often).
func RefreshTrayMenu() {
	if theTray == nil {
		return
	}
	cs := core.ConnSnapshot()
	sig := fmt.Sprintf("%v|%s|%s|%v|%s",
		cs.Status == core.ConnConnected, cs.EntryName, cs.Mode, core.AutoEnabled(), core.GetSettings().Language)
	if sig == trayMenuSig {
		return
	}
	trayMenuSig = sig
	theTray.SetMenu(buildTrayMenu())
}

// CreateTray installs the system-tray icon and its menu.
//
// Only called where something will display it: registering an icon into a
// desktop with no StatusNotifierItem host produces a log line nobody reads and
// an application the user cannot reach.
func CreateTray(icon []byte) {
	if theApp == nil {
		return
	}
	theTray = theApp.SystemTray.New()
	theTray.SetTooltip("Vair")
	theTray.SetIcon(icon)
	theTray.OnClick(func() { ShowMain() })
	RefreshTrayMenu() // build the initial (state-aware) menu
}

// pendingDeepLink holds a vair:// URL the app was LAUNCHED with until the
// frontend mounts and pulls it (SettingsService.TakePendingDeepLink) — the
// frontend asks rather than being told, so there is no startup race.
var pendingDeepLink atomic.Value

// SetPendingDeepLink stores a link the app was started with.
func SetPendingDeepLink(url string) { pendingDeepLink.Store(url) }

// TakePendingDeepLink returns that link once and forgets it.
func TakePendingDeepLink() string {
	if v := pendingDeepLink.Swap(""); v != nil {
		if dl, ok := v.(string); ok {
			return dl
		}
	}
	return ""
}
