//go:build windows

package core

import (
	"fmt"
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

// RU-bypass sets (route Russian sites/IPs DIRECT — "Russian sites without VPN").
var ruBypassRuleSetDefs = []ruRuleSetDef{
	{"geosite-ru.srs", "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ru.srs"},
	{"geoip-ru.srs", "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-ru.srs"},
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
	ruRefreshMu  sync.Mutex
	ruLastTry    = map[string]time.Time{} // per-file last download attempt
	customBLName = "blocklist-custom.txt"
)

// ruRuleSetLocalPath returns the on-disk path of a rule-set file under binDir.
func ruRuleSetLocalPath(file string) string { return filepath.Join(binDir, file) }

// customBlocklistPath is the on-disk path of the user's custom blocklist (a plain
// domain list fetched from BlocklistURL). Empty contents / missing = none.
func customBlocklistPath() string { return filepath.Join(binDir, customBLName) }

// refreshRuRuleSets best-effort updates the RU-bypass sets. Kept for the existing
// caller (singboxRuRuleSet).
func refreshRuRuleSets() { refreshRuleSets(ruBypassRuleSetDefs) }

// refreshBlockedRuleSets best-effort updates the RU-blocked sets.
func refreshBlockedRuleSets() { refreshRuleSets(ruBlockedRuleSetDefs) }

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

// refreshCustomBlocklist fetches the user's custom blocklist URL (a plain domain
// list) into binDir, with the same throttle/fallback. No-op for an empty URL.
func refreshCustomBlocklist(url string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	ruRefreshMu.Lock()
	defer ruRefreshMu.Unlock()
	path := customBlocklistPath()
	if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) <= ruRuleSetMaxAge {
		return
	}
	key := "custom:" + url
	if last, ok := ruLastTry[key]; ok && time.Since(last) < ruRefreshThrottle {
		return
	}
	ruLastTry[key] = time.Now()
	data, err := download(&http.Client{Timeout: ruDownloadTimeout}, url)
	if err != nil || len(data) == 0 {
		vlog("warning", "custom blocklist: fetch failed (%v) — using last copy", err)
		return
	}
	if err := atomicWrite(path, data); err == nil {
		vlog("info", "custom blocklist: updated from %s (%d bytes)", url, len(data))
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
