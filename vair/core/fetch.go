package core

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// sourceHTTPClient is the client used to fetch subscription sources. When a
// PROXY connection is live it routes the fetch THROUGH that proxy, so a source
// URL blocked on the bare link still loads once you're connected (the 1.10
// behaviour that only worked in TUN mode, where the system-wide tunnel already
// carries everything). TUN needs nothing extra here for the same reason; an idle
// or proxy-less state fetches directly.
//
// The dedicated source-fetch inbound is preferred: its traffic is PINNED to the
// proxy outbound, so the refresh rides the tunnel in every routing mode
// (through the regular port the RU modes can route a subscription host direct
// — dead on a censored network). See ownHTTPClient.
func sourceHTTPClient(timeout time.Duration) *http.Client {
	return ownHTTPClient(timeout)
}

// ─────────────────────────── config sources ─────────────────────

// SourceDef is one configured source of configurations: a URL, a local file, or
// a private GitHub repository.
type SourceDef struct {
	URL   string
	Group string
}

var sourceDefs = []SourceDef{
	{"https://raw.githubusercontent.com/lm705/vair/refs/heads/main/vless_alive.txt", ""},
	{"https://raw.githack.com/lm705/vair/main/vless_alive.txt", ""}, // fallback
}

// Built-in GitHub PAT source for the main "Sources" tab (all empty by default →
// disabled). Per-tab GitHub imports are configured in the UI and stored on each
// Tab (GitHubOwner/Repo/File/PAT); both paths share fetchGitHubPATContent.
const (
	githubOwner = ""
	githubRepo  = ""
	githubFile  = ""
	githubPAT   = ""
)

func fetchAndInit() { fetchAndInitSilent(false) }

// sourcesFetch is who asked for a SOURCES refresh, which decides what it may do
// to the list on screen.
type sourcesFetch int

const (
	// sourcesFetchAsked: RELOAD, or an empty list at start. With the spinner;
	// the results are cleared, on a failure too (see the failure branch).
	sourcesFetchAsked sourcesFetch = iota
	// sourcesFetchScheduled: the tab's own refresh interval. Silent, otherwise
	// as asked.
	sourcesFetchScheduled
	// sourcesFetchDeferred: the refresh put off at start until the first
	// session (afterSessionUp). Nobody asked for it, so it keeps what the user
	// measured and fails without a word; see refreshSourcesDeferred.
	sourcesFetchDeferred
)

// fetchAndInitSilent (re)loads the SOURCES tab. When silent (an auto-refresh
// background refresh where main already shows its persisted configs), it does
// NOT set the fetching flag or emit "loading" — so the already-visible list
// isn't blanked by a spinner while it re-downloads.
func fetchAndInitSilent(silent bool) {
	if silent {
		fetchSources(sourcesFetchScheduled)
	} else {
		fetchSources(sourcesFetchAsked)
	}
}

// refreshSourcesDeferred is the SOURCES refresh that waited for the first
// session, so that it goes through the tunnel. It is the program's own errand,
// run because a connection came up, and it used to behave like RELOAD: every
// ping and speed on the tab was cleared, AUTO (when on) tested the tab again,
// and when the list could not be fetched — a server that does not reach GitHub
// is enough — "Source unavailable" came up as well. Now:
//   - a configuration that is still in the list keeps its results; only the new
//     ones are untested, and the tab is tested again only when the list changed;
//   - a failure changes nothing and shows nothing (a line in the log): the list
//     and its results stay, and the next session tries again.
func refreshSourcesDeferred() { fetchSources(sourcesFetchDeferred) }

