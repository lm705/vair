//go:build windows

package shell

// TrayHostAvailable is unconditionally true on Windows: the notification area
// is part of the shell, not an optional component, and Shell_NotifyIcon has
// nowhere to fail that would leave the app unreachable.
func TrayHostAvailable(bool) bool { return true }
