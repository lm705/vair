package shell

import "strings"

// DeepLinkFromArgs returns the first vair:// argument (a deep link the OS passed
// when launching us), or "" if none.
func DeepLinkFromArgs(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(strings.ToLower(a), "vair://") {
			return a
		}
	}
	return ""
}

// HasAutostartFlag reports whether we were launched at logon (the HKCU Run-key
// command appends --autostart), so we should start hidden in the tray.
func HasAutostartFlag(args []string) bool {
	for _, a := range args {
		if a == "--autostart" {
			return true
		}
	}
	return false
}
