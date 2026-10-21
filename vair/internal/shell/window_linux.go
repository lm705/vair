package shell

import "github.com/wailsapp/wails/v3/pkg/application"

// InitialWindowSize picks the startup window size using the same rule as
// Windows (see idealWindowSize in window.go), but from an assumed work area
// rather than a measured one.
//
// Querying the real monitor would mean asking Wails for the screen, and on
// Wayland a client cannot reliably learn the usable area before its surface is
// mapped — the compositor decides placement, and there is no equivalent of
// SPI_GETWORKAREA. Assuming a common 1080p desktop and letting idealWindowSize
// clamp to its 980x620 floor gives a sane window everywhere; a tiling WM will
// override the size anyway, and on a smaller screen the compositor shrinks the
// window to fit.
func InitialWindowSize() (int, int) {
	workW, workH := workAreaDIP()
	w, h := idealWindowSize(workW, workH, 80)
	return int(w), int(h)
}

// workAreaDIP is the assumed usable area — see the note above for why it is not
// measured here.
func workAreaDIP() (int32, int32) { return 1920, 1040 }

// ApplyWindowIcon is a no-op here, and deliberately so.
//
// There is no WM_SETICON equivalent: on Linux the icon comes from the
// application's .desktop file, matched to the window by app_id (Wayland) or
// WM_CLASS (X11). That is why main.go sets ProgramName and why the desktop file
// must be named to match — the icon is a packaging concern, not a runtime call.
//
// It must NOT inherit the Windows version's 10-second polling loop for the
// native window handle: there is nothing to wait for.
func ApplyWindowIcon() {}

// SetWindowIcon is a no-op for the same reason: there is no .ico to install and
// nothing to install it on. It exists so main can hand the icon over without
// asking which platform it is on.
func SetWindowIcon([]byte) {}

// RaiseWindow brings the window back from the tray. The Windows twin also calls
// Restore for the minimised case; here that case cannot be served at all.
//
// A HIDDEN window comes back fine — Show maps a fresh surface and the
// compositor places it normally, which is the close-to-tray path and the one
// that matters. A MINIMISED window does not: mutter's focus-stealing prevention
// only un-minimises on a request carrying an XDG activation token, and a tray
// menu is drawn by the shell, so the app has no token to pass. GNOME leaves the
// window minimised and posts a "…is ready" notification instead.
//
// Measured on GNOME 46/Wayland: "Show Vair" from the tray does nothing for a
// minimised window, and neither does unmapping it first (tried — Hide then Show
// leaves it minimised just the same). Wayland also gives no way to detect the
// state: xdg_toplevel has maximised, fullscreen and activated but no minimised,
// so Wails' IsMinimised always answers false here.
//
// The window is not lost — clicking Vair in the dash or the dock restores it,
// because THAT request carries a token. Nothing in this function can substitute
// for it, so it does not pretend to.
func RaiseWindow(w *application.WebviewWindow) {
	w.Show()
	w.Focus()
}

// WebviewDataPath exists here only because main.go names it inside the shared
// application.Options literal, whose Windows section is compiled on every
// platform and ignored everywhere else. There is nothing to point at: WebKitGTK
// keeps its cache under the app's own data dir without being told.
func WebviewDataPath() string { return "" }
