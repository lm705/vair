//go:build windows

package main

import "golang.org/x/sys/windows"

// echoOff stops the console echoing what is typed, and returns the undo. It
// does nothing when standard input is not a console.
func echoOff() func() {
	h := windows.Handle(windows.Stdin)
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return func() {}
	}
	if windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT) != nil {
		return func() {}
	}
	return func() { windows.SetConsoleMode(h, mode) } //nolint:errcheck
}
