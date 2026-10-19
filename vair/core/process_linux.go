package core

import (
	"os/exec"
	"syscall"
	"time"
)

// prepareProc puts the child in its own process group and ties its life to ours.
//
// Setpgid is not cosmetic on Linux — it is what makes killTree work. In hybrid
// TUN mode sing-box spawns xray, so signalling only the direct child leaves a
// live grandchild holding the local SOCKS port; the next connect then fails to
// bind with no obvious cause. Setpgid gives the whole subtree one negative PID
// to signal.
//
// Pdeathsig covers the case killTree cannot: the app never gets to run its own
// teardown. Observed, not theorised — `kill -9` on the GUI during a TUN session
// left xray running with a config full of proxy credentials, reparented to init,
// for as long as the machine stayed up. The helper cleans up its own engine when
// the socket closes, but xray is OUR child and nothing was watching it.
//
// The known caveat is that the kernel delivers this when the THREAD that forked
// exits, not the process — and Go may retire an OS thread under a goroutine.
// It is safe here because engines are only ever started from binding calls,
// which Wails dispatches either on an ordinary scheduler thread (Go parks those,
// it does not exit them) or on the GTK main thread, which outlives everything.
// A short-lived cgo callback thread would be the dangerous case, and no engine
// is started from one.
func prepareProc(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

// killTree terminates cmd and its process group, then waits for it. SIGTERM
// first so the engine can close its tunnel cleanly, SIGKILL if it doesn't go.
//
// The negative PID is the whole point: kill(-pgid) reaches every process in the
// group, which is how the xray grandchild dies with its parent. It falls back to
// signalling the process alone if the group id can't be read, which is better
// than not killing anything.
func killTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	signalTree := func(sig syscall.Signal) {
		if pgid, err := syscall.Getpgid(pid); err == nil {
			syscall.Kill(-pgid, sig) //nolint:errcheck
			return
		}
		syscall.Kill(pid, sig) //nolint:errcheck
	}

	signalTree(syscall.SIGTERM)

	waitDone := make(chan struct{})
	go func() { cmd.Wait(); close(waitDone) }() //nolint:errcheck
	select {
	case <-waitDone:
	case <-time.After(killTreeGrace):
		signalTree(syscall.SIGKILL)
		// Bounded even after SIGKILL: a process stuck in uninterruptible sleep
		// would otherwise hang the whole disconnect path.
		select {
		case <-waitDone:
		case <-time.After(killTreeGrace):
		}
	}
}
