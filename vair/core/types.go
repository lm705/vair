package core

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
	// SrcFetchPort is the engine's dedicated source-fetch inbound (proxy mode):
	// subscription refreshes connect here and are pinned to the proxy outbound,
	// so they always go through the tunnel regardless of the routing mode.
	// Internal — not exposed to the frontend.
	SrcFetchPort int       `json:"-"`
	TUNIface     string    `json:"tun_iface,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	ErrMsg       string    `json:"error,omitempty"`
	UptimeSec    int64     `json:"uptime_sec"`
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
	pingRunning   int32
	speedRunning  int32
	conn          *connManager
}

var state = &AppState{
	conn:          newConnManager(),
	tabEntries:    make(map[string][]*ConfigEntry),
	cancelledTabs: make(map[string]bool),
	fetching:      make(map[string]bool),
	activeTab:     "main",
	tabs:          []Tab{{ID: "main", Name: "Sources", IsMain: true, Closable: false}},
}

// ─────────────────────────── tab persistence ────────────────────

// Path helpers (tabsDir/dataDirPath/runtimeDirPath/dataPath/runtimePath/
// fileExists) now live in paths.go — pointed at the vair2 data dir for 2.0.

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
	// Atomic write: a kill mid-write must never leave a truncated tabs.json (that
	// would drop the user's tabs on the next load). Write a temp file, fsync, then
	// rename over the target — the rename is atomic on the same filesystem.
	tmp := tabsFilePath() + ".tmp"
	if f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644); err == nil {
		_, werr := f.Write(data)
		f.Sync()
		f.Close()
		if werr == nil {
			if err := os.Rename(tmp, tabsFilePath()); err != nil {
				fmt.Fprintf(os.Stderr, "⚠ saveTabs rename: %v\n", err)
			}
			return
		}
	}
	// Fallback (rename unsupported / temp failed): best-effort direct write.
	if err := os.WriteFile(tabsFilePath(), data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ saveTabs write: %v\n", err)
	}
}

// loadTabs reads tabs.json into state.tabs. Returns false when the file exists
// but couldn't be parsed — the caller MUST NOT then re-save (that would clobber
// the user's tabs with the default [main]). A missing file returns true (a
// fresh install; saving the default is correct).
func loadTabs() (ok bool) {
	data, err := os.ReadFile(tabsFilePath())
	if err != nil {
		return true // no file yet → first run; default [main] is fine to persist
	}
	var pd persistedData
	if err := json.Unmarshal(data, &pd); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ loadTabs (keeping file, not overwriting): %v\n", err)
		return false
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
	return true
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

// broadcast delivers a domain event to the frontend. SSE (the 1.10 transport,
// with its per-client Lossy / 2-second reliable tiers) was replaced by Wails
// events in 2.0: the runtime handles fan-out, so this just emits. The frontend
// listens via Events.On(ev.Type, …) and reads e.data.payload.
func (s *AppState) broadcast(ev SSEEvent) {
	// A config's ping/speed result changed → bump the sort-order cache version so
	// a ping/speed-sorted view re-sorts on its next read (see memSig). Cheap
	// atomic; the re-sort itself only happens when the frontend re-fetches.
	if ev.Type == "entry_update" {
		bumpResultVer()
	}
	Events.Emit(ev.Type, ev)
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
