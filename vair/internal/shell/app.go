// Package shell is everything that talks to the system: the window and its
// chrome, the tray, autostart, vair:// links, the process list, QR capture and
// firewall rules.
//
// It is also where the platform difference lives. Files come in _windows.go /
// _linux.go pairs, and reading their names is the fastest way to see what the
// two systems do not share. Nothing here decides program behaviour; it carries
// out what core and the services ask for.
package shell

import "github.com/wailsapp/wails/v3/pkg/application"

// The running application and its main window.
//
// They live here rather than in main because everything that needs them is in
// this package or downstream of it: the tray raises the window, the dialogs
// need an app to parent themselves to, the services ask for the clipboard.
// main creates the app and hands it over; nothing here creates one, so there is
// exactly one owner and one direction of dependency.
var (
	theApp     *application.App
	mainWindow *application.WebviewWindow
)

// SetApp records the application. Called once, from main, before anything else
// in this package is used.
func SetApp(a *application.App) { theApp = a }

// App is the running application, or nil before main has created it. Callers
// that can run early — the event bridge, for one — must check.
func App() *application.App { return theApp }

// SetMainWindow records the main window once main has created it.
func SetMainWindow(w *application.WebviewWindow) { mainWindow = w }

// MainWindow is the app's primary window, or nil before it exists.
func MainWindow() *application.WebviewWindow { return mainWindow }

// ShowMain brings the main window back from the tray: un-hide, un-minimise,
// focus. Also nudges the AUTO panel, whose Detach / Back-to-app button follows
// the main window's visibility.
func ShowMain() {
	if mainWindow == nil {
		return
	}
	RaiseWindow(mainWindow)
	EmitMainVis()
}

// EmitMainVis tells the frontend whether the main window is on screen. The AUTO
// panel's Detach / Back-to-app button is the only consumer, and it has to react
// to every route in and out of the tray — the tray icon, the titlebar ✕, a
// second launch — which is why this sits with the window rather than with the
// panel's service.
func EmitMainVis() {
	if theApp != nil {
		theApp.Event.Emit("main_vis", mainWindow != nil && mainWindow.IsVisible())
	}
}
