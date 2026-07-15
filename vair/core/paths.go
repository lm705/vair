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

// tabsDir is %LOCALAPPDATA%\vair (fallback %APPDATA%\vair, then ".") — the root
// of Vair's data, shared with the 1.10 release. (The function name is kept from
// 1.10 so ported domain code calls it unchanged.)
func tabsDir() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, appDirName)
	}
	if d := os.Getenv("APPDATA"); d != "" {
		return filepath.Join(d, appDirName)
	}
	return "."
}

// dataDirPath is the durable data subfolder (db, tabs, settings).
func dataDirPath() string {
	d := filepath.Join(tabsDir(), "data")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// runtimeDirPath is transient engine state, regenerated each run.
func runtimeDirPath() string {
	d := filepath.Join(tabsDir(), "runtime")
	_ = os.MkdirAll(d, 0o755)
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

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
