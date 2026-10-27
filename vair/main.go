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
	// The uninstaller runs us with this before it deletes anything, elevated, to
	// put the firewall, the system proxy and the network adapters back. It is the
	// first thing checked because none of the rest — no window, no engines, no
	// store — has any business starting for it.
	if shell.HasUninstallCleanupFlag(os.Args) {
		shell.RunUninstallCleanup()
		return
	}
	// `Vair.exe --release-block`: take the kill-switch block down and exit —
	// through the helper service where one is installed, which needs no rights,
	// and elevated where there is none. Without the service the red button runs
	// it through a UAC prompt; a person runs it by hand when the window is not
	// there. Before the single-instance check, like
	// the uninstaller's flag, because it is meant to run beside a running copy.
	if shell.HasReleaseBlockFlag(os.Args) {
		os.Exit(core.ReleaseBlockFromCommandLine())
	}
	// And this one when the installer's "start with Windows" box was ticked: the
	// program records its own setting rather than the installer writing the
	// registry behind its back, so the switch in Settings tells the truth.
	if shell.HasEnableAutostartFlag(os.Args) {
		core.PreloadSettings()
		shell.RunEnableAutostart()
		return
	}

	// Before anything else: the background goroutine below can start the web
	// interface within milliseconds, and a server with no assets serves nothing.
	// fs.Sub, not the raw embed — callers ask for "index.html", not
	// "frontend/dist/index.html".
	if dist, err := fs.Sub(assets, "frontend/dist"); err == nil {
		remote.SetAssets(dist)
	} else {
		log.Printf("embedded frontend: %v", err)
	}

	// Measured before the app exists, because two things below depend on it: the
	// quit-on-last-window-closed contract and whether a tray is created at all.
	// atLogin narrows a retry to the one case where a false negative is likely —
	// see shell.TrayHostAvailable.
	hasTray := shell.TrayHostAvailable(shell.HasAutostartFlag(os.Args))
	core.SetTrayAvailable(hasTray)

	app := application.New(application.Options{
		Name:        "Vair",
		Description: "Vair 2.7.0",
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
			application.NewService(&services.ConnectionsService{}),
			application.NewService(&services.UnlockService{}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		// One running instance; a second launch re-focuses the first and forwards
		// any vair:// deep link it was started with.
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: shell.AppID,
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				// The uninstaller is about to delete this copy: quit the way the
				// tray's Exit does, so the tunnel and the proxy are put back first.
				if shell.IsQuitForUninstall(data.Args) {
					go func() {
						core.Shutdown()
						shell.App().Quit()
					}()
					return
				}
				if dl := shell.DeepLinkFromArgs(data.Args); dl != "" {
					shell.App().Event.Emit("deeplink", dl)
				}
				shell.ShowMain()
			},
		},
		Windows: application.WindowsOptions{
			WebviewUserDataPath: shell.WebviewDataPath(),
			// WM_QUERYENDSESSION (0x0011): Windows is about to end the session.
			// Said to core before WM_ENDSESSION and before the engines are
			// killed, so the teardown that follows starts no new process.
			WndProcInterceptor: func(_ uintptr, msg uint32, _, _ uintptr) (uintptr, bool) {
				if msg == 0x0011 {
					core.SessionEnding()
				}
				return 0, false
			},
			// The interface is a page served from inside the program and talks to
			// nothing else; the WebView2 runtime under it, left to itself, does — a
			// capture of a cold start showed it reaching edge.microsoft.com a minute
			// in, from the real address, before any tunnel. These switches turn its
			// own background traffic off: component and configuration updates,
			// reliability reports, crash uploads and hyperlink-auditing pings. Wails
			// already turns SmartScreen off for its windows.
			AdditionalBrowserArgs: []string{
				"--disable-background-networking",
				"--disable-component-update",
				"--disable-domain-reliability",
				"--disable-breakpad",
				"--no-pings",
			},
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
	// Only now, and not a line earlier: application.New is where a second copy
	// finds the first one running, hands it its arguments and exits. Everything
	// below reads and writes this program's state — settings.json, the config
	// store, the engines on disk, the background loops that save as they go —
	// and it all used to run BEFORE that check. A second launch (the shortcut
	// clicked while Vair sat in the tray, a vair:// link, an autostart racing
	// a manual start) read the files, started writing, and was then ended by
	// os.Exit in the middle of whatever it was doing. A settings.json cut off
	// that way cannot be read at the next start, the program runs on defaults,
	// and the first change of any kind writes those defaults over the user's
	// settings.
	//
	// Settings + tabs + configs load SYNCHRONOUSLY so the very first render already
	// has the active tab's data — no "0 configs" flash before the store finishes
	// loading (the 1.10 behaviour the user expects). Engine PATHS are registered
	// synchronously too, so ConnService.AppInfo() reports sing-box as available
	// immediately (else the TUN pill briefly reads "sing-box not found"). Only the
	// genuinely heavy/slow work — writing the ~80 MB of embedded engine bytes to
	// disk, the SOURCES network fetch, the background loops — is deferred to a
	// goroutine so it never blocks the window from appearing.
	core.PreloadSettings()
	engines.RegisterPaths() // sing-box path known before the frontend asks
	core.Init()             // SQLite store + tabs + configs into memory (data ready)
	// Only the portable build carries engines to unpack.
	core.SetPortableBuild(len(embeddedEngineFiles()) > 0)
	go func() {
		core.CleanupUpdateLeftovers() // stray <exe>.new/.old from an aborted update
		if err := engines.Extract(embeddedEngineFiles()); err != nil {
			log.Printf("engine extract: %v", err)
		}
		core.StartBackground()     // supervisor + auto-refresh + SOURCES fetch
		remote.ApplySetting()      // web interface, if it was left enabled
		shell.ApplyProxyFirewall() // the LAN proxy firewall rule, matching the saved bind address
		// Autostart: the entry made to agree with the switch. On, it is
		// re-pointed at THIS exe, so an entry migrated from 1.10 (or a moved
		// binary) launches the right one at logon; off, an entry that would
		// start this program anyway is removed (see ReconcileAutostart).
		_ = shell.ReconcileAutostart(core.GetSettings().AutostartEnabled)
	}()

	shell.SetApp(app)
	// The same teardown as the tray's Quit, for the exits the program does not
	// start itself — above all Windows ending the session, which Wails turns
	// into an application quit. Without it a restart of Windows was a crash to
	// Vair: the system proxy left pointing at a dead port and a proxy
	// kill-switch block still standing after the reboot.
	app.OnShutdown(core.ShutdownUnattended)
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
		Title:  "Vair",
		Width:  winW,
		Height: winH,
		// The smallest window the layout is drawn for: the narrow layout down to
		// 480 px, and three rows of the table with the dock open. It could be
		// dragged down to 150×150 before, where nothing on it could be read.
		MinWidth:         480,
		MinHeight:        440,
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
