package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────── types ───────────────────────────────

type Status string

const (
	StatusPending      Status = "pending"
	StatusTestingPing  Status = "testing_ping"
	StatusTestingSpeed Status = "testing_speed"
	StatusOK           Status = "ok"
	StatusFailed       Status = "failed"
	StatusSkipped      Status = "skipped"
)

type ConfigEntry struct {
	mu          sync.Mutex
	Index       int     `json:"index"`
	Raw         string  `json:"raw"`
	Name        string  `json:"name"`
	Host        string  `json:"host"`
	Port        int     `json:"port"`
	Network     string  `json:"network"`
	Security    string  `json:"security"`
	Protocol    string  `json:"protocol,omitempty"`
	PingStatus  Status  `json:"ping_status"`
	Delay       int64   `json:"delay"`
	PingErr     string  `json:"ping_err,omitempty"`
	SpeedStatus Status  `json:"speed_status"`
	SpeedMBps   float64 `json:"speed_mbps"`
	SpeedErr    string  `json:"speed_err,omitempty"`
	SpeedLive   float64 `json:"speed_live"`
}

func (e *ConfigEntry) snap() ConfigEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	return *e
}

// ─────────────────────────── connection types ────────────────────

type ConnMode string
type ConnStatus string

const (
	ModeProxy ConnMode = "proxy"
	ModeTUN   ConnMode = "tun"
)
const (
	ConnIdle          ConnStatus = "idle"
	ConnConnecting    ConnStatus = "connecting"
	ConnConnected     ConnStatus = "connected"
	ConnDisconnecting ConnStatus = "disconnecting"
	ConnError         ConnStatus = "error"
)

