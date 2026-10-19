//go:build windows

package shell

// FramelessWindow reports whether the window should be drawn without native
// decorations, with the app supplying its own titlebar.
//
// Windows: yes. The custom titlebar is the product's look, and Win32 handles a
// frameless window with a drag region without complaint.
func FramelessWindow() bool { return true }
