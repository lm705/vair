//go:build linux || darwin

package main

import "golang.org/x/sys/unix"

// echoOff stops the terminal echoing what is typed, and returns the undo. It
// does nothing when standard input is not a terminal.
func echoOff() func() {
	fd := int(unix.Stdin)
	t, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return func() {}
	}
	quiet := *t
	quiet.Lflag &^= unix.ECHO
	if unix.IoctlSetTermios(fd, ioctlSetTermios, &quiet) != nil {
		return func() {}
	}
	return func() { unix.IoctlSetTermios(fd, ioctlSetTermios, t) } //nolint:errcheck
}
