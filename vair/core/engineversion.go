package core

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
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

// detectSingboxVersion asks the engine for its version and remembers the
// answer. Called off the hot path (from the shell, after the engines are
// registered) because it spawns a process; everything that reads the answer
// takes what is known at the time and falls back to "unknown", which is the
// conservative branch.
func detectSingboxVersion() {
	if !singboxProbe.CompareAndSwap(false, true) {
		return
	}
	defer singboxProbe.Store(false)
	out, err := engineVersionOutput(func() string { _, b := engineBins(); return b })
	if err != nil {
		vlog("warn", "sing-box version could not be read (%v)", err)
		return
	}
	major, minor, ok := parseSingboxVersion(out)
	if !ok {
		return
	}
	singboxVer.Lock()
	singboxVer.major, singboxVer.minor, singboxVer.known = major, minor, true
	singboxVer.Unlock()
	vlog("info", "sing-box %d.%d", major, minor)
}

// One probe per engine at a time. SetEngines runs twice at startup — once to
// register the paths, once after extraction — and the session panel asks again
// when the version is still unknown; without the guard those would stack up
// behind the same slow first launch.
var singboxProbe, xrayProbe atomic.Bool

// versionProbeWaits are the pauses before each attempt. The first launch of a
// freshly extracted engine after a cold boot is the slow one: the antivirus
// scans the new file before it lets it run, and a single five-second attempt
// gave up there and never asked again, so the session panel said "present"
// for the rest of the run.
var versionProbeWaits = []time.Duration{0, 5 * time.Second, 30 * time.Second}

// versionProbeTimeout bounds one attempt.
const versionProbeTimeout = 20 * time.Second

// engineVersionOutput runs "<engine> version" until it answers or the attempts
// run out. The path is read again before each attempt because the extraction
// goroutine may register the final one in the meantime.
func engineVersionOutput(bin func() string) (string, error) {
	err := errors.New("engine not registered")
	for _, wait := range versionProbeWaits {
		time.Sleep(wait)
		path := bin()
		if path == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), versionProbeTimeout)
		cmd := exec.CommandContext(ctx, path, "version")
		// Hidden, like every other process this program starts. A console engine
		// launched without it flashes a black window over the desktop — and this
		// one runs at startup, which is the most visible moment there is.
		prepareProc(cmd)
		out, e := cmd.Output()
		cancel()
		if e == nil {
			return string(out), nil
		}
		err = e
	}
	return "", err
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

// xrayVer is the xray version on disk, for the session panel — which showed a
// version for sing-box and only "present" for xray.
var xrayVer struct {
	sync.RWMutex
	v string
}

// xrayVersionRe matches the engine's first line: "Xray 26.3.27 (Xray, …)".
var xrayVersionRe = regexp.MustCompile(`Xray\s+v?(\d+\.\d+\.\d+)`)

// detectXrayVersion asks xray for its version and remembers the answer; off
// the startup path and retried, like detectSingboxVersion.
func detectXrayVersion() {
	if !xrayProbe.CompareAndSwap(false, true) {
		return
	}
	defer xrayProbe.Store(false)
	out, err := engineVersionOutput(func() string { b, _ := engineBins(); return b })
	if err != nil {
		vlog("warn", "xray version could not be read (%v)", err)
		return
	}
	if m := xrayVersionRe.FindStringSubmatch(out); m != nil {
		xrayVer.Lock()
		xrayVer.v = m[1]
		xrayVer.Unlock()
		vlog("info", "xray %s", m[1])
	}
}

// xrayVersionString is what detectXrayVersion learned, or "".
func xrayVersionString() string {
	xrayVer.RLock()
	defer xrayVer.RUnlock()
	return xrayVer.v
}
