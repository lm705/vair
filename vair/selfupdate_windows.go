//go:build windows

package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// replaceAndRelaunch swaps the running exe for the freshly-downloaded newExe and
// restarts. A running .exe can't overwrite itself, and our Job Object has
// KILL_ON_JOB_CLOSE, so we hand the job to a detached PowerShell helper that has
// broken away from the job (same trick as restartAsAdmin): it waits for THIS
// process to exit (freeing the file + the HTTP port), overwrites the exe with
// the new build, then launches it — preserving elevation when we were elevated.
// On success this process exits and does not return.
func replaceAndRelaunch(newExe string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	pid := os.Getpid()
	esc := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	verb := ""
	if checkAdmin() {
		verb = " -Verb RunAs"
	}
	// $pid is a reserved PowerShell automatic variable — use $vpid for our PID.
	script := fmt.Sprintf(
		"$vpid=%d; "+
			"for($i=0;$i -lt 100;$i++){ if(-not (Get-Process -Id $vpid -ErrorAction SilentlyContinue)){break}; Start-Sleep -Milliseconds 150 }; "+
			"Start-Sleep -Milliseconds 300; "+
			"for($i=0;$i -lt 30;$i++){ try{ Move-Item -Force '%s' '%s'; break }catch{ Start-Sleep -Milliseconds 200 } }; "+
			"Start-Process -FilePath '%s' -WorkingDirectory '%s'%s",
		pid, esc(newExe), esc(exe), esc(exe), esc(dir), verb)

	enc := base64.StdEncoding.EncodeToString(utf16LE(script))
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-EncodedCommand", enc)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createBreakawayFromJob | createNoWindow,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Tear down the tunnel cleanly, then exit so the helper can replace the file
	// and the new instance can grab the port.
	go func() {
		time.Sleep(200 * time.Millisecond)
		stopConnection()
		os.Exit(0)
	}()
	return nil
}

// cleanupUpdateLeftovers removes a stray <exe>.new from a failed/aborted update.
// Best-effort; called once at startup.
func cleanupUpdateLeftovers() {
	if exe, err := os.Executable(); err == nil {
		os.Remove(exe + ".new")
		os.Remove(exe + ".old")
	}
}
