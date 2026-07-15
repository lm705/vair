package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"vair/core"
)

// Wails embeds the built frontend (frontend/dist) into the binary.
//
//go:embed all:frontend/dist
var assets embed.FS

// trayIcon is the system-tray icon (PNG; Windows tray decodes via png.Decode).
//
//go:embed assets/tray.png
var trayIcon []byte

// theApp / mainWindow are captured globally so the tray, single-instance handler
// and deep-link delivery can reach the running app and its window.
var (
	theApp     *application.App
	mainWindow *application.WebviewWindow
	// pendingDeepLink holds a vair:// URL we were LAUNCHED with until the
	// frontend mounts and pulls it (TakePendingDeepLink) — no startup race.
	pendingDeepLink atomic.Value
)

func init() {
	// Typed events registered up front so the binding generator emits a strongly
	// typed JS/TS API; the domain events (conn_update, entry_update, …) are
	// emitted dynamically by core via the emitter bridge.
	application.RegisterEvent[string]("deeplink")
}

// windowBackground is the native window + webview background for the current
// theme. It MUST match the app's CSS --bg: during a live resize the compositor
// paints this colour where the webview hasn't caught up yet, so a mismatch
// (always-dark on a light theme) flashes black bands at the edges.
func windowBackground() application.RGBA {
	if core.UITheme() == "light" {
		return application.NewRGB(244, 244, 245) // #f4f4f5 — light --bg
	}
	return application.NewRGB(12, 12, 12) // #0c0c0c — dark --bg
}

// applyWindowTheme re-syncs every live window's native background to the theme
// (called from SettingsService.Set when the theme changes).
func applyWindowTheme() {
	bg := windowBackground()
	if mainWindow != nil {
		mainWindow.SetBackgroundColour(bg)
	}
	if autoWindow != nil {
		autoWindow.SetBackgroundColour(bg)
	}
}

// webviewDataPath gives Vair its own WebView2 user-data folder under the app's
// data root (%LOCALAPPDATA%\Vair). A dedicated folder (rather than the Wails
// default %APPDATA%\<BinaryName.exe>) keeps the WebView2 cache next to the rest
// of the app's data. It holds only browser cache — no user data to migrate.
func webviewDataPath() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	p := filepath.Join(base, "Vair", "WebView2")
	_ = os.MkdirAll(p, 0o755)
	return p
}

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
	m.Add(trayT("Show Vair", "Показать Vair")).OnClick(func(_ *application.Context) { showMain() })
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

// refreshTrayMenu rebuilds the tray menu when the connection / auto / language
// state that shapes it actually changed (cheap signature guard, since auto_update
// fires often).
func refreshTrayMenu() {
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

// showMain brings the main window back from the tray (un-hide, un-minimise, focus).
func showMain() {
	if mainWindow == nil {
		return
	}
	mainWindow.Show()
	mainWindow.Restore()
	mainWindow.Focus()
	emitMainVis() // the AUTO panel flips Back-to-app → Detach
}

// main is the Ф1 shell: frameless window, single-instance, dedicated WebView2
// data folder, custom titlebar controls (App.tsx), system tray, the vair:// deep
// link scheme and autostart support. Ф2 binds the real services.
func main() {
	// Settings + tabs + configs load SYNCHRONOUSLY so the very first render already
	// has the active tab's data — no "0 configs" flash before the store finishes
	// loading (the 1.10 behaviour the user expects). Engine PATHS are registered
	// synchronously too, so ConnService.AppInfo() reports sing-box as available
	// immediately (else the TUN pill briefly reads "sing-box not found"). Only the
	// genuinely heavy/slow work — writing the ~80 MB of embedded engine bytes to
	// disk, the SOURCES network fetch, the background loops — is deferred to a
	// goroutine so it never blocks the window from appearing.
	core.PreloadSettings()
	registerEnginePaths() // sing-box path known before the frontend asks
	core.Init()           // SQLite store + tabs + configs into memory (data ready)
	go func() {
		core.CleanupUpdateLeftovers() // stray <exe>.new/.old from an aborted update
		if err := extractEngines(); err != nil {
			log.Printf("engine extract: %v", err)
		}
		core.StartBackground()  // supervisor + auto-refresh + SOURCES fetch
		applyRemoteSetting()    // LAN remote server, if it was left enabled
		// Autostart: re-point the HKCU Run key at THIS exe when enabled, so an
		// entry migrated from 1.10 (or a moved binary) launches the right one at
		// logon instead of the old path. No-op / removal when disabled.
		if core.GetSettings().AutostartEnabled {
			_ = applyAutostart(true)
		}
	}()

	theApp = application.New(application.Options{
		Name:        "Vair",
		Description: "Vair 2.0.0",
		Services: []application.Service{
			application.NewService(&ConfigService{}),
			application.NewService(&TabService{}),
			application.NewService(&ConnService{}),
			application.NewService(&TestService{}),
			application.NewService(&SettingsService{}),
			application.NewService(&AutoService{}),
			application.NewService(&LogService{}),
			application.NewService(&QRService{}),
			application.NewService(&UpdateService{}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		// One running instance; a second launch re-focuses the first and forwards
		// any vair:// deep link it was started with.
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.vair.app",
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				if dl := deepLinkFromArgs(data.Args); dl != "" {
					theApp.Event.Emit("deeplink", dl)
				}
				showMain()
			},
		},
		Windows: application.WindowsOptions{
			WebviewUserDataPath: webviewDataPath(),
			// Closing/hiding the window keeps Vair alive in the tray; the app only
			// quits via the tray's "Выход".
			DisableQuitOnLastWindowClosed: true,
		},
	})
	app := theApp

	// Register the vair:// scheme (default on, like 1.10). Best-effort.
	_ = registerDeepLink(true)

	// Default window = 80% of the monitor's work area, floored at 980×620 and
	// capped at the work area (small monitors never get a window larger than
	// the screen). Wails centers it.
	winW, winH := initialWindowSize()
	mainWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Vair",
		Width:            winW,
		Height:           winH,
		Frameless:        true,
		BackgroundColour: windowBackground(), // matches the theme's --bg (resize-lag paint)
		URL:              "/",
	})

	// Launched at logon (--autostart) → start hidden in the tray.
	if hasAutostartFlag(os.Args) {
		mainWindow.Hide()
	}

	// Any show/hide of the main window (tray, titlebar-✕-to-tray via the JS
	// runtime, detach) updates the AUTO panel's Detach/Back-to-app button.
	mainWindow.OnWindowEvent(events.Common.WindowShow, func(_ *application.WindowEvent) { emitMainVis() })
	mainWindow.OnWindowEvent(events.Common.WindowHide, func(_ *application.WindowEvent) { emitMainVis() })

	// Window/taskbar/alt-tab icons from the multi-size icon.ico (exact 1.10
	// setWindowIcon behaviour); runs once the native window exists.
	go applyWindowIcon()

	// System tray: left-click shows the window, right-click opens the menu.
	theTray = app.SystemTray.New()
	theTray.SetTooltip("Vair")
	theTray.SetIcon(trayIcon)
	theTray.OnClick(func() { showMain() })
	refreshTrayMenu() // build the initial (state-aware) menu

	// A vair:// link we were launched with: stash it — the frontend PULLS it via
	// SettingsService.TakePendingDeepLink() once mounted (race-free; the
	// "deeplink" event stays for second-instance forwarding, when the frontend
	// is already alive).
	if dl := deepLinkFromArgs(os.Args); dl != "" {
		pendingDeepLink.Store(dl)
	}

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
