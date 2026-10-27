//go:build windows

package core

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
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
	// With the helper service — or in a copy the setup program installed,
	// which comes with one — the answer is the service, never relaunching the
	// app as administrator.
	if serviceMode() || installedBySetupHere() {
		return ensureService()
	}
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

// ensureService brings the helper service back: started when it is only
// stopped (an interactive user may do that, without a prompt), repaired with
// one administrator prompt when it is missing or will not start — the setup
// program's own "vair-helper -repair", from the installation.
func ensureService() string {
	if ok, _ := helperReachable(); ok {
		return ""
	}
	dir := ServiceInstallDir()
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return err.Error()
		}
		dir = filepath.Dir(exe)
	}
	helper := filepath.Join(dir, "vair-helper.exe")
	if _, err := os.Stat(helper); err != nil {
		return "the Vair helper service is missing from " + dir + " — reinstall Vair"
	}
	esc := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	script := "$p = Start-Process -FilePath '" + esc(helper) + "' -ArgumentList '-repair' -Verb RunAs -WindowStyle Hidden -Wait -PassThru; exit $p.ExitCode"
	if _, err := psOutputFor(script, 5*time.Minute); err != nil {
		return "the Vair helper service could not be repaired: " + err.Error()
	}
	serviceState.Lock()
	serviceState.at = time.Time{} // ask the service manager again
	serviceState.Unlock()
	if ok, _ := helperReachable(); !ok {
		_, msg := helperProblem()
		return msg
	}
	vlog("info", "the Vair helper service is running again")
	return ""
}
