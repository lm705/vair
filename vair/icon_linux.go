package main

// No .ico on Linux: the desktop takes the app's icon from the hicolor theme the
// package installs into, keyed by the desktop entry's Icon= name.
func windowIcon() []byte { return nil }
