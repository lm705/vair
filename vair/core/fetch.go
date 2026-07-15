package core

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// sourceHTTPClient is the client used to fetch subscription sources. When a
// PROXY connection is live it routes the fetch THROUGH that proxy, so a source
// URL blocked on the bare link still loads once you're connected (the 1.10
// behaviour that only worked in TUN mode, where the system-wide tunnel already
// carries everything). TUN needs nothing extra here for the same reason; an idle
// or proxy-less state fetches directly.
func sourceHTTPClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{}
	cs := state.conn.snap()
	if cs.Status == ConnConnected && cs.Mode == ModeProxy && cs.HTTPPort > 0 {
		if pu, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", cs.HTTPPort)); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// ─────────────────────────── config sources ─────────────────────
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

// fetchAndInitSilent (re)loads the SOURCES tab. When silent (a startup or
// auto-refresh background refresh where main already shows its persisted
// configs), it does NOT set the fetching flag or emit "loading" — so the
// already-visible list isn't blanked by a spinner while it re-downloads.
func fetchAndInitSilent(silent bool) {
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
	for _, src := range sourceDefs {
		lines, meta, err := fetchURLMeta(src.URL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠  fetch %s: %v\n", src.URL, err)
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
			fmt.Fprintf(os.Stderr, "⚠  fetch %s: no proxy configs in response (%d lines)\n", src.URL, len(lines))
			continue
		}
		if meta != nil { // metadata of the source we actually use (others are fallbacks)
			subs = append(subs, *meta)
		}
		raws = append(raws, lines...)
		fmt.Printf("ℹ  fetched %d lines from %s\n", len(lines), src.URL)
		break
	}

	// Fetch from private GitHub repo via PAT
	ghLines, err := fetchGitHubPAT()
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠  GitHub PAT fetch: %v\n", err)
	} else {
		raws = append(raws, ghLines...)
		if len(ghLines) > 0 {
			fmt.Printf("ℹ  fetched %d configs from GitHub PAT\n", len(ghLines))
		}
	}

	// If nothing fetched, keep existing entries
	if len(raws) == 0 {
		fmt.Fprintf(os.Stderr, "⚠  no configs fetched, keeping existing\n")
		loadedSignal("main") // keep existing rows; client re-fetches its window
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
		body := nodeBody(strings.TrimSpace(r))
		if body == "" || seen[body] {
			continue
		}
		seen[body] = true
		deduped = append(deduped, r)
	}

	entries := make([]*ConfigEntry, 0, len(deduped))
	for _, raw := range deduped {
		e := &ConfigEntry{Raw: raw, PingStatus: StatusPending, Delay: -1, SpeedStatus: StatusPending}
		n, parseErr := parseNode(raw)
		if parseErr != nil {
			e.Name = raw[:minInt(40, len(raw))]
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
	addedN, removedN, addedIdx := reloadDelta("main", entries) // before storeReplace overwrites
	storeReplace("main", entries)                              // persist to the store (outside the lock — DB write can be slow)
	loadedSignal("main")                                       // client re-fetches its window from the store
	broadcastReloadDelta("main", addedN, removedN, addedIdx)
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	// A refresh rebuilds entries with no ping data. If this tab is part of the
	// auto-connect pool, re-ping it so the supervisor can rank by real delay
	// (covers auto-refresh, manual refresh and the startup load). No-ops when
	// auto-connect is off.
	go autoPingAfterRefresh("main")
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

func (m *subMeta) isEmpty() bool {
	return m.Title == "" && m.Total == 0 && m.Expire == 0 && m.Upload == 0 &&
		m.Download == 0 && m.UpdateInterval == "" && m.Updated == "" && m.Count == 0 &&
		m.Info == "" && len(m.Notes) == 0
}

// fetchURL is the metadata-less entry point kept for callers that only need the
// config lines.
func fetchURL(u string) ([]string, error) {
	lines, _, err := fetchURLMeta(u)
	return lines, err
}

// subFetchUserAgent is sent on every subscription fetch. Some panels / hosts
// reject the Go default "Go-http-client" UA with 403 as an anti-bot measure and
// only answer a "real" client. Our own "Vair/<version>" passes that and, being
// unknown to panels, gets the plain URI / base64 list rather than a clash-YAML /
// sing-box-JSON variant we don't parse. No host is special-cased; we don't
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
func decodeProfileTitle(s string) string {
	s = strings.TrimSpace(s)
	raw := strings.TrimPrefix(s, "base64:")
	if dec, err := base64.StdEncoding.DecodeString(raw); err == nil && len(dec) > 0 {
		return strings.TrimSpace(string(dec))
	}
	if dec, err := base64.RawStdEncoding.DecodeString(raw); err == nil && len(dec) > 0 {
		return strings.TrimSpace(string(dec))
	}
	return s
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
			meta.Title, consumed = val, true
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
func fetchGitHubPATContent(owner, repo, file, pat string) ([]string, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	file = strings.TrimLeft(strings.TrimSpace(file), "/")
	pat = strings.TrimSpace(pat)
	if owner == "" || repo == "" || file == "" || pat == "" {
		return nil, fmt.Errorf("incomplete GitHub config")
	}
	apiURL := "https://api.github.com/repos/" + owner + "/" + repo + "/contents/" + file
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

// parseConfigLines parses node URLs from a single in-memory text blob. Used for
// URL responses and pasted text — kept around as a convenience wrapper over
// parseConfigReader. A whole-blob base64 paste (a subscription body) is decoded
// first.
func parseConfigLines(text string) []*ConfigEntry {
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
	return parseConfigReader(f), nil
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
				e.Name = cfg[:minInt(40, len(cfg))]
				// Even when the body is unparseable, the scheme is known — label by
				// it (e.g. a broken trojan:// link shows "trojan", not the UI's
				// "vless" fallback for an empty protocol).
				e.Protocol = internLow(schemeProtocol(cfg))
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
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ parseConfigReader: %v\n", err)
	}
	for i, e := range entries {
		e.Index = i
	}
	return entries
}

// dedupByBody removes entries whose node body (everything before the
// `#` fragment) already appeared at a smaller index. The first occurrence
// in input order is kept. Used by tabs in DedupMode "delete" — the
// reversible "hide" mode achieves the same visual result via a JS view
// filter in matches(), without touching the underlying entries.
func dedupByBody(entries []*ConfigEntry) []*ConfigEntry {
	if len(entries) <= 1 {
		return entries
	}
	seen := make(map[string]bool, len(entries))
	out := make([]*ConfigEntry, 0, len(entries))
	for _, e := range entries {
		if e == nil {
			continue
		}
		body := nodeBody(e.Raw)
		if body == "" || seen[body] {
			continue
		}
		seen[body] = true
		out = append(out, e)
	}
	return out
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
	deduped := dedupByBody(entries)
	removed := len(entries) - len(deduped)
	if removed == 0 {
		saveTabs()
		return
	}
	for i, e := range deduped {
		e.Index = i
	}
	storeReplace(tabID, deduped) // SQLite is the source of truth now
	loadedSignal(tabID)
	// Report how many duplicates were dropped (the "−N" toast) — otherwise a
	// delete-dedup silently shrinks the list.
	broadcastReloadDelta(tabID, 0, removed, nil)
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
