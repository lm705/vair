package core

import (
	"os"
	"path/filepath"
)

// appRootDir is $XDG_DATA_HOME/vair, defaulting to ~/.local/share/vair per the
// freedesktop base-directory spec. The data/ and runtime/ subfolders underneath
// are laid out exactly as on Windows, so migrateDataLayout and every dataPath /
// runtimePath caller works unchanged.
//
// Note this is the DATA dir, not the config dir: the bulk of what lives here is
// configs.db, which is application state rather than something a user edits by
// hand, and keeping settings.json beside it preserves the single-tree layout the
// backup/export code assumes.
func appRootDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, appDirName)
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".local", "share", appDirName)
	}
	return homeFallbackDir()
}

// volatileRootDir is $XDG_RUNTIME_DIR/vair — tmpfs, already mode 0700 and owned
// by the user, cleared when the session ends. That is exactly the right home for
// generated engine configs, which carry proxy credentials and Reality keys.
//
// XDG_RUNTIME_DIR is absent in some contexts (ssh sessions without a seat, some
// containers). Falling back to the app's own runtime/ dir keeps things working
// there; volatileDirPath creates it 0700 either way, so the permission promise
// holds even when tmpfs doesn't.
func volatileRootDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, appDirName)
	}
	return filepath.Join(tabsDir(), "runtime")
}
