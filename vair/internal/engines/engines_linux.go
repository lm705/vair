package engines

import (
	"os"
	"path/filepath"
	"strings"

	"vair/core"
)

// Engine discovery on Linux differs from Windows in one important way: the
// binaries are expected to come from the PACKAGE, not to be extracted into the
// user's home.
//
// That is a security requirement, not a packaging preference. The privileged
// helper planned for TUN mode runs with ProtectHome=yes precisely so it cannot
// be asked to execute something under $HOME — a user-writable sing-box that the
// helper would run as root is a trivial escalation. So the package path wins,
// and the writable fallback exists only for development and for tarball
// installs where TUN is not available anyway.
var engineSearchDirs = []string{
	"/usr/lib/vair/bin", // .deb / .rpm — root-owned, what the helper may spawn
	"/usr/local/lib/vair/bin",
}

// engineDir is the writable fallback: the app's own data dir. Note it is NOT
// /tmp — that is frequently mounted noexec under systemd hardening, and is
// wiped between boots.
func engineDir() (string, error) {
	dir := filepath.Join(core.AppRootDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// RegisterPaths points core at whichever engine copies exist, preferring
// the package location. Rule-sets always resolve to a writable directory of
// their own, because the refresher rewrites them and the package dir may be
// read-only — see core.SetRuleSetDir.
func RegisterPaths() {
	dirs := engineSearchDirs
	if d, err := engineDir(); err == nil {
		dirs = append(dirs, d)
	}
	for _, dir := range dirs {
		xray := filepath.Join(dir, "xray")
		singbox := filepath.Join(dir, "sing-box")
		if !fileExists(singbox) && !fileExists(xray) {
			continue
		}
		core.SetBinDir(dir)
		core.SetEngines(xray, singbox)
		break
	}
	if d, err := engineDir(); err == nil {
		rs := filepath.Join(filepath.Dir(d), "rulesets")
		seedRuleSets(rs)
		core.SetRuleSetDir(rs)
	}
}

// seedRuleSets copies the geo data and rule-sets shipped beside the engines into
// the writable directory, once, for files that aren't there yet.
//
// It has to happen before anything reads them: xray resolves geoip.dat and
// geosite.dat through XRAY_LOCATION_ASSET, which SetRuleSetDir points here, and
// refuses to start if they are missing. The package copies are read-only, so
// they cannot simply be used in place — the refresher rewrites two of these
// files.
//
// Existing files are never overwritten: a refreshed copy is newer than the
// packaged one, and clobbering it on every launch would undo the refresh.
func seedRuleSets(dst string) {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return
	}
	for _, src := range ruleSetSourceDirs() {
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

// ruleSetSourceDirs are the places a packaged build leaves the shipped geo data,
// plus the engine dir itself for tarball/dev layouts where everything sits
// together.
func ruleSetSourceDirs() []string {
	dirs := []string{"/usr/lib/vair/rulesets", "/usr/local/lib/vair/rulesets"}
	if d, err := engineDir(); err == nil {
		dirs = append(dirs, d)
	}
	return dirs
}

func isRuleSetFile(name string) bool {
	return strings.HasSuffix(name, ".srs") || strings.HasSuffix(name, ".dat") || name == "gfw.txt"
}

// Extract is a no-op here: nothing is embedded on Linux, because the packages
// install the engines under /usr/lib/vair/bin where the privileged helper is
// allowed to run them from. The argument is accepted and ignored so the two
// platforms present the same call to main.
func Extract([]File) error { return nil }

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
