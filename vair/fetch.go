package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

// ─────────────────────────── config sources ─────────────────────
type SourceDef struct {
	URL   string
	Group string
}

var sourceDefs = []SourceDef{
	{"https://raw.githubusercontent.com/lm705/vair/refs/heads/main/vless_alive.txt", ""},
	{"https://raw.githack.com/lm705/vair/main/vless_alive.txt", ""}, // fallback
}

const (
	githubOwner  = ""
	githubRepo   = ""
	githubFile   = ""
	githubPAT    = ""
	githubAPIURL = "https://api.github.com/repos/" + githubOwner + "/" + githubRepo + "/contents/" + githubFile
)

func fetchAndInit() {
	settingsMu.RLock()
	sourcesEnabled := appSettings.SourcesEnabled
	settingsMu.RUnlock()

	if !sourcesEnabled {
		state.mu.Lock()
		state.tabEntries["main"] = nil
		if state.activeTab == "main" {
			state.entries = nil
		}
		state.mu.Unlock()
		if state.activeTab == "main" {
			state.broadcast(SSEEvent{Type: "loaded", Payload: []ConfigEntry{}})
		}
		return
	}

	if state.activeTab == "main" {
		state.broadcast(SSEEvent{Type: "loading", Payload: nil})
	}
	var raws []string

	// Fetch from sources (with fallback — only skip rest if vless configs were received)
	for _, src := range sourceDefs {
		lines, err := fetchURL(src.URL)
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
		state.mu.RLock()
		cur := state.tabEntries["main"]
		state.mu.RUnlock()
		if state.activeTab == "main" && cur != nil {
			snaps := make([]ConfigEntry, len(cur))
			for i, e := range cur {
				snaps[i] = e.snap()
			}
			state.broadcast(SSEEvent{Type: "loaded", Payload: snaps})
		}
		return
	}

	// Get main tab's exclude filter (skipped when the filter is toggled off —
	// the rules persist but aren't applied).
	state.mu.RLock()
	var excludeFilter []string
	for _, t := range state.tabs {
		if t.IsMain {
			if !t.ExcludeDisabled {
				excludeFilter = t.ExcludeFilter
			}
			break
		}
	}
	state.mu.RUnlock()

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
			e.PingStatus = StatusFailed
			e.PingErr = parseErr.Error()
			e.SpeedStatus = StatusFailed
			e.SpeedErr = parseErr.Error()
		} else if shouldSkip(n.Name, string(n.Kind), n.Host, n.Network, n.Security, excludeFilter) {
			continue
		} else {
			e.Name = n.Name
			e.Host = n.Host
			e.Port = n.Port
			e.Network = n.Network
			e.Security = n.Security
			e.Protocol = string(n.Kind)
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
	state.tabEntries["main"] = entries
	// Only update active entries if main tab is active
	if state.activeTab == "main" {
		state.entries = entries
	}
	state.mu.Unlock()
	// Only broadcast loaded data if main tab is active
	if state.activeTab == "main" {
		snaps := make([]ConfigEntry, len(entries))
		for i, e := range entries {
			snaps[i] = e.snap()
		}
		state.broadcast(SSEEvent{Type: "loaded", Payload: snaps})
	}
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

func fetchURL(u string) ([]string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, err
	}
	text := string(body)

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
	return lines, nil
}

func fetchGitHubPAT() ([]string, error) {
	if githubPAT == "" || githubOwner == "" {
		return nil, nil
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", githubAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+githubPAT)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
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
	var lines []string
	for _, l := range strings.Split(string(decoded), "\n") {
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
				e.PingStatus = StatusFailed
				e.PingErr = parseErr.Error()
				e.SpeedStatus = StatusFailed
				e.SpeedErr = parseErr.Error()
			} else {
				e.Name = n.Name
				e.Host = n.Host
				e.Port = n.Port
				e.Network = n.Network
				e.Security = n.Security
				e.Protocol = string(n.Kind)
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
func applyDeleteDedupInPlace(tabID string) {
	state.mu.Lock()
	entries := state.tabEntries[tabID]
	if len(entries) == 0 {
		state.mu.Unlock()
		saveTabs()
		return
	}
	deduped := dedupByBody(entries)
	if len(deduped) == len(entries) {
		state.mu.Unlock()
		saveTabs()
		return
	}
	for i, e := range deduped {
		e.Index = i
	}
	state.tabEntries[tabID] = deduped
	if state.activeTab == tabID {
		state.entries = deduped
	}
	state.mu.Unlock()
	if state.activeTab == tabID {
		snaps := make([]ConfigEntry, len(deduped))
		for i, e := range deduped {
			snaps[i] = e.snap()
		}
		state.broadcast(SSEEvent{Type: "loaded", Payload: snaps, Tab: tabID})
	}
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
