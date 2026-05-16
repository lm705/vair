package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ─────────────────────────── constants ───────────────────────────

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

const (
	pingTestURLDefault   = "https://www.gstatic.com/generate_204"
	pingTimeout   = 1500 * time.Millisecond
	warmupTimeout = 2 * time.Second
	pingRounds    = 3

	speedTestURLDefault   = "https://speed.cloudflare.com/__down?bytes=10000000"
	speedDuration  = 4 * time.Second
	speedUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

	startupTimeout = 4 * time.Second
	dialTimeout    = 5 * time.Second

	// xrayStartupTimeout: time to wait for xray HTTP port to open during ping/speed test.
	// Increased for Windows Defender scan delay on first run of extracted binary.
	xrayStartupTimeout = 8 * time.Second

	// xrayConnTimeout: time to wait for xray to start in persistent proxy connection mode.
	xrayConnTimeout = 12 * time.Second

	// tunStartupTimeout: time to wait for sing-box TUN adapter to come up.
	tunStartupTimeout = 3 * time.Second

	// Default ports for persistent proxy connection
	connHTTPPort  = 10819
	connSOCKSPort = 10818

	webPort = 19876
)

// ─────────────────────────── proxy auth ──────────────────────────
// Random credentials generated once per program launch.
// Protects SOCKS5 inbound from abuse by malicious apps on the same machine.
// See: https://habr.com/ru/articles/1020080/
var (
	proxyAuthUser string
	proxyAuthPass string
)

func init() {
	proxyAuthUser = randomHex(16)
	proxyAuthPass = randomHex(32)
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// ─────────────────────────── settings ──────────────────────────────

type AppSettings struct {
	SourcesEnabled     bool     `json:"sources_enabled"`
	RuSitesDirect      bool     `json:"ru_sites_direct"`
	DirectDomains      []string `json:"direct_domains"`
	DirectApps         []string `json:"direct_apps"`
	TrayEnabled        bool     `json:"tray_enabled"`
	// Per-user concurrency overrides for bulk tests. Zero / unset falls back
	// to the defaults below. Capped at sane upper bounds inside the
	// accessors so a fat-fingered "9999" doesn't melt the local network.
	PingConcurrency    int `json:"ping_concurrency,omitempty"`
	SpeedConcurrency   int `json:"speed_concurrency,omitempty"`
	// Customisable test endpoints. Empty → use built-in defaults
	// (pingTestURLDefault / speedTestURLDefault). The dropdown in
	// Settings → Testing lets the user pick a preset or "Custom URL".
	PingTestURL        string `json:"ping_test_url,omitempty"`
	SpeedTestURL       string `json:"speed_test_url,omitempty"`
	// TUN adapter MTU. Zero / unset / out-of-range → 9000 (current default).
	// Recommended values: 9000 (default, jumbo) or 1500 / 1408 if a slow
	// network can't handle big frames.
	TUNMTU             int    `json:"tun_mtu,omitempty"`
	// Traffic statistics. Inverted bool so the JSON zero value (omitted /
	// false) means "enabled" — matches the default-on UI.
	StatsDisabled      bool   `json:"stats_disabled,omitempty"`
	// Lifetime traffic counters, persisted between sessions. Bytes.
	StatsTotalUp       int64  `json:"stats_total_up,omitempty"`
	StatsTotalDown     int64  `json:"stats_total_down,omitempty"`
}

const (
	defaultPingConcurrency  = 10
	defaultSpeedConcurrency = 5
	maxPingConcurrency      = 200
	maxSpeedConcurrency     = 100
)

// currentPingConcurrency returns the user's chosen ping concurrency, clamped
// to [1, maxPingConcurrency]. Used by runPingAll instead of a const so the
// setting is honored without a restart.
func currentPingConcurrency() int {
	settingsMu.RLock()
	n := appSettings.PingConcurrency
	settingsMu.RUnlock()
	if n <= 0 {
		return defaultPingConcurrency
	}
	if n > maxPingConcurrency {
		return maxPingConcurrency
	}
	return n
}

// currentSpeedConcurrency mirrors currentPingConcurrency for speed tests.
// The default is lower (5) because each speed test holds a full data
// transfer for several seconds — running 200 in parallel saturates anything.
func currentSpeedConcurrency() int {
	settingsMu.RLock()
	n := appSettings.SpeedConcurrency
	settingsMu.RUnlock()
	if n <= 0 {
		return defaultSpeedConcurrency
	}
	if n > maxSpeedConcurrency {
		return maxSpeedConcurrency
	}
	return n
}

// currentPingURL returns the URL used by the ping test. Falls back to the
// built-in default if the user hasn't picked one. Read at every call so
// changes apply without a restart.
func currentPingURL() string {
	settingsMu.RLock()
	u := strings.TrimSpace(appSettings.PingTestURL)
	settingsMu.RUnlock()
	if u == "" {
		return pingTestURLDefault
	}
	return u
}

// currentSpeedURL returns the URL used by the speed test.
func currentSpeedURL() string {
	settingsMu.RLock()
	u := strings.TrimSpace(appSettings.SpeedTestURL)
	settingsMu.RUnlock()
	if u == "" {
		return speedTestURLDefault
	}
	return u
}

// currentMTU returns the TUN-adapter MTU. Out-of-range or unset → 9000
// (the historical default the codebase shipped with). 576 is the smallest
// MTU IPv4 hosts must support; above 9000 isn't useful for any LAN we'll see.
func currentMTU() int {
	settingsMu.RLock()
	m := appSettings.TUNMTU
	settingsMu.RUnlock()
	if m < 576 || m > 9000 {
		return 9000
	}
	return m
}

// statsEnabled reports whether traffic statistics collection is on.
// We store the negation (StatsDisabled) so the JSON zero-value/omitted
// case means "enabled", matching the default-on UI.
func statsEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return !appSettings.StatsDisabled
}

var appSettings = AppSettings{SourcesEnabled: true}
var settingsMu sync.RWMutex

func settingsFilePath() string {
	return filepath.Join(tabsDir(), "settings.json")
}

func loadSettings() {
	data, err := os.ReadFile(settingsFilePath())
	if err != nil {
		return
	}
	settingsMu.Lock()
	defer settingsMu.Unlock()
	json.Unmarshal(data, &appSettings)
}

func saveSettings() {
	settingsMu.RLock()
	data, _ := json.MarshalIndent(appSettings, "", "  ")
	settingsMu.RUnlock()
	os.MkdirAll(tabsDir(), 0755)
	os.WriteFile(settingsFilePath(), data, 0644)
}

func shouldSkip(name string, countries []string) bool {
	if len(countries) == 0 {
		return false
	}
	low := strings.ToLower(name)
	for _, c := range countries {
		if strings.Contains(low, strings.ToLower(c)) {
			return true
		}
	}
	return false
}

// ─────────────────────────── types ───────────────────────────────

type VlessParams struct {
	Raw           string
	UUID          string
	Host          string
	Port          int
	Name          string
	Network       string
	Security      string
	Path          string
	Host2         string
	ServiceName   string
	SNI           string
	ALPN          string
	Fingerprint   string
	AllowInsecure bool
	Flow          string
	PublicKey     string
	ShortID       string
	SpiderX       string
}

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
}

type connManager struct {
	mu      sync.Mutex
	state   ConnState
	cmd     *exec.Cmd    // main process (xray for proxy, sing-box for TUN)
	xrayCmd *exec.Cmd    // secondary xray process (hybrid TUN mode only)
	cancel  context.CancelFunc
	xrayCancel context.CancelFunc
	tmpCfg  string
	xrayTmpCfg string
	// Per-session traffic counter. Allocated fresh on every connect so
	// the byte tallies don't bleed across sessions. nil while idle.
	counter   *trafficCounter
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
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	IsMain         bool      `json:"is_main"`
	Closable       bool      `json:"closable"`
	SourceURL      string    `json:"source_url,omitempty"`
	SourceURLs     []string  `json:"source_urls,omitempty"`
	SourceFiles    []TabFile `json:"source_files,omitempty"`
	RefreshMin     int       `json:"refresh_min,omitempty"`
	ExcludeFilter  []string  `json:"exclude_filter,omitempty"`
	// DedupMode is "" (off), "hide" (client-side view filter, reversible),
	// or "delete" (server-side removal, not reversible). The Tab.Dedup
	// boolean from earlier dev builds is auto-migrated to "hide" on load.
	DedupMode      string    `json:"dedup_mode,omitempty"`
}

type persistedTab struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	SourceURL      string    `json:"source_url,omitempty"`
	SourceURLs     []string  `json:"source_urls,omitempty"`
	SourceFiles    []TabFile `json:"source_files,omitempty"`
	RefreshMin     int       `json:"refresh_min,omitempty"`
	ExcludeFilter  []string  `json:"exclude_filter,omitempty"`
	// Two fields cover the migration: old builds wrote `dedup: true` for
	// what's now `dedup_mode: "hide"`. loadTabs picks DedupMode if set,
	// otherwise upgrades the legacy bool.
	Dedup          bool      `json:"dedup,omitempty"`
	DedupMode      string    `json:"dedup_mode,omitempty"`
	Configs        []string  `json:"configs,omitempty"`
}
type persistedData struct {
	Tabs []persistedTab `json:"tabs"`
}

// ─────────────────────────── SSE / state ─────────────────────────

type SSEEvent struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
	Tab     string      `json:"tab,omitempty"`
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
	activeTab:  "main",
	tabs:       []Tab{{ID: "main", Name: "Sources", IsMain: true, Closable: false}},
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

func (s *AppState) broadcast(ev SSEEvent) {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- ev:
		default:
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

// ─────────────────────────── VLESS parsing ───────────────────────

func parseVless(raw string) (*VlessParams, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "vless://") {
		return nil, fmt.Errorf("not a vless URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("url.Parse: %w", err)
	}
	p := &VlessParams{Raw: raw}
	p.UUID = u.User.Username()
	p.Host = u.Hostname()
	if p.Host == "" {
		return nil, fmt.Errorf("empty host")
	}
	if port := u.Port(); port == "" {
		p.Port = 443
	} else if p.Port, err = strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("bad port: %w", err)
	}
	p.Name = u.Fragment
	if p.Name == "" {
		p.Name = fmt.Sprintf("%s:%d", p.Host, p.Port)
	}
	q := u.Query()
	get := func(k string) string { return q.Get(k) }
	p.Network = get("type")
	if p.Network == "" {
		p.Network = "tcp"
	}
	p.Security = get("security")
	if p.Security == "" {
		p.Security = "none"
	}
	p.Path, p.Host2, p.ServiceName = get("path"), get("host"), get("serviceName")
	p.SNI, p.ALPN, p.Fingerprint = get("sni"), get("alpn"), get("fp")
	p.AllowInsecure = get("allowInsecure") == "1"
	p.Flow = get("flow")
	p.PublicKey, p.ShortID, p.SpiderX = get("pbk"), get("sid"), get("spx")
	return p, nil
}


// ─────────────────────────── xray config (test + System Proxy) ───

func buildXrayConfig(p *VlessParams, httpPort, socksPort int) map[string]interface{} {
	user := map[string]interface{}{"id": p.UUID, "encryption": "none"}
	if p.Flow != "" {
		user["flow"] = p.Flow
	}
	outSettings := map[string]interface{}{
		"vnext": []interface{}{map[string]interface{}{
			"address": p.Host, "port": p.Port, "users": []interface{}{user},
		}},
	}
	stream := map[string]interface{}{"network": p.Network, "security": p.Security}
	switch p.Network {
	case "ws":
		ws := map[string]interface{}{"path": p.Path}
		if p.Host2 != "" {
			ws["headers"] = map[string]interface{}{"Host": p.Host2}
		}
		stream["wsSettings"] = ws
	case "grpc":
		stream["grpcSettings"] = map[string]interface{}{"serviceName": p.ServiceName, "multiMode": false}
	case "h2", "http":
		h2 := map[string]interface{}{"path": p.Path}
		if p.Host2 != "" {
			h2["host"] = []string{p.Host2}
		}
		stream["httpSettings"] = h2
	case "httpupgrade":
		hu := map[string]interface{}{"path": p.Path}
		if p.Host2 != "" {
			hu["host"] = p.Host2
		}
		stream["httpupgradeSettings"] = hu
	case "splithttp", "xhttp":
		// xhttp is the newer name for splithttp in xray-core 1.8+
		sh := map[string]interface{}{"path": p.Path}
		if p.Host2 != "" {
			sh["host"] = p.Host2
		}
		stream["xhttpSettings"] = sh
	}
	switch p.Security {
	case "tls":
		tls := map[string]interface{}{"serverName": p.SNI, "allowInsecure": p.AllowInsecure}
		if p.Fingerprint != "" {
			tls["fingerprint"] = p.Fingerprint
		}
		if p.ALPN != "" {
			tls["alpn"] = strings.Split(p.ALPN, ",")
		}
		stream["tlsSettings"] = tls
	case "reality":
		stream["realitySettings"] = map[string]interface{}{
			"serverName": p.SNI, "fingerprint": p.Fingerprint,
			"publicKey": p.PublicKey, "shortId": p.ShortID, "spiderX": p.SpiderX,
		}
	}
	persistent := socksPort > 0
	sniffing := map[string]interface{}{
		"enabled":      persistent,
		"destOverride": []string{"http", "tls", "quic"},
	}
	inbounds := []interface{}{
		map[string]interface{}{
			"tag": "http", "listen": "127.0.0.1", "port": httpPort,
			"protocol": "http",
			"settings": map[string]interface{}{"auth": "noauth"},
			"sniffing": sniffing,
		},
	}
	if persistent {
		inbounds = append(inbounds, map[string]interface{}{
			"tag": "socks", "listen": "127.0.0.1", "port": socksPort,
			"protocol": "socks",
			"settings": map[string]interface{}{
				"auth": "password",
				"accounts": []interface{}{
					map[string]interface{}{"user": proxyAuthUser, "pass": proxyAuthPass},
				},
				"udp": true,
			},
			"sniffing": sniffing,
		})
	}
	cfg := map[string]interface{}{
		"log":      map[string]interface{}{"loglevel": "warning"},
		"inbounds": inbounds,
		"outbounds": []interface{}{
			map[string]interface{}{
				"tag": "proxy", "protocol": "vless",
				"settings": outSettings, "streamSettings": stream,
			},
			map[string]interface{}{"tag": "direct", "protocol": "freedom",
				"settings": map[string]interface{}{"domainStrategy": "UseIPv4"}},
			map[string]interface{}{"tag": "block", "protocol": "blackhole", "settings": map[string]interface{}{}},
		},
	}
	if persistent {
		rules := []interface{}{
			map[string]interface{}{"type": "field", "ip": []string{"geoip:private"}, "outboundTag": "direct"},
		}
		// Russian sites bypass VPN in proxy mode (uses xray's built-in geodata)
		settingsMu.RLock()
		ruDirect := appSettings.RuSitesDirect
		directDomains := make([]string, len(appSettings.DirectDomains))
		copy(directDomains, appSettings.DirectDomains)
		settingsMu.RUnlock()
		if ruDirect {
			rules = append(rules,
				map[string]interface{}{"type": "field", "domain": []string{"geosite:category-ru"}, "outboundTag": "direct"},
				map[string]interface{}{"type": "field", "ip": []string{"geoip:ru"}, "outboundTag": "direct"},
			)
		}
		// Custom domains → direct (user-defined in Settings)
		if len(directDomains) > 0 {
			// xray uses "domain" field with domain suffixes
			var suffixes []string
			for _, d := range directDomains {
				d = strings.TrimSpace(d)
				if d == "" {
					continue
				}
				// "domain:" prefix = suffix match (vk.com matches *.vk.com and vk.com)
				suffixes = append(suffixes, "domain:"+d)
			}
			if len(suffixes) > 0 {
				rules = append(rules,
					map[string]interface{}{"type": "field", "domain": suffixes, "outboundTag": "direct"},
				)
			}
		}
		rules = append(rules,
			map[string]interface{}{"type": "field", "network": "tcp,udp", "outboundTag": "proxy"},
		)
		cfg["routing"] = map[string]interface{}{
			"domainStrategy": "IPIfNonMatch",
			"rules":          rules,
		}
	}
	return cfg
}



func buildHybridTUNConfig(ifaceName string, xrayHTTPPort, xraySocksPort int) map[string]interface{} {
	// Hybrid TUN: sing-box routes traffic, xray handles VLESS protocol.
	//
	// DNS: "local" = OS system resolver. With strict_route=false, the OS resolver's
	// UDP packets reach the router naturally through the physical NIC (not TUN).
	// This works on both Ethernet and WiFi without knowing the router IP.
	//
	// strict_route=false: auto_route still captures most traffic through TUN,
	// but system-level DNS and some edge cases can escape through the physical NIC.
	// This avoids the chicken-and-egg DNS problem on WiFi.
	dns := map[string]interface{}{
		"servers": []interface{}{
			map[string]interface{}{"tag": "dns-local", "type": "local"},
		},
		"final":             "dns-local",
		"independent_cache": true,
	}

	tun := map[string]interface{}{
		"type":           "tun",
		"tag":            "tun-in",
		"interface_name": ifaceName,
		"address":        []string{"172.19.0.1/30"},
		"mtu":            currentMTU(),
		"auto_route":     true,
		"strict_route":   false,
		"stack":          "gvisor",
	}

	proxyOut := map[string]interface{}{
		"type":        "socks",
		"tag":         "proxy",
		"server":      "127.0.0.1",
		"server_port": xraySocksPort,
		"username":    proxyAuthUser,
		"password":    proxyAuthPass,
	}

	// Read routing settings
	settingsMu.RLock()
	ruSitesDirect := appSettings.RuSitesDirect
	directDomains := make([]string, len(appSettings.DirectDomains))
	copy(directDomains, appSettings.DirectDomains)
	directApps := make([]string, len(appSettings.DirectApps))
	copy(directApps, appSettings.DirectApps)
	settingsMu.RUnlock()

	rules := []interface{}{
		map[string]interface{}{"action": "sniff"},
		map[string]interface{}{"protocol": "dns", "action": "hijack-dns"},
		// Exclude xray process from TUN to prevent routing loop.
		map[string]interface{}{
			"process_name": []string{"xray.exe", "xray"},
			"outbound":     "direct",
		},
		map[string]interface{}{"ip_is_private": true, "outbound": "direct"},
	}

	// User-configured apps that bypass VPN (direct connection).
	if len(directApps) > 0 {
		var appNames []string
		for _, a := range directApps {
			a = strings.TrimSpace(a)
			if a != "" {
				appNames = append(appNames, a)
			}
		}
		if len(appNames) > 0 {
			rules = append(rules, map[string]interface{}{
				"process_name": appNames,
				"outbound":     "direct",
			})
		}
	}

	// Russian sites bypass VPN
	if ruSitesDirect {
		rules = append(rules,
			map[string]interface{}{"rule_set": "geosite-ru", "outbound": "direct"},
			map[string]interface{}{"rule_set": "geoip-ru", "outbound": "direct"},
		)
	}

	// Custom domains → direct (user-defined in Settings)
	if len(directDomains) > 0 {
		var suffixes []string
		for _, d := range directDomains {
			d = strings.TrimSpace(d)
			if d != "" {
				suffixes = append(suffixes, d)
			}
		}
		if len(suffixes) > 0 {
			rules = append(rules,
				map[string]interface{}{"domain_suffix": suffixes, "outbound": "direct"},
			)
		}
	}

	route := map[string]interface{}{
		"auto_detect_interface":   true,
		"default_domain_resolver": "dns-local",
		"find_process":            true,
		"rules":                   rules,
		"final":                   "proxy",
	}

	if ruSitesDirect {
		route["rule_set"] = []interface{}{
			map[string]interface{}{
				"type":             "remote",
				"tag":              "geosite-ru",
				"format":           "binary",
				"url":              "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-category-ru.srs",
				"download_detour":  "direct",
				"update_interval":  "24h",
			},
			map[string]interface{}{
				"type":             "remote",
				"tag":              "geoip-ru",
				"format":           "binary",
				"url":              "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-ru.srs",
				"download_detour":  "direct",
				"update_interval":  "24h",
			},
		}
	}

	return map[string]interface{}{
		"log":      map[string]interface{}{"level": "warn", "timestamp": true},
		"dns":      dns,
		"inbounds": []interface{}{tun},
		"outbounds": []interface{}{
			proxyOut,
			map[string]interface{}{"type": "direct", "tag": "direct"},
		},
		"route": route,
	}
}

// ─────────────────────────── helpers ─────────────────────────────

func findFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func waitForPort(port int, deadline time.Time) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return true
		}
		time.Sleep(80 * time.Millisecond)
	}
	return false
}

func makeSharedTransport(httpPort int) *http.Transport {
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", httpPort))
	return &http.Transport{
		Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: false, ForceAttemptHTTP2: false,
		MaxIdleConnsPerHost: 4, TLSHandshakeTimeout: warmupTimeout,
		DialContext: (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
	}
}

func shortErr(s string) string {
	if idx := strings.LastIndex(s, ": "); idx >= 0 && idx < len(s)-2 {
		if tail := s[idx+2:]; len(tail) < 90 {
			return tail
		}
	}
	if len(s) > 90 {
		return s[:90]
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func writeTempJSON(data interface{}, prefix string) (string, error) {
	tmp, err := os.CreateTemp("", prefix+"-*.json")
	if err != nil {
		return "", err
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err = enc.Encode(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	tmp.Close()
	return tmp.Name(), nil
}

// ─────────────────────────── xray lifecycle (testing) ────────────

func withXray(p *VlessParams, ttl time.Duration, fn func(httpPort int, tr *http.Transport) error) error {
	httpPort, err := findFreePort()
	if err != nil {
		return fmt.Errorf("no free port")
	}
	tmpPath, err := writeTempJSON(buildXrayConfig(p, httpPort, -1), "xray-test")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	defer os.Remove(tmpPath)
	ctx, cancel := context.WithTimeout(context.Background(), ttl)
	defer cancel()
	cmd := exec.CommandContext(ctx, state.xrayBin, "run", "-c", tmpPath)
	cmd.Stdout = io.Discard
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	hideProcess(cmd)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("xray start: %w", err)
	}
	trackPID(cmd.Process.Pid)
	defer func() {
		cmd.Process.Kill() //nolint:errcheck
		untrackPID(cmd.Process.Pid)
	}()

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	portResult := make(chan bool, 1)
	go func() {
		portResult <- waitForPort(httpPort, time.Now().Add(xrayStartupTimeout))
	}()
	select {
	case exitErr := <-exitCh:
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg == "" {
			if exitErr != nil { errMsg = exitErr.Error() } else { errMsg = "exited unexpectedly" }
		}
		if len(errMsg) > 160 { errMsg = "..." + errMsg[len(errMsg)-160:] }
		return fmt.Errorf("xray: %s", errMsg)
	case ready := <-portResult:
		if !ready {
			return fmt.Errorf("xray: port not ready after %s", xrayStartupTimeout)
		}
	}
	return fn(httpPort, makeSharedTransport(httpPort))
}

// ─────────────────────────── system proxy ────────────────────────

func setSystemProxy(port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	bypass := "localhost;127.*;10.*;172.16.*;192.168.*;*.local;<local>"
	rp := `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	for _, c := range [][]string{
		{"reg", "add", rp, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "1", "/f"},
		{"reg", "add", rp, "/v", "ProxyServer", "/t", "REG_SZ", "/d", addr, "/f"},
		{"reg", "add", rp, "/v", "ProxyOverride", "/t", "REG_SZ", "/d", bypass, "/f"},
	} {
		if err := runHidden(c[0], c[1:]...).Run(); err != nil {
			return fmt.Errorf("reg: %w", err)
		}
	}
	runHidden("rundll32.exe", "inetcpl.cpl,ClearMyTracksByProcess", "8").Run() //nolint:errcheck
	return nil
}

func unsetSystemProxy() {
	rp := `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	runHidden("reg", "add", rp, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "0", "/f").Run() //nolint:errcheck
}

// ─────────────────────────── connection manager ──────────────────

func startProxyConnection(entry *ConfigEntry) {
	cm := state.conn
	stopConnectionLocked(cm)

	state.mu.RLock()
	connTab := state.activeTab
	state.mu.RUnlock()

	p, err := parseVless(entry.Raw)
	if err != nil {
		setConnError(cm, entry, err.Error(), connTab)
		return
	}

	cm.mu.Lock()
	cm.state = ConnState{Status: ConnConnecting, Mode: ModeProxy, EntryIndex: entry.Index, EntryName: p.Name, ConnTab: connTab, ConnRaw: entry.Raw}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})

	httpPort, socksPort := connHTTPPort, connSOCKSPort
	if !portFree(httpPort) {
		if pf, e := findFreePort(); e == nil {
			httpPort = pf
		}
	}
	if !portFree(socksPort) {
		if pf, e := findFreePort(); e == nil {
			socksPort = pf
		}
	}

	// The user-visible ports above are what apps connect to (system proxy
	// points at httpPort). xray itself listens on internal ports — bytes
	// flow ext → counter → int → xray. The forwarder is a transparent
	// localhost TCP relay that tallies bytes per direction.
	intHTTPPort, e1 := findFreePort()
	if e1 != nil {
		setConnError(cm, entry, "no free port for xray http")
		return
	}
	intSOCKSPort, e2 := findFreePort()
	if e2 != nil {
		setConnError(cm, entry, "no free port for xray socks")
		return
	}

	tmpPath, err := writeTempJSON(buildXrayConfig(p, intHTTPPort, intSOCKSPort), "xray-conn")
	if err != nil {
		setConnError(cm, entry, err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, state.xrayBin, "run", "-c", tmpPath)
	cmd.Stdout = io.Discard
	hideProcess(cmd)

	// Capture stderr so we can report the real error if xray crashes.
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf

	if err = cmd.Start(); err != nil {
		cancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "xray start: "+err.Error())
		return
	}

	// Watch for immediate crash in background.
	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()

	// Wait for xray HTTP port OR an immediate crash, whichever comes first.
	portResult := make(chan bool, 1)
	go func() {
		portResult <- waitForPort(intHTTPPort, time.Now().Add(xrayConnTimeout))
	}()

	select {
	case exitErr := <-exitCh:
		// xray exited before the port opened — report stderr
		cancel()
		os.Remove(tmpPath)
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg == "" {
			if exitErr != nil {
				errMsg = exitErr.Error()
			} else {
				errMsg = "xray exited unexpectedly"
			}
		}
		if len(errMsg) > 200 {
			errMsg = "..." + errMsg[len(errMsg)-200:]
		}
		setConnError(cm, entry, "xray: "+errMsg)
		return
	case ready := <-portResult:
		if !ready {
			cancel()
			os.Remove(tmpPath)
			setConnError(cm, entry, "xray: port not ready after timeout")
			return
		}
	}

	// xray is now listening on the internal ports. Bring up the byte
	// counters in front of them on the externally visible ports. Apps
	// (and the system proxy below) see the same ports they always did.
	counter := &trafficCounter{}
	fwdCtx, fwdCancel := context.WithCancel(context.Background())
	if _, err := startCountingForwarder(fwdCtx, httpPort, intHTTPPort, counter, "proxy-http"); err != nil {
		fwdCancel()
		cancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "proxy http counter: "+err.Error())
		return
	}
	if _, err := startCountingForwarder(fwdCtx, socksPort, intSOCKSPort, counter, "proxy-socks"); err != nil {
		fwdCancel()
		cancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "proxy socks counter: "+err.Error())
		return
	}

	if err = setSystemProxy(httpPort); err != nil {
		fmt.Fprintf(os.Stderr, "⚠  setSystemProxy: %v\n", err)
	}

	cm.mu.Lock()
	cm.cmd = cmd
	cm.cancel = cancel
	cm.tmpCfg = tmpPath
	cm.counter = counter
	cm.fwdCancel = fwdCancel
	cm.state = ConnState{
		Status: ConnConnected, Mode: ModeProxy, ConnTab: connTab, ConnRaw: entry.Raw,
		EntryIndex: entry.Index, EntryName: p.Name,
		HTTPPort: httpPort, SOCKSPort: socksPort,
		StartedAt: time.Now(),
	}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	startUptimeTicker(cm)
	startStatsTicker(cm)
}