func fetchSources(kind sourcesFetch) {
	silent := kind != sourcesFetchAsked
	if !silent {
		// Mark the Sources tab as fetching so a switch to it shows a spinner;
		// cleared on every return path.
		state.mu.Lock()
		state.fetching["main"] = true
		state.mu.Unlock()
		defer func() {
			state.mu.Lock()
			delete(state.fetching, "main")
			state.mu.Unlock()
		}()
	}

	settingsMu.RLock()
	sourcesEnabled := appSettings.SourcesEnabled
	settingsMu.RUnlock()

	if !sourcesEnabled {
		storeReplace("main", nil) // Sources disabled → clear the store
		loadedSignal("main")
		return
	}

	// Non-silent only: tag main as loading so the spinner shows if the user
	// switches to it before the fetch finishes.
	if !silent {
		state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: "main"})
	}
	var raws []string
	var subs []subMeta

	// Fetch from sources (with fallback — only skip rest if vless configs were received)
	// failed records why a source didn't yield configs. The built-in sources are
	// a primary + fallbacks, so a failure here isn't necessarily fatal — but when
	// they ALL fail these records are the only explanation the user gets (the
	// toast and the SOURCES settings modal read them), and previously they were
	// dropped, leaving a bare "source unavailable" with no reason.
	var failed []subMeta
	prevSubs := tabSubs("main")
	for _, src := range sourceDefs {
		lines, meta, err := fetchURLMeta(src.URL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠  fetch %s: %s\n", sourceForLog(src.URL), fetchErrText(err))
			failed = append(failed, failedMeta(prevSubs, src.URL, shortErr(fetchErrText(err))))
			continue
		}
		// Check if any line actually contains a recognised proxy URL.
		hasNode := false
		for _, l := range lines {
			for _, s := range nodeSchemes {
				if strings.Contains(l, s) {
					hasNode = true
					break
				}
			}
			if hasNode {
				break
			}
		}
		if !hasNode {
			fmt.Fprintf(os.Stderr, "⚠  fetch %s: no proxy configs in response (%d lines)\n", sourceForLog(src.URL), len(lines))
			failed = append(failed, failedMeta(prevSubs, src.URL, "no configs found"))
			continue
		}
		if meta != nil { // metadata of the source we actually use (others are fallbacks)
			subs = append(subs, *meta)
		}
		raws = append(raws, lines...)
		fmt.Printf("ℹ  fetched %d lines from %s\n", len(lines), sourceForLog(src.URL))
		break
	}

	// Fetch from private GitHub repo via PAT
	ghLines, err := fetchGitHubPAT()
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠  GitHub PAT fetch: %s\n", fetchErrText(err))
	} else {
		raws = append(raws, ghLines...)
		if len(ghLines) > 0 {
			fmt.Printf("ℹ  fetched %d configs from GitHub PAT\n", len(ghLines))
		}
	}

	// If nothing fetched, keep the existing entries — but still treat this as a
	// refresh (clear the stale test results, report the failure and re-run the
	// pool test), exactly like the user-tab path in fetchTabURLs. See the comment
	// there for why: a successful fetch wipes ping/speed by rebuilding the
	// entries, so skipping that on failure made RELOAD look like a no-op and left
	// AUTO ranking by hours-old numbers.
	if len(raws) == 0 {
		// Every source failed, so `failed` holds the reasons.
		var errs []string
		for _, s := range failed {
			if s.Error != "" {
				errs = append(errs, s.Error)
			}
		}
		if kind == sourcesFetchDeferred {
			vlog("warn", "SOURCES: the refresh after connecting failed (%s) — configs and results kept, the next session tries again",
				strings.Join(errs, "; "))
			sourcesDeferred.Store(true)
			return
		}
		vlog("warn", "SOURCES: source unreachable (%s) — configs kept, test results cleared",
			strings.Join(errs, "; "))
		resetTabResultsMem("main")
		// Persist the per-source errors so the SOURCES settings modal shows them
		// (the tab keeps its configs, so these records are the only trace).
		if len(failed) > 0 {
			state.mu.Lock()
			for i := range state.tabs {
				if state.tabs[i].ID == "main" {
					state.tabs[i].Subs = failed
					break
				}
			}
			state.mu.Unlock()
			state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
		}
		loadedSignal("main") // keep existing rows; client re-fetches its window
		// AFTER loadedSignal — see the note in fetchTabURLs' matching branch.
		state.broadcast(SSEEvent{Type: "source_error", Payload: map[string]any{
			"tab": "main", "errors": errs,
		}, Tab: "main"})
		go autoPingAfterRefresh("main")
		return
	}

	// The Sources exclude filter is NOT applied here anymore — it's a VIEW filter
	// (memWindow hides matching configs on read), so every config is kept and
	// toggling the filter re-filters instantly with no re-fetch.

	// Deduplicate by body. Two configs that connect to the same server
	// (same uuid@host:port?params) are functionally identical regardless
	// of the name fragment, so we collapse them. Sources doesn't expose a
	// toggle for this — it's always on. Non-Sources tabs handle dedup as
	// a client-side view filter (see matches() in the JS) which the user
	// can toggle without losing data.
	seen := make(map[string]bool, len(raws))
	var deduped []string
	for _, r := range raws {
		if strings.TrimSpace(r) == "" {
			continue
		}
		key := dedupKey(r) // canonical identity, not the literal body
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, r)
	}

	entries := make([]*ConfigEntry, 0, len(deduped))
	for _, raw := range deduped {
		e := &ConfigEntry{Raw: raw, PingStatus: StatusPending, Delay: -1, SpeedStatus: StatusPending}
		n, parseErr := parseNode(raw)
		if parseErr != nil {
			e.Name = brokenConfigName(raw)
			// Label unparseable configs by their URL scheme so they don't fall
			// back to the UI's "vless" default (e.g. a broken trojan:// link).
			e.Protocol = internLow(schemeProtocol(raw))
			e.PingStatus = StatusFailed
			e.PingErr = parseErr.Error()
			e.SpeedStatus = StatusFailed
			e.SpeedErr = parseErr.Error()
		} else {
			e.Name = n.Name
			e.Host = n.Host
			e.Port = n.Port
			e.Network = internLow(n.Network)
			e.Security = internLow(n.Security)
			e.Protocol = internLow(string(n.Kind))
		}
		entries = append(entries, e)
	}
	// Rename duplicate display names so each config is uniquely addressable
	// in the UI (applies to every tab, including Sources).
	disambiguateNames(entries)
	for i, e := range entries {
		e.Index = i
	}
	state.mu.Lock()
	// Reconcile the Sources tab's subscription info to this fetch (clears it when
	// no source carried any).
	for i := range state.tabs {
		if state.tabs[i].ID == "main" {
			state.tabs[i].Subs = subs
			break
		}
	}
	state.mu.Unlock()
	if kind == sourcesFetchDeferred {
		keepMeasured("main", entries)
	}
	addedN, removedN, addedIdx := reloadDelta("main", entries) // before storeReplace overwrites
	storeReplace("main", entries)                              // persist to the store (outside the lock — DB write can be slow)
	loadedSignal("main")                                       // client re-fetches its window from the store
	broadcastReloadDelta("main", addedN, removedN, addedIdx)
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	// A refresh rebuilds entries with no ping data. If this tab is part of the
	// auto-connect pool, re-ping it so the supervisor can rank by real delay
	// (covers auto-refresh, manual refresh and the startup load). No-ops when
	// auto-connect is off. The deferred refresh kept the results, so the same
	// list has nothing new to test.
	if kind != sourcesFetchDeferred || addedN > 0 {
		go autoPingAfterRefresh("main")
	}
	// Big subscription parses leave Go's heap allocated — nudge the runtime
	// to release it back to the OS so memory usage doesn't appear to grow
	// after every reload. See the matching call in fetchTabURLs.
	debug.FreeOSMemory()
}

