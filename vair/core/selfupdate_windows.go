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
// and restarts. A running .exe can't overwrite itself, so a detached
// PowerShell helper (broken away from the Job Object) waits for THIS process
// to exit, moves the exe aside as <exe>.old, puts the new one in its place and
// launches it — preserving elevation when we were elevated. On success this
// process exits and does not return.
//
// The old exe used to be overwritten outright, so a new build that could not
// start left nothing to go back to. Now the helper watches the new one for 15
// seconds: if it ends with an error code in that time, the old exe goes back
// and is started instead. A build that starts keeps <exe>.old for its first
// minute (CleanupUpdateLeftovers).
func replaceAndRelaunch(newExe string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	script := updateHelperScript(os.Getpid(), exe, newExe, isPrivileged())

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

// updateHelperScript is the PowerShell that swaps the exe once process pid has
// gone, and puts the old one back when the new one fails to start.
func updateHelperScript(pid int, exe, newExe string, elevated bool) string {
	dir := filepath.Dir(exe)
	esc := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	verb := ""
	if elevated {
		verb = " -Verb RunAs"
	}
	// $pid is a reserved PowerShell automatic variable — use $vpid for our PID.
	// $p.Handle is read straight after the start: Windows PowerShell reports no
	// exit code for a Start-Process child whose handle was not opened while it
	// ran.
	return fmt.Sprintf(
		"$vpid=%d; $exe='%s'; $new='%s'; $old=$exe+'.old'; $dir='%s'; "+
			"for($i=0;$i -lt 100;$i++){ if(-not (Get-Process -Id $vpid -ErrorAction SilentlyContinue)){break}; Start-Sleep -Milliseconds 150 }; "+
			"Start-Sleep -Milliseconds 300; "+
			"$swapped=$false; "+
			"for($i=0;$i -lt 30;$i++){ try{ Move-Item -Force -LiteralPath $exe -Destination $old -ErrorAction Stop; $swapped=$true; break }catch{ Start-Sleep -Milliseconds 200 } }; "+
			"if($swapped){ try{ Move-Item -Force -LiteralPath $new -Destination $exe -ErrorAction Stop }catch{ Move-Item -Force -LiteralPath $old -Destination $exe; $swapped=$false } }; "+
			"$p=Start-Process -FilePath $exe -WorkingDirectory $dir%s -PassThru; "+
			"if($swapped -and $p){ $null=$p.Handle; "+
			"if($p.WaitForExit(15000) -and $p.ExitCode -ne 0){ "+
			"Start-Sleep -Milliseconds 500; "+
			"try{ Move-Item -Force -LiteralPath $exe -Destination $new -ErrorAction Stop; Move-Item -Force -LiteralPath $old -Destination $exe -ErrorAction Stop; "+
			"Start-Process -FilePath $exe -WorkingDirectory $dir%s }catch{} } }",
		pid, esc(exe), esc(newExe), esc(dir), verb, verb)

}

// CleanupUpdateLeftovers removes a stray <exe>.new from a failed or aborted
// update at once, and the previous build, <exe>.old, after the first minute:
// until then the update helper may still need it to undo an update that does
// not start (replaceAndRelaunch). Best-effort; called once at startup.
func CleanupUpdateLeftovers() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	os.Remove(exe + ".new")
	if _, err := os.Stat(exe + ".old"); err == nil {
		go func() {
			time.Sleep(time.Minute)
			os.Remove(exe + ".old")
		}()
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
