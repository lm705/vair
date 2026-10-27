//go:build windows

package engines

import (
	"fmt"
	"os"
	"path/filepath"

	"vair/core"
)

// Engine discovery on Windows. Two layouts, one search order, and the same
// division of labour as Linux (see engines_linux.go):
//
//  1. <folder of the exe>\bin — what an installer laid down. Owned by whoever
//     installed it, and for a per-machine install that means the user running
//     Vair cannot rewrite it. Preferred whenever it exists.
//  2. <folder of the exe> — engines sitting beside the program: a zip anyone
//     unpacked, and the dev tree, where bin\ holds Vair.exe and both engines.
//  3. %LOCALAPPDATA%\vair\bin — writable, and where the portable build unpacks
//     the copies it carries inside itself.
//
// The order is not about convenience. A privileged Windows service for TUN —
// the same shape the Linux helper already has — may only ever spawn a binary
// the user cannot replace, so the read-only copy has to win when both exist.
// Nothing here depends on that service yet, and when it arrives it must apply
// this list to its OWN install directory rather than trusting what the app
// resolved: (2) and (3) are both writable by whoever runs the program.
//
// Rule-sets never resolve to (1): the refresher rewrites them at runtime, and
// a package directory is the wrong place for a file that changes. They live in
// %LOCALAPPDATA%\vair\rulesets and are seeded from whatever shipped.

// installedBinDir is the shipped copy of the engines: bin\ beside the running
// exe, or the exe's own folder. "" when neither holds an engine. Symlinks are
// resolved first so a shortcut target does not send us to the wrong folder.
func installedBinDir() string {
	// With the helper service: its own bin\ — the engines it runs and the
	// ones its blocks let out are those, so the app runs the same files. A
	// portable copy on a machine with an installed Vair uses them too.
	if dir := core.ServiceInstallDir(); dir != "" {
		bin := filepath.Join(dir, "bin")
		if fileExists(filepath.Join(bin, "sing-box.exe")) && fileExists(filepath.Join(bin, "xray.exe")) {
			return bin
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	here := filepath.Dir(exe)
	for _, dir := range []string{filepath.Join(here, "bin"), here} {
		if fileExists(filepath.Join(dir, "sing-box.exe")) || fileExists(filepath.Join(dir, "xray.exe")) {
			return dir
		}
	}
	return ""
}

// writableBinDir is %LOCALAPPDATA%\vair\bin — the portable build's own copy of
// the engines, and the fallback for a dev tree.
func writableBinDir() (string, error) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		local = os.Getenv("APPDATA")
	}
	if local == "" {
		return os.MkdirTemp("", "vair-")
	}
	dir := filepath.Join(local, "vair", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}
	return dir, nil
}

