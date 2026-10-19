//go:build windows

package core

import (
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

const createNoWindow = 0x08000000

// prepareProc configures a child process before it is started. On Windows that
// means hiding the console window the engines would otherwise flash up.
//
// The name is deliberately not "hideProcess" any more: the Linux implementation
// uses this same hook to put the child in its own process group, without which
// killTree cannot reach the grandchildren. It is load-bearing on one platform
// and cosmetic on the other, so it is named for when it runs, not what it does.
func prepareProc(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags = createNoWindow
}

func runHidden(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	prepareProc(cmd)
	return cmd
}

// killTree terminates cmd and everything it spawned, then waits for it.
//
// Go's Process.Kill() is soft on Windows: the process may drain pending work
// (DNS queries with long timeouts, for instance) before it goes. taskkill /F /T
// forces it and takes the whole tree with it, which matters because the hybrid
// TUN path runs xray as a child of sing-box.
func killTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	// In parallel with Go's own kill — whichever lands first wins.
	go runHidden("taskkill", "/F", "/T", "/PID", pid).Run() //nolint:errcheck
	cmd.Process.Kill()                                      //nolint:errcheck

	waitDone := make(chan struct{})
	go func() { cmd.Wait(); close(waitDone) }() //nolint:errcheck
	select {
	case <-waitDone:
	case <-time.After(killTreeGrace):
		runHidden("taskkill", "/F", "/T", "/PID", pid).Run() //nolint:errcheck
	}
}
