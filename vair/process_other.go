//go:build !windows

package main

import (
	"context"
	"os/exec"
)

func hideProcess(cmd *exec.Cmd) {}

func runHidden(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

func runHiddenContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
