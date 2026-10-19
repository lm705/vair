package core

// Self-update — ported verbatim from the 1.10 selfupdate.go. The manifest is
// version.json in the author's repo; the download is SHA-256 verified before
// the platform replace-and-relaunch swaps the exe.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// updateManifestURLs point at a small JSON describing the latest build. Same
// raw + githack fallback pattern as the config sources.
var updateManifestURLs = []string{
	"https://raw.githubusercontent.com/lm705/vair/main/version.json",
	"https://raw.githack.com/lm705/vair/main/version.json",
}

// updateManifest is the published version.json.
//
// url/sha256 describe the Windows build and are the ORIGINAL fields: every
// released client reads them, so they can never change meaning or disappear.
// Assets carries everything else, keyed "<goos>/<goarch>[/<kind>]":
//
//	{
//	  "version": "2.3.0",
//	  "url":     "…/Vair.exe",          // windows/amd64, kept for old clients
//	  "sha256":  "…",
//	  "assets": {
//	    "linux/amd64/deb": {"url": "…/vair_2.3.0-1_amd64.deb", "sha256": "…"},
//	    "linux/amd64/rpm": {"url": "…/vair-2.3.0-1.x86_64.rpm", "sha256": "…"}
//	  }
//	}
//
// A manifest without Assets behaves exactly as before, which is why this can
// ship before anything publishes one.
type updateManifest struct {
	Version string                 `json:"version"`
	URL     string                 `json:"url"`
	SHA256  string                 `json:"sha256"`
	Notes   string                 `json:"notes,omitempty"`
	Assets  map[string]updateAsset `json:"assets,omitempty"`
}

type updateAsset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// pickAsset returns the download for THIS build, and whether one was found.
//
// The kind comes from how the app was installed (Capabilities.SelfUpdate), so a
// packaged copy is pointed at the .deb and never at a tarball it could not use.
// Falls back to the top-level url/sha256, which is the Windows build — correct
// on Windows, and on Linux it means "no asset for you", which the caller turns
// into a link rather than a download.
// kind is taken as an argument rather than read inside, so the selection can be
// tested for an install kind this machine does not have — the packaged-Linux
// case is unreachable from a test binary, which never lives in a system prefix.
func (m *updateManifest) pickAsset(kind string) (updateAsset, bool) {
	base := runtime.GOOS + "/" + runtime.GOARCH
	if kind != "" && kind != "none" && kind != "inplace" {
		if a, ok := m.Assets[base+"/"+kind]; ok && a.URL != "" {
			return a, true
		}
	}
	if a, ok := m.Assets[base]; ok && a.URL != "" {
		return a, true
	}
	if runtime.GOOS == "windows" && m.URL != "" {
		return updateAsset{URL: m.URL, SHA256: m.SHA256}, true
	}
	return updateAsset{}, false
}

// UpdateInfo is what the update check returns to the UI.
type UpdateInfo struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
	// Notify is Available AND the user hasn't picked "don't show again" for
	// this (or a newer) version — drives the startup banner.
	Notify bool   `json:"notify,omitempty"`
	Notes  string `json:"notes,omitempty"`
	Error  string `json:"error,omitempty"`
	// DownloadURL is the artifact for THIS platform, when the manifest names
	// one. Exposed (unlike url below) because the UI links to it where it
	// cannot install the update itself — a packaged Linux build sends the user
	// to the .deb rather than to a page to hunt through.
	DownloadURL string `json:"download_url,omitempty"`
	// URL/SHA are not exposed to the UI — apply re-fetches the manifest so the
	// client can't point the updater elsewhere.
	url    string
	sha256 string
}

// parseSemver turns "v1.9.0" / "1.9" into comparable parts; missing parts are 0.
func parseSemver(s string) [3]int {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "v"))
	var out [3]int
	for i, p := range strings.SplitN(s, ".", 3) {
		num := p
		for j := 0; j < len(p); j++ {
			if p[j] < '0' || p[j] > '9' {
				num = p[:j]
				break
			}
		}
		n, _ := strconv.Atoi(num)
		out[i] = n
	}
	return out
}