type ConnState struct {
	Status     ConnStatus `json:"status"`
	Mode       ConnMode   `json:"mode"`
	EntryIndex int        `json:"entry_index"`
	EntryName  string     `json:"entry_name"`
	ConnTab    string     `json:"conn_tab"`
	ConnRaw    string     `json:"conn_raw,omitempty"` // raw vless:// URL for matching after reload
	HTTPPort   int        `json:"http_port,omitempty"`
	SOCKSPort  int        `json:"socks_port,omitempty"`
	TUNIface   string     `json:"tun_iface,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	ErrMsg     string     `json:"error,omitempty"`
	UptimeSec  int64      `json:"uptime_sec"`
	// StatsUnavailable is true for the pure-sing-box TUN path (Hysteria2/
	// TUIC): there's no local SOCKS hop to instrument, so session/lifetime
	// traffic counters don't move. The UI shows a small note instead.
	StatsUnavailable bool `json:"stats_unavailable,omitempty"`
	// Chain is the ordered hop labels when the active connection is a multi-hop
	// chain (e.g. ["🇺🇸 USA","🇳🇱 NL"]). Empty for a normal single-node
	// connection. The conn-bar shows them as "A → B".
	Chain []string `json:"chain,omitempty"`
	// ChainRaws is the ordered hop raw URLs, parallel to Chain. Used by the UI to
	// highlight every hop row (matched by raw, since names can collide) and to
	// put the disconnect button on the exit hop only. Last element = exit.
	ChainRaws []string `json:"chain_raws,omitempty"`
}

type connManager struct {
	// actionMu serialises an entire connect / disconnect / failover
	// sequence. cm.mu (below) is only a short-lived field guard —
	// stopConnectionLocked releases it during the multi-second process
	// kill, so cm.mu alone does NOT prevent two connect flows from
	// double-spawning engines. Every full sequence (user connect, user
	// disconnect, supervisor failover) holds actionMu start-to-finish.
	// The auto-supervisor takes it via TryLock so a user action always
	// wins and the supervisor simply skips that tick.
	actionMu   sync.Mutex
	mu         sync.Mutex
	state      ConnState
	cmd        *exec.Cmd // main process (xray for proxy, sing-box for TUN)
	xrayCmd    *exec.Cmd // secondary xray process (hybrid TUN mode only)
	cancel     context.CancelFunc
	xrayCancel context.CancelFunc
	tmpCfg     string
	xrayTmpCfg string
	// Per-session traffic counter. Allocated fresh on every connect so
	// the byte tallies don't bleed across sessions. nil while idle.
	counter *trafficCounter
	// Cancels the byte-counting forwarders (1 in TUN mode, 2 in proxy
	// mode for the HTTP + SOCKS inbounds). Set during connect, called
	// during disconnect to close the listeners.
	fwdCancel context.CancelFunc
}

func newConnManager() *connManager {
	return &connManager{state: ConnState{Status: ConnIdle, EntryIndex: -1}}
}

func (cm *connManager) snap() ConnState {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	s := cm.state
	if s.Status == ConnConnected && !s.StartedAt.IsZero() {
		s.UptimeSec = int64(time.Since(s.StartedAt).Seconds())
	}
	return s
}

// ─────────────────────────── tabs ───────────────────────────────

type TabFile struct {
	Name string `json:"name"`
	// Path is the on-disk location of the file. We store only the path —
	// the content is read fresh on every fetch, so there's no in-memory
	// duplication and the file size becomes a non-issue. The trade-off:
	// if the file is moved or deleted between sessions, its entries are
	// dropped from the tab until the file returns (same behaviour URLs
	// already have when fetch fails).
	Path  string `json:"path"`
	Size  int64  `json:"size,omitempty"`  // last-known size in bytes, informational
	Mtime int64  `json:"mtime,omitempty"` // last-known mtime (unix seconds), informational
	// Disabled skips this file on fetch while keeping it in the tab's list (the
	// same on/off behaviour URL sources have via SourceDisabled).
	Disabled bool `json:"disabled,omitempty"`
}

type Tab struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	IsMain     bool     `json:"is_main"`
	Closable   bool     `json:"closable"`
	SourceURL  string   `json:"source_url,omitempty"`
	SourceURLs []string `json:"source_urls,omitempty"`
	// SourceDisabled lists URL sources the user switched OFF in tab settings.
	// They stay in SourceURLs (so the row and its subscription info persist) but
	// are skipped on fetch. Matched by exact URL string.
	SourceDisabled []string  `json:"source_disabled,omitempty"`
	SourceFiles    []TabFile `json:"source_files,omitempty"`
	RefreshMin     int       `json:"refresh_min,omitempty"`
	ExcludeFilter  []string  `json:"exclude_filter,omitempty"`
	// ExcludeDisabled / RefreshDisabled turn OFF the exclude filter / auto-refresh
	// for this tab WITHOUT clearing their values (the filter rules and refresh
	// interval persist but aren't applied while off). Inverted bool so the JSON
	// zero value (omitted) means "enabled": both default ON for every tab.
	ExcludeDisabled bool `json:"exclude_disabled,omitempty"`
	RefreshDisabled bool `json:"refresh_disabled,omitempty"`
	// DedupMode is "" (off), "hide" (client-side view filter, reversible),
	// or "delete" (server-side removal, not reversible). The Tab.Dedup
	// boolean from earlier dev builds is auto-migrated to "hide" on load.
	DedupMode string `json:"dedup_mode,omitempty"`
	// AutoRefreshTest runs a background test of the tab's configs after a
	// SCHEDULED auto-refresh (never on a manual RELOAD): "" (off), "ping"
	// (ping only), or "speed" (full ping→speed). Independent of AUTO mode.
	AutoRefreshTest string `json:"auto_refresh_test,omitempty"`
	// Subs holds subscription metadata (title, traffic quota, expiry, …) parsed on
	// the last fetch from the response headers / "#"-comments — one entry per
	// source URL that carried any. Empty when none. Persisted so it shows
	// immediately on startup before the next re-fetch.
	Subs []subMeta `json:"subs,omitempty"`
	// GitHub private-repo import (per-tab). When GitHubEnabled and owner/repo/
	// file/PAT are all set, the tab additionally pulls a config file from a
	// private GitHub repository via the Contents API on every fetch/refresh,
	// appended after URL + file sources. The PAT is stored in plaintext in
	// tabs.json (same trust level as the configs themselves).
	GitHubEnabled bool   `json:"github_enabled,omitempty"`
	GitHubOwner   string `json:"github_owner,omitempty"`
	GitHubRepo    string `json:"github_repo,omitempty"`
	GitHubFile    string `json:"github_file,omitempty"`
	GitHubPAT     string `json:"github_pat,omitempty"`
}

// gitHubReady reports whether a tab's GitHub import is enabled and fully
// configured (owner, repo, file path and PAT all present).
func (t *Tab) gitHubReady() bool {
	return t.GitHubEnabled && t.GitHubOwner != "" && t.GitHubRepo != "" &&
		t.GitHubFile != "" && t.GitHubPAT != ""
}

type persistedTab struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	SourceURL       string    `json:"source_url,omitempty"`
	SourceURLs      []string  `json:"source_urls,omitempty"`
	SourceDisabled  []string  `json:"source_disabled,omitempty"`
	SourceFiles     []TabFile `json:"source_files,omitempty"`
	RefreshMin      int       `json:"refresh_min,omitempty"`
	ExcludeFilter   []string  `json:"exclude_filter,omitempty"`
	ExcludeDisabled bool      `json:"exclude_disabled,omitempty"`
	RefreshDisabled bool      `json:"refresh_disabled,omitempty"`
	// Two fields cover the migration: old builds wrote `dedup: true` for
	// what's now `dedup_mode: "hide"`. loadTabs picks DedupMode if set,
	// otherwise upgrades the legacy bool.
	Dedup           bool      `json:"dedup,omitempty"`
	DedupMode       string    `json:"dedup_mode,omitempty"`
	AutoRefreshTest string    `json:"auto_refresh_test,omitempty"`
	Subs            []subMeta `json:"subs,omitempty"`
	Sub             *subMeta  `json:"sub,omitempty"` // legacy single-sub form; migrated on load
	Configs         []string  `json:"configs,omitempty"`
	// GitHub private-repo import (see Tab).
	GitHubEnabled bool   `json:"github_enabled,omitempty"`
	GitHubOwner   string `json:"github_owner,omitempty"`
	GitHubRepo    string `json:"github_repo,omitempty"`
	GitHubFile    string `json:"github_file,omitempty"`
	GitHubPAT     string `json:"github_pat,omitempty"`
}
type persistedData struct {
	Tabs []persistedTab `json:"tabs"`
}

// strInSlice reports whether s is present in list (exact match).
func strInSlice(s string, list []string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// subsOf returns a persisted tab's subscription metadata, migrating the legacy
// single `sub` field to the new `subs` list when an old tabs.json is loaded.
func (pt persistedTab) subsOf() []subMeta {
	if len(pt.Subs) > 0 {
		return pt.Subs
	}
	if pt.Sub != nil {
		return []subMeta{*pt.Sub}
	}
	return nil
}

// ─────────────────────────── SSE / state ─────────────────────────

type SSEEvent struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
	Tab     string      `json:"tab,omitempty"`
	// Lossy marks an event as safe to drop if the receiver's channel is
	// full. Use this for high-frequency intermediate events (live-progress
	// callbacks, in-flight byte counters, bulk progress ticks) where the
	// *next* event supersedes this one anyway. Terminal events (entry's
	// final OK/Failed status, conn state transitions, bulk_*_done) leave
	// this false so broadcast() blocks long enough to deliver them even
	// under heavy event flow. Field is JSON-omitted — only the server
	// inspects it. See broadcast() for the drop/block policy.
	Lossy bool `json:"-"`
}

type AppState struct {
	mu sync.RWMutex
	// In-memory working copy of all configs: reads (window/sort/filter,
	// index handlers, tests, AUTO) are served from here for speed; SQLite is the
	// durable backing store (loaded into memory at startup, written through on
	// changes). The UI still windows, so WebView2 stays light. tabEntries is keyed
	// by tab_id and ordered by entry Index; entries is the active tab's slice.
	tabEntries    map[string][]*ConfigEntry
	entries       []*ConfigEntry
	tabs          []Tab
	cancelledTabs map[string]bool // tabs pending cancellation
	fetching      map[string]bool // tab_id -> a reload/fetch is in flight
	activeTab     string
	xrayBin       string
	singboxBin    string
	clients       map[chan SSEEvent]struct{}
	clientMu      sync.Mutex
	pingRunning   int32
	speedRunning  int32
	conn          *connManager
}

var state = &AppState{
	clients:       make(map[chan SSEEvent]struct{}),
	conn:          newConnManager(),
	tabEntries:    make(map[string][]*ConfigEntry),
	cancelledTabs: make(map[string]bool),
	fetching:      make(map[string]bool),
	activeTab:     "main",
	tabs:          []Tab{{ID: "main", Name: "Sources", IsMain: true, Closable: false}},
}

// ─────────────────────────── tab persistence ────────────────────

func tabsDir() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "vair")
	}
	if d := os.Getenv("APPDATA"); d != "" {
		return filepath.Join(d, "vair")
	}
	return "."
}

// The data dir is organized into subfolders: data/ (durable user data — db, tabs,
// settings), runtime/ (transient engine state regenerated each run), bin/
// (engine binaries). dataPath also falls back to the legacy flat layout so an
// upgrade never loses a file that hasn't been moved yet (see migrateDataLayout).
func dataDirPath() string { d := filepath.Join(tabsDir(), "data"); os.MkdirAll(d, 0755); return d }
func runtimeDirPath() string {
	d := filepath.Join(tabsDir(), "runtime")
	os.MkdirAll(d, 0755)
	return d
}

// dataPath returns the path of a durable data file, preferring data/ but using
// the legacy root location if the file still lives there (migration not yet run).
func dataPath(name string) string {
	np := filepath.Join(dataDirPath(), name)
	if _, err := os.Stat(np); err == nil {
		return np
	}
	if old := filepath.Join(tabsDir(), name); fileExists(old) {
		return old
	}
	return np
}

// runtimePath returns the path of a transient runtime file under runtime/.
func runtimePath(name string) string { return filepath.Join(runtimeDirPath(), name) }

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// migrateDataLayout moves files from the legacy flat %LOCALAPPDATA%\vair layout
// into data/ and runtime/ subfolders, once. Called at startup BEFORE anything
// opens them. Safe on upgrade: each move only happens if the source exists and
// the destination doesn't (rename = atomic on the same volume); a failure leaves
// the file in place and dataPath()'s fallback still finds it.
func migrateDataLayout() {
	root := tabsDir()
	dataDirPath()    // ensure dirs exist
	runtimeDirPath() //
	move := func(name, sub string) {
		old := filepath.Join(root, name)
		if !fileExists(old) {
			return
		}
		dst := filepath.Join(root, sub, name)
		if fileExists(dst) {
			return // already migrated; leave the stray legacy file alone
		}
		if err := os.Rename(old, dst); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ migrate %s → %s: %v\n", name, sub, err)
		}
	}
	for _, n := range []string{"configs.db", "tabs.json", "settings.json"} {
		move(n, "data")
	}
	for _, n := range []string{"proxy.active", "last-singbox.log",
		"last-singbox-proxy.json", "last-singbox-hybrid.json", "last-singbox-tun.json", "last-xray-hybrid.json"} {
		move(n, "runtime")
	}
}

func tabsFilePath() string {
	return dataPath("tabs.json")
}

func saveTabs() {
	state.mu.RLock()
	var pd persistedData
	for _, t := range state.tabs {
		pt := persistedTab{
			ID: t.ID, Name: t.Name,
			SourceURLs: t.SourceURLs, SourceDisabled: t.SourceDisabled,
			SourceFiles: t.SourceFiles, RefreshMin: t.RefreshMin,
			ExcludeFilter: t.ExcludeFilter, ExcludeDisabled: t.ExcludeDisabled,
			RefreshDisabled: t.RefreshDisabled, DedupMode: t.DedupMode,
			AutoRefreshTest: t.AutoRefreshTest, Subs: t.Subs,
			GitHubEnabled: t.GitHubEnabled, GitHubOwner: t.GitHubOwner,
			GitHubRepo: t.GitHubRepo, GitHubFile: t.GitHubFile, GitHubPAT: t.GitHubPAT,
		}
		if len(t.SourceURLs) == 1 {
			pt.SourceURL = t.SourceURLs[0]
		}
		// Configs are NOT written here anymore — they live in the SQLite store
		// (configs.db), which is the persistence. tabs.json holds only metadata.
		// (Export still snapshots configs from the store; see buildSettingsExport.)
		pd.Tabs = append(pd.Tabs, pt)
	}
	state.mu.RUnlock()
	dir := tabsDir()
	os.MkdirAll(dir, 0755)
	data, err := json.MarshalIndent(pd, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ saveTabs: %v\n", err)
		return
	}
	if err := os.WriteFile(tabsFilePath(), data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ saveTabs write: %v\n", err)
	}
}

func loadTabs() {
	data, err := os.ReadFile(tabsFilePath())
	if err != nil {
		return
	}
	var pd persistedData
	if err := json.Unmarshal(data, &pd); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ loadTabs: %v\n", err)
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, pt := range pd.Tabs {
		// Main tab: update settings only (ExcludeFilter, RefreshMin)
		if pt.ID == "main" {
			for i, t := range state.tabs {
				if t.ID == "main" {
					state.tabs[i].ExcludeFilter = pt.ExcludeFilter
					state.tabs[i].RefreshMin = pt.RefreshMin
					state.tabs[i].ExcludeDisabled = pt.ExcludeDisabled
					state.tabs[i].RefreshDisabled = pt.RefreshDisabled
					state.tabs[i].AutoRefreshTest = pt.AutoRefreshTest
					state.tabs[i].Subs = pt.subsOf()
					break
				}
			}
			continue
		}
		urls := pt.SourceURLs
		if len(urls) == 0 && pt.SourceURL != "" {
			urls = []string{pt.SourceURL}
		}
		// Upgrade pre-3-state dev builds: `dedup: true` → DedupMode "hide".
		// The new explicit DedupMode wins if present.
		mode := pt.DedupMode
		if mode == "" && pt.Dedup {
			mode = "hide"
		}
		tab := Tab{
			ID: pt.ID, Name: pt.Name, IsMain: false, Closable: true,
			SourceURLs: urls, SourceDisabled: pt.SourceDisabled, SourceFiles: pt.SourceFiles,
			RefreshMin: pt.RefreshMin, ExcludeFilter: pt.ExcludeFilter,
			ExcludeDisabled: pt.ExcludeDisabled, RefreshDisabled: pt.RefreshDisabled,
			DedupMode:       mode,
			AutoRefreshTest: pt.AutoRefreshTest, Subs: pt.subsOf(),
			GitHubEnabled: pt.GitHubEnabled, GitHubOwner: pt.GitHubOwner,
			GitHubRepo: pt.GitHubRepo, GitHubFile: pt.GitHubFile, GitHubPAT: pt.GitHubPAT,
		}
		state.tabs = append(state.tabs, tab)
		// One-time migration: older tabs.json embedded the configs (pt.Configs).
		// Seed the store from them ONLY if the store has none for this tab. Write
		// straight to SQLite (NOT storeReplace — we already hold state.mu, and
		// storeReplace would re-take it → deadlock); loadConfigsIntoMemory() loads
		// it into memory right after loadTabs returns.
		if len(pt.Configs) > 0 && store != nil {
			if n, _ := store.tabCount(tab.ID); n == 0 {
				store.replaceTabConfigs(tab.ID, parseConfigLines(strings.Join(pt.Configs, "\n")))
			}
		}
	}
}

func nextTabNumber() int {
	state.mu.RLock()
	defer state.mu.RUnlock()
	used := make(map[int]bool)
	for _, t := range state.tabs {
		// Support both old "custom-N" and new "tab-N-timestamp" formats
		name := t.ID
		if strings.HasPrefix(name, "custom-") {
			name = strings.TrimPrefix(name, "custom-")
		} else if strings.HasPrefix(name, "tab-") {
			name = strings.TrimPrefix(name, "tab-")
			if idx := strings.Index(name, "-"); idx > 0 {
				name = name[:idx] // "2-1713456789" → "2"
			}
		} else {
			continue
		}
		if n, err := strconv.Atoi(name); err == nil {
			used[n] = true
		}
	}
	for i := 1; ; i++ {
		if !used[i] {
			return i
		}
	}
}

// broadcast fans an SSE event out to every connected client.
//
// Two delivery tiers — driven by ev.Lossy:
//
//   - Lossy (live-progress, in-flight stats, bulk-progress ticks): send
//     non-blocking. If the receiver's 1024-slot buffer is full, drop this
//     event. A later event will supersede it anyway, so loss is harmless.
//
//   - Reliable (terminal entry updates, conn state, bulk_*_done, tabs):
//     block up to 2 seconds. These represent a state TRANSITION the client
//     must observe — dropping them leaves rows stuck on stale status (the
//     classic "connecting…" pill that only disappears after RELOAD).
//
// The client list is snapshotted under the lock and the actual sends happen
// OUTSIDE the lock. Otherwise a single slow client would serialize every
// broadcast through clientMu, and the 2-second reliable budget would
// multiply by the client count for unrelated events on other clients.
func (s *AppState) broadcast(ev SSEEvent) {
	s.clientMu.Lock()
	clients := make([]chan SSEEvent, 0, len(s.clients))
	for ch := range s.clients {
		clients = append(clients, ch)
	}
	s.clientMu.Unlock()

	if ev.Lossy {
		for _, ch := range clients {
			select {
			case ch <- ev:
			default:
				// buffer full — drop. The next live-progress / counter /
				// bulk-tick event will replace this one's stale value, so
				// the client converges on the right state on its own.
			}
		}
		return
	}

	for _, ch := range clients {
		select {
		case ch <- ev:
		case <-time.After(2 * time.Second):
			// Two seconds is enough that a momentary GC/IO pause on the
			// SSE writer can't drop a terminal event, and short enough
			// that a truly dead client doesn't pin a sender forever.
			// If we do drop here, the row may show "stale" until the
			// next reload — but at this point the client is unreachable
			// anyway and reload is the right recovery.
		}
	}
}
func (s *AppState) addClient(ch chan SSEEvent) {
	s.clientMu.Lock()
	s.clients[ch] = struct{}{}
	s.clientMu.Unlock()
}
func (s *AppState) removeClient(ch chan SSEEvent) {
	s.clientMu.Lock()
	delete(s.clients, ch)
	s.clientMu.Unlock()
}

// ─────────────────────────── admin check ─────────────────────────

func checkAdmin() bool {
	f, err := os.Open(`\\.\PHYSICALDRIVE0`)
	if err == nil {
		f.Close()
		return true
	}
	return false
}
