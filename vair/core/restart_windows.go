//go:build windows

package core

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// RestartAdmin relaunches Vair elevated and exits this instance (1.10
// restartAsAdmin — the "requires admin ↗" chip next to the TUN pill). The
// detached PowerShell helper waits out our shutdown, then Start-Process
// -Verb RunAs shows the UAC prompt.
func RestartAdmin() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(exe)

	esc := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	script := "Start-Sleep -Milliseconds 900; " +
		"Start-Process -FilePath '" + esc(exe) + "'" +
		" -WorkingDirectory '" + esc(dir) + "' -Verb RunAs"

	enc := base64.StdEncoding.EncodeToString(utf16LE(script))
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-EncodedCommand", enc)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createBreakawayFromJob | createNoWindow,
	}
	if err := cmd.Start(); err != nil {
		// Helper could not be spawned — stay running rather than vanish.
		return
	}
	// Clear the tunnel/system proxy before handing over to the elevated copy.
	stopConnection()
	os.Exit(0)
}
