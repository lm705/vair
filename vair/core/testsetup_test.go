package core

import (
	"os"
	"testing"
)

// TestMain redirects the app's data directory to a throwaway temp folder for the
// entire test run. tabsDir() reads LOCALAPPDATA / APPDATA, so any test that
// exercises a handler which calls saveTabs() / saveSettings() would otherwise
// overwrite the user's real %LOCALAPPDATA%\vair config (tabs.json, settings.json).
// This safety net makes that impossible — every test writes to the temp dir.
//
// This file is dev-only (Go excludes *_test.go from builds). DO NOT remove it,
// and DO NOT point tabsDir() at the real profile in tests. See AGENT_HANDOFF §13.5.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "vair-test-")
	if err == nil {
		os.Setenv("LOCALAPPDATA", tmp)
		os.Setenv("APPDATA", tmp)
	}
	code := m.Run()
	if tmp != "" {
		os.RemoveAll(tmp)
	}
	os.Exit(code)
}