func startTUNConnection(entry *ConfigEntry) {
	state.mu.RLock()
	connTab := state.activeTab
	state.mu.RUnlock()

	if !checkAdmin() {
		setConnError(state.conn, entry, "TUN mode requires administrator/root. Run the program as admin.")
		return
	}
	if state.singboxBin == "" {
		setConnError(state.conn, entry, "sing-box not found. Pass path as 2nd arg: vair xray.exe sing-box.exe")
		return
	}

	cm := state.conn
	stopConnectionLocked(cm)

	// Best-effort cleanup of any stale adapters from previous sessions.
	// With unique names below this is no longer strictly required,
	// but prevents accumulation of ghost adapters in Device Manager.
	removeTUNAdapter()

	p, err := parseVless(entry.Raw)
	if err != nil {
		setConnError(cm, entry, err.Error())
		return
	}

	cm.mu.Lock()
	cm.state = ConnState{Status: ConnConnecting, Mode: ModeTUN, EntryIndex: entry.Index, EntryName: p.Name, ConnTab: connTab, ConnRaw: entry.Raw}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})

	// Unique interface name per session avoids "file already exists".
	// Even if Windows hasn't fully cleaned the previous adapter kernel-side,
	// a new name means sing-box never conflicts with the old one.
	tunIfaceName := fmt.Sprintf("xc-tun-%d", time.Now().Unix()%10000)

	// 1. Start xray as local HTTP+SOCKS proxy
	xrayHTTPPort, e1 := findFreePort()
	if e1 != nil {
		setConnError(cm, entry, "no free port for xray")
		return
	}
	xraySocksPort, e2 := findFreePort()
	if e2 != nil {
		setConnError(cm, entry, "no free port for xray socks")
		return
	}
	xrayCfg := buildXrayConfig(p, xrayHTTPPort, xraySocksPort)
	// For hybrid TUN: xray resolves the VPN server hostname via the OS resolver.
	// sing-box's route rule `ip_is_private → direct` catches the resulting DNS
	// packets (OS resolver hits the router at 192.168.x.1 which is private IP)
	// and routes them out through the physical interface (direct outbound),
	// not TUN. So we don't need to override xray's DNS — the default works.
	// Simplify routing: everything goes to proxy.
	// sing-box handles private IP routing, xray doesn't need geoip.
	xrayCfg["routing"] = map[string]interface{}{
		"domainStrategy": "AsIs",
		"rules": []interface{}{
			map[string]interface{}{"type": "field", "network": "tcp,udp", "outboundTag": "proxy"},
		},
	}
	xrayTmpPath, err := writeTempJSON(xrayCfg, "xray-hybrid")
	if err != nil {
		setConnError(cm, entry, "xray config write: "+err.Error())
		return
	}
	// Save debug copy
	if debugPath := filepath.Join(tabsDir(), "last-xray-hybrid.json"); true {
		os.MkdirAll(tabsDir(), 0755)
		data, _ := json.MarshalIndent(xrayCfg, "", "  ")
		os.WriteFile(debugPath, data, 0644)
	}
	xrayCtx, xrayCancel := context.WithCancel(context.Background())
	xrayCmd := exec.CommandContext(xrayCtx, state.xrayBin, "run", "-c", xrayTmpPath)
	xrayCmd.Stdout = io.Discard
	// Capture xray stderr for debugging
	var xrayStderrBuf strings.Builder
	xrayCmd.Stderr = &xrayStderrBuf
	hideProcess(xrayCmd)
	if err = xrayCmd.Start(); err != nil {
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "xray hybrid start: "+err.Error())
		return
	}
	// Wait for xray port
	if !waitForPort(xrayHTTPPort, time.Now().Add(xrayStartupTimeout)) {
		xrayCmd.Process.Kill() //nolint:errcheck
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "xray hybrid: port not ready")
		return
	}
	fmt.Printf("ℹ  hybrid TUN: xray proxy on :%d/%d for %s\n", xrayHTTPPort, xraySocksPort, p.Network)

	// Insert a byte-counting forwarder between sing-box and xray's SOCKS
	// inbound. sing-box's `proxy` outbound dials this forwarder; the
	// forwarder relays into xray. xray remains unmodified. This is the
	// same trick used in proxy mode and gives us per-session traffic
	// stats with no protocol-specific code.
	counter := &trafficCounter{}
	fwdCtx, fwdCancel := context.WithCancel(context.Background())
	counterSocksPort, err := startCountingForwarder(fwdCtx, 0, xraySocksPort, counter, "tun-socks")
	if err != nil {
		fwdCancel()
		xrayCmd.Process.Kill() //nolint:errcheck
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "tun counter: "+err.Error())
		return
	}
	// 2. Build sing-box TUN config routing through xray proxy (via counter)
	cfg := buildHybridTUNConfig(tunIfaceName, xrayHTTPPort, counterSocksPort)
	tmpPath, err := writeTempJSON(cfg, "singbox-tun")
	if err != nil {
		fwdCancel()
		xrayCmd.Process.Kill() //nolint:errcheck
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "singbox config write: "+err.Error())
		return
	}
	fmt.Printf("ℹ  sing-box hybrid config: %s\n", tmpPath)
	// Save debug copy
	if debugPath := filepath.Join(tabsDir(), "last-singbox-hybrid.json"); true {
		os.MkdirAll(tabsDir(), 0755)
		data, _ := json.MarshalIndent(cfg, "", "  ")
		os.WriteFile(debugPath, data, 0644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, state.singboxBin, "run", "-c", tmpPath)
	stderrPipe, _ := cmd.StderrPipe()
	cmd.Stdout = io.Discard
	hideProcess(cmd)

	if err = cmd.Start(); err != nil {
		cancel()
		os.Remove(tmpPath)
		fwdCancel()
		xrayCmd.Process.Kill() //nolint:errcheck
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "sing-box start: "+err.Error())
		return
	}

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	var stderrLines []string
	var stderrMu sync.Mutex
	// Write all sing-box stderr to a log file for debugging
	logPath := filepath.Join(tabsDir(), "last-singbox.log")
	os.MkdirAll(tabsDir(), 0755)
	logFile, _ := os.Create(logPath)
	go func() {
		if stderrPipe == nil { return }
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			stderrMu.Lock()
			stderrLines = append(stderrLines, line)
			stderrMu.Unlock()
			if logFile != nil {
				fmt.Fprintln(logFile, line)
			}
		}
		if logFile != nil { logFile.Close() }
	}()

	select {
	case <-exitCh:
		cancel()
		os.Remove(tmpPath)
		fwdCancel()
		xrayCmd.Process.Kill() //nolint:errcheck
		xrayCancel()
		os.Remove(xrayTmpPath)
		stderrMu.Lock()
		lines := stderrLines
		stderrMu.Unlock()
		errMsg := "sing-box crashed at startup"
		for i := len(lines) - 1; i >= 0 && i >= len(lines)-3; i-- {
			if lines[i] != "" { errMsg = lines[i]; break }
		}
		if len(errMsg) > 180 { errMsg = "..." + errMsg[len(errMsg)-180:] }
		setConnError(cm, entry, errMsg)
		return
	case <-time.After(tunStartupTimeout):
	}

	cm.mu.Lock()
	cm.cmd = cmd
	cm.cancel = cancel
	cm.tmpCfg = tmpPath
	cm.xrayCmd = xrayCmd
	cm.xrayCancel = xrayCancel
	cm.xrayTmpCfg = xrayTmpPath
	cm.counter = counter
	cm.fwdCancel = fwdCancel
	cm.state = ConnState{
		Status: ConnConnected, Mode: ModeTUN, ConnTab: connTab, ConnRaw: entry.Raw,
		EntryIndex: entry.Index, EntryName: p.Name,
		TUNIface: tunIfaceName, StartedAt: time.Now(),
	}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	startUptimeTicker(cm)
	startStatsTicker(cm)
}

func stopConnection() {
	stopConnectionLocked(state.conn)
	state.broadcast(SSEEvent{Type: "conn_update", Payload: state.conn.snap()})
}

// removeTUNAdapter removes the WinTUN/TUN adapter by name so it doesn't linger
// in the system and doesn't cause "file already exists" on reconnect.
func removeTUNAdapter() {
	runHidden("powershell", "-NonInteractive", "-Command",
		`Get-NetAdapter -Name 'xc-tun-*' -ErrorAction SilentlyContinue | Remove-NetAdapter -Confirm:$false`,
	).Run() //nolint:errcheck
}


