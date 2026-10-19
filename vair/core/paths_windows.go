package core

import (
	"os"
	"path/filepath"
)

// appRootDir is %LOCALAPPDATA%\vair, falling back to %APPDATA%\vair — the layout
// the 1.10 release used, so an upgrading user's data is found in place.
//
// The old third fallback was "." and it is gone deliberately: returning a
// relative path meant the config DB, tabs and settings landed in the process's
// working directory. For a tray app started at login that is a directory the
// user never chose and will not think to look in, and two launches from
// different places would silently see two different databases.
func appRootDir() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, appDirName)
	}
	if d := os.Getenv("APPDATA"); d != "" {
		return filepath.Join(d, appDirName)
	}
	if d, err := os.UserConfigDir(); err == nil && d != "" {
		return filepath.Join(d, appDirName)
	}
	return homeFallbackDir()
}

// volatileRootDir — Windows has no tmpfs equivalent of $XDG_RUNTIME_DIR, and
// %TEMP% is already per-user with a restrictive ACL, so the runtime/ subfolder
// of the app's own tree is both private enough and easier to reason about. This
// keeps generated engine configs inside one directory tree we fully control.
func volatileRootDir() string { return filepath.Join(tabsDir(), "runtime") }
