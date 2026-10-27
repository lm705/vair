package validate

import (
	"fmt"
	"strings"
)

// checkWindowsPath is checkPath for a configuration the Windows helper service
// runs as SYSTEM.
//
// Lexical, like the Linux check, and for the same reason it can be: the
// directories it allows (under Program Files and ProgramData) are writable by
// administrators only, so a user who can reach the pipe cannot plant a link
// inside them. What it has to handle that Linux does not is the number of ways
// Windows spells a path. So it refuses, rather than normalises, everything but
// the plainest form — a drive, backslashes, plain names:
//
//   - \\?\ and \\.\ paths, which skip Windows' own normalisation, and network
//     shares, which are another machine;
//   - anything relative, "C:file" included (relative to the drive's current
//     directory);
//   - "." and ".." segments;
//   - a colon after the drive — an alternate data stream of a file — and the
//     characters Windows does not allow in a name, control characters first;
//   - "~" — a short 8.3 name, a second spelling of a long one;
//   - a segment ending in a dot or a space, which Windows silently drops;
//   - device names (CON, NUL, COM1…), which are devices in every directory.
//
// Then the prefix test, regardless of case, as Windows compares names.
func checkWindowsPath(key, p string, opt Options) error {
	bad := func(why string) error { return fmt.Errorf("%s %q %s", key, p, why) }
	q := strings.ReplaceAll(p, "/", `\`)
	if strings.HasPrefix(q, `\\`) {
		return bad(`must be a plain drive path (no \\?\, \\.\ or network share)`)
	}
	if len(q) < 3 || !isDriveLetter(q[0]) || q[1] != ':' || q[2] != '\\' {
		return bad("must be an absolute path on a drive")
	}
	if strings.Contains(q[2:], ":") {
		return bad("names an alternate data stream")
	}
	if strings.ContainsFunc(q, func(r rune) bool { return r < 0x20 || strings.ContainsRune(`<>"|?*`, r) }) {
		return bad("has a character Windows does not allow in a name")
	}
	if strings.Contains(q, "~") {
		return bad("uses a short (8.3) name")
	}
	for _, seg := range strings.Split(q[3:], `\`) {
		switch {
		case seg == "":
			return bad("has an empty segment")
		case seg == "." || seg == "..":
			return bad("has a relative segment")
		case strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " "):
			return bad("has a segment ending in a dot or a space")
		case isDeviceName(seg):
			return bad("names a device")
		}
	}
	lower := strings.ToLower(q)
	for _, prefix := range opt.AllowedPathPrefixes {
		pre := strings.ToLower(strings.TrimRight(strings.ReplaceAll(prefix, "/", `\`), `\`))
		if pre == "" {
			continue
		}
		// The trailing separator matters: a plain string prefix would accept
		// C:\ProgramData\Vair-evil for an allowed C:\ProgramData\Vair.
		if lower == pre || strings.HasPrefix(lower, pre+`\`) {
			return nil
		}
	}
	return fmt.Errorf("%s %q is outside the allowed directories %v", key, p, opt.AllowedPathPrefixes)
}

func isDriveLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// isDeviceName reports CON, PRN, AUX, NUL, CONIN$, CONOUT$, COM0–9 and LPT0–9,
// with or without an extension — "nul.srs" is the null device as much as "nul"
// is.
func isDeviceName(seg string) bool {
	base := strings.ToLower(seg)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimRight(base, " ")
	switch base {
	case "con", "prn", "aux", "nul", "conin$", "conout$":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '0' && base[3] <= '9' {
		return true
	}
	return false
}
