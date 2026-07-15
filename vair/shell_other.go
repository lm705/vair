//go:build !windows

package main

// Registry-based autostart and URL-scheme registration are Windows-only for now.
// Linux equivalents (XDG autostart .desktop, MIME/scheme handler) land with the
// Linux port in Ф6.
func applyAutostart(bool) error   { return nil }
func registerDeepLink(bool) error { return nil }
