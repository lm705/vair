package services

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"vair/core"
	"vair/internal/shell"
)

// AutoService exposes the auto-connect master toggle + "switch now". Live status
// arrives via the "auto" Wails events.
//
// The AUTO panel lives in its OWN window (620×760, /?view=auto), coexisting
// with the main window: the header's auto button opens it alongside; Detach
// hides the main window (the panel's button then reads "Back to app"); the
// panel's ✕ closes just the panel. The app never ends up with zero visible
// windows: closing the panel while the main window is hidden restores it.
type AutoService struct{}

// autoWindow is the AUTO panel window (nil while closed).
var autoWindow *application.WebviewWindow

// Enabled reports whether auto-connect is on.
func (a *AutoService) Enabled() bool { return core.AutoEnabled() }

// SetEnabled turns auto-connect on/off.
func (a *AutoService) SetEnabled(on bool) { core.SetAuto(on) }

// SwitchNow forces an immediate failover/connect. Returns why it cannot (AUTO
// off, nothing in the candidate tabs), or "".
func (a *AutoService) SwitchNow() string { return core.AutoSwitch() }

// Sweep reports how far the test of the candidate pool has got — for a panel
// opened mid-sweep; later changes arrive as "auto_sweep" events.
func (a *AutoService) Sweep() core.AutoSweepProgress { return core.AutoSweep() }

// Candidates returns the ranked candidate pool for the AUTO panel list.
func (a *AutoService) Candidates() []core.AutoCandDTO { return core.AutoCandidates() }

// ConnectCand connects to the candidate with the given raw URL (panel click).
func (a *AutoService) ConnectCand(raw, mode string) bool { return core.AutoConnectCand(raw, mode) }

// ReloadPool re-fetches the sources of every pool tab (the panel's reload).
func (a *AutoService) ReloadPool() { core.ReloadPool() }

// MainVisible reports whether the main window is currently shown — the panel
// shows "Detach" when it is, "Back to app" when it isn't.
func (a *AutoService) MainVisible() bool {
	return shell.MainWindow() != nil && shell.MainWindow().IsVisible()
}

// OpenAuto opens (or focuses) the AUTO panel window ALONGSIDE the main window —
// the header's auto button. Both windows stay visible.
func (a *AutoService) OpenAuto() {
	if autoWindow != nil {
		autoWindow.Show()
		autoWindow.Focus()
		return
	}
	panelW, panelH := shell.PanelWindowSize(620, 760)
	autoWindow = shell.App().Window.NewWithOptions(application.WebviewWindowOptions{
		// Same title as the main window ON PURPOSE: the window is frameless (the
		// title shows only in Task Manager / Alt-Tab), and Windows 10's Task
		// Manager kept displaying a distinct title ("Vair — Auto") for the whole
		// process even after this window was closed. 1.10 always ran under a
		// single "Vair" title; identical titles reproduce that.
		Title: "Vair",
		// Its natural size, or as much of it as the screen has: on a small
		// laptop 760 tall opened a window whose own close button was below the
		// bottom of the desktop.
		Width:            panelW,
		Height:           panelH,
		Frameless:        true,
		BackgroundColour: shell.WindowBackground(), // matches the theme's --bg (resize-lag paint)
		URL:              "/?view=auto",
	})
	// The panel's ✕ (or a stray OS close) closes just the panel — but never
	// leaves the app with no visible window: if the main window is hidden
	// (detached mode), bring it back.
	autoWindow.OnWindowEvent(events.Common.WindowClosing, func(_ *application.WindowEvent) {
		autoWindow = nil
		if shell.MainWindow() == nil || !shell.MainWindow().IsVisible() {
			shell.ShowMain()
		}
	})
}

// Detach hides the MAIN window, leaving the panel as the only visible window
// (the compact 1.10 standalone-AUTO mode). The panel stays where it is.
func (a *AutoService) Detach() {
	if shell.MainWindow() != nil {
		shell.MainWindow().Hide()
	}
	shell.EmitMainVis()
}

// Attach brings the main window back ("Back to app"); the panel stays open.
func (a *AutoService) Attach() {
	shell.ShowMain() // emits main_vis
}

// CloseAuto closes the panel window (its titlebar ✕). The WindowClosing hook
// set in OpenAuto restores the main window if it was hidden.
func (a *AutoService) CloseAuto() {
	if autoWindow != nil {
		autoWindow.Close()
	}
}

// Sessions is the connection log, newest first: what was connected, for how
// long, and what ended it.
func (a *AutoService) Sessions() []core.SessionRow { return core.SessionLog() }

// ClearSessions forgets every recorded session.
func (a *AutoService) ClearSessions() { core.ClearSessionLog() }