// ruleSetsDir is the writable home of the geo data, the .srs sets and the GFW
// list. Always under the user's profile, whichever layout the engines came from.
func ruleSetsDir() (string, error) {
	bin, err := writableBinDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(bin), "rulesets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// installed records which layout won, so Extract does not undo the choice by
// pointing core back at the writable copy it did not use.
var installed string

// RegisterPaths records the engine paths with core SYNCHRONOUSLY, before the
// (slow) extraction runs in the background. Without this, the frontend's first
// ConnService.AppInfo() call races the extraction goroutine and sees
// state.singboxBin == "" → the TUN pill shows "sing-box not found". The paths
// are valid the instant the directory exists; in the portable build the .exe
// files land a moment later (Extract), well before the user could click connect.
func RegisterPaths() {
	dir := installedBinDir()
	if dir != "" {
		installed = dir
	} else {
		d, err := writableBinDir()
		if err != nil {
			return
		}
		dir = d
	}
	core.SetBinDir(dir)
	core.SetEngines(filepath.Join(dir, "xray.exe"), filepath.Join(dir, "sing-box.exe"))

	if rs, err := ruleSetsDir(); err == nil {
		seedRuleSets(rs)
		sweepLegacyRuleSets(rs)
		core.SetRuleSetDir(rs)
	}
}

// Extract writes the engines and rule-set baselines a portable build carries
// into the writable directories, then points core at them.
//
// The bytes arrive as an argument rather than being embedded here: //go:embed
// cannot reach outside its own directory and the data lives in bin/ at the
// repository root, so the directives stay beside the data while the logic lives
// in a package of its own. A packaged build passes nothing and this returns
// immediately.
func Extract(files []File) error {
	if len(files) == 0 {
		return nil
	}
	if installed != "" {
		// The engines are already on disk, in the copy we prefer. Unpacking a
		// second set into the profile would waste 100 MB and leave two versions
		// for a future bug report to disagree about.
		return nil
	}
	bin, err := writableBinDir()
	if err != nil {
		return err
	}
	rs, err := ruleSetsDir()
	if err != nil {
		return err
	}
	for _, f := range files {
		dst := filepath.Join(destDirFor(f, bin, rs), f.Name)
		if f.KeepExisting {
			// A rule-set baseline: written only when MISSING, so a runtime
			// refresh is never clobbered by the copy that shipped with the
			// release.
			if _, statErr := os.Stat(dst); statErr == nil {
				continue
			}
			os.WriteFile(dst, f.Data, os.FileMode(f.Perm)) //nolint:errcheck
			continue
		}
		if needsExtract(dst, f.Data) {
			if err := os.WriteFile(dst, f.Data, os.FileMode(f.Perm)); err != nil {
				return fmt.Errorf("extract %s: %w", f.Name, err)
			}
		}
	}
	core.SetBinDir(bin)
	core.SetEngines(filepath.Join(bin, "xray.exe"), filepath.Join(bin, "sing-box.exe"))
	return nil
}

// destDirFor sorts one embedded file into the directory it belongs in: engines
// beside each other in bin, everything the engines READ in rulesets, where the
// refresher can rewrite it.
func destDirFor(f File, bin, rulesets string) string {
	if isRuleSetFile(f.Name) {
		return rulesets
	}
	return bin
}

// seedRuleSets copies the geo data and rule-sets that shipped with the program
// into the writable directory, once, for files that aren't there yet.
//
// It has to happen before anything reads them: xray resolves geoip.dat and
// geosite.dat through XRAY_LOCATION_ASSET, which core points here, and refuses
// to start without them.
//
// Existing files are never overwritten — a refreshed copy is newer than the one
// that shipped, and clobbering it at every launch would undo the refresh. That
// is also what carries a 2.5-and-earlier profile forward: those releases kept
// the rule-sets in bin\, so bin\ is one of the sources, and a set refreshed
// last week moves across instead of reverting to the baseline.
func seedRuleSets(dst string) {
	for _, src := range ruleSetSourceDirs() {
		if src == "" || src == dst {
			continue
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !isRuleSetFile(name) || fileExists(filepath.Join(dst, name)) {
				continue
			}
			if data, err := os.ReadFile(filepath.Join(src, name)); err == nil {
				os.WriteFile(filepath.Join(dst, name), data, 0o644) //nolint:errcheck
			}
		}
	}
}

// ruleSetSourceDirs are the places a shipped copy of the geo data can be, best
// first: beside the installed engines, then the profile folder older releases
// wrote to.
func ruleSetSourceDirs() []string {
	dirs := []string{installedBinDir()}
	if d, err := writableBinDir(); err == nil {
		dirs = append(dirs, d)
	}
	return dirs
}

// needsExtract reports whether dst is missing or differs from src (by size, then
// the first 512 bytes — catches same-size updates).
func needsExtract(dst string, src []byte) bool {
	info, err := os.Stat(dst)
	if err != nil || info.Size() != int64(len(src)) {
		return true
	}
	f, err := os.Open(dst)
	if err != nil {
		return true
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n < 512 || n > len(src) {
		return true
	}
	for i := 0; i < n; i++ {
		if buf[i] != src[i] {
			return true
		}
	}
	return false
}

// sweepLegacyRuleSets removes the geo data left in the profile's bin\ folder by
// 2.5 and earlier, once the same file is safely in rulesets\.
//
// seedRuleSets copies rather than moves — deliberately, because the source may
// be a read-only package directory — and the copy left every .srs, .dat and
// gfw.txt sitting in bin\ as well: forty-odd megabytes of files nothing reads
// any more, in a folder the user opens looking for the engines. A user found
// them and asked what they were, which is the right question to have to ask
// about a file the program stopped using two releases ago.
//
// Only the WRITABLE bin — the one under the user's profile. The installed
// directory beside the exe is the shipped copy: it may be read-only, it may be
// shared, and it is not ours to tidy.
func sweepLegacyRuleSets(rulesets string) {
	bin, err := writableBinDir()
	if err != nil || bin == "" || bin == rulesets {
		return
	}
	entries, err := os.ReadDir(bin)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !isRuleSetFile(name) {
			continue
		}
		// Never before the copy exists where it is read from now: a delete that
		// runs first turns a stale file into a missing one, and xray refuses to
		// start without geoip.dat.
		if !fileExists(filepath.Join(rulesets, name)) {
			continue
		}
		_ = os.Remove(filepath.Join(bin, name))
	}
}
