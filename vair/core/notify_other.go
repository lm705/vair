//go:build !windows

package core

// NotificationsBlockedReason has no Windows-style master switch to read here:
// a Linux desktop either runs a notification daemon or it does not, and the
// call simply goes nowhere when it does not.
func NotificationsBlockedReason(string) string { return "" }