// subMeta holds optional subscription metadata: traffic quota / expiry (from the
// Subscription-Userinfo response header used by Marzban/3x-ui/Remnawave panels)
// and title / update-interval / date / count (from those response headers OR the
// leading "#"-comment lines static raw files embed, since a static file can't set
// its own HTTP headers). All fields are best-effort — anything missing stays zero.
type subMeta struct {
	Title          string   `json:"title,omitempty"`
	URL            string   `json:"url,omitempty"`   // source URL this metadata came from
	Error          string   `json:"error,omitempty"` // set by the caller when the source failed to load
	Upload         int64    `json:"upload,omitempty"`
	Download       int64    `json:"download,omitempty"`
	Total          int64    `json:"total,omitempty"`
	Expire         int64    `json:"expire,omitempty"`
	UpdateInterval string   `json:"update_interval,omitempty"`
	Updated        string   `json:"updated,omitempty"`
	Count          int      `json:"count,omitempty"`
	Info           string   `json:"info,omitempty"`
	Notes          []string `json:"notes,omitempty"` // any other leading "#"-comment lines, verbatim
}

// sourceForLog is a source's address as it may be printed: scheme and host.
// The rest of a subscription link — path and query — is usually the
// credential itself, the panel's token for this user, and what is printed to
// stderr does not stay on screen: on Linux a desktop session sends it to the
// journal.
func sourceForLog(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil || u.Host == "" {
		return "(source)"
	}
	if (u.Path == "" || u.Path == "/") && u.RawQuery == "" {
		return u.Scheme + "://" + u.Host
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// fetchErrText is a fetch error without the address in it. Go's HTTP client
// puts the whole URL into its errors — Get "https://…/sub/<token>": … — and this
// text is shown in the tab's list of sources and in its notices, which people
// copy into a bug report or a screenshot. What went wrong is the part after.
func fetchErrText(err error) string {
	var ue *neturl.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}

// failedMeta is the record of a source that did not load this time. It keeps
// what the source said about itself the last time it did — its title, traffic,
// expiry — with the error added: the configurations from that fetch stay in
// the tab, and the description above the table used to vanish while they were
// still there.
func failedMeta(prev []subMeta, url, errText string) subMeta {
	for _, p := range prev {
		if p.URL == url {
			p.Error = errText
			return p
		}
	}
	return subMeta{URL: url, Error: errText}
}

// tabSubs is a copy of a tab's current source records.
func tabSubs(tabID string) []subMeta {
	state.mu.RLock()
	defer state.mu.RUnlock()
	for _, t := range state.tabs {
		if t.ID == tabID {
			return append([]subMeta(nil), t.Subs...)
		}
	}
	return nil
}

func (m *subMeta) isEmpty() bool {
	return m.Title == "" && m.Total == 0 && m.Expire == 0 && m.Upload == 0 &&
		m.Download == 0 && m.UpdateInterval == "" && m.Updated == "" && m.Count == 0 &&
		m.Info == "" && len(m.Notes) == 0
}

// subFetchUserAgent is sent on every subscription fetch. Some panels / hosts
// reject the Go default "Go-http-client" UA with 403 as an anti-bot measure and
// only answer a "real" client. Our own "Vair/<version>" passes that and, being
// unknown to panels, gets the plain URI / base64 list rather than one of the
// YAML or JSON variants we don't parse. No host is special-cased; we don't
// impersonate another product.
const subFetchUserAgent = "Vair/" + appVersion

// fetchURLMeta fetches a subscription URL and returns both its config lines and
// any subscription metadata found in the response headers or the body's leading
// "#"-comments. meta is nil when nothing was found.
func fetchURLMeta(u string) ([]string, *subMeta, error) {
	client := sourceHTTPClient(15 * time.Second) // via the proxy when connected
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", subFetchUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// Cap the body at 64 MB (~250k configs). The old 10 MB cap silently
	// truncated very large subscriptions at ~40k configs (10 MB ÷ ~260 B/line).
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
	if err != nil {
		return nil, nil, err
	}
	text := string(body)

	// Metadata: response headers first (panels), then the leading "#"-comments
	// (static files). Parse comments from the RAW text — for a base64 body there
	// are none, and a plaintext body keeps them at the very top.
	meta := &subMeta{}
	parseSubHeaders(resp, meta)
	parseCommentMeta(text, meta)

	// Try to detect base64 subscription:
	// If the text doesn't contain "://" in the first few hundred chars, try base64 decode
	sample := text
	if len(sample) > 500 {
		sample = sample[:500]
	}
	if !strings.Contains(sample, "://") {
		// Looks like base64 — try to decode
		cleaned := strings.ReplaceAll(strings.TrimSpace(text), "\n", "")
		cleaned = strings.ReplaceAll(cleaned, "\r", "")
		if decoded, err := base64.StdEncoding.DecodeString(cleaned); err == nil {
			text = string(decoded)
		} else if decoded, err := base64.RawStdEncoding.DecodeString(cleaned); err == nil {
			text = string(decoded)
		} else if decoded, err := base64.URLEncoding.DecodeString(cleaned); err == nil {
			text = string(decoded)
		} else if decoded, err := base64.RawURLEncoding.DecodeString(cleaned); err == nil {
			text = string(decoded)
		}
		// If all decoding attempts fail, fall through and try to parse as-is
	}
	// A JSON subscription — an array of xray or sing-box configurations, as
	// some providers and v2rayNG hand out — has no link on any line.
	if links, ok := jsonConfigsInText(text); ok {
		text = strings.Join(links, "\n")
	}

	var lines []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if looksLikeNodeURL(l) {
			lines = append(lines, l)
		}
	}
	if meta.isEmpty() {
		meta = nil
	} else {
		meta.URL = u
		if meta.Count == 0 { // panels don't send a count — show how many we got
			meta.Count = len(lines)
		}
	}
	return lines, meta, nil
}

// parseSubHeaders fills meta from the subscription HTTP response headers.
func parseSubHeaders(resp *http.Response, meta *subMeta) {
	if ui := resp.Header.Get("Subscription-Userinfo"); ui != "" {
		// "upload=…; download=…; total=…; expire=…" (bytes; expire is unix seconds)
		for _, kv := range strings.Split(ui, ";") {
			kv = strings.TrimSpace(kv)
			eq := strings.IndexByte(kv, '=')
			if eq < 0 {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(kv[:eq]))
			n, _ := strconv.ParseInt(strings.TrimSpace(kv[eq+1:]), 10, 64)
			switch key {
			case "upload":
				meta.Upload = n
			case "download":
				meta.Download = n
			case "total":
				meta.Total = n
			case "expire":
				meta.Expire = n
			}
		}
	}
	if t := resp.Header.Get("Profile-Title"); t != "" && meta.Title == "" {
		meta.Title = decodeProfileTitle(t)
	}
	if iv := resp.Header.Get("Profile-Update-Interval"); iv != "" && meta.UpdateInterval == "" {
		meta.UpdateInterval = strings.TrimSpace(iv)
	}
}

// decodeProfileTitle handles the "base64:<b64>" form (and bare base64) some
// panels use for Profile-Title; falls back to the raw value.
//
// A BARE value is only decoded when the result is sane text (valid UTF-8, no
// control characters): plenty of ordinary titles are accidentally valid base64
// ("VairVPN1"), and blindly decoding those turned a readable name into binary
// noise. An explicit "base64:" prefix is always honoured.
func decodeProfileTitle(s string) string {
	s = strings.TrimSpace(s)
	tagged := strings.HasPrefix(s, "base64:")
	raw := strings.TrimSpace(strings.TrimPrefix(s, "base64:"))
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding} {
		dec, err := enc.DecodeString(raw)
		if err != nil || len(dec) == 0 {
			continue
		}
		out := strings.TrimSpace(string(dec))
		if tagged || isPlainText(out) {
			return out
		}
	}
	if tagged {
		return raw // tagged but undecodable — at least drop the marker
	}
	return s
}

// isPlainText reports whether s is valid UTF-8 without control characters —
// the guard that keeps a coincidentally-base64 title from being "decoded".
func isPlainText(s string) bool {
	if s == "" || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 && r != '\t' {
			return false
		}
		if r == 0xFFFD {
			return false // replacement char = mis-decoded bytes
		}
	}
	return true
}

// parseCommentMeta scans the leading "#"-comment block of a static subscription
// file. It recognises a few well-known fields (title / update-interval / date /
// count) across English and Russian wording, filling only what a header didn't
// already provide, and collects EVERY other comment line verbatim into Notes —
// so any description the author wrote is shown, not just hard-coded keys. Stops
// at the first config line (or after a small cap) so it never walks a huge body.
func parseCommentMeta(text string, meta *subMeta) {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if i > 100 {
			break
		}
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if !strings.HasPrefix(l, "#") {
			if looksLikeNodeURL(l) {
				break // reached the configs — no more metadata above
			}
			continue
		}
		// Strip ALL leading '#', then drop pure separator lines ("######",
		// "# ====", "# ----") which carry no description.
		body := strings.TrimSpace(strings.TrimLeft(l, "#"))
		if body == "" || strings.Trim(body, "=-*_~ \t") == "" {
			continue
		}
		key, val := "", body
		if colon := strings.IndexByte(body, ':'); colon >= 0 {
			key = strings.ToLower(strings.TrimSpace(body[:colon]))
			val = strings.TrimSpace(body[colon+1:])
		}
		consumed := false
		switch {
		case (strings.HasPrefix(key, "profile-title") || key == "title" || key == "название") && val != "" && meta.Title == "":
			// Same "base64:<b64>" form as the HTTP header (feeds put the encoded
			// title in a #profile-title comment too) — decode it, else the raw
			// "base64:8J+Gky…" string would be shown as the subscription name.
			meta.Title, consumed = decodeProfileTitle(val), true
		case strings.HasPrefix(key, "profile-update-interval") && val != "" && meta.UpdateInterval == "":
			meta.UpdateInterval, consumed = val, true
		case (strings.HasPrefix(key, "date") || strings.Contains(key, "updated") || strings.Contains(key, "обнов") || key == "дата") && val != "" && meta.Updated == "":
			meta.Updated, consumed = val, true
		case (key == "count" || strings.Contains(key, "конфиг") || strings.Contains(key, "config") ||
			strings.Contains(key, "количеств") || strings.Contains(key, "nodes") ||
			strings.Contains(key, "servers") || strings.Contains(key, "серверов")) && meta.Count == 0:
			if f := strings.Fields(val); len(f) > 0 {
				if n, err := strconv.Atoi(strings.Trim(f[0], ",.")); err == nil {
					meta.Count, consumed = n, true
				}
			}
		}
		// Anything not pulled into a structured field is a free-form description
		// (announcements, usage rules, contacts, …) — keep it so it's shown.
		if !consumed && len(meta.Notes) < 8 {
			if len(body) > 300 {
				body = body[:300]
			}
			meta.Notes = append(meta.Notes, body)
		}
	}
}

