package shell

import (
	"net/url"
	"strings"
)

// webLink returns s when it is a web address — http or https, with a host —
// and ok=false for anything else.
//
// OpenURL hands its argument to the system's "open this" handler, which opens
// whatever it is given: a web page, but equally a file or a program. Every
// caller in the program passes a web address, so that is all it accepts; a
// path or another scheme is refused rather than opened.
func webLink(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "\x00\r\n\t ") {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.String(), true
	}
	return "", false
}
