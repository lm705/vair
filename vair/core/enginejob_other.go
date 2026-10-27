//go:build !windows

package core

import "os/exec"

// tieToVair: on Linux the TUN engine runs in the privileged helper, which
// takes a dropped owner as a teardown; see enginejob_windows.go.
func tieToVair(*exec.Cmd) {}
