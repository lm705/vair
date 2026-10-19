package shell

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	"vair/core"
)

// WindowBackground is the native window + webview background for the current
// theme. It MUST match the app's CSS --bg: during a live resize the compositor
// paints this colour where the webview hasn't caught up yet, so a mismatch
// (always-dark on a light theme) flashes black bands at the edges.
func WindowBackground() application.RGBA {
	if core.UITheme() == "light" {
		return application.NewRGB(244, 244, 245) // #f4f4f5 — light --bg
	}
	return application.NewRGB(12, 12, 12) // #0c0c0c — dark --bg
}

// ApplyWindowTheme re-syncs live windows' native backgrounds to the theme
// (called from SettingsService.Set when the theme changes).
//
// The main window is this package's own; any others are passed in, because the
// windows other packages open belong to them. Today that is the AUTO panel.
func ApplyWindowTheme(others ...*application.WebviewWindow) {
	bg := WindowBackground()
	if mainWindow != nil {
		mainWindow.SetBackgroundColour(bg)
	}
	for _, w := range others {
		if w != nil {
			w.SetBackgroundColour(bg)
		}
	}
}
