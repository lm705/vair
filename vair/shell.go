package main

import "strings"

// deepLinkFromArgs returns the first vair:// argument (a deep link the OS passed
// when launching us), or "" if none.
func deepLinkFromArgs(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(strings.ToLower(a), "vair://") {
			return a
		}
	}
	return ""
}

// hasAutostartFlag reports whether we were launched at logon (the HKCU Run-key
// command appends --autostart), so we should start hidden in the tray.
func hasAutostartFlag(args []string) bool {
	for _, a := range args {
		if a == "--autostart" {
			return true
		}
	}
	return false
}