// semverNewer reports whether a is a strictly newer version than b.
func semverNewer(a, b string) bool {
	pa, pb := parseSemver(a), parseSemver(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

// updateHTTPClient routes update traffic through the live tunnel when we're
// connected, so the check/download works when GitHub is blocked on the bare link.
func updateHTTPClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{}
	cs := state.conn.snap()
	if cs.Status == ConnConnected && cs.Mode == ModeProxy && cs.HTTPPort > 0 {
		if pu, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", cs.HTTPPort)); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// fetchUpdateManifest pulls version.json from one URL, checks its signature,
// and decodes it.
//
// Order matters: nothing from the document is used before the signature holds.
// The manifest names the download URL and the hash it must match, so an
// unverified one decides what this program executes.
func fetchUpdateManifest(u string) (*updateManifest, error) {
	client := updateHTTPClient(15 * time.Second)
	raw, err := fetchBounded(client, u, 64*1024)
	if err != nil {
		return nil, err
	}
	sig, err := fetchBounded(client, u+".sig", 1024)
	if err != nil {
		// No signature reachable is the same outcome as a bad one: unproven.
		return nil, errUnverifiedManifest
	}
	if err := verifyManifest(raw, string(sig)); err != nil {
		vlog("warn", "update: signature check failed for %s", u)
		return nil, err
	}

	var m updateManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if m.Version == "" {
		return nil, fmt.Errorf("manifest has no version")
	}
	return &m, nil
}

// fetchBounded reads at most max bytes from u.
func fetchBounded(client *http.Client, u string, max int64) ([]byte, error) {
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, max))
}

// CheckForUpdate fetches the manifest (with fallback) and reports whether a
// newer build is available.
func CheckForUpdate() UpdateInfo {
	info := UpdateInfo{Current: appVersion}
	var man *updateManifest
	var lastErr error
	for _, u := range updateManifestURLs {
		m, err := fetchUpdateManifest(u)
		if err != nil {
			lastErr = err
			continue
		}
		man = m
		break
	}
	if man == nil {
		info.Error = "could not reach the update server"
		if errors.Is(lastErr, errUnverifiedManifest) {
			// Deliberately not phrased as tampering. A freshly published
			// manifest and its signature are cached separately by the CDN, so
			// for a few minutes after every release this is what an honest
			// update looks like. The log line above says which URL it was.
			info.Error = "update information is not available yet — try again in a few minutes"
		} else if lastErr != nil {
			info.Error = lastErr.Error()
		}
		return info
	}
	info.Latest = man.Version
	info.Notes = man.Notes
	if a, ok := man.pickAsset(platformCaps().SelfUpdate); ok {
		info.url, info.sha256 = a.URL, a.SHA256
		info.DownloadURL = a.URL
	}
	info.Available = semverNewer(man.Version, appVersion)
	if info.Available {
		settingsMu.RLock()
		dismissed := appSettings.UpdateDismissedVersion
		settingsMu.RUnlock()
		info.Notify = dismissed == "" || semverNewer(man.Version, dismissed)
	}
	return info
}

// DismissUpdateVersion records "don't show again" for the given version so the
// startup banner stays hidden until something strictly newer ships.
func DismissUpdateVersion(version string) {
	if version == "" {
		return
	}
	settingsMu.Lock()
	appSettings.UpdateDismissedVersion = version
	settingsMu.Unlock()
	saveSettings()
}

// broadcastUpdate pushes an update_status event (state ∈ checking / downloading
// / verifying / ready / error / uptodate) with an optional percent + message.
func broadcastUpdate(stateStr, msg string, pct int) {
	state.broadcast(SSEEvent{Type: "update_status", Payload: map[string]interface{}{
		"state": stateStr, "msg": msg, "pct": pct,
	}, Lossy: stateStr == "downloading"})
}

// updateRunning guards against two concurrent update attempts.
var updateRunning bool

// RunUpdate re-checks the manifest, downloads the new exe (through the tunnel
// when connected), verifies its SHA-256, and hands off to the platform
// replace-and-relaunch. Progress streams via "update_status" events. Run in a
// goroutine. The SHA check is mandatory.
func RunUpdate() {
	if updateRunning {
		return
	}
	updateRunning = true
	defer func() { updateRunning = false }()

	// Refuse before downloading anything on a platform that cannot install it.
	// Without this the Linux path downloaded a WINDOWS .exe, wrote it next to
	// the binary as <exe>.new, called a no-op replaceAndRelaunch and reported
	// success — a fake update, plus a stray file, plus a user who now believes
	// they are on the new version.
	if reason := updateApplyBlocked(); reason != "" {
		broadcastUpdate("error", reason, 0)
		return
	}

	broadcastUpdate("checking", "", 0)
	info := CheckForUpdate()
	if info.Error != "" {
		broadcastUpdate("error", info.Error, 0)
		return
	}
	if !info.Available {
		broadcastUpdate("uptodate", "", 0)
		return
	}
	if info.url == "" || info.sha256 == "" {
		broadcastUpdate("error", "update manifest is missing the download URL or checksum", 0)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		broadcastUpdate("error", err.Error(), 0)
		return
	}
	newPath := exe + ".new"

	broadcastUpdate("downloading", info.Latest, 0)
	client := updateHTTPClient(20 * time.Minute)
	resp, err := client.Get(info.url)
	if err != nil {
		broadcastUpdate("error", "download failed: "+err.Error(), 0)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		broadcastUpdate("error", fmt.Sprintf("download HTTP %d", resp.StatusCode), 0)
		return
	}
	out, err := os.Create(newPath)
	if err != nil {
		broadcastUpdate("error", err.Error(), 0)
		return
	}
	h := sha256.New()
	buf := make([]byte, 256*1024)
	total := resp.ContentLength
	var done int64
	lastPct := -1
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(newPath)
				broadcastUpdate("error", "write: "+werr.Error(), 0)
				return
			}
			h.Write(buf[:n])
			done += int64(n)
			if total > 0 {
				if pct := int(done * 100 / total); pct != lastPct {
					lastPct = pct
					broadcastUpdate("downloading", info.Latest, pct)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(newPath)
			broadcastUpdate("error", "download: "+rerr.Error(), 0)
			return
		}
	}
	out.Close()

	broadcastUpdate("verifying", info.Latest, 100)
	sum := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(sum, info.sha256) {
		os.Remove(newPath)
		broadcastUpdate("error", "checksum mismatch — download rejected", 0)
		return
	}

	broadcastUpdate("ready", info.Latest, 100)
	if err := replaceAndRelaunch(newPath); err != nil {
		os.Remove(newPath)
		broadcastUpdate("error", "could not apply update: "+err.Error(), 0)
		return
	}
	// replaceAndRelaunch exits the process; nothing runs past here on success.
}
