package main

import (
	"embed"
	"io/fs"
	"log"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"vair/core"
	"vair/internal/engines"
	"vair/internal/remote"
	"vair/internal/services"
	"vair/internal/shell"
)

// Wails embeds the built frontend (frontend/dist) into the binary.
//
//go:embed all:frontend/dist
var assets embed.FS

// trayIcon is the system-tray icon (PNG; Windows tray decodes via png.Decode).
//
//go:embed assets/tray.png
var trayIcon []byte

func init() {
	// Typed events registered up front so the binding generator emits a strongly
	// typed JS/TS API; the domain events (conn_update, entry_update, …) are
	// emitted dynamically by core via the emitter bridge.
	application.RegisterEvent[string]("deeplink")
}

func main() {
	// Settings + tabs + configs load SYNCHRONOUSLY so the very first render already
	// has the active tab's data — no "0 configs" flash before the store finishes
	// loading (the 1.10 behaviour the user expects). Engine PATHS are registered
	// synchronously too, so ConnService.AppInfo() reports sing-box as available
	// immediately (else the TUN pill briefly reads "sing-box not found"). Only the
	// genuinely heavy/slow work — writing the ~80 MB of embedded engine bytes to
	// disk, the SOURCES network fetch, the background loops — is deferred to a
	// goroutine so it never blocks the window from appearing.
	// Before anything else: the background goroutine below can start the web
	// interface within milliseconds, and a server with no assets serves nothing.
	// fs.Sub, not the raw embed — callers ask for "index.html", not
	// "frontend/dist/index.html".
	if dist, err := fs.Sub(assets, "frontend/dist"); err == nil {
		remote.SetAssets(dist)
	} else {
		log.Printf("embedded frontend: %v", err)
	}

	core.PreloadSettings()
	engines.RegisterPaths() // sing-box path known before the frontend asks
	core.Init()             // SQLite store + tabs + configs into memory (data ready)
	go func() {
		core.CleanupUpdateLeftovers() // stray <exe>.new/.old from an aborted update
		if err := engines.Extract(embeddedEngineFiles()); err != nil {
			log.Printf("engine extract: %v", err)
		}
		core.StartBackground()        // supervisor + auto-refresh + SOURCES fetch
		remote.ApplySetting()         // web interface, if it was left enabled
		shell.ApplyTunProxyFirewall() // LAN proxy-sharing firewall rule, if it was left enabled
		// Autostart: re-point the HKCU Run key at THIS exe when enabled, so an
		// entry migrated from 1.10 (or a moved binary) launches the right one at
		// logon instead of the old path. No-op / removal when disabled.
		if core.GetSettings().AutostartEnabled {
			_ = shell.ApplyAutostart(true)
		}
	}()

	// Measured before the app exists, because two things below depend on it: the
	// quit-on-last-window-closed contract and whether a tray is created at all.
	// atLogin narrows a retry to the one case where a false negative is likely —
	// see shell.TrayHostAvailable.
	hasTray := shell.TrayHostAvailable(shell.HasAutostartFlag(os.Args))
	core.SetTrayAvailable(hasTray)

	app := application.New(application.Options{
		Name:        "Vair",
		Description: "Vair 2.5.0",
		Services: []application.Service{
			application.NewService(&services.ConfigService{}),
			application.NewService(&services.TabService{}),
			application.NewService(&services.ConnService{}),
			application.NewService(&services.TestService{}),
			application.NewService(&services.SettingsService{}),
			application.NewService(&services.AutoService{}),
			application.NewService(&services.LogService{}),
			application.NewService(&services.QRService{}),
			application.NewService(&services.ClipboardService{}),
			application.NewService(&services.UpdateService{}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		// One running instance; a second launch re-focuses the first and forwards
		// any vair:// deep link it was started with.
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: shell.AppID,
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				if dl := shell.DeepLinkFromArgs(data.Args); dl != "" {
					shell.App().Event.Emit("deeplink", dl)
				}
				shell.ShowMain()
			},
		},
		Windows: application.WindowsOptions{
			WebviewUserDataPath: shell.WebviewDataPath(),
			// Closing/hiding the window keeps Vair alive in the tray; the app only
			// quits via the tray's "Выход".
			DisableQuitOnLastWindowClosed: true,
		},
		Linux: application.LinuxOptions{
			// The same contract as Windows, but ONLY where a tray exists to get
			// back from. Without this twin, closing the window on Linux QUITS the
			// app — a silent behavioural divergence that reads as
			// a crash, and one that skips core.Shutdown() and so leaves the system
			// proxy pointing at a listener that no longer exists.
			//
			// with no icon left to reopen it from — and a VPN client that is
			// running, holding the system proxy and unreachable is a worse outcome
			// than one that exits when you close it. Hence the measurement rather
			// than a constant.
			DisableQuitOnLastWindowClosed: hasTray,
			// Must equal the StartupWMClass in the desktop file — see shell.WMClassID.
			ProgramName: shell.WMClassID,
		},
	})
	shell.SetApp(app)
	shell.SetWindowIcon(windowIcon())

	// Reconcile the vair:// scheme with the SETTING, not with true. Forcing it on
	// at every launch quietly overrode a user who had turned it off — on Windows
	// it rewrote the registry key they had removed, and on Linux it would
	// re-point the desktop's handler association each start. Best-effort either
	// way; the default is on.
	_ = shell.RegisterDeepLink(core.GetSettings().DeepLinkEnabled)

	// Default window = 80% of the monitor's work area, floored at 980×620 and
	// capped at the work area (small monitors never get a window larger than
	// the screen). Wails centers it.
	winW, winH := shell.InitialWindowSize()
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Vair",
		Width:            winW,
		Height:           winH,
		Frameless:        shell.FramelessWindow(),
		BackgroundColour: shell.WindowBackground(), // matches the theme's --bg (resize-lag paint)
		URL:              "/",
		EnableFileDrop:   true, // drop a config / source-list file onto the window
	})
	shell.SetMainWindow(win)

	// Files dropped onto the window are imported like a paste: config files fill
	// the active tab, files that are just a list of links become tab sources.
	// core.ImportFiles does the parsing/routing and broadcasts the result.
	win.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		if e == nil || e.Context() == nil {
			return
		}
		go core.ImportFiles(e.Context().DroppedFiles())
	})

	// Launched at logon (--autostart) → start hidden in the tray. "In the tray"
	// is the operative part: with no tray to hide into, a hidden window is an
	// application the user cannot open, so it starts visible instead. Slightly
	// more intrusive at login, and the alternative is a process they can only
	// reach with a task manager.
	if shell.HasAutostartFlag(os.Args) && hasTray {
		win.Hide()
	}

	// Any show/hide of the main window (tray, titlebar-✕-to-tray via the JS
	// runtime, detach) updates the AUTO panel's Detach/Back-to-app button.
	win.OnWindowEvent(events.Common.WindowShow, func(_ *application.WindowEvent) { shell.EmitMainVis() })
	win.OnWindowEvent(events.Common.WindowHide, func(_ *application.WindowEvent) { shell.EmitMainVis() })

	// Window/taskbar/alt-tab icons from the multi-size icon.ico (exact 1.10
	// setWindowIcon behaviour); runs once the native window exists.
	go shell.ApplyWindowIcon()

	// System tray: left-click shows the window, right-click opens the menu.
	// Skipped where nothing would display it — registering an icon into the void
	// only produces a log line nobody reads.
	if hasTray {
		shell.CreateTray(trayIcon)
	}

	// A vair:// link we were launched with: stash it — the frontend PULLS it via
	// SettingsService.TakePendingDeepLink() once mounted (race-free; the
	// "deeplink" event stays for second-instance forwarding, when the frontend
	// is already alive).
	if dl := shell.DeepLinkFromArgs(os.Args); dl != "" {
		shell.SetPendingDeepLink(dl)
	}

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