func stopConnectionLocked(cm *connManager) {
	cm.mu.Lock()
	if cm.state.Status == ConnIdle {
		cm.mu.Unlock()
		return
	}
	wasTUN := cm.state.Mode == ModeTUN
	wasProxy := cm.state.Mode == ModeProxy
	prevHTTPPort := cm.state.HTTPPort
	cm.state.Status = ConnDisconnecting

	// Extract process references under lock, then release lock for killing
	mainCmd := cm.cmd
	mainCancel := cm.cancel
	mainTmpCfg := cm.tmpCfg
	xrayCmd := cm.xrayCmd
	xrayCancel := cm.xrayCancel
	xrayTmpCfg := cm.xrayTmpCfg
	fwdCancel := cm.fwdCancel
	counter := cm.counter

	// Clear references immediately so a concurrent call sees them as nil
	cm.cmd = nil
	cm.cancel = nil
	cm.tmpCfg = ""
	cm.xrayCmd = nil
	cm.xrayCancel = nil
	cm.xrayTmpCfg = ""
	cm.fwdCancel = nil
	cm.counter = nil
	cm.mu.Unlock()

	// Stop accepting new proxied connections immediately. In-flight relays
	// drain naturally (their source/dest closes propagate); we don't wait
	// because the xray kill below will close the destinations for them.
	if fwdCancel != nil {
		fwdCancel()
	}

	// Roll any unpersisted bytes from this session into the lifetime
	// total before xray dies. The counter is in-process so its values
	// are available regardless of what xray does. The 30-second ticker
	// has already persisted most of it; this picks up the tail.
	//
	// We skip the fold-in when the user has stats disabled — the toggle
	// semantics are "don't grow the lifetime counter while I'm not
	// watching", and folding in here would silently bump it on every
	// disconnect even with stats off. The session bytes themselves stay
	// in the in-memory counter (which is about to be discarded), so the
	// next session starts from a clean slate either way.
	if counter != nil && statsEnabled() {
		persistCounterDelta(counter)
		state.broadcast(SSEEvent{Type: "stats_update", Payload: statsSnapshot(nil)})
	}

	// === Kill processes WITHOUT holding the lock ===

	// Cancel contexts first (sends signals)
	if mainCancel != nil {
		mainCancel()
	}
	if xrayCancel != nil {
		xrayCancel()
	}

	// Kill sing-box and xray in parallel. Go's Process.Kill() on Windows is
	// soft — it sends WM_CLOSE-like signal and process may try to drain pending
	// operations before exit (DNS queries with long timeouts, etc).
	// taskkill /F forces immediate termination.
	killProc := func(cmd *exec.Cmd, done chan<- struct{}) {
		defer close(done)
		if cmd == nil || cmd.Process == nil {
			return
		}
		pid := cmd.Process.Pid
		// Force-kill immediately in parallel with Go's kill (whoever wins).
		go runHidden("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run() //nolint:errcheck
		cmd.Process.Kill()                                                      //nolint:errcheck
		waitDone := make(chan struct{})
		go func() { cmd.Wait(); close(waitDone) }() //nolint:errcheck
		select {
		case <-waitDone:
		case <-time.After(1500 * time.Millisecond):
			// Still running — one more taskkill attempt
			runHidden("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run() //nolint:errcheck
		}
	}

	mainDone := make(chan struct{})
	xrayDone := make(chan struct{})
	go killProc(mainCmd, mainDone)
	go killProc(xrayCmd, xrayDone)
	<-mainDone
	<-xrayDone

	// Cleanup temp files
	if mainTmpCfg != "" {
		os.Remove(mainTmpCfg)
	}
	if xrayTmpCfg != "" {
		os.Remove(xrayTmpCfg)
	}

	if wasProxy {
		unsetSystemProxy()
		if prevHTTPPort > 0 {
			deadline := time.Now().Add(400 * time.Millisecond)
			for time.Now().Before(deadline) {
				if portFree(prevHTTPPort) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
	}
	if wasTUN {
		removeTUNAdapter()
	}

	// Re-acquire lock to set final state
	cm.mu.Lock()
	cm.state = ConnState{Status: ConnIdle, EntryIndex: -1}
	cm.mu.Unlock()
}

func setConnError(cm *connManager, entry *ConfigEntry, msg string, tab ...string) {
	name := entry.Name
	if name == "" {
		name = fmt.Sprintf("#%d", entry.Index)
	}
	connTab := ""
	if len(tab) > 0 {
		connTab = tab[0]
	}
	cm.mu.Lock()
	cm.state = ConnState{Status: ConnError, EntryIndex: entry.Index, EntryName: name, ErrMsg: msg, ConnTab: connTab, ConnRaw: entry.Raw}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
}

func startUptimeTicker(cm *connManager) {
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for range t.C {
			cm.mu.Lock()
			ok := cm.state.Status == ConnConnected
			cm.mu.Unlock()
			if !ok {
				return
			}
			state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
		}
	}()
}

// statsSnapshot returns a JSON-friendly view of current traffic
// counters. session_* fields are zero when the counter is nil (idle).
// total_* always reflects the value the lifetime total would take if
// we persisted right now — i.e. the value on disk plus whatever the
// live counter has accumulated since the last 30-second checkpoint.
// Without this fold-in, the displayed total would only tick once
// every 30 s (and on disconnect), confusing users who watch it.
func statsSnapshot(counter *trafficCounter) map[string]interface{} {
	settingsMu.RLock()
	totalUp := appSettings.StatsTotalUp
	totalDown := appSettings.StatsTotalDown
	enabled := !appSettings.StatsDisabled
	settingsMu.RUnlock()
	var sessUp, sessDown int64
	if counter != nil {
		sessUp = counter.Up.Load()
		sessDown = counter.Down.Load()
		// Fold in the not-yet-persisted portion of this session so the
		// displayed total grows in lockstep with session bytes. The
		// next persist run will move these bytes into appSettings and
		// bump LastPersisted — at which point the same display value
		// comes from the disk side and the delta drops to zero, so the
		// total never jumps.
		totalUp += sessUp - counter.LastPersistedUp.Load()
		totalDown += sessDown - counter.LastPersistedDown.Load()
	}
	return map[string]interface{}{
		"session_up":   sessUp,
		"session_down": sessDown,
		"total_up":     totalUp,
		"total_down":   totalDown,
		"enabled":      enabled,
	}
}

// startStatsTicker broadcasts the current traffic counters once per
// second while the session is alive. It also folds session deltas into
// the lifetime total every 30 seconds so a crash never loses more than
// half a minute of data. Final fold-in happens in stopConnectionLocked.
func startStatsTicker(cm *connManager) {
	go func() {
		t := time.NewTicker(1 * time.Second)
		defer t.Stop()
		var ticksSincePersist int
		for range t.C {
			cm.mu.Lock()
			ok := cm.state.Status == ConnConnected
			counter := cm.counter
			cm.mu.Unlock()
			if !ok || counter == nil {
				return
			}
			if !statsEnabled() {
				continue
			}
			state.broadcast(SSEEvent{Type: "stats_update", Payload: statsSnapshot(counter)})
			ticksSincePersist++
			if ticksSincePersist >= 30 {
				ticksSincePersist = 0
				persistCounterDelta(counter)
			}
		}
	}()
}

// persistCounterDelta folds the unpersisted portion of the session
// counter into the lifetime total and updates the LastPersisted markers
// so the same bytes aren't counted again. Safe to call from anywhere;
// no-op when there's nothing new.
func persistCounterDelta(counter *trafficCounter) {
	curUp := counter.Up.Load()
	curDown := counter.Down.Load()
	deltaUp := curUp - counter.LastPersistedUp.Load()
	deltaDown := curDown - counter.LastPersistedDown.Load()
	if deltaUp <= 0 && deltaDown <= 0 {
		return
	}
	settingsMu.Lock()
	if deltaUp > 0 {
		appSettings.StatsTotalUp += deltaUp
	}
	if deltaDown > 0 {
		appSettings.StatsTotalDown += deltaDown
	}
	settingsMu.Unlock()
	counter.LastPersistedUp.Store(curUp)
	counter.LastPersistedDown.Store(curDown)
	saveSettings()
}

// ─────────────────────────── ping / speed ────────────────────────

func measurePing(tr *http.Transport) (int64, error) {
	url := currentPingURL()
	wc := &http.Client{Transport: tr, Timeout: warmupTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	w, err := wc.Get(url)
	if err != nil {
		return -1, fmt.Errorf("warmup: %w", err)
	}
	io.Copy(io.Discard, w.Body)
	w.Body.Close()
	mc := &http.Client{Transport: tr, Timeout: pingTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var best int64 = -1
	var lastErr error
	for i := 0; i < pingRounds; i++ {
		start := time.Now()
		resp, e := mc.Get(url)
		if e != nil {
			lastErr = e
			if best > 0 {
				break
			}
			continue
		}
		ms := time.Since(start).Milliseconds()
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if best < 0 || ms < best {
			best = ms
		}
	}
	if best < 0 {
		if lastErr != nil {
			return -1, lastErr
		}
		return -1, fmt.Errorf("all measurements failed")
	}
	return best, nil
}

func measureSpeed(tr *http.Transport, onProgress func(float64)) (float64, error) {
	sc := &http.Client{Transport: tr, Timeout: speedDuration + 5*time.Second}
	req, err := http.NewRequest("GET", currentSpeedURL(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", speedUserAgent)
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := sc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 64*1024)
	var total int64
	start := time.Now()
	deadline := start.Add(speedDuration)
	lastReport := start
	for time.Now().Before(deadline) {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			total += int64(n)
		}
		now := time.Now()
		if onProgress != nil && now.Sub(lastReport) >= 400*time.Millisecond && total > 0 {
			onProgress(float64(total) / now.Sub(start).Seconds() / 1024 / 1024)
			lastReport = now
		}
		if rerr != nil {
			break
		}
	}
	if total == 0 {
		return 0, fmt.Errorf("no data received")
	}
	return float64(total) / time.Since(start).Seconds() / 1024 / 1024, nil
}

// ─────────────────────────── entry runners ───────────────────────

func runPingForEntry(entry *ConfigEntry) {
	p, err := parseVless(entry.Raw)
	if err != nil {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = err.Error()
		entry.mu.Unlock()
		return
	}
	ttl := startupTimeout + warmupTimeout + pingTimeout*time.Duration(pingRounds) + 3*time.Second
	if err = withXray(p, ttl, func(_ int, tr *http.Transport) error {
		delay, e := measurePing(tr)
		entry.mu.Lock()
		defer entry.mu.Unlock()
		if e != nil || delay < 0 {
			entry.PingStatus = StatusFailed
			entry.Delay = -1
			entry.PingErr = cleanPingErr(e)
		} else {
			entry.PingStatus = StatusOK
			entry.Delay = delay
			entry.PingErr = ""
		}
		return nil
	}); err != nil {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = shortErr(err.Error())
		entry.mu.Unlock()
	}
}

// runSpeedForEntry always does ping→speed in a single xray session.
// Per-row ⬇ speed button should re-measure ping even if already tested,
// because conditions may have changed and speed result is meaningless without fresh ping.
func runSpeedForEntry(entry *ConfigEntry, tabID string) {
	p, err := parseVless(entry.Raw)
	if err != nil {
		entry.mu.Lock()
		entry.SpeedStatus = StatusFailed
		entry.SpeedErr = err.Error()
		entry.mu.Unlock()
		return
	}
	ttl := startupTimeout + warmupTimeout + pingTimeout*time.Duration(pingRounds) + speedDuration + 10*time.Second
	if err = withXray(p, ttl, func(_ int, tr *http.Transport) error {
		// Always re-ping — gives fresh delay AND warms the tunnel for speed test
		delay, pingErr := measurePing(tr)
		entry.mu.Lock()
		if pingErr != nil || delay < 0 {
			entry.PingStatus = StatusFailed
			entry.Delay = -1
			entry.PingErr = cleanPingErr(pingErr)
			entry.SpeedStatus = StatusSkipped
			entry.SpeedErr = "ping failed"
			entry.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
			return nil
		}
		entry.PingStatus = StatusOK
		entry.Delay = delay
		entry.PingErr = ""
		entry.SpeedStatus = StatusTestingSpeed
		entry.SpeedLive = 0
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
		mbps, e := measureSpeed(tr, func(live float64) {
			entry.mu.Lock()
			entry.SpeedLive = live
			entry.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
		})
		entry.mu.Lock()
		defer entry.mu.Unlock()
		entry.SpeedLive = 0
		if e != nil {
			entry.SpeedStatus = StatusFailed
			entry.SpeedMBps = 0
			entry.SpeedErr = shortErr(e.Error())
		} else {
			entry.SpeedStatus = StatusOK
			entry.SpeedMBps = mbps
			entry.SpeedErr = ""
		}
		return nil
	}); err != nil {
		entry.mu.Lock()
		entry.SpeedStatus = StatusFailed
		entry.SpeedMBps = 0
		entry.SpeedLive = 0
		entry.SpeedErr = shortErr(err.Error())
		entry.mu.Unlock()
	}
}

func runPingAndSpeedForEntry(entry *ConfigEntry, tabID string) {
	p, err := parseVless(entry.Raw)
	if err != nil {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = err.Error()
		entry.SpeedStatus = StatusSkipped
		entry.SpeedErr = "parse error"
		entry.mu.Unlock()
		return
	}
	ttl := startupTimeout + warmupTimeout + pingTimeout*time.Duration(pingRounds) + speedDuration + 10*time.Second
	if err = withXray(p, ttl, func(_ int, tr *http.Transport) error {
		delay, pingErr := measurePing(tr)
		entry.mu.Lock()
		if pingErr != nil || delay < 0 {
			entry.PingStatus = StatusFailed
			entry.Delay = -1
			entry.PingErr = cleanPingErr(pingErr)
			entry.SpeedStatus = StatusSkipped
			entry.SpeedErr = "ping failed"
			entry.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
			return nil
		}
		entry.PingStatus = StatusOK
		entry.Delay = delay
		entry.PingErr = ""
		entry.SpeedStatus = StatusTestingSpeed
		entry.SpeedLive = 0
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
		mbps, sErr := measureSpeed(tr, func(live float64) {
			entry.mu.Lock()
			entry.SpeedLive = live
			entry.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
		})
		entry.mu.Lock()
		defer entry.mu.Unlock()
		entry.SpeedLive = 0
		if sErr != nil {
			entry.SpeedStatus = StatusFailed
			entry.SpeedMBps = 0
			entry.SpeedErr = shortErr(sErr.Error())
		} else {
			entry.SpeedStatus = StatusOK
			entry.SpeedMBps = mbps
			entry.SpeedErr = ""
		}
		return nil
	}); err != nil {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = shortErr(err.Error())
		entry.SpeedStatus = StatusSkipped
		entry.SpeedErr = "xray failed"
		entry.mu.Unlock()
	}
}

func cleanPingErr(e error) string {
	if e == nil {
		return "timeout"
	}
	s := e.Error()
	if strings.Contains(s, "context deadline") || strings.Contains(s, "timeout") {
		return "timeout"
	}
	return shortErr(s)
}

// ─────────────────────────── bulk runners ────────────────────────

var (
	pingCancelCh  chan struct{}
	speedCancelCh chan struct{}
	testMu        sync.Mutex
	testingTab    string // which tab is being tested
)

func cancelPingAll() {
	testMu.Lock()
	if pingCancelCh != nil {
		select {
		case <-pingCancelCh:
		default:
			close(pingCancelCh)
		}
	}
	testMu.Unlock()
}

func cancelSpeedAll() {
	testMu.Lock()
	if speedCancelCh != nil {
		select {
		case <-speedCancelCh:
		default:
			close(speedCancelCh)
		}
	}
	testMu.Unlock()
}

func isTestCancelled(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// runPingAll runs ping tests against entries. If `onlyIndices` is non-nil,
// only entries whose Index is in that set are tested (used for FILTER on UI:
// only visible configs are tested). Pass nil to test every entry.
func runPingAll(onlyIndices map[int]bool) {
	// If speed is running, cancel it only (don't start ping)
	if atomic.LoadInt32(&state.speedRunning) == 1 {
		cancelSpeedAll()
		return
	}
	// If ping is running, cancel it
	if atomic.LoadInt32(&state.pingRunning) == 1 {
		cancelPingAll()
		return
	}
	if !atomic.CompareAndSwapInt32(&state.pingRunning, 0, 1) {
		return
	}
	testMu.Lock()
	pingCancelCh = make(chan struct{})
	cancelCh := pingCancelCh
	testMu.Unlock()
	defer atomic.StoreInt32(&state.pingRunning, 0)
	state.mu.RLock()
	allEntries := make([]*ConfigEntry, len(state.entries))
	copy(allEntries, state.entries)
	tabID := state.activeTab
	state.mu.RUnlock()

	// Restrict to onlyIndices if provided.
	var entries []*ConfigEntry
	if onlyIndices != nil {
		for _, e := range allEntries {
			if onlyIndices[e.Index] {
				entries = append(entries, e)
			}
		}
	} else {
		entries = allEntries
	}

	testMu.Lock()
	testingTab = tabID
	testMu.Unlock()
	state.broadcast(SSEEvent{Type: "bulk_ping_start", Payload: len(entries), Tab: tabID})
	sem := make(chan struct{}, currentPingConcurrency())
	var wg sync.WaitGroup
	var done int64
	for _, e := range entries {
		if isTestCancelled(cancelCh) { break }
		wg.Add(1)
		go func(ent *ConfigEntry) {
			defer wg.Done()
			state.mu.RLock()
			cancelled := state.cancelledTabs[tabID]
			state.mu.RUnlock()
			if cancelled || isTestCancelled(cancelCh) {
				atomic.AddInt64(&done, 1)
				return
			}
			sem <- struct{}{}
			defer func() { <-sem }()
			if isTestCancelled(cancelCh) {
				atomic.AddInt64(&done, 1)
				return
			}
			ent.mu.Lock()
			ent.PingStatus = StatusTestingPing
			ent.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			runPingForEntry(ent)
			n := atomic.AddInt64(&done, 1)
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			state.broadcast(SSEEvent{Type: "bulk_ping_progress", Payload: map[string]interface{}{"done": n, "total": int64(len(entries))}, Tab: tabID})
		}(e)
	}
	wg.Wait()
	state.broadcast(SSEEvent{Type: "bulk_ping_done", Tab: tabID})
}

// runSpeedAll mirrors runPingAll: when onlyIndices is non-nil, only those
// entries are tested (for FILTER-aware testing).
func runSpeedAll(onlyIndices map[int]bool) {
	// If ping is running, cancel it only (don't start speed)
	if atomic.LoadInt32(&state.pingRunning) == 1 {
		cancelPingAll()
		return
	}
	// If speed is running, cancel it
	if atomic.LoadInt32(&state.speedRunning) == 1 {
		cancelSpeedAll()
		return
	}
	if !atomic.CompareAndSwapInt32(&state.speedRunning, 0, 1) {
		return
	}
	testMu.Lock()
	speedCancelCh = make(chan struct{})
	cancelCh := speedCancelCh
	testMu.Unlock()
	defer atomic.StoreInt32(&state.speedRunning, 0)
	state.mu.RLock()
	allEntries := make([]*ConfigEntry, len(state.entries))
	copy(allEntries, state.entries)
	tabID := state.activeTab
	state.mu.RUnlock()

	var entries []*ConfigEntry
	if onlyIndices != nil {
		for _, e := range allEntries {
			if onlyIndices[e.Index] {
				entries = append(entries, e)
			}
		}
	} else {
		entries = allEntries
	}

	testMu.Lock()
	testingTab = tabID
	testMu.Unlock()
	state.broadcast(SSEEvent{Type: "bulk_speed_start", Payload: len(entries), Tab: tabID})
	sem := make(chan struct{}, currentSpeedConcurrency())
	var wg sync.WaitGroup
	var done int64
	for _, e := range entries {
		if isTestCancelled(cancelCh) { break }
		wg.Add(1)
		go func(ent *ConfigEntry) {
			defer wg.Done()
			state.mu.RLock()
			cancelled := state.cancelledTabs[tabID]
			state.mu.RUnlock()
			if cancelled || isTestCancelled(cancelCh) {
				atomic.AddInt64(&done, 1)
				return
			}
			sem <- struct{}{}
			defer func() { <-sem }()
			if isTestCancelled(cancelCh) {
				atomic.AddInt64(&done, 1)
				return
			}
			ent.mu.Lock()
			ent.PingStatus = StatusTestingPing
			ent.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			runSpeedForEntry(ent, tabID)
			n := atomic.AddInt64(&done, 1)
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			state.broadcast(SSEEvent{Type: "bulk_speed_progress", Payload: map[string]interface{}{"done": n, "total": int64(len(entries))}, Tab: tabID})
		}(e)
	}
	wg.Wait()
	state.broadcast(SSEEvent{Type: "bulk_speed_done", Tab: tabID})
}


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
		// Check if any line actually contains vless://
		hasVless := false
		for _, l := range lines {
			if strings.Contains(l, "vless://") {
				hasVless = true
				break
			}
		}
		if !hasVless {
			fmt.Fprintf(os.Stderr, "⚠  fetch %s: no vless configs in response (%d lines)\n", src.URL, len(lines))
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

	// Get main tab's exclude filter
	state.mu.RLock()
	var excludeFilter []string
	for _, t := range state.tabs {
		if t.IsMain {
			excludeFilter = t.ExcludeFilter
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
		body := vlessBody(strings.TrimSpace(r))
		if body == "" || seen[body] {
			continue
		}
		seen[body] = true
		deduped = append(deduped, r)
	}

	entries := make([]*ConfigEntry, 0, len(deduped))
	for _, raw := range deduped {
		e := &ConfigEntry{Raw: raw, PingStatus: StatusPending, Delay: -1, SpeedStatus: StatusPending}
		p, parseErr := parseVless(raw)
		if parseErr != nil {
			e.Name = raw[:minInt(40, len(raw))]
			e.PingStatus = StatusFailed
			e.PingErr = parseErr.Error()
			e.SpeedStatus = StatusFailed
			e.SpeedErr = parseErr.Error()
		} else if shouldSkip(p.Name, excludeFilter) {
			continue
		} else {
			e.Name = p.Name
			e.Host = p.Host
			e.Port = p.Port
			e.Network = p.Network
			e.Security = p.Security
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
		if strings.HasPrefix(l, "vless://") {
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
		if strings.HasPrefix(l, "vless://") {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// parseConfigLines parses vless URLs from a single in-memory text blob.
// Used for URL responses and pasted text — kept around as a convenience
// wrapper over parseConfigReader.
func parseConfigLines(text string) []*ConfigEntry {
	return parseConfigReader(strings.NewReader(text))
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
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Extract vless:// URL from anywhere in the line
		idx := strings.Index(line, "vless://")
		if idx < 0 {
			continue
		}
		line = line[idx:]
		// Trim trailing garbage (spaces, markdown links etc)
		if sp := strings.IndexAny(line, " \t\r"); sp > 0 {
			line = line[:sp]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Dedup
		if seen[line] {
			continue
		}
		seen[line] = true
		e := &ConfigEntry{Raw: line, PingStatus: StatusPending, Delay: -1, SpeedStatus: StatusPending}
		p, parseErr := parseVless(line)
		if parseErr != nil {
			e.Name = line[:minInt(40, len(line))]
			e.PingStatus = StatusFailed
			e.PingErr = parseErr.Error()
			e.SpeedStatus = StatusFailed
			e.SpeedErr = parseErr.Error()
		} else {
			e.Name = p.Name
			e.Host = p.Host
			e.Port = p.Port
			e.Network = p.Network
			e.Security = p.Security
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ parseConfigReader: %v\n", err)
	}
	for i, e := range entries {
		e.Index = i
	}
	return entries
}

// vlessBody returns the part of a vless URL before the `#` fragment — i.e.
// the connection details (uuid@host:port?params) without the human name.
// Two configs with identical bodies but different names are functionally
// the same server/configuration, which is what dedup should compare on.
func vlessBody(raw string) string {
	// Use the LAST `#` since query strings can technically contain a `#`
	// when malformed, and we want everything up to the fragment marker.
	if i := strings.LastIndex(raw, "#"); i >= 0 {
		return raw[:i]
	}
	return raw
}

// dedupByBody removes entries whose vless body (everything before the
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
		body := vlessBody(e.Raw)
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

// setVlessName replaces the URL fragment of a vless URL with the given
// (decoded) name, percent-encoding it as needed. Used by disambiguateNames
// so that copying a row puts the disambiguated name on the clipboard
// (otherwise "anycast - 3" reverts to plain "anycast" on paste).
func setVlessName(raw, name string) string {
	encoded := url.PathEscape(name)
	if i := strings.LastIndex(raw, "#"); i >= 0 {
		return raw[:i+1] + encoded
	}
	return raw + "#" + encoded
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
				e.Raw = setVlessName(e.Raw, cand)
				taken[cand] = 1
				taken[base] = n + 1
				break
			}
		}
	}
}

// ─────────────────────────── HTTP handlers ───────────────────────

func handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	ch := make(chan SSEEvent, 128)
	state.addClient(ch)
	defer state.removeClient(ch)
	send := func(ev SSEEvent) { data, _ := json.Marshal(ev); fmt.Fprintf(w, "data: %s\n\n", data); flusher.Flush() }
	state.mu.RLock()
	if len(state.entries) > 0 {
		snaps := make([]ConfigEntry, len(state.entries))
		for i, e := range state.entries {
			snaps[i] = e.snap()
		}
		state.mu.RUnlock()
		send(SSEEvent{Type: "loaded", Payload: snaps})
	} else {
		state.mu.RUnlock()
	}
	send(SSEEvent{Type: "conn_update", Payload: state.conn.snap()})
	send(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	// Send initial stats so the freshly-loaded UI shows lifetime totals
	// without waiting for the first ticker pulse (which only fires while
	// connected). We pass the live counter if a session is in progress.
	state.conn.mu.Lock()
	initialCounter := state.conn.counter
	state.conn.mu.Unlock()
	send(SSEEvent{Type: "stats_update", Payload: statsSnapshot(initialCounter)})
	send(SSEEvent{Type: "app_info", Payload: map[string]interface{}{
		"singbox_available": state.singboxBin != "",
		"is_admin":          checkAdmin(),
		"os":                "windows",
	}})
	ctx := r.Context()
	for {
		select {
		case ev := <-ch:
			send(ev)
		case <-ctx.Done():
			return
		}
	}
}

func handleConnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	idx, err := strconv.Atoi(r.URL.Query().Get("idx"))
	if err != nil {
		http.Error(w, "bad idx", 400)
		return
	}
	mode := r.URL.Query().Get("mode")
	state.mu.RLock()
	if idx < 0 || idx >= len(state.entries) {
		state.mu.RUnlock()
		http.Error(w, "not found", 404)
		return
	}
	entry := state.entries[idx]
	state.mu.RUnlock()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
	if mode == "tun" {
		go startTUNConnection(entry)
	} else {
		go startProxyConnection(entry)
	}
}

func handleDisconnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	// Set visual state immediately, then start cleanup in background
	cm := state.conn
	cm.mu.Lock()
	if cm.state.Status != ConnIdle && cm.state.Status != ConnDisconnecting {
		cm.state.Status = ConnDisconnecting
	}
	s := cm.state
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: s})
	go stopConnection()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleConnState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(state.conn.snap())
}

func handlePingOne(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	idx, err := strconv.Atoi(r.URL.Query().Get("idx"))
	if err != nil {
		http.Error(w, "bad idx", 400)
		return
	}
	state.mu.RLock()
	if idx < 0 || idx >= len(state.entries) {
		state.mu.RUnlock()
		http.Error(w, "not found", 404)
		return
	}
	entry := state.entries[idx]
	tabID := state.activeTab
	state.mu.RUnlock()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
	go func() {
		entry.mu.Lock()
		entry.PingStatus = StatusTestingPing
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})

		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "⚠ ping test panic: %v\n", r)
				}
			}()
			runPingForEntry(entry)
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			fmt.Fprintf(os.Stderr, "⚠ ping test timeout for #%d\n", entry.Index)
		}

		entry.mu.Lock()
		if entry.PingStatus == StatusTestingPing {
			entry.PingStatus = StatusFailed
			entry.PingErr = "timeout"
			entry.Delay = -1
		}
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	}()
}

func handleSpeedOne(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	idx, err := strconv.Atoi(r.URL.Query().Get("idx"))
	if err != nil {
		http.Error(w, "bad idx", 400)
		return
	}
	state.mu.RLock()
	if idx < 0 || idx >= len(state.entries) {
		state.mu.RUnlock()
		http.Error(w, "not found", 404)
		return
	}
	entry := state.entries[idx]
	tabID := state.activeTab
	state.mu.RUnlock()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
	go func() {
		entry.mu.Lock()
		entry.SpeedStatus = StatusTestingSpeed
		entry.SpeedMBps   = 0
		entry.SpeedLive   = 0
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})

		// Hard timeout: if runSpeedForEntry doesn't return in 45s, force-fail.
		// This catches any possible deadlock/hang in xray, transport, or HTTP client.
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "⚠ speed test panic: %v\n", r)
				}
			}()
			runSpeedForEntry(entry, tabID)
		}()
		select {
		case <-done:
			// Normal completion
		case <-time.After(25 * time.Second):
			fmt.Fprintf(os.Stderr, "⚠ speed test timeout for #%d\n", entry.Index)
		}

		// Guarantee status is never stuck on "testing"
		entry.mu.Lock()
		if entry.SpeedStatus == StatusTestingSpeed {
			entry.SpeedStatus = StatusFailed
			entry.SpeedErr = "timeout"
			entry.SpeedLive = 0
		}
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	}()
}

