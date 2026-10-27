package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"vair/helper/api"
)

// What the Linux helper and the Windows service share: the request handling
// that does not depend on how a tunnel is built or a block is put up.

// version is reported in the hello response. It is the protocol that decides
// compatibility; this is for humans reading logs.
const version = "2.7.0"

// writeTimeout bounds every write to a client.
//
// Without it a client that has stopped READING wedges the helper: the socket
// buffer fills, the write blocks forever, and it blocks while holding the
// session lock. That is not a hypothetical — a GUI that has stopped reading is
// exactly the case the lease exists to clean up after, so the cleanup path was
// the one guaranteed to hit it.
const writeTimeout = 5 * time.Second

// ruleSetName is the only shape of name accepted from the socket.
//
// Deliberately narrow. The helper builds the path itself, so a name is not
// supposed to be able to express one — and this is the check that makes that
// true rather than merely intended. No slash, no leading dot, no traversal, one
// extension, and a length a filesystem will not argue with.
var ruleSetName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}\.srs$`)

func leaseDuration(sec int) time.Duration {
	if sec <= 0 {
		sec = api.DefaultLeaseSec
	}
	return time.Duration(sec) * time.Second
}

// ifaceFromConfig pulls the interface name out of the validated config for
// reporting. The validator has already confirmed it is one of ours.
func ifaceFromConfig(raw []byte) string {
	var doc struct {
		Inbounds []struct {
			Type  string `json:"type"`
			Iface string `json:"interface_name"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	for _, in := range doc.Inbounds {
		if in.Type == "tun" {
			return in.Iface
		}
	}
	return ""
}

// engineLevel maps sing-box's own prefix onto the app's log levels so the panel
// filters behave the same as they do when the GUI spawns the engine itself.
//
// As words: sing-box's DNS answers say "exchanged example.org NOERROR", which
// the substring test filed as an error — a red line in the log panel for every
// lookup of a verbose session. The app's own reader (core hasLevelWord) learnt
// this long ago; this is the same test for the lines the helper forwards.
func engineLevel(line string) string {
	switch {
	case levelWord(line, "FATAL") || levelWord(line, "ERROR"):
		return "error"
	case levelWord(line, "WARN"):
		return "warn"
	default:
		return "info"
	}
}

// levelWord reports whether word appears not as the tail of a longer one.
func levelWord(line, word string) bool {
	for i := 0; i+len(word) <= len(line); {
		j := strings.Index(line[i:], word)
		if j < 0 {
			return false
		}
		j += i
		if j == 0 || !isLetter(line[j-1]) {
			return true
		}
		i = j + 1
	}
	return false
}

func isLetter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }
