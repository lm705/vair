package core

// The startup notice of 2.7.2: a Telegram channel exists, 3.0.0 is coming there
// and needs testers (frontend/src/StartupNotice.tsx). It belongs to this
// release only; 3.0.0 leaves it out — this file, the component, and the one
// settings field with its line in keepProgramOwned.

// DismissStartupNotice records "Don't show again".
//
// One field, like SetSelectedMode, and not a settings save: the window's copy
// of the settings may be older than the file, and writing it back whole would
// reset whatever changed since.
func DismissStartupNotice() {
	settingsMu.Lock()
	changed := !appSettings.StartupNoticeDismissed
	appSettings.StartupNoticeDismissed = true
	settingsMu.Unlock()
	if changed {
		saveSettings()
	}
}