// parseFilterIndices reads an optional JSON body of the form
// {"indices":[0,3,5,...]} and returns it as a map for fast membership checks.
// Returns nil if no body or the body is empty/invalid — caller treats nil as
// "test everything" for backwards compatibility.
func parseFilterIndices(r *http.Request) map[int]bool {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil || len(body) == 0 {
		return nil
	}
	var req struct {
		Indices []int `json:"indices"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	if req.Indices == nil {
		return nil
	}
	m := make(map[int]bool, len(req.Indices))
	for _, idx := range req.Indices {
		m[idx] = true
	}
	return m
}

func handlePingAll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	indices := parseFilterIndices(r)
	go runPingAll(indices)
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleSpeedAll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	indices := parseFilterIndices(r)
	go runSpeedAll(indices)
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleTestsCancel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	tabID := r.URL.Query().Get("tab")
	testMu.Lock()
	onThisTab := tabID == "" || testingTab == "" || testingTab == tabID
	testMu.Unlock()
	if !onThisTab {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
		return
	}
	if atomic.LoadInt32(&state.pingRunning) == 1 {
		cancelPingAll()
	}
	if atomic.LoadInt32(&state.speedRunning) == 1 {
		cancelSpeedAll()
	}
	// Wait for tests to actually finish before returning
	for i := 0; i < 70; i++ {
		if atomic.LoadInt32(&state.pingRunning) == 0 && atomic.LoadInt32(&state.speedRunning) == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleReload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	state.mu.RLock()
	tabID := state.activeTab
	var sourceURLs []string
	var sourceFiles []TabFile
	for _, t := range state.tabs {
		if t.ID == tabID {
			sourceURLs = t.SourceURLs
			sourceFiles = t.SourceFiles
			break
		}
	}
	state.mu.RUnlock()

	if tabID == "main" {
		go fetchAndInit()
	} else if len(sourceURLs) > 0 || len(sourceFiles) > 0 {
		go func() {
			state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: tabID})
			fetchTabURLs(tabID, sourceURLs, sourceFiles)
		}()
	} else {
		// No URL: reset all test results for this tab
		go func() {
			state.mu.Lock()
			entries := state.tabEntries[tabID]
			for _, e := range entries {
				e.mu.Lock()
				e.PingStatus = StatusPending
				e.Delay = -1
				e.PingErr = ""
				e.SpeedStatus = StatusPending
				e.SpeedMBps = 0
				e.SpeedLive = 0
				e.SpeedErr = ""
				e.mu.Unlock()
			}
			state.mu.Unlock()
			if state.activeTab == tabID {
				snaps := make([]ConfigEntry, len(entries))
				for i, e := range entries {
					snaps[i] = e.snap()
				}
				state.broadcast(SSEEvent{Type: "loaded", Payload: snaps, Tab: tabID})
			}
		}()
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// ─────────────────────────── tab handlers ───────────────────────

func handleTabCreate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	n := nextTabNumber()
	// Use timestamp in ID to prevent reuse of cancelled tab IDs.
	// Display name uses the sequential number for readability.
	tab := Tab{
		ID:       fmt.Sprintf("tab-%d-%d", n, time.Now().UnixMilli()),
		Name:     fmt.Sprintf("Tab %d", n),
		IsMain:   false,
		Closable: true,
	}
	state.mu.Lock()
	state.tabs = append(state.tabs, tab)
	state.tabEntries[tab.ID] = nil
	delete(state.cancelledTabs, tab.ID) // clear stale cancellation from deleted tab with same ID
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()
	json.NewEncoder(w).Encode(tab)
}

func handleTabDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	if id == "main" {
		http.Error(w, "cannot delete main tab", 400)
		return
	}
	state.mu.Lock()
	state.cancelledTabs[id] = true // signal running tests to stop
	for i, t := range state.tabs {
		if t.ID == id {
			state.tabs = append(state.tabs[:i], state.tabs[i+1:]...)
			delete(state.tabEntries, id)
			break
		}
	}
	if state.activeTab == id {
		state.activeTab = "main"
		state.entries = state.tabEntries["main"]
	}
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	state.mu.RLock()
	snaps := make([]ConfigEntry, len(state.entries))
	for i, e := range state.entries {
		snaps[i] = e.snap()
	}
	state.mu.RUnlock()
	state.broadcast(SSEEvent{Type: "active_tab", Payload: state.activeTab})
	state.broadcast(SSEEvent{Type: "loaded", Payload: snaps})
	saveTabs()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleTabSwitch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	state.mu.Lock()
	found := false
	for _, t := range state.tabs {
		if t.ID == id {
			found = true
			break
		}
	}
	if !found {
		state.mu.Unlock()
		http.Error(w, "tab not found", 404)
		return
	}
	state.activeTab = id
	entries := state.tabEntries[id]
	state.entries = entries
	state.mu.Unlock()
	snaps := make([]ConfigEntry, len(entries))
	for i, e := range entries {
		snaps[i] = e.snap()
	}
	state.broadcast(SSEEvent{Type: "active_tab", Payload: id})
	state.broadcast(SSEEvent{Type: "loaded", Payload: snaps})
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleTabPaste(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	if id == "" {
		id = state.activeTab
	}
	if id == "main" {
		http.Error(w, "cannot paste into main tab", 400)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	newEntries := parseConfigLines(string(body))
	state.mu.Lock()
	existing := state.tabEntries[id]
	baseIdx := len(existing)
	for i, e := range newEntries {
		e.Index = baseIdx + i
	}
	existing = append(existing, newEntries...)
	state.tabEntries[id] = existing
	if state.activeTab == id {
		state.entries = existing
	}
	state.mu.Unlock()
	if state.activeTab == id {
		snaps := make([]ConfigEntry, len(existing))
		for i, e := range existing {
			snaps[i] = e.snap()
		}
		state.broadcast(SSEEvent{Type: "loaded", Payload: snaps})
	}
	saveTabs()
	w.WriteHeader(200)
	fmt.Fprintf(w, `{"added":%d}`, len(newEntries))
}

func handleTabRename(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	name := r.URL.Query().Get("name")
	if id == "main" || name == "" {
		http.Error(w, "bad request", 400)
		return
	}
	state.mu.Lock()
	for i, t := range state.tabs {
		if t.ID == id {
			state.tabs[i].Name = name
			break
		}
	}
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleTabSetURL(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	type tabSettingsReq struct {
		URLs          []string  `json:"urls"`
		Files         []TabFile `json:"files"`
		RefreshMin    int       `json:"refresh_min"`
		ExcludeFilter []string  `json:"exclude_filter"`
		// New 3-state field. Legacy "dedup":true is accepted below for
		// older clients during the migration window.
		DedupMode     string    `json:"dedup_mode"`
		Dedup         bool      `json:"dedup"`
	}
	var req tabSettingsReq
	if r.Body != nil {
		// Body contains URLs, file metadata, and config arrays. Content
		// is read directly from disk, never sent over this endpoint.
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
		json.Unmarshal(body, &req)
	}
	// Normalize the mode. Empty or unrecognized → "" (off). Old `dedup: true`
	// payloads from any in-flight client get treated as "hide".
	newMode := req.DedupMode
	switch newMode {
	case "off":
		newMode = ""
	case "hide", "delete", "":
		// recognized
	default:
		newMode = ""
	}
	if newMode == "" && req.Dedup {
		newMode = "hide"
	}

	// Clean URLs
	var cleanURLs []string
	for _, u := range req.URLs {
		u = strings.TrimSpace(u)
		if u != "" {
			cleanURLs = append(cleanURLs, u)
		}
	}
	// Clean files: every file now needs a Path. Drag-drop is gone, so the
	// only way files enter the system is the native picker which always
	// provides a path. We stat each path here to refresh size/mtime for
	// display purposes; content is read lazily by fetchTabURLs.
	var cleanFiles []TabFile
	for _, f := range req.Files {
		f.Name = strings.TrimSpace(f.Name)
		f.Path = strings.TrimSpace(f.Path)
		if f.Path == "" {
			continue
		}
		if f.Name == "" {
			f.Name = filepath.Base(f.Path)
		}
		if info, err := os.Stat(f.Path); err == nil {
			f.Size = info.Size()
			f.Mtime = info.ModTime().Unix()
		}
		cleanFiles = append(cleanFiles, f)
	}

	var sourcesChanged bool
	var oldMode string
	state.mu.Lock()
	for i, t := range state.tabs {
		if t.ID == id {
			if !t.IsMain {
				oldURLs := strings.Join(t.SourceURLs, "|")
				newURLs := strings.Join(cleanURLs, "|")
				oldFilesKey := tabFilesKey(t.SourceFiles)
				newFilesKey := tabFilesKey(cleanFiles)
				sourcesChanged = (oldURLs != newURLs) || (oldFilesKey != newFilesKey)
				oldMode = t.DedupMode
				state.tabs[i].SourceURLs = cleanURLs
				state.tabs[i].SourceFiles = cleanFiles
				state.tabs[i].DedupMode = newMode
			}
			state.tabs[i].RefreshMin = req.RefreshMin
			state.tabs[i].ExcludeFilter = req.ExcludeFilter
			break
		}
	}
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()

	// "delete" mode requested AND mode has actually transitioned into delete
	// AND sources didn't change → apply server-side dedup to the current
	// entries in place. (When sources changed we re-fetch below, and that
	// path applies delete-dedup via fetchTabURLs.)
	if !sourcesChanged && id != "main" && newMode == "delete" && oldMode != "delete" {
		applyDeleteDedupInPlace(id)
		w.WriteHeader(200)
		w.Write([]byte("ok"))
		return
	}

	// Otherwise: dedup is purely a view-filter concern — toggling it must
	// not touch server entries, otherwise paste-only tabs (no URLs, no
	// files) lose every config the moment dedup is flipped. The client
	// already picked up the new dedup_mode via the tabs_update broadcast
	// above and re-rendered.
	if !sourcesChanged || id == "main" {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
		return
	}
	if len(cleanURLs) > 0 || len(cleanFiles) > 0 {
		// Clear old entries before fetching new ones (prevents stale data from removed sources)
		state.mu.Lock()
		state.tabEntries[id] = nil
		if state.activeTab == id {
			state.entries = nil
		}
		state.mu.Unlock()
		if state.activeTab == id {
			state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: id})
		}
		go fetchTabURLs(id, cleanURLs, cleanFiles)
	} else {
		// All sources removed — clear entries. We only reach here when
		// sourcesChanged is true AND there are no remaining sources;
		// paste-only tabs have sourcesChanged=false and exit above.
		state.mu.Lock()
		state.tabEntries[id] = nil
		if state.activeTab == id {
			state.entries = nil
		}
		state.mu.Unlock()
		if state.activeTab == id {
			state.broadcast(SSEEvent{Type: "loaded", Payload: []ConfigEntry{}, Tab: id})
		}
		saveTabs()
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// tabFilesKey produces a deterministic string for change-detection on a list
// of files. Two file lists are "the same set of sources" if their (name,path)
// pairs match in order. Size/mtime are intentionally NOT part of the key —
// content changes on disk are picked up by RELOAD, not by save.
func tabFilesKey(files []TabFile) string {
	var sb strings.Builder
	for _, f := range files {
		sb.WriteString(f.Name)
		sb.WriteByte('|')
		sb.WriteString(f.Path)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// fetchTabURLs fetches configs from multiple URLs and replaces tab entries.
// Files (already loaded content) are appended after URL contents in order.
//
// For each file with a Path, we re-read the file from disk before parsing
// so RELOAD picks up edits the user made outside of Vair. The freshly read
// content (and updated mtime) is written back into state.tabs so it gets
// persisted and the next reload starts from the same baseline.
func fetchTabURLs(tabID string, urls []string, files []TabFile) {
	var allEntries []*ConfigEntry
	for _, u := range urls {
		lines, err := fetchURL(u)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ tab %s fetch %s: %v\n", tabID, u, err)
			continue
		}
		entries := parseConfigLines(strings.Join(lines, "\n"))
		allEntries = append(allEntries, entries...)
	}
	// Read each file fresh from its on-disk path. Content lives in memory
	// only for the moment it takes parseConfigLines to walk through it —
	// after that it's eligible for GC. We refresh size/mtime on the way so
	// the UI can display current file metadata. A missing or unreadable
	// file just contributes zero entries this cycle (same behaviour URLs
	// have on fetch failure); the user can RELOAD again later.
	updatedFiles := make([]TabFile, len(files))
	copy(updatedFiles, files)
	for i := range updatedFiles {
		f := &updatedFiles[i]
		if f.Path == "" {
			fmt.Fprintf(os.Stderr, "⚠ tab %s: file %q has no path, skipping\n", tabID, f.Name)
			continue
		}
		// Refresh stat info for the UI/persistence (size, mtime).
		if info, statErr := os.Stat(f.Path); statErr == nil {
			f.Size = info.Size()
			f.Mtime = info.ModTime().Unix()
		}
		// Stream the file line by line. parseConfigFile uses bufio.Scanner
		// internally so peak memory is one line, not the whole file —
		// essential for the multi-hundred-MB subscription dumps some
		// users have. Each entry's Raw owns its own bytes, so the file
		// contents become eligible for GC the moment scanning finishes.
		entries, err := parseConfigFile(f.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ tab %s read %s: %v\n", tabID, f.Path, err)
			continue
		}
		allEntries = append(allEntries, entries...)
	}

	// If nothing fetched, keep existing entries
	if len(allEntries) == 0 {
		fmt.Fprintf(os.Stderr, "⚠ tab %s: no configs fetched, keeping existing\n", tabID)
		state.mu.RLock()
		cur := state.tabEntries[tabID]
		state.mu.RUnlock()
		if state.activeTab == tabID && cur != nil {
			snaps := make([]ConfigEntry, len(cur))
			for i, e := range cur {
				snaps[i] = e.snap()
			}
			state.broadcast(SSEEvent{Type: "loaded", Payload: snaps, Tab: tabID})
		}
		return
	}

	// Apply per-tab exclude filter and read dedup mode
	state.mu.RLock()
	var excludeFilter []string
	var dedupMode string
	for _, t := range state.tabs {
		if t.ID == tabID {
			excludeFilter = t.ExcludeFilter
			dedupMode = t.DedupMode
			break
		}
	}
	state.mu.RUnlock()

	if len(excludeFilter) > 0 {
		var filtered []*ConfigEntry
		for _, e := range allEntries {
			if !shouldSkip(e.Name, excludeFilter) {
				filtered = append(filtered, e)
			}
		}
		allEntries = filtered
	}

	// "delete" mode removes body-duplicates on the server side. "hide" is a
	// client-side view filter handled by matches() in the JS — server keeps
	// every entry in that case so toggling back to "off" / "hide" → "off"
	// restores everything instantly. "" / unset means no dedup at all.
	if dedupMode == "delete" {
		allEntries = dedupByBody(allEntries)
	}

	// Always: rename duplicate display names so each entry is uniquely
	// identifiable in the table — applied to every tab globally.
	disambiguateNames(allEntries)

	// Re-index
	for i, e := range allEntries {
		e.Index = i
	}
	state.mu.Lock()
	state.tabEntries[tabID] = allEntries
	// Persist any updated file content/mtime back to the tab so it survives
	// restart and the next RELOAD doesn't have to detect changes from
	// scratch.
	for i := range state.tabs {
		if state.tabs[i].ID == tabID {
			state.tabs[i].SourceFiles = updatedFiles
			break
		}
	}
	if state.activeTab == tabID {
		state.entries = allEntries
	}
	state.mu.Unlock()
	if state.activeTab == tabID {
		snaps := make([]ConfigEntry, len(allEntries))
		for i, e := range allEntries {
			snaps[i] = e.snap()
		}
		state.broadcast(SSEEvent{Type: "loaded", Payload: snaps, Tab: tabID})
	}
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()
	// Multi-hundred-MB subscription files can leave Go's heap several GB
	// large after parsing — the previous entries are unreferenced but the
	// runtime would normally hold that memory for future allocations. Hint
	// at returning it to the OS so Task Manager actually reflects the
	// drop. Doing this here (rather than on a timer) means we release
	// right after the big spike, when there's nothing else to allocate.
	debug.FreeOSMemory()
}

func handleTabDeleteEntries(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	if id == "main" {
		http.Error(w, "cannot delete from main tab", 400)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1*1024*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	var indices []int
	if err := json.Unmarshal(body, &indices); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), 400)
		return
	}
	toRemove := make(map[int]bool, len(indices))
	for _, idx := range indices {
		toRemove[idx] = true
	}
	state.mu.Lock()
	old := state.tabEntries[id]
	var kept []*ConfigEntry
	for _, e := range old {
		if !toRemove[e.Index] {
			kept = append(kept, e)
		}
	}
	// Re-index
	for i, e := range kept {
		e.Index = i
	}
	state.tabEntries[id] = kept
	if state.activeTab == id {
		state.entries = kept
	}
	state.mu.Unlock()
	if state.activeTab == id {
		snaps := make([]ConfigEntry, len(kept))
		for i, e := range kept {
			snaps[i] = e.snap()
		}
		state.broadcast(SSEEvent{Type: "loaded", Payload: snaps})
	}
	saveTabs()
	w.WriteHeader(200)
	fmt.Fprintf(w, `{"remaining":%d}`, len(kept))
}

func handleTabReorder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	var ids []string
	if err := json.Unmarshal(body, &ids); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), 400)
		return
	}
	state.mu.Lock()
	tabMap := make(map[string]Tab)
	for _, t := range state.tabs {
		tabMap[t.ID] = t
	}
	newTabs := make([]Tab, 0, len(ids))
	for _, id := range ids {
		if t, ok := tabMap[id]; ok {
			newTabs = append(newTabs, t)
			delete(tabMap, id)
		}
	}
	// Append any remaining tabs not in the reorder list
	for _, t := range state.tabs {
		if _, ok := tabMap[t.ID]; ok {
			newTabs = append(newTabs, t)
		}
	}
	state.tabs = newTabs
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// ─────────────────────────── embedded UI ─────────────────────────

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Vair</title>
<style>
@import url('https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500;700&display=swap');
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
:root{
  --bg:#0c0c0c;--bg2:#111;--bg3:#181818;--bg4:#1e1e1e;
  --border:#222;--border2:#2a2a2a;
  --text:#c8c8c8;--dim:#4a4a4a;--dim2:#303030;
  --accent:#e8c547;--green:#4ade80;--red:#f87171;
  --blue:#60a5fa;--orange:#fb923c;--purple:#c084fc;--teal:#2dd4bf;
  --font:'JetBrains Mono',Consolas,monospace;
}
html,body{height:100%;background:var(--bg);color:var(--text);font-family:var(--font);font-size:13px;line-height:1.4;user-select:none;-webkit-user-select:none}
body{display:flex;flex-direction:column;height:100vh;overflow:hidden}
input,textarea{user-select:text;-webkit-user-select:text}

/* ── connection bar ── */
#conn-bar{
  flex-shrink:0;border-top:2px solid var(--border);
  padding:0 16px;display:flex;align-items:center;gap:10px;flex-wrap:nowrap;
  background:var(--bg2);transition:background .3s,border-color .3s;
  min-height:38px;overflow:visible;
}
#conn-bar.cp{background:#071a09;border-top-color:#1a3a20}
#conn-bar.conn-tun{background:#060f1f;border-top-color:#1a2a40}
#conn-bar.cc{background:#12120a;border-top-color:#2a2a12}
#conn-bar.ce{background:#1a0707;border-top-color:#3a1212}

.cdot{width:8px;height:8px;min-width:8px;max-width:8px;border-radius:50%;flex-shrink:0;align-self:center;background:var(--dim2);transition:background .3s}
.cdot.cp{background:var(--green);animation:pulse 2s infinite}
.cdot.conn-tun{background:var(--blue);animation:pulse 2s infinite}
.cdot.cc{background:var(--accent);animation:blink .7s infinite}
.cdot.ce{background:var(--red)}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.4}}

#clabel{font-size:11px;font-weight:700;letter-spacing:.07em;color:var(--dim);white-space:nowrap;min-width:88px;flex-shrink:0}
#clabel.cp{color:var(--green)}
#clabel.conn-tun{color:var(--blue)}
#clabel.cc{color:var(--accent)}
#clabel.ce{color:var(--red)}
#cdetail{font-size:11px;color:var(--dim);flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0}
#cdetail span{color:var(--text)}
#cports{display:none;gap:6px;align-items:center;flex-shrink:0}
.pchip{
  background:var(--bg3);border:1px solid var(--border2);border-radius:3px;
  padding:2px 7px;font-size:10px;cursor:pointer;transition:border-color .15s;white-space:nowrap;
}
.pchip:hover{border-color:var(--accent)}.pchip b{color:var(--accent)}


/* mode pills */
.mode-wrap{display:flex;gap:4px;align-items:center;flex-shrink:0;white-space:nowrap}
.mpill{
  all:unset;cursor:pointer;font-family:var(--font);font-size:10px;font-weight:700;
  padding:3px 10px;border-radius:3px;text-transform:uppercase;letter-spacing:.07em;
  border:1px solid var(--border2);color:var(--dim);transition:all .15s;white-space:nowrap;
}
.mpill:hover{color:var(--text);border-color:var(--dim)}
.mpill.sel-proxy{color:var(--green);border-color:var(--green);background:rgba(74,222,128,.08)}
.mpill.sel-tun  {color:var(--blue); border-color:var(--blue); background:rgba(96,165,250,.08)}
.mpill.off{opacity:1;cursor:pointer;color:var(--dim);border-color:var(--border2)}
.mpill.off:hover{color:var(--text);border-color:var(--dim)}
.mode-wrap .mtip{
  font-size:10px;color:var(--accent);border:1px solid rgba(232,197,71,.3);border-radius:3px;
  padding:2px 7px;background:rgba(232,197,71,.06);white-space:nowrap;cursor:pointer;
  transition:all .15s;
}
.mode-wrap .mtip:hover{background:rgba(232,197,71,.12);border-color:rgba(232,197,71,.5)}

/* header */
header{
  flex-shrink:0;background:var(--bg2);
  padding:9px 16px;display:flex;align-items:center;gap:14px;flex-wrap:wrap;
}
.stats{display:flex;gap:14px;align-items:center}
.stat{display:flex;flex-direction:column;align-items:flex-end;gap:1px}
.sv{font-size:16px;font-weight:700;line-height:1}
.sl{font-size:9px;color:var(--dim);text-transform:uppercase;letter-spacing:.1em}
.sv.ok{color:var(--green)}.sv.err{color:var(--red)}.sv.ms{color:var(--accent)}.sv.sp{color:var(--teal)}
.sv.tx{color:var(--teal);font-variant-numeric:tabular-nums;font-size:12px;line-height:1.1}
.stat.traf{margin-left:8px;padding-left:14px;border-left:1px solid var(--border2)}
.stat.traf .sv{white-space:nowrap}
.spacer{flex:1}
.ctrls{display:flex;gap:5px;align-items:center;flex-wrap:wrap}
.btn{
  all:unset;cursor:pointer;font-family:var(--font);font-size:11px;font-weight:700;
  padding:5px 11px;border-radius:3px;letter-spacing:.07em;text-transform:uppercase;
  display:inline-flex;align-items:center;gap:5px;transition:opacity .15s;white-space:nowrap;
  border:1px solid transparent;
}
.btn:hover{opacity:.8}.btn:active{opacity:.55}
.btn.ghost  {background:transparent;         color:var(--text);  border-color:var(--border2)}
.btn.red    {background:rgba(248,113,113,.12);color:var(--red);  border-color:rgba(248,113,113,.35)}
.btn.sm     {padding:3px 8px;font-size:10px;letter-spacing:.05em}
.btn.sm-disc{padding:3px 9px;font-size:10px;background:rgba(248,113,113,.1);color:var(--red);border-color:rgba(248,113,113,.3)}
.btn:disabled{opacity:.22;pointer-events:none}
.btn.dim-btn{opacity:.4;color:var(--dim)}
.btn.dim-btn:hover{opacity:.6}

.prog-area{flex-shrink:0}
.pbar-row{height:2px;background:var(--dim2);position:relative}
.pbar-fill{height:100%;transition:width .22s ease;width:0;position:absolute;inset:0}
.pbar-ping{background:var(--accent)}

/* tabs */
.tab-bar{display:flex;gap:2px;align-items:center;flex-shrink:0;overflow-x:auto;max-width:55%}
.tab-btn{
  all:unset;cursor:pointer;font-family:var(--font);font-size:10px;font-weight:700;
  padding:3px 7px;border-radius:3px;color:var(--dim);border:1px solid var(--border2);
  transition:all .15s;white-space:nowrap;text-transform:uppercase;letter-spacing:.05em;
  display:inline-flex;align-items:center;gap:3px;
}
.tab-btn:hover{color:var(--text);border-color:var(--dim)}
.tab-btn.active{color:var(--accent);border-color:var(--accent)}
.tab-add{
  all:unset;cursor:pointer;font-family:var(--font);font-size:10px;font-weight:700;
  padding:3px 0;min-width:20px;display:flex;align-items:center;justify-content:center;
  border-radius:3px;color:var(--dim);border:1px solid var(--border2);transition:all .15s;flex-shrink:0;
}
.tab-add:hover{color:var(--accent);border-color:var(--accent)}
.tab-btn.dragging{opacity:.4}
.tab-btn.drag-over{border-color:var(--accent);box-shadow:inset 0 0 0 1px var(--accent)}

/* tab context menu */
.ctx-menu{
  position:fixed;z-index:9999;background:var(--bg3);border:1px solid var(--border2);
  border-radius:5px;padding:4px 0;min-width:180px;box-shadow:0 6px 20px rgba(0,0,0,.5);
  font-family:var(--font);font-size:11px;
}
.ctx-menu-item{
  padding:6px 14px;color:var(--text);cursor:pointer;transition:background .08s;
  display:flex;align-items:center;gap:8px;
}
.ctx-menu-item:hover{background:var(--bg4)}
.ctx-menu-item.danger{color:var(--red)}
.ctx-menu-item.danger:hover{background:rgba(248,113,113,.1)}
.ctx-sep{height:1px;background:var(--border2);margin:3px 0}

/* tab settings modal */
.modal-overlay{
  position:fixed;inset:0;z-index:9998;background:rgba(0,0,0,.55);
  display:flex;align-items:center;justify-content:center;
}
.modal-box{
  background:var(--bg2);border:1px solid var(--border2);border-radius:6px;
  padding:18px 22px;min-width:360px;max-width:90vw;
  /* Cap to viewport so long settings don't push the close button off-screen.
     Falling back to scroll keeps the close button reachable on any window
     size — including small mobile-style aspect ratios. */
  max-height:85vh;overflow-y:auto;
}
.modal-title{font-size:13px;font-weight:700;color:var(--accent);margin-bottom:14px;text-transform:uppercase;letter-spacing:.06em}
.modal-label{font-size:10px;color:var(--dim);text-transform:uppercase;letter-spacing:.08em;margin-bottom:4px}
.modal-input{
  width:100%;background:var(--bg3);border:1px solid var(--border2);border-radius:3px;
  color:var(--text);font-family:var(--font);font-size:12px;padding:6px 10px;outline:none;
  margin-bottom:12px;
}
.modal-input:focus{border-color:var(--accent)}
.modal-btns{display:flex;gap:8px;justify-content:flex-end;margin-top:6px}
.modal-row{display:flex;align-items:center;justify-content:space-between;margin-bottom:12px}
.modal-row-label{font-size:11px;color:var(--text)}
.toggle{position:relative;width:36px;height:20px;cursor:pointer;flex-shrink:0}
.toggle input{display:none}
.toggle-track{position:absolute;inset:0;background:var(--dim2);border-radius:10px;transition:.2s}
.toggle input:checked+.toggle-track{background:var(--accent)}
.toggle-thumb{position:absolute;top:2px;left:2px;width:16px;height:16px;background:#fff;border-radius:50%;transition:.2s}
.toggle input:checked~.toggle-thumb{left:18px}
.chips-wrap{display:flex;flex-wrap:wrap;gap:4px;margin-bottom:10px;min-height:26px;padding:4px 6px;background:var(--bg3);border:1px solid var(--border2);border-radius:3px}
.chip{display:inline-flex;align-items:center;gap:3px;font-size:10px;font-weight:700;
  padding:2px 6px 2px 8px;border-radius:99px;background:rgba(232,197,71,.12);color:var(--accent);border:1px solid rgba(232,197,71,.3)}
.chip-x{cursor:pointer;opacity:.5;font-size:9px}.chip-x:hover{opacity:1;color:var(--red)}
.chip-input{border:0;background:transparent;color:var(--text);font-family:var(--font);font-size:11px;outline:none;flex:1;min-width:80px}
.modal-hint{font-size:9px;color:var(--dim);margin-top:-6px;margin-bottom:10px}
.settings-section{margin-bottom:16px;padding-bottom:12px;border-bottom:1px solid var(--border2)}
.settings-section:last-child{border-bottom:0;margin-bottom:0;padding-bottom:0}
.section-header{font-size:11px;font-weight:700;color:var(--accent);text-transform:uppercase;letter-spacing:.08em;margin-bottom:10px}

/* Compact numeric input used in Settings (concurrency, refresh interval) */
.num-input{
  width:70px;margin-bottom:0;padding:4px 8px;font-size:11px;text-align:right;
}
/* hide spinners on Firefox / WebView2 - they look out of place in this UI */
.num-input::-webkit-inner-spin-button,.num-input::-webkit-outer-spin-button{
  -webkit-appearance:none;margin:0;
}
.num-input{-moz-appearance:textfield}

/* Segmented selector (Off / Hide / Delete for dedup mode). Each button is
 * a standalone ghost-style chip — same border/typography as the add-url /
 * add-file buttons in the same modal. The active option lights up amber
 * (text + border), mirroring the active-tab treatment in .tab-btn.active.
 * A small left margin keeps the group from crowding the row label. */
.seg-group{
  display:inline-flex;gap:4px;margin-left:12px;
}
.seg-btn{
  all:unset;cursor:pointer;font-family:var(--font);font-size:10px;font-weight:700;
  padding:3px 7px;border-radius:3px;
  color:var(--dim);border:1px solid var(--border2);
  text-transform:uppercase;letter-spacing:.05em;
  transition:color .15s, border-color .15s;
}
.seg-btn:hover:not(.active){color:var(--text);border-color:var(--dim)}
.seg-btn.active{color:var(--accent);border-color:var(--accent)}
.url-row{display:flex;gap:4px;margin-bottom:4px;align-items:center}
.url-row .modal-input{flex:1;margin-bottom:0}
.url-rm{all:unset;cursor:pointer;color:var(--dim);font-size:12px;width:18px;height:18px;display:flex;align-items:center;justify-content:center;border-radius:3px}
.url-rm:hover{color:var(--red);background:rgba(248,113,113,.1)}

/* file rows in tab settings */
.file-row{
  display:flex;gap:6px;margin-bottom:4px;align-items:center;
  background:var(--bg3);border:1px solid var(--border2);border-radius:3px;
  padding:5px 10px;font-size:11px;color:var(--text);
}
.file-row .file-ico{color:var(--dim);flex-shrink:0;font-size:12px}
.file-row .file-name{flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.file-row .file-size{color:var(--dim);font-size:10px;flex-shrink:0;margin-right:2px}
.file-row.new{border-color:rgba(74,222,128,.35);background:rgba(74,222,128,.05)}

/* row selection */
tbody tr.selected{background:rgba(232,197,71,.08)!important;box-shadow:inset 3px 0 0 var(--accent)}

.toolbar{
  flex-shrink:0;background:var(--bg2);border-bottom:1px solid var(--border);
  padding:5px 16px;display:flex;align-items:center;gap:10px;flex-wrap:wrap;
}
.tl{font-size:10px;color:var(--dim);text-transform:uppercase;letter-spacing:.1em;white-space:nowrap}
.finput{
  background:var(--bg3);border:1px solid var(--border2);border-radius:3px;
  color:var(--text);font-family:var(--font);font-size:12px;padding:3px 9px;width:180px;outline:none;
}
.finput:focus{border-color:var(--accent)}
.sort-group{display:flex;gap:4px}
.sort-btn{
  all:unset;cursor:pointer;font-family:var(--font);font-size:10px;
  padding:2px 8px;border-radius:2px;color:var(--dim);border:1px solid var(--border2);transition:all .15s;
}
.sort-btn:hover{color:var(--text);border-color:var(--dim)}
.sort-btn.active{color:var(--accent);border-color:var(--accent)}

.tw{flex:1;overflow-y:auto}
table{width:100%;border-collapse:collapse;table-layout:fixed}
/* virtual scroll spacer rows — no borders, no hover */
tbody tr.vspacer{background:transparent!important;border:0!important;pointer-events:none}
tbody tr.vspacer:hover{background:transparent!important}
tbody tr.vspacer td{border:0!important;padding:0!important}
thead{position:sticky;top:0;z-index:10;background:var(--bg2)}
thead th{
  padding:6px 10px;text-align:left;font-size:9px;text-transform:uppercase;
  letter-spacing:.12em;color:var(--dim);border-bottom:1px solid var(--border);white-space:nowrap;
}
thead th.ct {text-align:center}
thead th.cs {text-align:center}
thead th.cp2{text-align:center}
thead th.csp{text-align:center}
thead th.ca {text-align:center}
tbody tr{border-bottom:1px solid var(--border);transition:background .08s}
tbody tr:hover{background:var(--bg3)}
tbody tr.row-cp{background:#071a09!important;box-shadow:inset 3px 0 0 var(--green)}
tbody tr.row-ct{background:#060f1f!important;box-shadow:inset 3px 0 0 var(--blue)}
td{padding:5px 10px;vertical-align:middle;font-size:12px}
.ci{width:38px;color:var(--dim);font-size:11px;text-align:left}
.cn{min-width:150px;max-width:210px;text-align:left}
.ch{max-width:155px;text-align:left}.ct{width:88px;text-align:center}.cs{width:72px;text-align:center}
.cp2{width:110px;text-align:center}
.csp{width:130px;text-align:center}
.ca{width:220px;text-align:right}

.nc{display:flex;flex-direction:column;gap:2px}
.nm{white-space:nowrap;overflow:hidden;text-overflow:ellipsis;color:var(--text)}
.nh{color:var(--dim);font-size:10px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.et{font-size:10px;color:var(--red);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:205px}

.vc{display:flex;align-items:center;justify-content:center;gap:5px}
.pill{font-size:10px;font-weight:700;padding:2px 7px;border-radius:99px;min-width:66px;max-width:110px;text-align:center;letter-spacing:.02em;border:1px solid;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.pill.pending  {color:var(--dim);border-color:var(--dim2);background:transparent}
.pill.tp       {color:var(--orange);border-color:rgba(251,146,60,.4);background:rgba(251,146,60,.07);animation:blink .9s infinite}
.pill.ts       {color:var(--teal);border-color:rgba(45,212,191,.4);background:rgba(45,212,191,.07);animation:blink .9s infinite}

.pill.ok-fast  {color:var(--green); border-color:rgba(74,222,128,.4);  background:rgba(74,222,128,.08)}
.pill.ok-ping  {color:var(--accent);border-color:rgba(232,197,71,.4);  background:rgba(232,197,71,.07)}
.pill.ok-speed {color:var(--teal);border-color:rgba(45,212,191,.3);background:rgba(45,212,191,.07)}
.pill.failed   {color:var(--red);border-color:rgba(248,113,113,.3);background:rgba(248,113,113,.07)}
.pill.skipped  {color:var(--dim);border-color:var(--dim2);background:transparent;font-style:italic}
@keyframes blink{0%,100%{opacity:1}50%{opacity:.3}}

.nb{font-size:10px;padding:1px 5px;border-radius:2px;border:1px solid var(--border);color:var(--dim)}
.nb.ws{color:#818cf8;border-color:rgba(129,140,248,.4)}
.nb.grpc{color:var(--orange);border-color:rgba(251,146,60,.4)}
.nb.h2{color:var(--blue);border-color:rgba(96,165,250,.4)}
.nb.tcp{color:var(--green);border-color:rgba(74,222,128,.4)}
.nb.httpupgrade,.nb.splithttp,.nb.xhttp{color:var(--purple);border-color:rgba(192,132,252,.4)}
.sb{font-size:10px;padding:1px 5px;border-radius:2px;color:var(--dim)}
.sb.tls{color:var(--blue)}.sb.reality{color:var(--purple);font-weight:700}

.act-cell{display:flex;gap:3px;align-items:center;justify-content:flex-end}
.cpb{all:unset;cursor:pointer;color:var(--dim);font-size:12px;padding:2px 4px;border-radius:2px;transition:color .12s;display:inline-flex}
.cpb:hover{color:var(--accent)}.cpb.done{color:var(--green)}


.center-msg{display:flex;flex-direction:column;align-items:center;justify-content:center;gap:14px;height:100%;color:var(--dim)}
.center-msg .ico{font-size:40px;line-height:1}
.spinner{width:18px;height:18px;border:2px solid var(--dim2);border-top-color:var(--accent);border-radius:50%;animation:spin .7s linear infinite;display:inline-block}
@keyframes spin{to{transform:rotate(360deg)}}

::-webkit-scrollbar{width:5px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:var(--dim2);border-radius:3px}
::-webkit-scrollbar-thumb:hover{background:var(--dim)}



/* ── Custom title bar (standalone frameless mode) ─────────────────────────── */
#titlebar{
  display:none;flex-shrink:0;height:36px;
  background:var(--bg);
  border-bottom:1px solid var(--border);
  align-items:stretch;
  user-select:none;-webkit-user-select:none;
  -webkit-app-region:drag;
  position:relative;
}
#titlebar.active{display:flex}
.tb-drag{
  flex:1;display:flex;align-items:center;padding:0 10px;gap:7px;
  -webkit-app-region:drag;cursor:default;
}
.tb-logo{
  width:34px;height:34px;flex-shrink:0;
  display:flex;align-items:center;justify-content:center;
}
.tb-logo img{width:32px;height:32px;object-fit:contain;display:block;pointer-events:none;image-rendering:crisp-edges;}
.tb-appname{
  font-size:13px;font-weight:700;color:var(--dim);
  letter-spacing:.05em;white-space:nowrap;
}
.tb-btns{
  display:flex;align-items:stretch;flex-shrink:0;
  -webkit-app-region:no-drag;
}
.tb-btn{
  all:unset;
  width:40px;height:36px;
  display:flex;align-items:center;justify-content:center;
  cursor:default;color:var(--dim);
  transition:background .1s,color .1s;
  font-size:14px;line-height:1;
}
.tb-btn:hover{background:var(--bg4);color:var(--text)}
.tb-btn.tb-close:hover{background:#c42b1c;color:#fff}
.tb-btn svg{pointer-events:none;display:block}

</style>
</head>
<body>

<!-- ── Custom title bar (standalone/frameless mode) ── -->
<div id="titlebar">
  <div class="tb-drag" id="tb-drag">
    <div class="tb-logo"><img id="tb-logo-img" src="" alt=""></div>
    <span class="tb-appname">Vair</span>
  </div>
  <div class="tb-btns">
    <button class="tb-btn" id="tb-min" title="Minimize">
      <svg width="10" height="1" viewBox="0 0 10 1"><rect width="10" height="1" fill="currentColor"/></svg>
    </button>
    <button class="tb-btn" id="tb-max" title="Maximize">
      <svg width="9" height="9" viewBox="0 0 9 9"><rect x="0.5" y="0.5" width="8" height="8" fill="none" stroke="currentColor" stroke-width="1"/></svg>
    </button>
    <button class="tb-btn tb-close" id="tb-close" title="Close">
      <svg width="10" height="10" viewBox="0 0 10 10"><line x1="0" y1="0" x2="10" y2="10" stroke="currentColor" stroke-width="1.2"/><line x1="10" y1="0" x2="0" y2="10" stroke="currentColor" stroke-width="1.2"/></svg>
    </button>
  </div>
</div>

<!-- ── Header ── -->
<header>
  <div class="stats">
    <div class="stat"><span class="sv" id="s-tot">0</span><span class="sl">configs</span></div>
    <div class="stat"><span class="sv ok" id="s-ok">0</span><span class="sl">ping ok</span></div>
    <div class="stat"><span class="sv err" id="s-er">0</span><span class="sl">failed</span></div>
    <div class="stat"><span class="sv ms" id="s-ms">—</span><span class="sl">best ping</span></div>
    <div class="stat"><span class="sv sp" id="s-sp">—</span><span class="sl">best speed</span></div>
    <div class="stat traf" id="stat-session" style="display:none"><span class="sv tx" id="s-sess">↑0 ↓0</span><span class="sl">session</span></div>
    <div class="stat traf" id="stat-total"><span class="sv tx" id="s-totl">↑0 ↓0</span><span class="sl">total</span></div>
  </div>
  <div class="spacer"></div>
  <div class="ctrls">
    <button class="btn ghost" id="btn-settings" onclick="openSettings()" title="Settings">&#9881;</button>
    <button class="btn ghost" id="btn-reload"    onclick="doReload()">reload</button>
    <button class="btn ghost" id="btn-ping-all"  onclick="doPingAll()">ping all</button>
    <button class="btn ghost"  id="btn-speed-all" onclick="doSpeedAll()">speed all</button>
  </div>
</header>

<div class="prog-area">
  <div class="pbar-row"><div class="pbar-fill pbar-ping"  id="pb-main"></div></div>
</div>

<div class="toolbar">
  <div class="tab-bar" id="tab-bar">
    <button class="tab-btn active" data-id="main" onclick="switchTab('main')">Sources</button>
    <button class="tab-add" onclick="addTab()" title="New tab (Ctrl+V to paste configs)">+</button>
  </div>
  <div class="spacer"></div>
  <span class="tl">filter</span>
  <input class="finput" id="fi" placeholder="name / host / type…" oninput="applyFilter()">
  <span id="fc" style="font-size:11px;color:var(--dim)"></span>
  <span class="tl" style="margin-left:6px">sort</span>
  <div class="sort-group">
    <button class="sort-btn active" id="sort-idx"   onclick="setSort('idx')">default</button>
    <button class="sort-btn"        id="sort-ping"  onclick="setSort('ping')">ping ↑</button>
    <button class="sort-btn"        id="sort-speed" onclick="setSort('speed')">speed ↓</button>
  </div>
</div>

<div class="tw">
  <div class="center-msg" id="msg-area">
    <div class="ico"><span class="spinner"></span></div>
    <p>Loading configs…</p>
  </div>
  <table id="tbl" style="display:none">
    <thead><tr>
      <th class="ci">#</th>
      <th class="cn">Name</th>
      <th class="ch">Host</th>
      <th class="ct">Transport</th>
      <th class="cs">Security</th>
      <th class="cp2">Ping</th>
      <th class="csp">Speed</th>
      <th class="ca">Actions</th>
    </tr></thead>
    <tbody id="tb"></tbody>
  </table>
</div>

<!-- ── Connection Bar (bottom) ── -->
<div id="conn-bar">
  <div class="cdot" id="cdot"></div>
  <span id="clabel">DISCONNECTED</span>
  <span id="cdetail" style="flex:1;color:var(--dim)"></span>
  <div id="cports"></div>

  <!-- Mode selector (always visible) -->
  <div class="mode-wrap" id="mode-wrap">
    <button class="mpill sel-proxy" id="mp-proxy" onclick="setMode('proxy')">proxy</button>
    <button class="mpill"           id="mp-tun"   onclick="setMode('tun')">tun</button>
    <span class="mtip" id="mtip" style="display:none"></span>
  </div>

  <button class="btn red" id="btn-dc" onclick="doDisconnect()" style="display:none">disconnect</button>
</div>

<script>
// ── state ──────────────────────────────────────────────────────────
const entries={};
let sortMode='idx', filterText='';
let connState={status:'idle',entry_index:-1,mode:'proxy'};
let appInfo={singbox_available:false,is_admin:false,os:'windows'};
let appSettingsJS={sources_enabled:true, ru_sites_direct:false, direct_domains:[], direct_apps:[], tray_enabled:false, ping_concurrency:10, speed_concurrency:5, ping_test_url:'', speed_test_url:'', tun_mtu:0, stats_disabled:false, stats_total_up:0, stats_total_down:0};
let selectedMode='proxy';

// ── SSE ───────────────────────────────────────────────────────────
const es=new EventSource('/api/stream');
es.onmessage=e=>{
  const ev=JSON.parse(e.data);
  // Filter tab-scoped events: ignore if event belongs to a different tab
  const evTab=ev.tab||'';
  const tabMatch=!evTab||evTab===activeTabId;
  if     (ev.type==='loading'&&tabMatch)             onLoading();
  else if(ev.type==='loaded'&&tabMatch)              onLoaded(ev.payload);
  else if(ev.type==='entry_update'&&tabMatch)        onUpdate(ev.payload);
  else if(ev.type==='conn_update')                   onConnUpdate(ev.payload);
  else if(ev.type==='stats_update')                   onStatsUpdate(ev.payload);
  else if(ev.type==='app_info')                      onAppInfo(ev.payload);
  else if(ev.type==='bulk_ping_start')               startBulk('ping',ev.payload,evTab);
  else if(ev.type==='bulk_ping_progress'&&tabMatch)  progBulk('ping',ev.payload);
  else if(ev.type==='bulk_ping_done')                doneBulk('ping','ping all','btn-ping-all','ghost',evTab);
  else if(ev.type==='bulk_speed_start')              startBulk('speed',ev.payload,evTab);
  else if(ev.type==='bulk_speed_progress'&&tabMatch) progBulk('speed',ev.payload);
  else if(ev.type==='bulk_speed_done')               doneBulk('speed','speed all','btn-speed-all','ghost',evTab);
  else if(ev.type==='tabs_update')                   onTabsUpdate(ev.payload);
  else if(ev.type==='active_tab')                    onActiveTab(ev.payload);
};
loadAppSettings();

// ── app info → update mode pills ──────────────────────────────────
function onAppInfo(info){
  appInfo=info;
  const tunBtn=document.getElementById('mp-tun');
  const tip=document.getElementById('mtip');
  const tunOk=info.singbox_available&&info.is_admin;
  if(!tunOk){
    tunBtn.classList.add('off');
    tip.style.display='';
    if(!info.singbox_available){
      tip.textContent='sing-box not found';
      tip.onclick=null;
      tip.style.cursor='default';
    } else {
      tip.textContent='requires admin ↗';
      tip.onclick=function(){ fetch('/api/restart-admin',{method:'POST'}); };
      tip.title='Restart Vair as Administrator';
    }
  } else {
    tunBtn.classList.remove('off');
    tip.style.display='none';
  }
  rebuildTable();
}

function setMode(m){
  if(m==='tun'&&(!appInfo.singbox_available||!appInfo.is_admin)){
    if(appInfo.singbox_available&&!appInfo.is_admin){
      fetch('/api/restart-admin',{method:'POST'});
    }
    return;
  }
  selectedMode=m;
  document.getElementById('mp-proxy').className='mpill'+(m==='proxy'?' sel-proxy':'');
  document.getElementById('mp-tun').className  ='mpill'+(m==='tun'?' sel-tun':'')
    +(!appInfo.singbox_available||!appInfo.is_admin?' off':'');
  rebuildTable();
}

// ── connection bar ─────────────────────────────────────────────────
function onConnUpdate(cs){
  connState=cs;
  const bar  =document.getElementById('conn-bar');
  const dot  =document.getElementById('cdot');
  const lbl  =document.getElementById('clabel');
  const det  =document.getElementById('cdetail');
  const ports=document.getElementById('cports');
  const btnDc=document.getElementById('btn-dc');
  const mwrap=document.getElementById('mode-wrap');

  bar.className=''; dot.className='cdot'; lbl.className='';

  const isp=cs.mode==='proxy', ist=cs.mode==='tun';
  const isActive = cs.status==='connected'||cs.status==='connecting'||cs.status==='error';

  // Hide mode switcher while connected — you can't change mode mid-session
  mwrap.style.display = isActive ? 'none' : '';

  if(cs.status==='connected'){
    const cls=isp?'cp':'conn-tun';
    bar.classList.add(cls); dot.classList.add(cls); lbl.className=cls;
    lbl.textContent=isp?'SYSTEM PROXY':'TUN MODE';
    det.innerHTML='via <span>'+x(cs.entry_name)+'</span>&ensp;·&ensp;'+fmtUptime(cs.uptime_sec);
    ports.style.display='flex';
    if(isp){
      ports.innerHTML=
        '<div class="pchip" onclick="cpText(\'127.0.0.1:'+cs.http_port+'\')">HTTP&nbsp;<b>'+cs.http_port+'</b></div>'+
        '<div class="pchip" onclick="cpText(\'127.0.0.1:'+cs.socks_port+'\')">SOCKS5&nbsp;<b>'+cs.socks_port+'</b></div>';
    } else {
      ports.innerHTML=
        '<div class="pchip">TUN&nbsp;<b>'+(cs.tun_iface||'vair-tun')+'</b></div>'+
        '<div class="pchip" style="pointer-events:none;color:var(--dim)">all traffic routed</div>';
    }
    btnDc.style.display='';
  } else if(cs.status==='connecting'){
    bar.classList.add('cc'); dot.classList.add('cc'); lbl.className='cc';
    lbl.textContent=ist?'STARTING TUN…':'CONNECTING…';
    det.innerHTML='<span>'+x(cs.entry_name)+'</span>';
    ports.style.display='none'; btnDc.style.display='';
  } else if(cs.status==='disconnecting'){
    bar.className=''; dot.className='cdot'; lbl.className='';
    lbl.textContent='DISCONNECTING…';
    det.textContent='';
    ports.style.display='none'; btnDc.style.display='none';
    mwrap.style.display='none';
  } else if(cs.status==='error'){
    bar.classList.add('ce'); dot.classList.add('ce'); lbl.className='ce';
    lbl.textContent='ERROR';
    det.innerHTML='<span style="color:var(--red)">'+x(cs.error||'unknown error')+'</span>';
    ports.style.display='none'; btnDc.style.display='';
  } else {
    lbl.textContent='DISCONNECTED';
    det.textContent='';
    ports.style.display='none'; btnDc.style.display='none';
  }

  // highlight row
  document.querySelectorAll('tbody tr.row-cp,tbody tr.row-ct').forEach(r=>{r.classList.remove('row-cp','row-ct')});
  if((cs.status==='connected'||cs.status==='connecting')&&(!cs.conn_tab||cs.conn_tab===activeTabId)){
    var cidx=findConnIdx();
    if(cidx>=0){
      const row=document.getElementById('r'+cidx);
      if(row)row.classList.add(cs.mode==='tun'?'row-ct':'row-cp');
    }
  }
  // Session stat visibility is tied to "connected" — re-render with the
  // last known counters so the panel hides/shows immediately rather than
  // waiting for the next stats tick.
  onStatsUpdate(lastStats);
  rebuildTable();
}

function fmtUptime(s){
  if(!s||s<0)return '';
  const h=Math.floor(s/3600),m=Math.floor((s%3600)/60),ss=s%60;
  return h>0?h+'h '+m+'m':(m>0?m+'m '+ss+'s':ss+'s');
}

// onStatsUpdate is fired by the server every ~1s while connected, and
// once on (re-)load. session_* is the live counter (only shown when
// connected); total_* is the persisted lifetime value (always shown).
let lastStats={session_up:0, session_down:0, total_up:0, total_down:0, enabled:true};
function onStatsUpdate(s){
  if(!s) return;
  lastStats=s;
  const enabled = s.enabled !== false;
  const statTot = document.getElementById('stat-total');
  const statSess= document.getElementById('stat-session');
  if(!enabled){
    statTot.style.display='none';
    statSess.style.display='none';
    return;
  }
  statTot.style.display='';
  document.getElementById('s-totl').textContent = '↑'+fmtBytes(s.total_up)+' ↓'+fmtBytes(s.total_down);
  // Session visibility tracks connection: shown while connected, hidden
  // otherwise. The actual show/hide happens in onConnUpdate but we also
  // refresh the value here so it updates as bytes flow.
  if(connState && connState.status==='connected'){
    statSess.style.display='';
    document.getElementById('s-sess').textContent = '↑'+fmtBytes(s.session_up)+' ↓'+fmtBytes(s.session_down);
  } else {
    statSess.style.display='none';
  }
}

// ── data ───────────────────────────────────────────────────────────
function onLoading(){
  document.getElementById('msg-area').style.display='flex';
  document.getElementById('tbl').style.display='none';
  document.getElementById('msg-area').innerHTML='<div class="ico"><span class="spinner"></span></div><p>Loading configs…</p>';
}
function onLoaded(list){
  Object.keys(entries).forEach(k=>delete entries[k]);
  document.getElementById('tb').innerHTML='';
  list.forEach(e=>entries[e.index]=e);
  document.getElementById('msg-area').style.display='none';
  document.getElementById('tbl').style.display='';
  selectedRows.clear();
  recomputeDupIndices();
  // Reset progress bar if we switched to a different tab than the one being tested
  if(bulkProgressTab&&bulkProgressTab!==activeTabId) setBar(0);
  rebuildTable();
}
function onUpdate(e){
  entries[e.index]=e;
  // Keep sortedList in sync so virtual scroll renders fresh data when scrolling back
  for(var i=0;i<sortedList.length;i++){
    if(sortedList[i].index===e.index){ sortedList[i]=e; break; }
  }
  updateRow(e.index);
  recalcStats();
}

// dupBodyIndices is the set of entry indices whose vless "body" (everything
// before the '#' fragment) was already seen at a smaller index. This is the
// view filter the per-tab dedup toggle drives — toggling it on hides these
// rows; toggling it off shows them again. Server keeps every entry.
let dupBodyIndices=new Set();

function vlessBodyJS(raw){
  if(!raw) return '';
  var i=raw.lastIndexOf('#');
  return i>=0 ? raw.substring(0,i) : raw;
}

function recomputeDupIndices(){
  dupBodyIndices=new Set();
  // Walk in entry-index order so "first occurrence" is stable: it doesn't
  // shift with the user's sort choice, so toggling dedup with different
  // sort modes hides the same rows.
  var ordered=Object.values(entries).slice().sort(function(a,b){return a.index-b.index;});
  var seen=new Set();
  for(var i=0;i<ordered.length;i++){
    var body=vlessBodyJS(ordered[i].raw);
    if(seen.has(body)) dupBodyIndices.add(ordered[i].index);
    else seen.add(body);
  }
}

// recalcStats updates the five header counters from sortedList, which is
// the list of rows currently visible on screen (after FILTER, exclude
// filter, and dedup view filter are applied). The previous version walked
// every entry — that surprised users when enabling dedup or filtering: the
// "configs" counter would stay at the total instead of matching what they
// could see. rebuildTable() populates sortedList and then calls this.
function recalcStats(){
  const vis = sortedList || [];
  document.getElementById('s-tot').textContent = vis.length;
  document.getElementById('s-ok').textContent  = vis.filter(e=>e.ping_status==='ok').length;
  document.getElementById('s-er').textContent  = vis.filter(e=>e.ping_status==='failed').length;
  const dl = vis.filter(e=>e.delay>0).map(e=>e.delay);
  const sp = vis.filter(e=>e.speed_mbps>0).map(e=>e.speed_mbps);
  document.getElementById('s-ms').textContent = dl.length?Math.min(...dl)+'ms':'—';
  document.getElementById('s-sp').textContent = sp.length?(Math.max(...sp).toFixed(1)+' MB/s'):'—';
}

function startBulk(id,total,evTab){
  bulkProgressTab=evTab||activeTabId;
  if(bulkProgressTab===activeTabId) setBar(0);
  // Grey out both buttons but keep them clickable for cancel
  var bp=document.getElementById('btn-ping-all');
  var bs=document.getElementById('btn-speed-all');
  bp.className='btn ghost dim-btn';
  bs.className='btn ghost dim-btn';
}
function progBulk(id,p){
  if(bulkProgressTab===activeTabId) setBar(p.done/p.total*100);
}
function doneBulk(id,label,btnId,cls,evTab){
  var dt=evTab||bulkProgressTab;
  if(dt===activeTabId){ setBar(100); setTimeout(()=>{setBar(0);},1200); }
  bulkProgressTab='';
  // Restore both buttons
  var bp=document.getElementById('btn-ping-all');
  var bs=document.getElementById('btn-speed-all');
  bp.className='btn ghost'; bp.textContent='ping all';
  bs.className='btn ghost'; bs.textContent='speed all';
}
function setBar(pct){ document.getElementById('pb-main').style.width=pct+'%'; }
function setBtn(id,dis,txt){ const b=document.getElementById(id); b.disabled=dis; b.textContent=txt; }

function setSort(m){
  sortMode=m;
  ['idx','ping','speed'].forEach(s=>document.getElementById('sort-'+s).classList.toggle('active',s===m));
  rebuildTable();
}
function applyFilter(){ filterText=document.getElementById('fi').value.toLowerCase(); rebuildTable(); }
function matches(e){
  // Per-tab dedup view filter — hide rows whose vless body appeared at an
  // earlier index. Only applies in "hide" mode; "delete" mode has already
  // removed those entries server-side, so they're not in the entries map.
  var tab=tabsList.find(function(t){return t.id===activeTabId;});
  if(tab && tab.dedup_mode==='hide' && dupBodyIndices.has(e.index)) return false;
  // Per-tab exclude filter
  var ef=tab&&tab.exclude_filter?tab.exclude_filter:[];
  if(ef.length>0){
    var nm=(e.name||'').toLowerCase();
    for(var i=0;i<ef.length;i++){
      if(nm.indexOf(ef[i].toLowerCase())>=0) return false;
    }
  }
  if(!filterText)return true;
  return(e.name||'').toLowerCase().includes(filterText)||(e.host||'').toLowerCase().includes(filterText)
    ||(e.network||'').toLowerCase().includes(filterText)||(e.security||'').toLowerCase().includes(filterText);
}

// ── table ──────────────────────────────────────────────────────────
// Virtual scroll: only renders visible rows + buffer. Works for 10,000+ configs.
// Keeps the full sorted list in sortedList, renders only the window.
// Top/bottom padding rows maintain correct scroll height.
let sortedList = [];
let rowHeight = 30; // measured on first render
let visibleWindow = { start: 0, end: 0 }; // inclusive start, exclusive end
const VBUFFER = 10; // rows rendered above/below viewport

function rebuildTable(){
  let list=Object.values(entries).filter(matches);
  if(sortMode==='ping'){
    list.sort((a,b)=>{if(a.delay>0&&b.delay>0)return a.delay-b.delay;if(a.delay>0)return -1;if(b.delay>0)return 1;return a.index-b.index;});
  }else if(sortMode==='speed'){
    // 1. Speed OK: sorted by speed descending
    // 2. Ping OK but speed failed/skipped: sorted by ping ascending
    // 3. Currently testing
    // 4. Ping failed: sorted by index
    function speedRank(e){
      if(e.speed_status==='ok' && e.speed_mbps>0) return 0;
      if(e.ping_status==='ok' && e.delay>0) return 1;
      if(e.speed_status==='testing_speed'||e.ping_status==='testing_ping') return 2;
      if(e.ping_status==='failed') return 3;
      return 4;
    }
    list.sort((a,b)=>{
      const ra=speedRank(a),rb=speedRank(b);
      if(ra!==rb) return ra-rb;
      if(ra===0) return b.speed_mbps-a.speed_mbps;
      if(ra===1) return a.delay-b.delay;
      return a.index-b.index;
    });
  }else{list.sort((a,b)=>a.index-b.index);}
  sortedList = list;
  document.getElementById('fc').textContent=filterText?(list.length+'/'+Object.keys(entries).length+' shown'):'';
  // Counters are derived from sortedList so any change in visibility — sort,
  // filter, dedup toggle, exclude filter, etc — keeps them in sync.
  recalcStats();
  visibleWindow = { start: 0, end: 0 }; // force full redraw
  renderVisibleRows();
}

function renderVisibleRows(){
  const tw = document.querySelector('.tw');
  const tb = document.getElementById('tb');
  if(!tw || !tb) return;
  const total = sortedList.length;

  // Small list: render everything (no virtualization overhead)
  if(total <= 100){
    tb.innerHTML = '';
    const frag = document.createDocumentFragment();
    for(let i=0;i<total;i++) frag.appendChild(buildRow(sortedList[i], i+1));
    tb.appendChild(frag);
    visibleWindow = { start: 0, end: total };
    restoreConnHighlight();
    return;
  }

  const scrollTop = tw.scrollTop;
  const viewH = tw.clientHeight;
  const firstVisible = Math.max(0, Math.floor(scrollTop / rowHeight) - VBUFFER);
  const lastVisible = Math.min(total, Math.ceil((scrollTop + viewH) / rowHeight) + VBUFFER);

  tb.innerHTML = '';
  const frag = document.createDocumentFragment();

  // Top spacer
  if(firstVisible > 0){
    const sp = document.createElement('tr');
    sp.style.height = (firstVisible * rowHeight) + 'px';
    sp.className = 'vspacer';
    frag.appendChild(sp);
  }
  // Visible rows
  for(let i=firstVisible; i<lastVisible; i++){
    frag.appendChild(buildRow(sortedList[i], i+1));
  }
  // Bottom spacer
  if(lastVisible < total){
    const sp = document.createElement('tr');
    sp.style.height = ((total - lastVisible) * rowHeight) + 'px';
    sp.className = 'vspacer';
    frag.appendChild(sp);
  }
  tb.appendChild(frag);
  visibleWindow = { start: firstVisible, end: lastVisible };

  // Measure actual row height on first render for accuracy
  if(total > 0 && lastVisible > firstVisible){
    const firstRow = tb.querySelector('tr:not(.vspacer)');
    if(firstRow){
      const h = firstRow.offsetHeight;
      if(h > 0 && Math.abs(h - rowHeight) > 2){
        rowHeight = h;
      }
    }
  }
  restoreConnHighlight();
}

function findConnIdx(){
  // Find entry index matching the connected config's raw URL (survives reload)
  if(!connState.conn_raw) return connState.entry_index;
  var vals=Object.values(entries);
  for(var i=0;i<vals.length;i++){
    if(vals[i].raw===connState.conn_raw) return vals[i].index;
  }
  return -1; // not found in current tab
}

function restoreConnHighlight(){
  if((connState.status==='connected'||connState.status==='connecting')&&(!connState.conn_tab||connState.conn_tab===activeTabId)){
    var idx=findConnIdx();
    if(idx>=0){
      var row=document.getElementById('r'+idx);
      if(row)row.classList.add(connState.mode==='tun'?'row-ct':'row-cp');
    }
  }
}

// Scroll listener for virtual window updates
(function attachVScroll(){
  const tw = document.querySelector('.tw');
  if(!tw) { setTimeout(attachVScroll, 100); return; }
  let scrollTimer = null;
  tw.addEventListener('scroll', ()=>{
    if(scrollTimer) return;
    scrollTimer = requestAnimationFrame(()=>{
      scrollTimer = null;
      renderVisibleRows();
    });
  }, {passive: true});
  window.addEventListener('resize', ()=>{
    renderVisibleRows();
  });
})();

function updateRow(idx){
  // If only a few items changed visually, targeted update is faster than full rebuild.
  // But for sorted views, position may change — rebuild.
  if(sortMode!=='idx'){ rebuildTable(); return; }
  const e=entries[idx]; if(!e)return;
  const old=document.getElementById('r'+idx);
  if(!old){
    // Row is outside virtual window, update underlying data only.
    // Next scroll/rebuild will render it correctly.
    return;
  }
  const pos=parseInt(old.cells[0].textContent)||idx+1;
  const nr=buildRow(e,pos);
  old.replaceWith(nr);
  if((connState.status==='connected'||connState.status==='connecting')&&findConnIdx()===idx)
    nr.classList.add(connState.mode==='tun'?'row-ct':'row-cp');
}

function buildRow(e,pos){
  const tr=document.createElement('tr'); tr.id='r'+e.index;
  if(selectedRows.has(e.index)) tr.classList.add('selected');
  tr.onclick=(ev)=>{
    if(ev.target.closest('.act-cell'))return;
    toggleRowSelect(e.index,ev);
  };
  tr.oncontextmenu=(ev)=>{
    ev.preventDefault();
    if(!selectedRows.has(e.index)){
      selectedRows.clear();
      selectedRows.add(e.index);
      rebuildTable();
    }
    showRowMenu(ev,e.index);
  };

  let pp='pill pending',pt='—';
  if(e.ping_status==='testing_ping'){pp='pill tp';pt='pinging…';}
  else if(e.ping_status==='ok'){pp=e.delay<150?'pill ok-fast':'pill ok-ping';pt=e.delay+'ms';}
  else if(e.ping_status==='failed'){pp='pill failed';pt=e.ping_err||'timeout';}

  let sp='pill pending',st='—';
  if(e.speed_status==='testing_speed'){
    sp='pill ts';
    if(e.speed_live>0){st=e.speed_live.toFixed(1)+' MB/s';}
    else{st='connecting…';}
  }else if(e.speed_status==='ok'){sp='pill ok-speed';st=e.speed_mbps.toFixed(2)+' MB/s';}
  else if(e.speed_status==='failed'){sp='pill failed';st=e.speed_err||'failed';}
  else if(e.speed_status==='skipped'){sp='pill skipped';st='—';}

  const nc=(e.network||'tcp').toLowerCase().replace(/[^a-z]/g,'');
  const isConn=connState&&connState.status==='connected'&&(!connState.conn_tab||connState.conn_tab===activeTabId)&&(connState.conn_raw?connState.conn_raw===e.raw:connState.entry_index===e.index);

  let connectBtn;
  if(isConn){
    connectBtn='<button class="btn sm-disc" onclick="doDisconnect()" title="Disconnect">disconnect</button>';
  } else {
    connectBtn='<button class="btn sm ghost" onclick="doConnect('+e.index+')" title="'+(selectedMode==='tun'?'TUN mode (all traffic)':'System Proxy (HTTP/SOCKS)')+'">connect</button>';
  }

  tr.innerHTML=
    '<td class="ci">'+pos+'</td>'+
    '<td class="cn"><div class="nc"><span class="nm" title="'+x(e.name)+'">'+x(e.name)+'</span>'+
    '</div></td>'+
    '<td class="ch"><span class="nh">'+x(e.host||'')+(e.port?':'+e.port:'')+'</span></td>'+
    '<td class="ct"><span class="nb '+nc+'">'+x(e.network||'tcp')+'</span></td>'+
    '<td class="cs"><span class="sb '+(e.security||'none')+'">'+x(e.security||'none')+'</span></td>'+
    '<td class="cp2"><div class="vc"><span class="'+pp+'" title="'+x(pt)+'">'+pt+'</span></div></td>'+
    '<td class="csp"><div class="vc"><span class="'+sp+'" title="'+x(st)+'">'+st+'</span></div></td>'+
    '<td class="ca"><div class="act-cell">'+
      connectBtn+
      '<button class="btn sm ghost" title="Ping" onclick="pingOne('+e.index+')">ping</button>'+
      '<button class="btn sm ghost"  title="Speed" onclick="speedOne('+e.index+')">speed</button>'+
      '<button class="cpb" title="Copy vless://" onclick="cpRaw(this,'+e.index+')">⎘</button>'+
    '</div></td>';
  return tr;
}

function x(s){return(s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;')}
function cpText(t){navigator.clipboard.writeText(t).catch(()=>{});}
function cpRaw(btn,idx){
  const e=entries[idx];if(!e)return;
  navigator.clipboard.writeText(e.raw).then(()=>{
    btn.classList.add('done');btn.textContent='✓';
    setTimeout(()=>{btn.classList.remove('done');btn.textContent='⎘';},1400);
  });
}

function doConnect(idx){
  fetch('/api/connect?idx='+idx+'&mode='+selectedMode,{method:'POST'}).catch(console.error);
}
function doDisconnect(){ fetch('/api/disconnect',{method:'POST'}).catch(console.error); }
function pingOne(idx)   { fetch('/api/ping/one?idx='+idx,{method:'POST'}).catch(console.error); }
function speedOne(idx)  { fetch('/api/speed/one?idx='+idx,{method:'POST'}).catch(console.error); }
// Collect indices of currently visible (filter+exclude) entries.
// matches() already accounts for both the filter input and the per-tab exclude.
function visibleIndices(){
  var out=[];
  Object.values(entries).forEach(function(e){ if(matches(e)) out.push(e.index); });
  return out;
}
// When sending bulk-test requests, restrict to currently visible entries so
// that running ping/speed all while a filter is active only tests the
// configs the user can see. Sending the indices is harmless when nothing is
// hidden — it just lists every entry.
function bulkTestBody(){
  return JSON.stringify({indices: visibleIndices()});
}
function doPingAll() {
  fetch('/api/ping/all',{method:'POST',headers:{'Content-Type':'application/json'},body:bulkTestBody()}).catch(console.error);
}
function doSpeedAll(){
  fetch('/api/speed/all',{method:'POST',headers:{'Content-Type':'application/json'},body:bulkTestBody()}).catch(console.error);
}
function doReload(){
  fetch('/api/tests/cancel?tab='+encodeURIComponent(activeTabId),{method:'POST'}).then(function(){
    fetch('/api/reload',{method:'POST'}).catch(console.error);
  }).catch(function(){
    fetch('/api/reload',{method:'POST'}).catch(console.error);
  });
}

// ── Tab management ───────────────────────────────────────────────────────────
let activeTabId='main';
let tabsList=[{id:'main',name:'Sources',is_main:true,closable:false}];
let selectedRows=new Set(); // selected config indices for deletion
let bulkProgressTab=''; // which tab the current bulk operation belongs to

function onTabsUpdate(tabs){
  // Detect a change to the active tab's dedup mode so we can refresh the
  // visible rows without waiting for a manual sort/filter event. Toggling
  // dedup is intended to be instantaneous from the user's perspective.
  // Note: "delete" mode causes a "loaded" event with a smaller entries
  // list, which itself triggers a rebuild via onLoaded — but we still
  // rebuild here to cover the "off"⇄"hide" transition where the entries
  // map doesn't change but the view filter does.
  var prev=tabsList.find(function(t){return t.id===activeTabId;});
  var next=tabs.find(function(t){return t.id===activeTabId;});
  var dedupFlipped=prev && next && ((prev.dedup_mode||'')!==(next.dedup_mode||''));
  tabsList=tabs;
  renderTabs();
  if(dedupFlipped){
    rebuildTable();
  }
}
function onActiveTab(id){
  activeTabId=id;
  selectedRows.clear();
  // Reset progress bar - only show if the active tab has an ongoing bulk operation
  if(bulkProgressTab!==id) setBar(0);
  renderTabs();
}

function renderTabs(){
  const bar=document.getElementById('tab-bar');
  const addBtn=bar.querySelector('.tab-add');
  bar.querySelectorAll('.tab-btn').forEach(b=>b.remove());
  tabsList.forEach(t=>{
    // Hide Sources tab when disabled in settings
    if(t.id==='main' && appSettingsJS.sources_enabled===false) return;
    const btn=document.createElement('button');
    btn.className='tab-btn'+(t.id===activeTabId?' active':'');
    btn.dataset.id=t.id;
    let html='<span class="tab-label">'+x(t.name)+'</span>';
    btn.innerHTML=html;
    btn.onclick=()=>switchTab(t.id);
    btn.oncontextmenu=(e)=>{e.preventDefault();showTabMenu(e,t);};
    btn.onmousedown=(e)=>{if(e.button===0)startTabDrag(e,t.id);};
    bar.insertBefore(btn,addBtn);
  });
}

// ── Mouse-based tab drag reorder ─────────────────────────────────────────────
var tabDragState=null;
function startTabDrag(e,dragId){
  var startX=e.clientX,startY=e.clientY,moved=false;
  var onMove=function(me){
    if(!moved&&(Math.abs(me.clientX-startX)>5||Math.abs(me.clientY-startY)>5)){
      moved=true;
      var el=document.querySelector('.tab-btn[data-id="'+dragId+'"]');
      if(el)el.classList.add('dragging');
    }
    if(!moved)return;
    // Find which tab button we're over
    var tabs=document.querySelectorAll('.tab-btn');
    tabs.forEach(function(tb){tb.classList.remove('drag-over');});
    var target=document.elementFromPoint(me.clientX,me.clientY);
    if(target){
      var tbtn=target.closest('.tab-btn');
      if(tbtn&&tbtn.dataset.id!==dragId)tbtn.classList.add('drag-over');
    }
  };
  var onUp=function(ue){
    document.removeEventListener('mousemove',onMove);
    document.removeEventListener('mouseup',onUp);
    document.querySelectorAll('.tab-btn').forEach(function(tb){tb.classList.remove('dragging','drag-over');});
    if(!moved)return;
    var target=document.elementFromPoint(ue.clientX,ue.clientY);
    if(!target)return;
    var tbtn=target.closest('.tab-btn');
    if(!tbtn||tbtn.dataset.id===dragId)return;
    var toId=tbtn.dataset.id;
    var ids=tabsList.map(function(tt){return tt.id;});
    var fi=ids.indexOf(dragId),ti=ids.indexOf(toId);
    if(fi<0||ti<0)return;
    ids.splice(fi,1);ids.splice(ti,0,dragId);
    fetch('/api/tab/reorder',{method:'POST',body:JSON.stringify(ids)}).catch(console.error);
  };
  document.addEventListener('mousemove',onMove);
  document.addEventListener('mouseup',onUp);
}

function switchTab(id){
  fetch('/api/tab/switch?id='+id,{method:'POST'}).catch(console.error);
}

function addTab(){
  fetch('/api/tab/create',{method:'POST'}).then(r=>r.json()).then(tab=>{
    switchTab(tab.id);
  }).catch(console.error);
}

function deleteTab(id){
  fetch('/api/tab/delete?id='+id,{method:'POST'}).catch(console.error);
}

// ── Context menu ─────────────────────────────────────────────────────────────
function showRowMenu(ev,idx){
  closeCtxMenu();
  var m=document.createElement('div');
  m.className='ctx-menu';m.id='ctx-menu';
  var selCount=selectedRows.size;
  var label=selCount>1?selCount+' configs':'config';
  m.innerHTML='<div class="ctx-menu-item" onclick="copySelected();closeCtxMenu()">Copy '+label+'</div>';
  if(activeTabId!=='main'){
    m.innerHTML+='<div class="ctx-menu-item danger" onclick="deleteSelectedRows();closeCtxMenu()">Delete '+label+'</div>';
  }
  m.style.left=ev.clientX+'px';m.style.top=ev.clientY+'px';
  document.body.appendChild(m);
  var rect=m.getBoundingClientRect();
  if(rect.right>window.innerWidth) m.style.left=(window.innerWidth-rect.width-4)+'px';
  if(rect.bottom>window.innerHeight) m.style.top=(window.innerHeight-rect.height-4)+'px';
  setTimeout(()=>document.addEventListener('click',closeCtxMenu,{once:true}),10);
}

// Right-click on empty table area — paste option
document.querySelector('.tw').addEventListener('contextmenu',function(ev){
  if(ev.target.closest('tr'))return; // handled by row menu
  if(activeTabId==='main')return;
  ev.preventDefault();
  closeCtxMenu();
  var m=document.createElement('div');
  m.className='ctx-menu';m.id='ctx-menu';
  m.innerHTML='<div class="ctx-menu-item" onclick="pasteFromClipboard();closeCtxMenu()">Paste configs</div>';
  m.style.left=ev.clientX+'px';m.style.top=ev.clientY+'px';
  document.body.appendChild(m);
  setTimeout(()=>document.addEventListener('click',closeCtxMenu,{once:true}),10);
});

function pasteFromClipboard(){
  navigator.clipboard.readText().then(function(text){
    if(!text||!text.includes('vless://'))return;
    fetch('/api/tab/paste?id='+activeTabId,{method:'POST',body:text}).catch(console.error);
  }).catch(()=>{});
}

// gatherSelectedLines collects raw vless URLs for every currently selected
// row, in on-screen (sortedList) order so the clipboard order matches what
// the user sees. selectedRows stores entry.index values, which are stable
// across sorts — clicking the visually-first row in speed sort stores that
// entry's index, then we look it up here. Anything still in selectedRows
// but missing from sortedList (shouldn't happen, but guard anyway) is
// appended at the end via the entries map.
function gatherSelectedLines(){
  if(selectedRows.size===0) return [];
  var lines=[],seen=new Set();
  for(var i=0;i<sortedList.length;i++){
    var e=sortedList[i];
    if(e && selectedRows.has(e.index) && e.raw){ lines.push(e.raw); seen.add(e.index); }
  }
  selectedRows.forEach(function(idx){
    if(seen.has(idx)) return;
    var e=entries[idx];
    if(e && e.raw) lines.push(e.raw);
  });
  return lines;
}

// flashCopySelected briefly turns every selected row's ⎘ button into a ✓.
// Rows outside the virtualized window won't have a button rendered — those
// are silently skipped, the user sees the feedback on whatever's visible.
function flashCopySelected(){
  selectedRows.forEach(function(idx){
    var row=document.getElementById('r'+idx);
    if(!row) return;
    var btn=row.querySelector('.cpb');
    if(!btn) return;
    btn.classList.add('done');
    btn.textContent='✓';
    setTimeout(function(){
      // Only revert if no newer flash overwrote it (cheap check via class).
      if(btn.classList.contains('done')){
        btn.classList.remove('done');
        btn.textContent='⎘';
      }
    },1400);
  });
}

// copySelected is the JS-driven copy path: right-click menu and any other
// callers that don't go through the browser's copy event. It uses the async
// Clipboard API which is what's available outside of a user-gesture copy
// event. Returns true on success so callers can chain UI feedback.
function copySelected(){
  if(selectedRows.size===0) return false;
  var lines=gatherSelectedLines();
  if(lines.length===0) return false;
  navigator.clipboard.writeText(lines.join('\n')).then(flashCopySelected).catch(function(){});
  return true;
}

// Browser copy event — fires for Ctrl+C and the Edge/WebView2 Copy menu.
// Handling it here (vs intercepting keydown + calling writeText) is more
// reliable: clipboardData.setData is synchronous, doesn't depend on
// permissions or focus state, and avoids races with the native copy
// pipeline that were causing stale clipboard contents on Ctrl+C.
document.addEventListener('copy',function(e){
  // Inputs and textareas have their own selection — let the native copy
  // handle them so users can still copy text out of the URL/filter fields.
  var ae=document.activeElement;
  if(ae && (ae.tagName==='INPUT' || ae.tagName==='TEXTAREA')) return;
  if(selectedRows.size===0) return; // no rows selected — fall through to native
  var lines=gatherSelectedLines();
  if(lines.length===0) return;
  e.preventDefault();
  if(e.clipboardData) e.clipboardData.setData('text/plain', lines.join('\n'));
  flashCopySelected();
});

function deleteSelectedRows(){
  if(selectedRows.size===0||activeTabId==='main')return;
  var indices=[...selectedRows];
  selectedRows.clear();
  fetch('/api/tab/delete-entries?id='+activeTabId,{method:'POST',body:JSON.stringify(indices)}).catch(console.error);
}

function showTabMenu(e,tab){
  closeCtxMenu();
  const m=document.createElement('div');
  m.className='ctx-menu';m.id='ctx-menu';
  if(!tab.is_main){
    m.innerHTML+='<div class="ctx-menu-item" onclick="openTabSettings(\''+tab.id+'\')">Settings</div>';
    m.innerHTML+='<div class="ctx-sep"></div>';
    m.innerHTML+='<div class="ctx-menu-item danger" onclick="deleteTab(\''+tab.id+'\');closeCtxMenu()">Delete tab</div>';
  } else {
    m.innerHTML+='<div class="ctx-menu-item" onclick="openSourcesSettings()">Settings</div>';
  }
  m.style.left=Math.min(e.clientX,window.innerWidth-200)+'px';
  m.style.top=Math.min(e.clientY,window.innerHeight-120)+'px';
  document.body.appendChild(m);
  setTimeout(()=>document.addEventListener('click',closeCtxMenu,{once:true}),10);
}
function closeCtxMenu(){
  const m=document.getElementById('ctx-menu');
  if(m)m.remove();
}

// ── Tab settings modal ───────────────────────────────────────────────────────
// ── settings ──────────────────────────────────────────────────────
function loadAppSettings(){
  fetch('/api/settings').then(r=>r.json()).then(s=>{
    appSettingsJS=s;
    rebuildTable();
  }).catch(()=>{});
}

function saveAppSettings(cb){
  fetch('/api/settings',{method:'POST',body:JSON.stringify(appSettingsJS)}).then(()=>{
    if(cb)cb();
  }).catch(console.error);
}

function openSettings(){
  if(document.getElementById('settings-modal'))return;
  var ov=document.createElement('div');
  ov.className='modal-overlay';ov.id='settings-modal';
  ov.onclick=function(ev){if(ev.target===ov)ov.remove();};

  var domains=appSettingsJS.direct_domains||[];
  var domainChipsHtml='';
  for(var i=0;i<domains.length;i++){
    domainChipsHtml+='<span class="chip" data-d="'+x(domains[i])+'">'+x(domains[i])+'<span class="chip-x" onclick="removeDomainChip(this)">x</span></span>';
  }

  var apps=appSettingsJS.direct_apps||[];
  var appChipsHtml='';
  for(var i=0;i<apps.length;i++){
    appChipsHtml+='<span class="chip" data-a="'+x(apps[i])+'">'+x(apps[i])+'<span class="chip-x" onclick="removeAppChip(this)">x</span></span>';
  }

  ov.innerHTML='<div class="modal-box" style="min-width:400px">'+
    '<div class="modal-title">Settings</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">Sources</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">Enable Sources tab</span>'+
        '<label class="toggle"><input type="checkbox" id="set-sources-on" '+(appSettingsJS.sources_enabled!==false?'checked':'')+' onchange="toggleSources(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
      '</div>'+
    '</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">Routing</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">Russian sites without VPN</span>'+
        '<label class="toggle"><input type="checkbox" id="set-ru-direct" '+(appSettingsJS.ru_sites_direct?'checked':'')+' onchange="toggleRuSites(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
      '</div>'+
      '<div class="modal-hint">Route traffic to Russian domains and IPs directly, bypassing VPN. Takes effect on next connection.</div>'+
      '<div class="modal-row" style="margin-top:10px;margin-bottom:4px">'+
        '<span class="modal-row-label">Custom domains without VPN</span>'+
      '</div>'+
      '<div class="chips-wrap" id="domain-chips">'+domainChipsHtml+
        '<input class="chip-input" id="domain-input" placeholder="e.g. vk.com, press Enter" onkeydown="domainChipKey(event)">'+
      '</div>'+
      '<div class="modal-hint">Enter a domain — all its subdomains are included automatically. Takes effect on next connection.</div>'+
      '<div class="modal-row" style="margin-top:10px;margin-bottom:4px">'+
        '<span class="modal-row-label">Apps without VPN (TUN mode only)</span>'+
      '</div>'+
      '<div class="chips-wrap" id="app-chips">'+appChipsHtml+
        '<input class="chip-input" id="app-input" placeholder="e.g. chrome.exe, press Enter" onkeydown="appChipKey(event)">'+
      '</div>'+
      '<div class="modal-hint">Process names that bypass VPN. Only works in TUN mode (system proxy can\'t be excluded per-app at the OS level). <a href="#" onclick="openProcessPicker(event)" style="color:var(--accent);text-decoration:underline">Browse running processes</a>. Takes effect on next connection.</div>'+
    '</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">Testing</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">Ping concurrency</span>'+
        '<input class="modal-input num-input" id="set-ping-conc" type="number" min="1" max="200" value="'+(appSettingsJS.ping_concurrency||10)+'" onchange="updateConcurrency(\'ping\',this.value)">'+
      '</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">Speed concurrency</span>'+
        '<input class="modal-input num-input" id="set-speed-conc" type="number" min="1" max="100" value="'+(appSettingsJS.speed_concurrency||5)+'" onchange="updateConcurrency(\'speed\',this.value)">'+
      '</div>'+
      '<div class="modal-hint">How many configs are pinged or speed-tested in parallel. Defaults: ping 10, speed 5. Takes effect on the next bulk test run.</div>'+
      '<div class="modal-row" style="display:block;margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">Ping URL</span>'+
        '<select class="modal-input" id="set-ping-url" style="margin-bottom:0" onchange="updateTestURL(\'ping\',this.value)">'+pingURLOptions()+'</select>'+
      '</div>'+
      '<div class="modal-row" id="ping-url-custom-row" style="display:'+(isCustomURL('ping')?'block':'none')+';margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">Custom ping URL</span>'+
        '<input class="modal-input" id="set-ping-url-custom" style="margin-bottom:0" placeholder="https://..." value="'+x(isCustomURL('ping')?(appSettingsJS.ping_test_url||''):'')+'" onchange="updateTestURLCustom(\'ping\',this.value)">'+
      '</div>'+
      '<div class="modal-row" style="display:block;margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">Speed URL</span>'+
        '<select class="modal-input" id="set-speed-url" style="margin-bottom:0" onchange="updateTestURL(\'speed\',this.value)">'+speedURLOptions()+'</select>'+
      '</div>'+
      '<div class="modal-row" id="speed-url-custom-row" style="display:'+(isCustomURL('speed')?'block':'none')+';margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">Custom speed URL</span>'+
        '<input class="modal-input" id="set-speed-url-custom" style="margin-bottom:0" placeholder="https://..." value="'+x(isCustomURL('speed')?(appSettingsJS.speed_test_url||''):'')+'" onchange="updateTestURLCustom(\'speed\',this.value)">'+
      '</div>'+
      '<div class="modal-hint">Speed test runs for ~4 seconds regardless of file size, measuring throughput. Ping test accepts any HTTP response — pick whichever endpoint your provider routes best.</div>'+
    '</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">Network</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">TUN MTU</span>'+
        '<input class="modal-input num-input" id="set-mtu" type="number" min="576" max="9000" value="'+(appSettingsJS.tun_mtu||9000)+'" onchange="updateMTU(this.value)">'+
      '</div>'+
      '<div class="modal-hint">Default 9000 (jumbo frames). If you see download stalls or sites hanging, try 1500 or 1408. Takes effect on next connection.</div>'+
    '</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">Statistics</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">Enable traffic statistics</span>'+
        '<label class="toggle"><input type="checkbox" id="set-stats" '+(!appSettingsJS.stats_disabled?'checked':'')+' onchange="toggleStats(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
      '</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label" id="stats-total-label">Lifetime total: ↑'+fmtBytes(appSettingsJS.stats_total_up||0)+' ↓'+fmtBytes(appSettingsJS.stats_total_down||0)+'</span>'+
        '<button class="btn ghost sm" onclick="resetTotalStats()">reset total</button>'+
      '</div>'+
      '<div class="modal-hint">Tracks bytes through the VPN tunnel in both modes. The lifetime total persists across sessions; the live session counter resets on every connect.</div>'+
    '</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">System</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">Minimize to tray on close</span>'+
        '<label class="toggle"><input type="checkbox" id="set-tray" '+(appSettingsJS.tray_enabled?'checked':'')+' onchange="toggleTray(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
      '</div>'+
    '</div>'+
    '<div class="modal-btns">'+
      '<button class="btn ghost" onclick="document.getElementById(\'settings-modal\').remove()">close</button>'+
    '</div>'+
  '</div>';
  document.body.appendChild(ov);
  // Focus the first usable input — the previous build referenced a
  // 'country-input' that no longer exists, which threw a TypeError.
  var firstInput=document.getElementById('domain-input');
  if(firstInput) firstInput.focus();
  // Pre-warm the running-process cache asynchronously so the Browse modal
  // opens instantly when the user clicks the link below.
  refreshProcessCache();
}

// processNamesCache stores the most recent process snapshot so the browse
// modal can render instantly. It's refreshed whenever Settings opens or
// the user clicks "refresh" inside the picker.
var processNamesCache=[];

function refreshProcessCache(){
  if(typeof window._goListProcesses!=='function') return;
  Promise.resolve(window._goListProcesses()).then(function(list){
    processNamesCache=Array.isArray(list)?list:[];
  }).catch(function(){});
}

// openProcessPicker opens a small modal listing currently running
// processes. Clicking a name adds it as an "Apps without VPN" chip.
function openProcessPicker(ev){
  if(ev){ ev.preventDefault(); ev.stopPropagation(); }
  if(document.getElementById('proc-modal')) return;
  // Refresh in case processes started/stopped since Settings opened.
  refreshProcessCache();
  var ov=document.createElement('div');
  ov.className='modal-overlay';ov.id='proc-modal';
  ov.onclick=function(e){ if(e.target===ov) ov.remove(); };
  ov.innerHTML='<div class="modal-box" style="min-width:380px;max-width:420px">'+
    '<div class="modal-title">Running processes</div>'+
    '<input class="modal-input" id="proc-filter" placeholder="filter…" autofocus>'+
    '<div class="modal-hint" style="margin-top:0">Click a process to add it to the Apps without VPN list.</div>'+
    '<div id="proc-list-box" style="max-height:300px;overflow-y:auto;border:1px solid var(--border2);border-radius:3px;background:var(--bg2);padding:4px"></div>'+
    '<div class="modal-btns">'+
      '<button class="btn ghost" onclick="refreshProcessCache();renderProcessList(document.getElementById(\'proc-filter\').value)">refresh</button>'+
      '<button class="btn ghost" onclick="document.getElementById(\'proc-modal\').remove()">close</button>'+
    '</div>'+
  '</div>';
  document.body.appendChild(ov);
  document.getElementById('proc-filter').addEventListener('input',function(e){
    renderProcessList(e.target.value);
  });
  renderProcessList('');
}

function renderProcessList(filter){
  var box=document.getElementById('proc-list-box');
  if(!box) return;
  filter=(filter||'').trim().toLowerCase();
  var existing=new Set((appSettingsJS.direct_apps||[]).map(function(s){return s.toLowerCase();}));
  var names=processNamesCache;
  var html='';
  var shown=0;
  for(var i=0;i<names.length;i++){
    var n=names[i];
    if(filter && n.indexOf(filter)<0) continue;
    var already=existing.has(n);
    html+='<div class="proc-item'+(already?' added':'')+'" onclick="addProcessFromPicker(\''+
      x(n).replace(/\\/g,'\\\\').replace(/\x27/g,'\\\x27')+'\')" '+
      'style="padding:5px 8px;cursor:pointer;font-size:11px;border-radius:2px;'+
      (already?'opacity:.5;':'')+'" '+
      'onmouseover="this.style.background=\'rgba(232,197,71,.12)\'" '+
      'onmouseout="this.style.background=\'\'">'+
      x(n)+(already?'  (already added)':'')+
      '</div>';
    shown++;
    if(shown>500){ html+='<div style="padding:4px 8px;color:var(--dim);font-size:10px">… '+(names.length-shown)+' more, refine filter</div>'; break; }
  }
  if(shown===0){
    html='<div style="padding:8px;color:var(--dim);font-size:11px;text-align:center">'+
      (names.length===0?'No process list available — only works in the desktop build.':'No matches')+
      '</div>';
  }
  box.innerHTML=html;
}

function addProcessFromPicker(name){
  if(!name) return;
  if(!appSettingsJS.direct_apps) appSettingsJS.direct_apps=[];
  var lower=name.toLowerCase();
  for(var i=0;i<appSettingsJS.direct_apps.length;i++){
    if(appSettingsJS.direct_apps[i].toLowerCase()===lower) return; // already there
  }
  appSettingsJS.direct_apps.push(lower);
  saveAppSettings(function(){
    // Re-render the chip strip in the underlying Settings modal.
    var wrap=document.getElementById('app-chips');
    var inp=document.getElementById('app-input');
    if(wrap && inp){
      wrap.querySelectorAll('.chip').forEach(function(c){c.remove();});
      for(var i=0;i<appSettingsJS.direct_apps.length;i++){
        var sp=document.createElement('span');
        sp.className='chip';sp.setAttribute('data-a',appSettingsJS.direct_apps[i]);
        sp.innerHTML=x(appSettingsJS.direct_apps[i])+'<span class="chip-x" onclick="removeAppChip(this)">x</span>';
        wrap.insertBefore(sp,inp);
      }
    }
    // Refresh the picker list so the just-added entry shows as "already added".
    var f=document.getElementById('proc-filter');
    if(f) renderProcessList(f.value);
  });
}

function toggleSources(on){
  appSettingsJS.sources_enabled=on;
  saveAppSettings(function(){
    renderTabs();
    if(!on && activeTabId==='main'){
      // Switch to first available tab or create one
      var other=tabsList.find(function(t){return t.id!=='main';});
      if(other) switchTab(other.id);
      else addTab();
    }
    if(on){
      // Re-show Sources tab; if no other tab active, switch to it and reload
      renderTabs();
      if(activeTabId==='main') doReload();
    }
  });
}

function toggleRuSites(on){
  appSettingsJS.ru_sites_direct=on;
  saveAppSettings();
}

function toggleTray(on){
  appSettingsJS.tray_enabled=on;
  saveAppSettings();
  if(typeof _goToggleTray==='function') _goToggleTray(on);
}

// updateConcurrency stores a clamped concurrency choice in app settings.
// Server clamps again on receive (defensive), but we mirror the bounds here
// so the visible input value stays sensible if the user types past them.
function updateConcurrency(kind, raw){
  var v=parseInt(raw,10);
  if(isNaN(v) || v<1) v=1;
  if(kind==='ping'){
    if(v>200) v=200;
    appSettingsJS.ping_concurrency=v;
    var inp=document.getElementById('set-ping-conc'); if(inp) inp.value=v;
  } else {
    if(v>100) v=100;
    appSettingsJS.speed_concurrency=v;
    var inp=document.getElementById('set-speed-conc'); if(inp) inp.value=v;
  }
  saveAppSettings();
}

// ── test URL pickers ───────────────────────────────────────────────
// The dropdowns hold a fixed preset list (kept in sync with the Go
// defaults). "Custom URL..." switches the dropdown to a sentinel value
// and reveals a text input. The empty string maps to "default" — what
// the program shipped with — so unsetting a custom URL is a one-click op.

var PING_PRESETS=[
  {url:'',                                                  label:'https://www.gstatic.com/generate_204 (default)'},
  {url:'https://www.google.com/generate_204',               label:'https://www.google.com/generate_204'},
  {url:'https://detectportal.firefox.com/success.txt',      label:'https://detectportal.firefox.com/success.txt'},
  {url:'https://captive.apple.com/hotspot-detect.html',     label:'https://captive.apple.com/hotspot-detect.html'},
  {url:'http://www.msftconnecttest.com/connecttest.txt',    label:'http://www.msftconnecttest.com/connecttest.txt'}
];
var SPEED_PRESETS=[
  {url:'',                                                  label:'https://speed.cloudflare.com/__down?bytes=10000000 (default)'},
  {url:'https://speed.cloudflare.com/__down?bytes=50000000',  label:'https://speed.cloudflare.com/__down?bytes=50000000'},
  {url:'http://cachefly.cachefly.net/100mb.test',           label:'http://cachefly.cachefly.net/100mb.test'},
  {url:'https://proof.ovh.net/files/100Mb.dat',             label:'https://proof.ovh.net/files/100Mb.dat'}
];

// isCustomURL: true when the user's current URL doesn't match any
// preset. We treat the empty string as the default (preset 0), not a
// custom URL — that's what the dropdown represents.
function isCustomURL(kind){
  var cur = kind==='ping' ? (appSettingsJS.ping_test_url||'') : (appSettingsJS.speed_test_url||'');
  if(cur==='') return false;
  var presets = kind==='ping' ? PING_PRESETS : SPEED_PRESETS;
  for(var i=0;i<presets.length;i++){ if(presets[i].url===cur) return false; }
  return true;
}

function urlOptionsHTML(presets, currentURL, customSelected){
  var html='';
  for(var i=0;i<presets.length;i++){
    var sel = (!customSelected && presets[i].url===currentURL) ? ' selected' : '';
    html += '<option value="'+x(presets[i].url)+'"'+sel+'>'+x(presets[i].label)+'</option>';
  }
  html += '<option value="__custom"'+(customSelected?' selected':'')+'>Custom URL…</option>';
  return html;
}
function pingURLOptions(){
  return urlOptionsHTML(PING_PRESETS, appSettingsJS.ping_test_url||'', isCustomURL('ping'));
}
function speedURLOptions(){
  return urlOptionsHTML(SPEED_PRESETS, appSettingsJS.speed_test_url||'', isCustomURL('speed'));
}

// updateTestURL handles a dropdown change. Selecting a preset writes
// its URL straight into settings (the empty string for the default is
// fine — currentPingURL()/currentSpeedURL() on the Go side falls back
// to the built-in default for empty). "__custom" reveals the text
// input but doesn't change the saved URL until the user types and
// blurs (updateTestURLCustom does the save).
function updateTestURL(kind, val){
  var customRow = document.getElementById(kind+'-url-custom-row');
  if(val==='__custom'){
    if(customRow) customRow.style.display='block';
    // Don't save anything yet — wait for the user to actually type a URL.
    // But seed the input with whatever they had (if it was already custom).
    var inp=document.getElementById('set-'+kind+'-url-custom');
    if(inp && !inp.value) inp.focus();
    return;
  }
  if(customRow) customRow.style.display='none';
  if(kind==='ping') appSettingsJS.ping_test_url = val;
  else appSettingsJS.speed_test_url = val;
  saveAppSettings();
}
function updateTestURLCustom(kind, raw){
  var v = (raw||'').trim();
  if(kind==='ping') appSettingsJS.ping_test_url = v;
  else appSettingsJS.speed_test_url = v;
  saveAppSettings();
}

// updateMTU clamps to [576, 9000] (the same clamp Go applies). Empty
// or non-numeric resets to default 9000. Setting takes effect on next
// connect — there's no point in trying to hot-swap the TUN MTU.
function updateMTU(raw){
  var v = parseInt(raw,10);
  if(isNaN(v) || v<=0){ v = 9000; }
  if(v < 576)  v = 576;
  if(v > 9000) v = 9000;
  appSettingsJS.tun_mtu = v;
  var inp=document.getElementById('set-mtu'); if(inp) inp.value=v;
  saveAppSettings();
}

// toggleStats turns the traffic counter UI on or off. Storage is
// inverted (stats_disabled) so the JSON default state is "enabled".
function toggleStats(on){
  appSettingsJS.stats_disabled = !on;
  saveAppSettings();
  // Refresh the panel immediately so the change is visible without
  // waiting for the next ticker pulse — when disabled, both stats hide.
  onStatsUpdate(Object.assign({}, lastStats, {enabled: on}));
}

// resetTotalStats clears the lifetime traffic totals. Server zeros the
// counters and broadcasts a stats_update; we also patch our local copy
// so the Settings dialog row updates without a re-open.
function resetTotalStats(){
  if(!confirm('Reset the lifetime traffic counter to 0? The current session is not affected.')) return;
  fetch('/api/stats/reset',{method:'POST'}).then(function(){
    appSettingsJS.stats_total_up = 0;
    appSettingsJS.stats_total_down = 0;
    var lbl=document.getElementById('stats-total-label');
    if(lbl) lbl.textContent='Lifetime total: ↑0 B ↓0 B';
  }).catch(console.error);
}



function domainChipKey(ev){
  if(ev.key!=='Enter')return;
  ev.preventDefault();
  var val=ev.target.value.trim().toLowerCase().replace(/^https?:\/\//, '').replace(/\/.*$/, '');
  if(!val)return;
  if(!appSettingsJS.direct_domains)appSettingsJS.direct_domains=[];
  for(var i=0;i<appSettingsJS.direct_domains.length;i++){
    if(appSettingsJS.direct_domains[i].toLowerCase()===val)return;
  }
  appSettingsJS.direct_domains.push(val);
  ev.target.value='';
  saveAppSettings(function(){
    var wrap=document.getElementById('domain-chips');
    var inp=document.getElementById('domain-input');
    var chips=wrap.querySelectorAll('.chip');
    chips.forEach(function(c){c.remove();});
    for(var i=0;i<appSettingsJS.direct_domains.length;i++){
      var sp=document.createElement('span');
      sp.className='chip';sp.setAttribute('data-d',appSettingsJS.direct_domains[i]);
      sp.innerHTML=x(appSettingsJS.direct_domains[i])+'<span class="chip-x" onclick="removeDomainChip(this)">x</span>';
      wrap.insertBefore(sp,inp);
    }
  });
}

function removeDomainChip(el){
  var chip=el.parentElement;
  var d=chip.getAttribute('data-d');
  chip.remove();
  if(!appSettingsJS.direct_domains)return;
  appSettingsJS.direct_domains=appSettingsJS.direct_domains.filter(function(v){return v.toLowerCase()!==d.toLowerCase();});
  saveAppSettings();
}

// Apps without VPN — process names matched by sing-box (TUN mode only).
function appChipKey(ev){
  if(ev.key!=='Enter')return;
  ev.preventDefault();
  // Strip whitespace + path; keep just the executable name. Case-insensitive.
  var raw=ev.target.value.trim();
  if(!raw)return;
  // If user pastes a full path like C:\Program Files\Foo\foo.exe, take the leaf.
  var leaf=raw.replace(/[\/\\]+$/,'');
  var sep=Math.max(leaf.lastIndexOf('\\'), leaf.lastIndexOf('/'));
  if(sep>=0) leaf=leaf.substring(sep+1);
  leaf=leaf.toLowerCase();
  if(!leaf)return;
  if(!appSettingsJS.direct_apps)appSettingsJS.direct_apps=[];
  for(var i=0;i<appSettingsJS.direct_apps.length;i++){
    if(appSettingsJS.direct_apps[i].toLowerCase()===leaf)return;
  }
  appSettingsJS.direct_apps.push(leaf);
  ev.target.value='';
  saveAppSettings(function(){
    var wrap=document.getElementById('app-chips');
    var inp=document.getElementById('app-input');
    if(!wrap||!inp)return;
    wrap.querySelectorAll('.chip').forEach(function(c){c.remove();});
    for(var i=0;i<appSettingsJS.direct_apps.length;i++){
      var sp=document.createElement('span');
      sp.className='chip';sp.setAttribute('data-a',appSettingsJS.direct_apps[i]);
      sp.innerHTML=x(appSettingsJS.direct_apps[i])+'<span class="chip-x" onclick="removeAppChip(this)">x</span>';
      wrap.insertBefore(sp,inp);
    }
  });
}

function removeAppChip(el){
  var chip=el.parentElement;
  var a=chip.getAttribute('data-a');
  chip.remove();
  if(!appSettingsJS.direct_apps)return;
  appSettingsJS.direct_apps=appSettingsJS.direct_apps.filter(function(v){return v.toLowerCase()!==a.toLowerCase();});
  saveAppSettings();
}

function openSourcesSettings(){
  closeCtxMenu();
  if(document.getElementById('tab-modal'))return;
  var tab=tabsList.find(function(t){return t.id==='main';});
  var ef=tab&&tab.exclude_filter?tab.exclude_filter:[];
  var chipsHtml='';
  for(var i=0;i<ef.length;i++){
    chipsHtml+='<span class="chip" data-c="'+x(ef[i])+'">'+x(ef[i])+'<span class="chip-x" onclick="this.parentElement.remove()">x</span></span>';
  }
  var ov=document.createElement('div');
  ov.className='modal-overlay';ov.id='tab-modal';
  ov.onclick=function(ev){if(ev.target===ov)ov.remove();};
  ov.innerHTML=
    '<div class="modal-box" style="min-width:400px">'+
      '<div class="modal-title">Sources Settings</div>'+
      '<div class="modal-label">Auto-refresh interval (minutes, 0 = off)</div>'+
      '<input class="modal-input" id="ms-src-refresh" type="number" min="0" value="'+(tab&&tab.refresh_min||0)+'" style="width:80px;margin-bottom:10px">'+
      '<div class="modal-label">Exclude filter</div>'+
      '<div class="chips-wrap" id="src-filter-chips">'+chipsHtml+
        '<input class="chip-input" id="src-filter-input" placeholder="e.g. Russia, press Enter" onkeydown="srcFilterKey(event)">'+
      '</div>'+
      '<div class="modal-hint">Configs with matching name will be hidden.</div>'+
      '<div class="modal-btns">'+
        '<button class="btn ghost" onclick="document.getElementById(\'tab-modal\').remove()">cancel</button>'+
        '<button class="btn ghost" onclick="saveSourcesSettings()">save</button>'+
      '</div>'+
    '</div>';
  document.body.appendChild(ov);
}
function srcFilterKey(ev){
  if(ev.key!=='Enter')return;
  ev.preventDefault();
  var val=ev.target.value.trim();
  if(!val)return;
  ev.target.value='';
  var wrap=document.getElementById('src-filter-chips');
  var inp=document.getElementById('src-filter-input');
  var sp=document.createElement('span');
  sp.className='chip';sp.setAttribute('data-c',val);
  sp.innerHTML=x(val)+'<span class="chip-x" onclick="this.parentElement.remove()">x</span>';
  wrap.insertBefore(sp,inp);
}
function saveSourcesSettings(){
  var refreshMin=parseInt(document.getElementById('ms-src-refresh').value)||0;
  var chips=document.querySelectorAll('#src-filter-chips .chip');
  var ef=[];
  chips.forEach(function(c){ef.push(c.getAttribute('data-c'));});
  document.getElementById('tab-modal').remove();
  fetch('/api/tab/set-url?id=main',{method:'POST',
    headers:{'Content-Type':'application/json'},
    body:JSON.stringify({refresh_min:refreshMin,exclude_filter:ef})
  }).then(function(){rebuildTable();}).catch(console.error);
}

// modalFiles holds the set of files (existing + newly added) for the
// currently open tab settings modal. Each item has {name, content, isNew}.
// We populate it from tab.source_files on open, mutate as the user adds /
// removes, then send it back on save.
var modalFiles=[];

// fmtBytes formats a byte count for both file sizes (used by the
// per-tab file picker) and traffic totals (used by the header stats
// panel and Settings). Lifetime totals routinely climb into GB/TB
// territory, so the scale runs all the way up. Compact precision
// (≤2 decimals) keeps the column width predictable as values grow.
function fmtBytes(n){
  if(!n||n<0) return '0 B';
  if(n<1024) return n+' B';
  const units=['KB','MB','GB','TB'];
  let v=n/1024, i=0;
  while(v >= 1024 && i < units.length-1){ v/=1024; i++; }
  const s = v >= 100 ? v.toFixed(0) : (v >= 10 ? v.toFixed(1) : v.toFixed(2));
  return s+' '+units[i];
}

function renderFileList(){
  var wrap=document.getElementById('ms-files');
  if(!wrap) return;
  if(modalFiles.length===0){
    wrap.innerHTML='<div class="modal-hint" style="margin:0 0 6px">No files added. Use the + add file button below.</div>';
    return;
  }
  var html='';
  for(var i=0;i<modalFiles.length;i++){
    var f=modalFiles[i];
    var pathTitle=f.path||f.name;
    var sz=typeof f.size==='number'?f.size:0;
    html+='<div class="file-row'+(f.isNew?' new':'')+'" title="'+x(pathTitle)+'">'+
      '<span class="file-ico">📄</span>'+
      '<span class="file-name">'+x(f.name)+'</span>'+
      '<span class="file-size">'+fmtBytes(sz)+'</span>'+
      '<button class="url-rm" onclick="removeModalFile('+i+')" title="Remove">x</button>'+
      '</div>';
  }
  wrap.innerHTML=html;
}

function removeModalFile(i){
  if(i<0||i>=modalFiles.length) return;
  modalFiles.splice(i,1);
  renderFileList();
}

// pickModalFiles opens the native Windows file dialog through the Go-side
// binding. Files come back with their full disk paths so the server can
// re-read them on RELOAD when their content changes.
//
// Drag-and-drop was supported earlier but was removed — without access to
// the original file path (which the web platform doesn't expose for dropped
// files) RELOAD couldn't pick up edits, which made the feature confusing.
function pickModalFiles(){
  if(typeof window._goPickFiles!=='function'){
    console.warn('Native file picker is unavailable — pickModalFiles is a no-op outside the desktop shell.');
    return;
  }
  Promise.resolve(window._goPickFiles()).then(function(picked){
    if(!picked||picked.length===0) return;
    for(var i=0;i<picked.length;i++){
      var f=picked[i];
      if(!f || !f.path) continue;
      modalFiles.push({
        name: f.name||'file.txt',
        path: f.path,
        size: typeof f.size==='number'?f.size:0,
        mtime: typeof f.mtime==='number'?f.mtime:0,
        isNew: true
      });
    }
    renderFileList();
  }).catch(function(err){
    console.error('native picker failed:', err);
  });
}

function openTabSettings(tabId){
  closeCtxMenu();
  const tab=tabsList.find(t=>t.id===tabId);
  if(!tab)return;
  var urls=tab.source_urls||[];
  if(urls.length===0&&tab.source_url) urls=[tab.source_url];
  var urlsHtml='';
  for(var i=0;i<urls.length;i++){
    urlsHtml+='<div class="url-row"><input class="modal-input ms-url" value="'+x(urls[i])+'" placeholder="https://raw.githubusercontent.com/..."><button class="url-rm" onclick="this.parentElement.remove()" title="Remove">x</button></div>';
  }
  if(urls.length===0) urlsHtml='<div class="url-row"><input class="modal-input ms-url" value="" placeholder="https://raw.githubusercontent.com/..."><button class="url-rm" onclick="this.parentElement.remove()" title="Remove">x</button></div>';

  var ef=tab.exclude_filter||[];
  var efHtml='';
  for(var i=0;i<ef.length;i++){
    efHtml+='<span class="chip" data-c="'+x(ef[i])+'">'+x(ef[i])+'<span class="chip-x" onclick="this.parentElement.remove()">x</span></span>';
  }

  // Seed modalFiles with what's already saved on the tab. We only carry
  // file metadata (name, path, size, mtime) — actual content is read from
  // disk on every fetch by the server, so the renderer never holds it.
  modalFiles=[];
  var existing=tab.source_files||[];
  for(var i=0;i<existing.length;i++){
    modalFiles.push({
      name: existing[i].name||'file.txt',
      path: existing[i].path||'',
      size: typeof existing[i].size==='number'?existing[i].size:0,
      mtime: typeof existing[i].mtime==='number'?existing[i].mtime:0,
      isNew: false
    });
  }

  const ov=document.createElement('div');
  ov.className='modal-overlay';ov.id='tab-modal';
  ov.onclick=(e)=>{if(e.target===ov)ov.remove();};
  ov.innerHTML=
    '<div class="modal-box" id="tab-modal-box" style="min-width:440px">'+
      '<div class="modal-title">Tab Settings</div>'+
      '<div class="modal-label">Name</div>'+
      '<input class="modal-input" id="ms-name" value="'+x(tab.name)+'" maxlength="40">'+
      '<div class="modal-label">Source URLs (raw links, base64 subscriptions)</div>'+
      '<div id="ms-urls">'+urlsHtml+'</div>'+
      '<button class="btn ghost" style="font-size:9px;margin:4px 0 8px" onclick="addURLRow()">+ add URL</button>'+
      '<div class="modal-label">Files (loaded in addition order, after URLs)</div>'+
      '<div id="ms-files"></div>'+
      '<button class="btn ghost" style="font-size:9px;margin:4px 0 8px" onclick="pickModalFiles()">+ add file</button>'+
      '<div class="modal-hint">Files are read from disk on every RELOAD, so edits propagate without re-adding. No size limit — only the path is stored.</div>'+
      '<div class="modal-label">Auto-refresh interval (minutes, 0 = off)</div>'+
      '<input class="modal-input" id="ms-refresh" type="number" min="0" value="'+(tab.refresh_min||0)+'" style="width:80px;margin-bottom:10px">'+
      '<div class="modal-row" style="margin-top:6px;margin-bottom:12px;align-items:flex-end">'+
        '<span class="modal-row-label">Deduplicate duplicate configs</span>'+
        renderDedupSeg(tab.dedup_mode||(tab.dedup?'hide':''))+
      '</div>'+
      '<div class="modal-hint"><strong>Off</strong>: show everything. <strong>Hide</strong>: filter from view, reversible. <strong>Delete</strong>: permanently remove duplicate entries. Matching is by vless body (ignores the name).</div>'+
      '<div class="modal-label">Exclude filter</div>'+
      '<div class="chips-wrap" id="tab-filter-chips">'+efHtml+
        '<input class="chip-input" id="tab-filter-input" placeholder="e.g. Russia, press Enter" onkeydown="tabFilterKey(event)">'+
      '</div>'+
      '<div class="modal-hint">Configs with matching name will be hidden.</div>'+
      '<div class="modal-btns">'+
        '<button class="btn ghost" onclick="document.getElementById(\'tab-modal\').remove()">cancel</button>'+
        '<button class="btn ghost" onclick="saveTabSettings(\''+tabId+'\')">save</button>'+
      '</div>'+
    '</div>';
  document.body.appendChild(ov);
  renderFileList();
  document.getElementById('ms-name').focus();
  document.getElementById('ms-name').select();
}
function tabFilterKey(ev){
  if(ev.key!=='Enter')return;
  ev.preventDefault();
  var val=ev.target.value.trim();
  if(!val)return;
  ev.target.value='';
  var wrap=document.getElementById('tab-filter-chips');
  var inp=document.getElementById('tab-filter-input');
  var sp=document.createElement('span');
  sp.className='chip';sp.setAttribute('data-c',val);
  sp.innerHTML=x(val)+'<span class="chip-x" onclick="this.parentElement.remove()">x</span>';
  wrap.insertBefore(sp,inp);
}
function addURLRow(){
  var wrap=document.getElementById('ms-urls');
  if(!wrap)return;
  var div=document.createElement('div');
  div.className='url-row';
  div.innerHTML='<input class="modal-input ms-url" value="" placeholder="https://raw.githubusercontent.com/..."><button class="url-rm" onclick="this.parentElement.remove()" title="Remove">x</button>';
  wrap.appendChild(div);
  div.querySelector('input').focus();
}

// renderDedupSeg returns HTML for the 3-state dedup selector shown in
// tab settings. The currently-selected mode gets the .active class; the
// underlying value is stored as data-mode on each button so saveTabSettings
// can read it back without consulting state.
function renderDedupSeg(currentMode){
  if(!currentMode) currentMode='';
  if(currentMode==='off') currentMode=''; // be lenient about legacy values
  var modes=[
    {v:'',       l:'Off'},
    {v:'hide',   l:'Hide'},
    {v:'delete', l:'Delete'}
  ];
  var html='<div class="seg-group" id="ms-dedup-seg" role="radiogroup" aria-label="Deduplicate configs">';
  for(var i=0;i<modes.length;i++){
    var m=modes[i];
    var active=(currentMode===m.v)?' active':'';
    html+='<button type="button" class="seg-btn'+active+'" data-mode="'+m.v+'" '+
      'onclick="selectDedupMode(this)" '+
      'title="'+(m.v===''?'No deduplication':m.v==='hide'?'Hide duplicates from view (reversible)':'Permanently delete duplicates')+'">'+
      m.l+'</button>';
  }
  return html+'</div>';
}

function selectDedupMode(btn){
  var group=btn.parentElement;
  group.querySelectorAll('.seg-btn').forEach(function(b){ b.classList.remove('active'); });
  btn.classList.add('active');
}

function saveTabSettings(tabId){
  var name=document.getElementById('ms-name').value.trim();
  var refreshMin=parseInt(document.getElementById('ms-refresh').value)||0;
  // Read the active segmented-control choice. Empty data-mode means "off".
  var dedupMode='';
  var activeSeg=document.querySelector('#ms-dedup-seg .seg-btn.active');
  if(activeSeg) dedupMode=activeSeg.getAttribute('data-mode')||'';
  var urlInputs=document.querySelectorAll('#ms-urls .ms-url');
  var urls=[];
  urlInputs.forEach(function(inp){
    var v=inp.value.trim();
    if(v) urls.push(v);
  });
  var chips=document.querySelectorAll('#tab-filter-chips .chip');
  var ef=[];
  chips.forEach(function(c){ef.push(c.getAttribute('data-c'));});
  // Send only the file metadata the server needs to find and read each
  // file. Strip isNew (client-only) and size/mtime (server re-stats on
  // save anyway, so what we'd send is stale). Content is never on the
  // wire — the server reads from disk on every fetch.
  var files=modalFiles
    .filter(function(f){ return !!f.path; })
    .map(function(f){ return {name:f.name, path:f.path}; });
  document.getElementById('tab-modal').remove();
  modalFiles=[];
  if(name){
    fetch('/api/tab/rename?id='+encodeURIComponent(tabId)+'&name='+encodeURIComponent(name),{method:'POST'}).catch(console.error);
  }
  fetch('/api/tab/set-url?id='+encodeURIComponent(tabId),{method:'POST',
    headers:{'Content-Type':'application/json'},
    body:JSON.stringify({urls:urls,files:files,refresh_min:refreshMin,exclude_filter:ef,dedup_mode:dedupMode})
  }).then(function(){rebuildTable();}).catch(console.error);
}

// ── Row selection + delete ───────────────────────────────────────────────────
document.addEventListener('keydown',function(e){
  // Ctrl+A: select all rows (prevent text selection always)
  if(e.ctrlKey&&(e.key==='a'||e.key==='A')){
    if(document.activeElement&&(document.activeElement.tagName==='INPUT'||document.activeElement.tagName==='TEXTAREA'))return;
    e.preventDefault();
    e.stopPropagation();
    selectedRows.clear();
    Object.values(entries).forEach(en=>selectedRows.add(en.index));
    rebuildTable();
    return;
  }
  // Ctrl+C is handled by the 'copy' event listener — see above.
  // That path is more reliable than intercepting keydown + calling
  // navigator.clipboard.writeText(), which is async, permission-gated,
  // and races with the native copy pipeline (the symptoms of which were
  // "копируется не та конфигурация" — the OS clipboard kept the
  // previous value when writeText silently failed).
  if((e.key==='Delete'||e.key==='Backspace')&&selectedRows.size>0&&activeTabId!=='main'){
    if(document.activeElement&&(document.activeElement.tagName==='INPUT'||document.activeElement.tagName==='TEXTAREA'))return;
    e.preventDefault();
    deleteSelectedRows();
  }
  if(e.key==='Escape'){
    if(document.getElementById('tab-modal'))document.getElementById('tab-modal').remove();
    if(selectedRows.size>0){selectedRows.clear();rebuildTable();}
  }
},true);

function toggleRowSelect(idx,e){
  if(e&&e.shiftKey&&selectedRows.size>0){
    // Range select using current visual order (sortedList)
    const idxs=sortedList.map(en=>en.index);
    const lastSel=[...selectedRows].pop();
    const from=idxs.indexOf(lastSel),to=idxs.indexOf(idx);
    if(from>=0&&to>=0){
      const lo=Math.min(from,to),hi=Math.max(from,to);
      for(let i=lo;i<=hi;i++) selectedRows.add(idxs[i]);
    }
  } else if(e&&e.ctrlKey){
    if(selectedRows.has(idx)) selectedRows.delete(idx); else selectedRows.add(idx);
  } else {
    if(selectedRows.has(idx)&&selectedRows.size===1) selectedRows.clear();
    else { selectedRows.clear(); selectedRows.add(idx); }
  }
  rebuildTable();
}


// ── Ctrl+V paste handler ─────────────────────────────────────────────────────
document.addEventListener('paste', function(e){
  if(document.activeElement&&(document.activeElement.tagName==='INPUT'||document.activeElement.tagName==='TEXTAREA'))return;
  const text=(e.clipboardData||window.clipboardData).getData('text');
  if(!text||!text.includes('vless://'))return;
  if(activeTabId==='main'){
    fetch('/api/tab/create',{method:'POST'}).then(r=>r.json()).then(tab=>{
      fetch('/api/tab/switch?id='+tab.id,{method:'POST'}).then(()=>{
        fetch('/api/tab/paste?id='+tab.id,{method:'POST',body:text}).catch(console.error);
      });
    }).catch(console.error);
    return;
  }
  fetch('/api/tab/paste?id='+activeTabId,{method:'POST',body:text}).catch(console.error);
});

// Drag-and-drop file ingestion has been removed.
// Without the original disk path (which the web platform doesn't expose
// for dropped files) RELOAD couldn't pick up later edits, which made the
// feature confusing. Use the "+ add file" button in tab settings instead —
// it goes through the native Windows file dialog and yields full paths.
//
// We still need to swallow drag/drop on the document, otherwise dropping a
// file would navigate the WebView away from the app to that file.
(function(){
  document.addEventListener('dragover', function(e){
    if(e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types,'Files')>=0){
      e.preventDefault();
      e.dataTransfer.dropEffect='none';
    }
  });
  document.addEventListener('drop', function(e){
    if(e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files.length>0){
      e.preventDefault();
    }
  });
})();

// ── Custom title bar (standalone frameless mode) ─────────────────────────────
(function(){
  if(typeof _goWinClose !== 'function') return;

  const titlebar = document.getElementById('titlebar');
  const btnMax   = document.getElementById('tb-max');
  titlebar.classList.add('active');

  // Load logo from Go binding
  if(typeof _goLogoBase64 === 'function'){
    _goLogoBase64().then(b64=>{
      if(b64) document.getElementById('tb-logo-img').src='data:image/png;base64,'+b64;
    }).catch(()=>{});
  }

  const svgMax     = '<svg width="9" height="9" viewBox="0 0 9 9"><rect x="0.5" y="0.5" width="8" height="8" fill="none" stroke="currentColor" stroke-width="1"/></svg>';
  const svgRestore = '<svg width="11" height="11" viewBox="0 0 11 11"><rect x="2.5" y="0.5" width="8" height="8" fill="none" stroke="currentColor" stroke-width="1"/><rect x="0.5" y="2.5" width="8" height="8" fill="var(--bg)" stroke="currentColor" stroke-width="1"/></svg>';

  function syncMaxIcon(){
    _goWinIsMaximized().then(v=>{
      btnMax.innerHTML = v ? svgRestore : svgMax;
      btnMax.title = v ? 'Restore' : 'Maximize';
    }).catch(()=>{});
  }
  syncMaxIcon();
  window.addEventListener('resize', syncMaxIcon);

  document.getElementById('tb-close').addEventListener('click', ()=>_goWinClose());
  document.getElementById('tb-min').addEventListener('click',   ()=>_goWinMinimize());
  btnMax.addEventListener('click', ()=>_goWinMaximize().then(syncMaxIcon));

  document.getElementById('tb-drag').addEventListener('mousedown', e=>{
    if(e.button===0 && !e.target.closest('.tb-btns')) _goWinDragStart();
  });
  document.getElementById('tb-drag').addEventListener('dblclick', e=>{
    if(!e.target.closest('.tb-btns')) _goWinMaximize().then(syncMaxIcon);
  });
})();

</script>
</body>
</html>`

// ── Track spawned test processes for cleanup ─────────────────────
var (
	spawnedPIDs   []int
	spawnedPIDsMu sync.Mutex
)

func trackPID(pid int) {
	spawnedPIDsMu.Lock()
	spawnedPIDs = append(spawnedPIDs, pid)
	spawnedPIDsMu.Unlock()
}

func untrackPID(pid int) {
	spawnedPIDsMu.Lock()
	for i, p := range spawnedPIDs {
		if p == pid {
			spawnedPIDs = append(spawnedPIDs[:i], spawnedPIDs[i+1:]...)
			break
		}
	}
	spawnedPIDsMu.Unlock()
}

// killOrphanedXray kills any test xray processes left behind.
func killOrphanedXray() {
	spawnedPIDsMu.Lock()
	pids := make([]int, len(spawnedPIDs))
	copy(pids, spawnedPIDs)
	spawnedPIDsMu.Unlock()
	for _, pid := range pids {
		if p, err := os.FindProcess(pid); err == nil {
			p.Kill() //nolint:errcheck
		}
	}
}

// ─────────────────────────── route registration ──────────────────

func handleSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == "GET" {
		w.Header().Set("Content-Type", "application/json")
		settingsMu.RLock()
		json.NewEncoder(w).Encode(appSettings)
		settingsMu.RUnlock()
		return
	}
	// POST: update settings
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	var newSettings AppSettings
	if err := json.Unmarshal(body, &newSettings); err != nil {
		http.Error(w, "bad json: "+err.Error(), 400)
		return
	}
	settingsMu.Lock()
	// Preserve server-authoritative counters. The Settings UI loads them
	// at open time and POSTs them back unchanged, but the 30s ticker can
	// update them while the dialog is open — using the client's snapshot
	// would roll them back. Reset goes through a dedicated endpoint.
	newSettings.StatsTotalUp = appSettings.StatsTotalUp
	newSettings.StatsTotalDown = appSettings.StatsTotalDown
	appSettings = newSettings
	settingsMu.Unlock()
	saveSettings()
	state.broadcast(SSEEvent{Type: "stats_update", Payload: statsSnapshot(currentSessionCounter())})
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// currentSessionCounter returns the live traffic counter if a session is
// active, or nil otherwise. Used by handlers that need a fresh stats
// snapshot without poking at connManager internals from the outside.
func currentSessionCounter() *trafficCounter {
	state.conn.mu.Lock()
	defer state.conn.mu.Unlock()
	return state.conn.counter
}

// handleStatsReset clears the lifetime traffic totals. The live session
// counter (if any) is left alone — the user is asking to reset
// "lifetime", and the current session will fold in normally on
// disconnect, starting accumulation fresh from zero.
func handleStatsReset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	settingsMu.Lock()
	appSettings.StatsTotalUp = 0
	appSettings.StatsTotalDown = 0
	settingsMu.Unlock()
	// Forget what was already folded in for the live session so its
	// future deltas don't immediately re-inflate the just-cleared
	// total. The visible session bytes (counter.Up/Down) stay intact.
	if c := currentSessionCounter(); c != nil {
		c.LastPersistedUp.Store(c.Up.Load())
		c.LastPersistedDown.Store(c.Down.Load())
	}
	saveSettings()
	state.broadcast(SSEEvent{Type: "stats_update", Payload: statsSnapshot(currentSessionCounter())})
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// startAutoRefresh periodically checks all tabs and refreshes those with RefreshMin > 0.
// For Sources tab, it uses the same interval logic.
func startAutoRefresh() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	lastRefresh := make(map[string]time.Time)
	for range ticker.C {
		state.mu.RLock()
		tabs := make([]Tab, len(state.tabs))
		copy(tabs, state.tabs)
		state.mu.RUnlock()

		settingsMu.RLock()
		srcEnabled := appSettings.SourcesEnabled
		settingsMu.RUnlock()

		for _, t := range tabs {
			if t.RefreshMin <= 0 {
				continue
			}
			if t.IsMain && !srcEnabled {
				continue
			}
			if time.Since(lastRefresh[t.ID]) < time.Duration(t.RefreshMin)*time.Minute {
				continue
			}
			lastRefresh[t.ID] = time.Now()
			if t.IsMain {
				go fetchAndInit()
			} else if len(t.SourceURLs) > 0 || len(t.SourceFiles) > 0 {
				go fetchTabURLs(t.ID, t.SourceURLs, t.SourceFiles)
			}
		}
	}
}

func handleRestartAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(200)
	w.Write([]byte("restarting"))
	go func() {
		time.Sleep(200 * time.Millisecond)
		stopConnection()
		killOrphanedXray()
		restartAsAdmin()
	}()
}

