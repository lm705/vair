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
}

type Tab struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	IsMain        bool      `json:"is_main"`
	Closable      bool      `json:"closable"`
	SourceURL     string    `json:"source_url,omitempty"`
	SourceURLs    []string  `json:"source_urls,omitempty"`
	SourceFiles   []TabFile `json:"source_files,omitempty"`
	RefreshMin    int       `json:"refresh_min,omitempty"`
	ExcludeFilter []string  `json:"exclude_filter,omitempty"`
	// DedupMode is "" (off), "hide" (client-side view filter, reversible),
	// or "delete" (server-side removal, not reversible). The Tab.Dedup
	// boolean from earlier dev builds is auto-migrated to "hide" on load.
	DedupMode string `json:"dedup_mode,omitempty"`
}

type persistedTab struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	SourceURL     string    `json:"source_url,omitempty"`
	SourceURLs    []string  `json:"source_urls,omitempty"`
	SourceFiles   []TabFile `json:"source_files,omitempty"`
	RefreshMin    int       `json:"refresh_min,omitempty"`
	ExcludeFilter []string  `json:"exclude_filter,omitempty"`
	// Two fields cover the migration: old builds wrote `dedup: true` for
	// what's now `dedup_mode: "hide"`. loadTabs picks DedupMode if set,
	// otherwise upgrades the legacy bool.
	Dedup     bool     `json:"dedup,omitempty"`
	DedupMode string   `json:"dedup_mode,omitempty"`
	Configs   []string `json:"configs,omitempty"`
}
type persistedData struct {
	Tabs []persistedTab `json:"tabs"`
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
	mu            sync.RWMutex
	entries       []*ConfigEntry // active tab entries
	tabs          []Tab
	tabEntries    map[string][]*ConfigEntry // tab_id -> entries
	cancelledTabs map[string]bool           // tabs pending cancellation
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

func tabsFilePath() string {
	return filepath.Join(tabsDir(), "tabs.json")
}

func saveTabs() {
	state.mu.RLock()
	var pd persistedData
	for _, t := range state.tabs {
		pt := persistedTab{
			ID: t.ID, Name: t.Name,
			SourceURLs: t.SourceURLs, SourceFiles: t.SourceFiles, RefreshMin: t.RefreshMin,
			ExcludeFilter: t.ExcludeFilter, DedupMode: t.DedupMode,
		}
		if len(t.SourceURLs) == 1 {
			pt.SourceURL = t.SourceURLs[0]
		}
		// Don't persist configs for main tab (fetched on startup)
		if !t.IsMain {
			if entries, ok := state.tabEntries[t.ID]; ok {
				for _, e := range entries {
					pt.Configs = append(pt.Configs, e.Raw)
				}
			}
		}
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
			SourceURLs: urls, SourceFiles: pt.SourceFiles,
			RefreshMin: pt.RefreshMin, ExcludeFilter: pt.ExcludeFilter, DedupMode: mode,
		}
		state.tabs = append(state.tabs, tab)
		entries := parseConfigLines(strings.Join(pt.Configs, "\n"))
		state.tabEntries[tab.ID] = entries
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