func fetchGitHubPAT() ([]string, error) {
	if githubPAT == "" || githubOwner == "" {
		return nil, nil
	}
	return fetchGitHubPATContent(githubOwner, githubRepo, githubFile, githubPAT)
}

// fetchGitHubPATContent pulls a single file from a (typically private) GitHub
// repository through the Contents API, authenticating with a personal access
// token, and returns the proxy-config lines it contains. The API returns the
// file body base64-encoded; once decoded we also run it through
// maybeDecodeBase64Blob so a base64 *subscription* committed to the repo is
// handled like any other base64 source. Used by the built-in source (above) and
// by each user tab's own GitHub import (see fetchTabURLs).
// githubContentsURL is the Contents API address of a file. Each part is
// escaped: a space, "#" or "?" in a file name cut the request off there and
// asked GitHub for something else.
func githubContentsURL(owner, repo, file string) string {
	parts := strings.Split(file, "/")
	for i, p := range parts {
		parts[i] = neturl.PathEscape(p)
	}
	return "https://api.github.com/repos/" + neturl.PathEscape(owner) + "/" + neturl.PathEscape(repo) +
		"/contents/" + strings.Join(parts, "/")
}

func fetchGitHubPATContent(owner, repo, file, pat string) ([]string, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	file = strings.TrimLeft(strings.TrimSpace(file), "/")
	pat = strings.TrimSpace(pat)
	if owner == "" || repo == "" || file == "" || pat == "" {
		return nil, fmt.Errorf("incomplete GitHub config")
	}
	apiURL := githubContentsURL(owner, repo, file)
	client := sourceHTTPClient(15 * time.Second) // via the proxy when connected
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API: %s", resp.Status)
	}
	var ghResp struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ghResp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(ghResp.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	text := maybeDecodeBase64Blob(string(decoded))
	if links, ok := jsonConfigsInText(text); ok {
		text = strings.Join(links, "\n")
	}
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if looksLikeNodeURL(l) {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// maybeDecodeBase64Blob handles a paste/body that is one base64 subscription
// blob rather than already-readable config URLs (no "://" near the start). It
// tries the four base64 variants and returns the decoded text only when it
// actually yields config URLs; otherwise the input is returned unchanged.
func maybeDecodeBase64Blob(text string) string {
	sample := text
	if len(sample) > 500 {
		sample = sample[:500]
	}
	if strings.Contains(sample, "://") {
		return text // already config URLs
	}
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		}
		return r
	}, strings.TrimSpace(text))
	if cleaned == "" {
		return text
	}
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
	} {
		if decoded, err := dec(cleaned); err == nil && strings.Contains(string(decoded), "://") {
			return string(decoded)
		}
	}
	return text
}