func registerRoutes() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(indexHTML))
	})
	http.HandleFunc("/api/stream", handleSSE)
	http.HandleFunc("/api/connect", handleConnect)
	http.HandleFunc("/api/disconnect", handleDisconnect)
	http.HandleFunc("/api/conn/state", handleConnState)
	http.HandleFunc("/api/ping/one", handlePingOne)
	http.HandleFunc("/api/ping/all", handlePingAll)
	http.HandleFunc("/api/speed/one", handleSpeedOne)
	http.HandleFunc("/api/speed/all", handleSpeedAll)
	http.HandleFunc("/api/tests/cancel", handleTestsCancel)
	http.HandleFunc("/api/reload", handleReload)
	http.HandleFunc("/api/tab/create", handleTabCreate)
	http.HandleFunc("/api/tab/delete", handleTabDelete)
	http.HandleFunc("/api/tab/switch", handleTabSwitch)
	http.HandleFunc("/api/tab/paste", handleTabPaste)
	http.HandleFunc("/api/tab/rename", handleTabRename)
	http.HandleFunc("/api/tab/set-url", handleTabSetURL)
	http.HandleFunc("/api/tab/delete-entries", handleTabDeleteEntries)
	http.HandleFunc("/api/tab/reorder", handleTabReorder)
	http.HandleFunc("/api/settings", handleSettings)
	http.HandleFunc("/api/stats/reset", handleStatsReset)
	http.HandleFunc("/api/restart-admin", handleRestartAdmin)
}

