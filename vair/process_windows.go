//go:build windows

package main

import (
	"context"
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func hideProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags = createNoWindow
}

func runHidden(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	hideProcess(cmd)
	return cmd
}

func runHiddenContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	hideProcess(cmd)
	return cmd
}
