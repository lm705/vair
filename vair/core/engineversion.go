package core

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Which sing-box is actually on disk.
//
// Two of the fields we write are on the engine's own deprecation clock, and a
// configuration key does not fail politely: sing-box accepts it, warns about
// it, and then one release later refuses to start with it. Reading the version
// once — the engine prints it in a line — is what lets a build write the key
// for the engines that need it and leave it out for the ones that would
// eventually reject it, instead of pinning us to whatever was true the day the
// config builder was written.
//
// It is also worth knowing on its own: "which engine version is this" is the
// first question of nearly every bug report, and until now the log did not say.

var singboxVer struct {
	sync.RWMutex
	major, minor int
	known        bool
}

// singboxVersionRe matches the engine's own line: "sing-box version 1.13.16".
var singboxVersionRe = regexp.MustCompile(`version\s+v?(\d+)\.(\d+)`)

// parseSingboxVersion pulls the major and minor out of that line. A build from
// source can print anything after the minor (a commit, "-beta.3"), which is why
// only the two numbers that decide compatibility are read.
func parseSingboxVersion(out string) (major, minor int, ok bool) {
	m := singboxVersionRe.FindStringSubmatch(out)
	if m == nil {
		return 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return major, minor, true
}

// detectSingboxVersion runs the binary once and remembers what it said. Called
// off the hot path (from the shell, after the engines are registered) because
// it spawns a process; everything that reads the answer takes what is known at
// the time and falls back to "unknown", which is the conservative branch.
func detectSingboxVersion() {
	_, bin := engineBins()
	if bin == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "version")
	// Hidden, like every other process this program starts. A console engine
	// launched without it flashes a black window over the desktop — and this one
	// runs at startup, which is the most visible moment there is.
	prepareProc(cmd)
	out, err := cmd.Output()
	if err != nil {
		vlog("warn", "sing-box version could not be read (%v)", err)
		return
	}
	major, minor, ok := parseSingboxVersion(string(out))
	if !ok {
		return
	}
	singboxVer.Lock()
	singboxVer.major, singboxVer.minor, singboxVer.known = major, minor, true
	singboxVer.Unlock()
	vlog("info", "sing-box %d.%d", major, minor)
}

// singboxAtLeast reports whether the engine on disk is at least this version.
// Unknown counts as "no": every caller uses it to decide whether a key that
// OLDER engines need may be left out, and leaving it out on an engine we could
// not identify would change how that engine behaves.
func singboxAtLeast(major, minor int) bool {
	singboxVer.RLock()
	defer singboxVer.RUnlock()
	if !singboxVer.known {
		return false
	}
	return singboxVer.major > major || (singboxVer.major == major && singboxVer.minor >= minor)
}

// dnsIndependentCache says whether to write dns.independent_cache.
//
// The key keeps one DNS cache per server. That matters here because the split
// routing modes deliberately resolve the same name two ways — a bypassed site
// through the direct resolver, a tunnelled one through the remote resolver —
// and a shared cache would hand the second caller whatever the first one got.
//
// sing-box made the behaviour unconditional in 1.14 and deprecated the key,
// scheduled for removal in 1.16, where a config that still carries it fails to
// start. So it is written for the engines that need it and left out for the
// ones that would refuse it, rather than being deleted on the calendar.
func dnsIndependentCache() bool { return !singboxAtLeast(1, 14) }
