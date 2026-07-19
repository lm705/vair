//go:build windows

package core

import (
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ── Local rule-sets with best-effort upstream refresh ─────────────────────────
//
// sing-box/xray need geo rule-sets to route traffic (RU-bypass: route Russian
// sites direct; RU-blocked: route only blocked-in-RU resources through the VPN).
// Configuring them as `type: remote` pointed at raw.githubusercontent.com made
// sing-box abort with a FATAL the moment GitHub was unreachable — which is the
// norm in Russia, the exact audience for these lists.
//
// Instead every set is shipped as an embedded baseline (extracted to binDir) and
// referenced as a LOCAL file. At connect time we still TRY to pull a fresher copy
// from upstream, but fall back to the local/embedded file whenever that fails, so
// the engine always has a valid file and can never abort over a blocked remote.

type ruRuleSetDef struct {
	file string // local filename under binDir
	url  string // upstream source (best-effort refresh)
}

// countryGeositeSrs maps a bypass country to its upstream sing-geosite file.
// Countries absent here have no domain list (IP-only bypass — currently KZ).
var countryGeositeSrs = map[string]string{
	"ru": "geosite-category-ru.srs",
	"cn": "geosite-cn.srs",
	"ir": "geosite-category-ir.srs",
}

// countryRuleSetDefs returns the local-file + upstream-URL defs for one bypass
// country ("bypass_countries" mode). Local names are geosite-<cc>.srs /
// geoip-<cc>.srs under binDir; embedded baselines guarantee they exist even
// when the upstream (GitHub) is unreachable.
func countryRuleSetDefs(cc string) []ruRuleSetDef {
	var defs []ruRuleSetDef
	if up, ok := countryGeositeSrs[cc]; ok {
		defs = append(defs, ruRuleSetDef{"geosite-" + cc + ".srs",
			"https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/" + up})
	}
	defs = append(defs, ruRuleSetDef{"geoip-" + cc + ".srs",
		"https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-" + cc + ".srs"})
	return defs
}

// refreshCountryRuleSets best-effort updates the rule sets of the given bypass
// countries (same throttle/fallback as every other set).
func refreshCountryRuleSets(ccs []string) {
	var defs []ruRuleSetDef
	for _, cc := range ccs {
		defs = append(defs, countryRuleSetDefs(cc)...)
	}
	refreshRuleSets(defs)
}

// RU-blocked sets (route only resources BLOCKED in Russia through the VPN). srs
// for sing-box, dat (xray geosite/geoip, category "ru-blocked") for xray. Source:
// runetfreedom, auto-updated upstream every ~6h.
var ruBlockedRuleSetDefs = []ruRuleSetDef{
	{"geosite-ru-blocked.srs", "https://raw.githubusercontent.com/runetfreedom/russia-v2ray-rules-dat/release/sing-box/rule-set-geosite/geosite-ru-blocked.srs"},
	{"geoip-ru-blocked.srs", "https://raw.githubusercontent.com/runetfreedom/russia-v2ray-rules-dat/release/sing-box/rule-set-geoip/geoip-ru-blocked.srs"},
	{"geosite-ru-blocked.dat", "https://raw.githubusercontent.com/runetfreedom/russia-blocked-geosite/release/geosite-ru-only.dat"},
	{"geoip-ru-blocked.dat", "https://raw.githubusercontent.com/runetfreedom/russia-blocked-geoip/release/ru-blocked.dat"},
}

const (
	// A local file fresher than this is left as-is (no download attempt).
	ruRuleSetMaxAge = 24 * time.Hour
	// Don't retry a (possibly blocked) download more than this often per file, so
	// a censored network can't add the timeout to back-to-back connects.
	ruRefreshThrottle = 30 * time.Minute
	// Hard cap on each download so a blocked host can't stall the connect.
	ruDownloadTimeout = 8 * time.Second
	// Upper bound on a fetched rule-set/list (the biggest dat is ~6 MB).
	ruDownloadMaxBytes = 32 * 1024 * 1024
)

var (
	ruRefreshMu sync.Mutex
	ruLastTry   = map[string]time.Time{} // per-file last download attempt
)

// ruRuleSetLocalPath returns the on-disk path of a rule-set file under binDir.
func ruRuleSetLocalPath(file string) string { return filepath.Join(binDir, file) }

// customBlocklistPathFor is the on-disk cache path for one "through VPN (URL)"
// source, keyed by a hash of the URL so multiple sources don't collide.
func customBlocklistPathFor(url string) string {
	h := fnv.New32a()
	h.Write([]byte(strings.TrimSpace(url)))
	return filepath.Join(binDir, fmt.Sprintf("blocklist-%08x.txt", h.Sum32()))
}

// refreshBlockedRuleSets best-effort updates the RU-blocked sets.
func refreshBlockedRuleSets() { refreshRuleSets(ruBlockedRuleSetDefs) }

// cnBlockedListDefs is the blocked-in-China source ("only_blocked_cn" mode): the
// community GFW list as a plain domain-per-line text file (Loyalsoldier's build
// of gfwlist). Text is enough for BOTH engines — the domains are emitted
// straight into the config (like the custom blocklist), so no extra .dat/.srs
// is needed and the main geosite.dat stays untouched.
var cnBlockedListDefs = []ruRuleSetDef{
	{"gfw.txt", "https://raw.githubusercontent.com/Loyalsoldier/v2ray-rules-dat/release/gfw.txt"},
}

// refreshCnBlockedList best-effort updates the blocked-in-China domain list.
func refreshCnBlockedList() { refreshRuleSets(cnBlockedListDefs) }

// refreshRuleSets refreshes the given files from upstream. Bounded + throttled:
// skips files that are still fresh, retries a given file at most every
// ruRefreshThrottle, and times out each download before falling back to the
// existing local copy. Safe to call on every connect — usually returns at once.
func refreshRuleSets(defs []ruRuleSetDef) {
	ruRefreshMu.Lock()
	defer ruRefreshMu.Unlock()

	client := &http.Client{Timeout: ruDownloadTimeout}
	for _, rs := range defs {
		path := ruRuleSetLocalPath(rs.file)
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) <= ruRuleSetMaxAge {
			continue // fresh enough
		}
		if last, ok := ruLastTry[rs.file]; ok && time.Since(last) < ruRefreshThrottle {
			continue // attempted recently — don't re-stall on a blocked host
		}
		ruLastTry[rs.file] = time.Now()
		data, err := download(client, rs.url)
		if err != nil || len(data) == 0 {
			vlog("warning", "rule-set %s: refresh failed (%v) — using local copy", rs.file, err)
			continue
		}
		if err := atomicWrite(path, data); err != nil {
			continue
		}
		vlog("info", "rule-set %s: updated from upstream (%d bytes)", rs.file, len(data))
	}
}

// refreshCustomBlocklists fetches each "through VPN (URL)" source into its own
// per-URL file under binDir, with the same throttle/fallback as every other
// list. A failed fetch keeps the last good copy for that URL.
func refreshCustomBlocklists(urls []string) {
	ruRefreshMu.Lock()
	defer ruRefreshMu.Unlock()
	client := &http.Client{Timeout: ruDownloadTimeout}
	for _, url := range urls {
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}
		path := customBlocklistPathFor(url)
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) <= ruRuleSetMaxAge {
			continue
		}
		key := "custom:" + url
		if last, ok := ruLastTry[key]; ok && time.Since(last) < ruRefreshThrottle {
			continue
		}
		ruLastTry[key] = time.Now()
		data, err := download(client, url)
		if err != nil || len(data) == 0 {
			vlog("warning", "custom blocklist %s: fetch failed (%v) — using last copy", url, err)
			continue
		}
		if err := atomicWrite(path, data); err == nil {
			vlog("info", "custom blocklist: updated from %s (%d bytes)", url, len(data))
		}
	}
}

func download(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, ruDownloadMaxBytes))
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return err
	}
	return nil
}