// brokenConfigName is what a config that did not parse is called in the table:
// the name it carries, when its link has one, and the start of the link when
// not. A JSON config the checks refused is a long run of base64 otherwise.
func brokenConfigName(raw string) string {
	if _, name := splitFragment(raw); strings.TrimSpace(name) != "" {
		return name
	}
	return raw[:minInt(40, len(raw))]
}

// parseConfigLines parses node URLs from a single in-memory text blob. Used for
// URL responses and pasted text — kept around as a convenience wrapper over
// parseConfigReader. A whole-blob base64 paste (a subscription body) is decoded
// first.
func parseConfigLines(text string) []*ConfigEntry {
	// A JSON document — an outbound, a whole xray or sing-box configuration,
	// or an array of them — becomes vair-json:// links first.
	if links, ok := jsonConfigsInText(text); ok {
		text = strings.Join(links, "\n")
	}
	return parseConfigReader(strings.NewReader(maybeDecodeBase64Blob(text)))
}

// parseConfigFile streams a file from disk a line at a time. This is the
// path that matters for big files: a 980 MB subscription dump is processed
// without ever holding more than one line in memory. The returned entries
// each own a fresh Raw string (no substring-sharing with the file content),
// so they don't pin the file's bytes alive after parsing.
func parseConfigFile(path string) ([]*ConfigEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// A JSON file — an exported xray or sing-box configuration, or an array of
	// them — is one document, not lines: it is read whole and handed to the
	// same conversion a paste gets. JSON files that size are never large.
	r := bufio.NewReader(f)
	if head, _ := r.Peek(512); isJSONStart(head) {
		data, err := io.ReadAll(io.LimitReader(r, 64<<20))
		if err != nil {
			return nil, err
		}
		return parseConfigLines(string(data)), nil
	}
	return parseConfigReader(r), nil
}

// isJSONStart reports whether a text begins, after whitespace and a byte-order
// mark, with the start of a JSON object or array.
func isJSONStart(b []byte) bool {
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	b = bytes.TrimLeft(b, " \t\r\n")
	return len(b) > 0 && (b[0] == '{' || b[0] == '[')
}

// parseConfigReader does the actual scanning. bufio.Scanner.Text() allocates
// a fresh string per line, so e.Raw doesn't share storage with anything
// upstream — that's the key fix for the "RAM doubles on every RELOAD"
// leak. The buffer cap is 4 MiB which comfortably handles the longest
// vless URLs (real ones are typically < 4 KiB).
// internLow returns a shared (static) backing string for the known
// low-cardinality transport / security / protocol values, so the thousands of
// configs that repeat "tcp" / "reality" / "vless" all point at ONE string
// instead of each carrying a freshly-parsed copy. This trims memory and, more
// importantly, the allocation churn while parsing a huge list (which is what
// inflated the heap peak). Unknown values pass through unchanged. Lock-free and
// bounded — no map, no growth, no behaviour change (the strings are equal).
func internLow(s string) string {
	switch s {
	case "tcp":
		return "tcp"
	case "ws":
		return "ws"
	case "grpc":
		return "grpc"
	case "h2":
		return "h2"
	case "http":
		return "http"
	case "httpupgrade":
		return "httpupgrade"
	case "splithttp":
		return "splithttp"
	case "xhttp":
		return "xhttp"
	case "kcp":
		return "kcp"
	case "quic":
		return "quic"
	case "tls":
		return "tls"
	case "reality":
		return "reality"
	case "xtls":
		return "xtls"
	case "none":
		return "none"
	case "vless":
		return "vless"
	case "vmess":
		return "vmess"
	case "trojan":
		return "trojan"
	case "ss":
		return "ss"
	case "ss2022":
		return "ss2022"
	case "hysteria2":
		return "hysteria2"
	case "tuic":
		return "tuic"
	}
	return s
}

