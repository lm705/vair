//go:build !windows

package main

func standaloneMain() {
	panic("standaloneMain: only supported on Windows")
}

func cleanupBinaries()          {}
func prewarmBinary(_ string)    {}
