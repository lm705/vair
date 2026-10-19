//go:build windows

package main

import _ "embed"

// The multi-size app icon, handed to internal/shell at startup.
//
// Here rather than there for the same reason the engine payload is: //go:embed
// cannot name build/windows/icon.ico from inside internal/shell.
//
//go:embed build/windows/icon.ico
var windowIconICO []byte

func windowIcon() []byte { return windowIconICO }
