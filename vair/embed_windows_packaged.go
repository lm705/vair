//go:build windows && !portable

package main

import "vair/internal/engines"

// The installed Windows build embeds nothing. Its engines and geo data are
// ordinary files the installer laid down in bin\ beside the exe, which is the
// same arrangement the Linux packages have always had — and for the same
// reason: a privileged service may only spawn a binary the user cannot rewrite,
// and %LOCALAPPDATA% is a directory the user can rewrite.
//
// The visible half of the trade is size. Without the payload the exe is around
// 16 MB instead of 126, nothing is unpacked at first run, and an update ships
// the program rather than the program plus a copy of two engines that did not
// change.
//
// The portable build is the other tag (embed_windows_portable.go) and keeps the
// old behaviour exactly: one file, unpacked into the profile.
func embeddedEngineFiles() []engines.File { return nil }