func parseConfigReader(r io.Reader) []*ConfigEntry {
	var entries []*ConfigEntry
	// NOTE: deliberately no de-duplication here. Dedup is a per-tab setting
	// ("delete" removes body-dupes server-side via dedupByBody, "hide" is a
	// reversible JS view filter, "" keeps everything). Collapsing duplicate
	// lines at parse time would silently drop configs even with dedup OFF —
	// e.g. pasting 1900 lines and only 1000 surviving. Keep every line; let
	// the tab's DedupMode decide.
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// One line may hold several configs glued together without whitespace
		// (e.g. "ss://…#namevmess://…ss://…"); splitConfigURLs returns each one.
		for _, cfg := range splitConfigURLs(line) {
			e := &ConfigEntry{Raw: cfg, PingStatus: StatusPending, Delay: -1, SpeedStatus: StatusPending}
			n, parseErr := parseNode(cfg)
			if parseErr != nil {
				e.Name = brokenConfigName(cfg)
				// Even when the body is unparseable, the scheme is known — label by
				// it (e.g. a broken trojan:// link shows "trojan", not the UI's
				// "vless" fallback for an empty protocol).
				e.Protocol = internLow(schemeProtocol(cfg))
				e.PingStatus = StatusFailed
				e.PingErr = parseErr.Error()
				e.SpeedStatus = StatusFailed
				e.SpeedErr = parseErr.Error()
			} else {
				fillEntryFromNode(e, n)
			}
			entries = append(entries, e)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ parseConfigReader: %v\n", err)
	}
	for i, e := range entries {
		e.Index = i
	}
	return entries
}

// dedupByBody removes duplicates — entries with the same canonical identity
// (dedupKey) — keeping one of each, in input order. The one kept is the first
// copy the tab's exclude filter shows; only when every copy is hidden, the
// first of them. Keeping the first in input order whatever the filter said
// deleted the copy on screen whenever a hidden twin came before it: a server
// the user could see vanished, and the one kept was one they could not.
// Used by tabs in DedupMode "delete"; "hide" reaches the same set in the
// windowed read, which applies the filter before it drops duplicates.
func dedupByBody(entries []*ConfigEntry, exclude []string) []*ConfigEntry {
	if len(entries) <= 1 {
		return entries
	}
	keys := make([]string, len(entries))
	keep := make(map[string]*ConfigEntry, len(entries))
	keptHidden := make(map[string]bool, len(entries))
	for i, e := range entries {
		if e == nil || strings.TrimSpace(e.Raw) == "" {
			continue
		}
		key := dedupKey(e.Raw) // canonical identity, not the literal body
		keys[i] = key
		if _, ok := keep[key]; !ok {
			keep[key] = e
			keptHidden[key] = hiddenByExclude(e, exclude)
		} else if keptHidden[key] && !hiddenByExclude(e, exclude) {
			keep[key] = e
			keptHidden[key] = false
		}
	}
	out := make([]*ConfigEntry, 0, len(keep))
	for i, e := range entries {
		if keys[i] != "" && keep[keys[i]] == e {
			out = append(out, e)
		}
	}
	return out
}

// hiddenByExclude reports whether a tab's exclude rules hide this config — the
// test the windowed read applies (memWindow).
func hiddenByExclude(e *ConfigEntry, rules []string) bool {
	if len(rules) == 0 {
		return false
	}
	e.mu.Lock()
	name, kind, host, network, security := e.Name, e.Protocol, e.Host, e.Network, e.Security
	e.mu.Unlock()
	return shouldSkip(name, kind, host, network, security, rules)
}

// applyDeleteDedupInPlace removes body-duplicates from the named tab's
// current entries and rebroadcasts the table. Used when the user flips
// DedupMode to "delete" without changing sources — there's nothing to
// re-fetch, but we still want the deletion to happen immediately. Indices
// are recomputed; names are *not* re-disambiguated because the existing
// names were already computed against the pre-dedup index order and
// re-running would produce double-suffix names like "USA - 1 - 1".
// reloadDelta computes how many configs a reload added / removed for a tab,
// comparing the new set against what the store currently holds. Returns (0,0) on
// the first load (nothing to diff). Call BEFORE storeReplace overwrites the old
// rows. Drives the "+N −M" toast on RELOAD.
func reloadDelta(tabID string, entries []*ConfigEntry) (added, removed int, addedIdx []int) {
	if store == nil {
		return 0, 0, nil
	}
	old, err := store.tabRawSet(tabID)
	if err != nil || len(old) == 0 {
		return 0, 0, nil
	}
	newSet := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		newSet[e.Raw] = struct{}{}
	}
	// Per-entry indices of rows whose raw wasn't there before — the client flashes
	// these when they scroll into view. Capped so a huge reload doesn't bloat the
	// event; the count below stays exact regardless.
	for _, e := range entries {
		if _, isOld := old[e.Raw]; !isOld && len(addedIdx) < flashIdxCap {
			addedIdx = append(addedIdx, e.Index)
		}
	}
	for r := range newSet {
		if _, ok := old[r]; !ok {
			added++
		}
	}
	for r := range old {
		if _, ok := newSet[r]; !ok {
			removed++
		}
	}
	return added, removed, addedIdx
}

const flashIdxCap = 20000

