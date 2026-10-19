package core

import (
	"os"
	"path/filepath"
)

// appDirName is "vair" — the same %LOCALAPPDATA% folder the 1.10 release used, so
// upgrading users keep all their data (DB, tabs, settings) with no export/import:
// 2.0 opens 1.10's configs.db / tabs.json / settings.json in place (same formats;
// the store applies its own migrations, and migrateDataLayout tidies the legacy
// flat layout into data\). NOTE: 2.0 replaces 1.10 — don't run both at once, or
// two writers hit the same DB.
const appDirName = "vair"

// appRootOverride redirects the whole tree, for tests only. The old TestMain
// trick — setting LOCALAPPDATA/APPDATA — only worked because every platform read
// those two variables; once Linux reads XDG_DATA_HOME instead, an env-based
// override silently stops protecting the developer's real profile on one of the
// two platforms. An explicit hook can't drift like that.
var appRootOverride string

// setAppRootForTest points the data tree at dir. Test-only; see TestMain.
func setAppRootForTest(dir string) { appRootOverride = dir }

// tabsDir is the root of Vair's data — %LOCALAPPDATA%\vair on Windows,
// $XDG_DATA_HOME/vair on Linux (see appRootDir in paths_{windows,linux}.go).
// The function name is kept from 1.10 so ported domain code calls it unchanged.
// It does NOT create the directory; callers that write do their own MkdirAll.
func tabsDir() string {
	if appRootOverride != "" {
		return appRootOverride
	}
	return appRootDir()
}

// AppRootDir is tabsDir for the shell layer, which needs to place the engine
// binaries and the rule-set cache beside the rest of the app's data.
func AppRootDir() string { return tabsDir() }

// dataDirPath is the durable data subfolder (db, tabs, settings).
//
// 0700, not 0755: this directory holds the config database, the web-interface
// token and the proxy passwords. On a shared Linux box a world-readable data
// dir hands every local account the token that controls this client over the
// network. Windows ignores the mode and relies on the profile's ACL, which
// already says the same thing.
func dataDirPath() string {
	d := filepath.Join(tabsDir(), "data")
	_ = os.MkdirAll(d, 0o700)
	_ = os.Chmod(d, 0o700) // an existing directory from an older version
	return d
}

// runtimeDirPath is transient engine state, regenerated each run.
func runtimeDirPath() string {
	d := filepath.Join(tabsDir(), "runtime")
	_ = os.MkdirAll(d, 0o700)
	_ = os.Chmod(d, 0o700) // engine configs live here; same reasoning as data/
	return d
}

// volatileDirPath is for files that must not outlive the session and may hold
// secrets — generated engine configs carry proxy credentials, Reality keys and
// the server address. On Linux that's $XDG_RUNTIME_DIR/vair: tmpfs, mode 0700,
// per-user, wiped at logout. The previous home for those files was os.CreateTemp
// with an empty dir, i.e. /tmp — a shared directory whose names are guessable by
// glob and whose contents survive a logout.
//
// Created 0700 (not 0755 like the two above) because that is the whole point.
func volatileDirPath() string {
	d := volatileRootDir()
	_ = os.MkdirAll(d, 0o700)
	return d
}

// dataPath returns a durable data file's path, preferring data/ but falling back
// to the legacy flat location if a file hasn't been migrated yet.
func dataPath(name string) string {
	np := filepath.Join(dataDirPath(), name)
	if _, err := os.Stat(np); err == nil {
		return np
	}
	if old := filepath.Join(tabsDir(), name); fileExists(old) {
		return old
	}
	return np
}

// runtimePath returns a transient runtime file's path under runtime/.
func runtimePath(name string) string { return filepath.Join(runtimeDirPath(), name) }

// homeFallbackDir is the last resort shared by both platforms: an absolute,
// per-user, predictable location. Anything is better than the "." this used to
// return — a relative path puts the config DB in whatever directory the process
// happened to be started from, which for a tray app launched at login is not a
// place the user will ever find.
func homeFallbackDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, "."+appDirName)
	}
	return filepath.Join(os.TempDir(), appDirName)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