func httpListenAndServe() error {
	if err := http.ListenAndServe(fmt.Sprintf(":%d", webPort), nil); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

// ─────────────────────────── main ────────────────────────────────

func main() {
	// On Windows the standalone build calls standaloneMain() which uses
	// embedded binaries and shows a native Fyne UI window (no browser needed).
	// On other platforms (or when LEGACY=1 env is set), fall through to
	// the classic CLI mode that takes xray/sing-box paths as arguments.
	if os.Getenv("LEGACY") == "" {
		standaloneMain()
		return
	}

	// ── Legacy / cross-platform CLI mode ─────────────────────────────
	xrayBin := "xray"
	if len(os.Args) > 1 {
		xrayBin = os.Args[1]
	}
	if _, err := exec.LookPath(xrayBin); err != nil {
		if _, err2 := os.Stat(xrayBin); err2 != nil {
			fmt.Fprintf(os.Stderr,
				"❌  xray not found.\n    Usage: %s [xray] [sing-box]\n    Releases: https://github.com/XTLS/Xray-core/releases\n",
				os.Args[0])
			os.Exit(1)
		}
	}
	state.xrayBin = xrayBin

	if len(os.Args) > 2 {
		sb := os.Args[2]
		if _, err := exec.LookPath(sb); err == nil {
			state.singboxBin = sb
		} else if _, err2 := os.Stat(sb); err2 == nil {
			state.singboxBin = sb
		} else {
			fmt.Fprintf(os.Stderr, "⚠  sing-box not found at %q — TUN mode unavailable\n", sb)
		}
	} else if path, err := exec.LookPath("sing-box"); err == nil {
		state.singboxBin = path
		fmt.Printf("ℹ  sing-box auto-detected: %s\n", path)
	}

	isAdmin := checkAdmin()
	fmt.Printf("✅  vair → http://localhost:%d\n    xray:     %s\n", webPort, xrayBin)
	if state.singboxBin != "" {
		fmt.Printf("    sing-box: %s\n", state.singboxBin)
	} else {
		fmt.Printf("    sing-box: not found (TUN unavailable)\n")
	}
	fmt.Printf("    admin: %v\n\n", isAdmin)
	if state.singboxBin != "" && !isAdmin {
		fmt.Println("⚠  Run as administrator to enable TUN mode.")
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		fmt.Println("\n⏹  Shutting down — cleaning up…")
		stopConnection()
		killOrphanedXray()
		os.Exit(0)
	}()

	registerRoutes()
	loadTabs()
	loadSettings()
	go startAutoRefresh()
	go fetchAndInit()
	if err := httpListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
