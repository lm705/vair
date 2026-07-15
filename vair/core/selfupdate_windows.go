//go:build windows

package core

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const createBreakawayFromJob = 0x01000000

// replaceAndRelaunch swaps the running exe for the freshly-downloaded newExe
// and restarts (ported verbatim from 1.10 selfupdate_windows.go). A running
// .exe can't overwrite itself, so a detached PowerShell helper (broken away
// from the Job Object) waits for THIS process to exit, overwrites the exe,
// then launches it — preserving elevation when we were elevated. On success
// this process exits and does not return.
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
	// Tear down the tunnel cleanly, then exit so the helper can replace the
	// file and the new instance can start fresh.
	go func() {
		time.Sleep(200 * time.Millisecond)
		stopConnection()
		os.Exit(0)
	}()
	return nil
}

// CleanupUpdateLeftovers removes a stray <exe>.new / <exe>.old from a
// failed/aborted update. Best-effort; called once at startup.
func CleanupUpdateLeftovers() {
	if exe, err := os.Executable(); err == nil {
		os.Remove(exe + ".new")
		os.Remove(exe + ".old")
	}
}

// utf16LE encodes s as little-endian UTF-16 bytes (no BOM, no NUL terminator)
// for PowerShell's -EncodedCommand.
func utf16LE(s string) []byte {
	u, _ := windows.UTF16FromString(s)
	out := make([]byte, 0, len(u)*2)
	for _, c := range u {
		if c == 0 { // drop the terminating NUL UTF16FromString appends
			break
		}
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}
