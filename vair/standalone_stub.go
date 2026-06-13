//go:build !windows

package main

func standaloneMain() {
	panic("standaloneMain: only supported on Windows")
}

func cleanupBinaries()       {}
func prewarmBinary(_ string) {}

// Stubs for the native bindings used on Windows. Linux/macOS builds compile
// only for syntax checking; nothing reaches these.
func pickConfigFiles(_ uintptr) []string                         { return nil }
func listRunningProcessNames() []string                          { return nil }
func openStorageLocation(_ string) error                         { return nil }
func saveJSONFile(_ uintptr, _ string, _ []byte) (string, error) { return "", nil }

func refreshRuRuleSets()                    {}
func refreshBlockedRuleSets()               {}
func refreshCustomBlocklist(_ string)       {}
func ruRuleSetLocalPath(file string) string { return file }
func customBlocklistPath() string           { return "" }

func applyAutostart(_ bool) error { return nil }

func replaceAndRelaunch(_ string) error { return nil }
func cleanupUpdateLeftovers()           {}
