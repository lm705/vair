//go:build !windows

package core

import "os/exec"

func hideProcess(cmd *exec.Cmd) {}

func runHidden(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
