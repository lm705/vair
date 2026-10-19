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

// EnsurePrivilege acquires whatever TUN mode needs on this platform, or returns
// a reason it could not.
//
// The name is deliberately about the GOAL rather than the mechanism: elevation
// by relaunch is a Windows answer, and the Linux answer will be a privileged
// helper that the GUI talks to without ever becoming root itself. A caller that
// asks to "restart as admin" would have to be rewritten for that; one that asks
// to ensure privilege does not.
//
// On success this never returns — the elevated copy takes over. Here it
// relaunches via a detached PowerShell helper that waits out our shutdown and
// then Start-Process -Verb RunAs, which is what shows the UAC prompt (1.10
// restartAsAdmin, behind the "requires admin ↗" chip).
func EnsurePrivilege() string {
	exe, err := os.Executable()
	if err != nil {
		return err.Error()
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
		// Helper could not be spawned — stay running rather than vanish, and say
		// why instead of leaving the chip looking inert.
		return "could not start the elevation helper: " + err.Error()
	}
	// Clear the tunnel/system proxy before handing over to the elevated copy.
	stopConnection()
	os.Exit(0)
	return ""
}