// keepMeasured gives each freshly fetched entry the results of the entry it
// replaces — the same server, by dedupKey, so a name the list renamed does not
// lose them. Only finished results are carried: a test still running holds the
// old entry and would never finish the new one, which would stay "testing".
// Returns how many entries kept something.
func keepMeasured(tabID string, entries []*ConfigEntry) int {
	state.mu.RLock()
	old := state.tabEntries[tabID]
	state.mu.RUnlock()
	if len(old) == 0 {
		return 0
	}
	type measured struct {
		pingSt  Status
		delay   int64
		pingErr string
		speedSt Status
		mbps    float64
		speedEr string
	}
	byKey := make(map[string]measured, len(old))
	for _, e := range old {
		e.mu.Lock()
		byKey[dedupKey(e.Raw)] = measured{e.PingStatus, e.Delay, e.PingErr, e.SpeedStatus, e.SpeedMBps, e.SpeedErr}
		e.mu.Unlock()
	}
	n := 0
	for _, e := range entries {
		m, ok := byKey[dedupKey(e.Raw)]
		if !ok {
			continue
		}
		kept := false
		if m.pingSt == StatusOK || m.pingSt == StatusFailed {
			e.PingStatus, e.Delay, e.PingErr = m.pingSt, m.delay, m.pingErr
			kept = true
		}
		if m.speedSt == StatusOK || m.speedSt == StatusFailed || m.speedSt == StatusSkipped {
			e.SpeedStatus, e.SpeedMBps, e.SpeedErr = m.speedSt, m.mbps, m.speedEr
			kept = true
		}
		if kept {
			n++
		}
	}
	return n
}

// broadcastReloadDelta fires the reload_delta event when a reload changed the set.
func broadcastReloadDelta(tabID string, added, removed int, addedIdx []int) {
	if added > 0 || removed > 0 {
		state.broadcast(SSEEvent{Type: "reload_delta", Payload: map[string]interface{}{"added": added, "removed": removed, "idx": addedIdx}, Tab: tabID})
	}
}

func applyDeleteDedupInPlace(tabID string) {
	entries := loadTabEntries(tabID)
	if len(entries) == 0 {
		saveTabs()
		return
	}
	exclude := tabExcludeFilter(tabID)
	deduped := dedupByBody(entries, exclude)
	removed := len(entries) - len(deduped)
	if removed == 0 {
		saveTabs()
		return
	}
	beforeIdx := entryIndices(entries)
	for i, e := range deduped {
		e.Index = i
	}
	storeReplace(tabID, deduped) // SQLite is the source of truth now
	// Undoable like any delete from the table: on a tab of its own the removed
	// copies exist nowhere else.
	rememberDelete(tabID, entries, deduped, beforeIdx, entryIndices(deduped))
	loadedSignal(tabID)
	// Report how many duplicates were dropped (the toast, with Undo) —
	// otherwise a delete-dedup silently shrinks the list. The copies the
	// exclude filter hides are counted apart: they leave nothing on screen,
	// and counting them with the rest announced "25 removed" over a list
	// that had lost one row.
	kept := make(map[*ConfigEntry]bool, len(deduped))
	for _, e := range deduped {
		kept[e] = true
	}
	hidden := 0
	for _, e := range entries {
		if e != nil && !kept[e] && hiddenByExclude(e, exclude) {
			hidden++
		}
	}
	state.broadcast(SSEEvent{Type: "reload_delta", Payload: map[string]interface{}{"added": 0, "removed": removed - hidden, "hidden": hidden, "undo": true}, Tab: tabID})
	saveTabs()
}

// disambiguateNames walks the entries in the given order. The first
// occurrence of any name is kept verbatim; for every subsequent occurrence
// the name gets a " - N" suffix where N starts at 1 and increments. If a
// candidate suffix collides with another entry already taken, we skip
// further to find a free one — handles inputs like ["USA","USA - 1","USA"]
// where naive numbering would collide.
//
// Applied globally to every tab: this is what makes long source dumps with
// many "🇺🇸 USA" entries individually addressable in the UI. We also
// rewrite e.Raw's fragment so the disambiguated name is what ends up on
// the clipboard when the row is copied.
func disambiguateNames(entries []*ConfigEntry) {
	if len(entries) == 0 {
		return
	}
	taken := make(map[string]int, len(entries))
	for _, e := range entries {
		if e == nil {
			continue
		}
		base := e.Name
		if taken[base] == 0 {
			taken[base] = 1
			continue
		}
		// Find first free "base - N".
		for n := taken[base]; ; n++ {
			cand := fmt.Sprintf("%s - %d", base, n)
			if taken[cand] == 0 {
				e.Name = cand
				e.Raw = setNodeName(e.Raw, cand)
				taken[cand] = 1
				taken[base] = n + 1
				break
			}
		}
	}
}

// fillEntryFromNode copies the display fields off a parsed node. Shared by the
// list scanner and by the manual add, so a hand-built config is indistinguishable
// from an imported one in the table.
func fillEntryFromNode(e *ConfigEntry, n *Node) {
	e.Name = n.Name
	e.Host = n.Host
	e.Port = n.Port
	e.Network = internLow(n.Network)
	e.Security = internLow(n.Security)
	e.Protocol = internLow(string(n.Kind))
}
