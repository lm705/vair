package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
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

	// 50 MB: large enough to fill the measurement window (speedDuration) for
	// any realistic proxy speed so the window isn't cut short by an early
	// EOF (the old "response too fast" false positive), yet within
	// Cloudflare's accepted __down range — bytes=100000000 is rejected by
	// the endpoint, so we cap at 50 MB. We stop reading at the deadline
	// anyway, so this size is an upper bound, not an actual 50 MB download.
	speedTestURLDefault   = "https://speed.cloudflare.com/__down?bytes=50000000"
	speedDuration  = 4 * time.Second
	speedUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

	startupTimeout = 4 * time.Second
	dialTimeout    = 5 * time.Second

	// xrayStartupTimeout: time to wait for xray HTTP port to open during ping/speed test.
	// Increased for Windows Defender scan delay on first run of extracted binary.
	xrayStartupTimeout = 8 * time.Second

	// xrayConnTimeout: time to wait for xray to start in persistent proxy connection mode.
	xrayConnTimeout = 12 * time.Second

	// singboxStartupTimeout: time to wait for the sing-box HTTP port to open
	// during ping/speed test of a UDP-family node (Hysteria2/TUIC). The QUIC
	// handshake is slower to come up than a TCP outbound, so this is a touch
	// more generous than xrayStartupTimeout.
	singboxStartupTimeout = 10 * time.Second

	// singboxConnTimeout: time to wait for sing-box to start in a persistent
	// proxy connection. sing-box binds the local HTTP inbound before it ever
	// dials the QUIC outbound, so this only bounds local listener readiness;
	// kept generous to cover first-run Defender scan of the extracted binary.
	singboxConnTimeout = 15 * time.Second

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
	// Fallback speed URL — used ONLY when the primary speed URL returns HTTP
	// 429 (rate-limit). "" / unset → the CacheFly default (on by default);
	// "__none" → fallback explicitly disabled; anything else → that URL. See
	// currentSpeedFallbackURL. Each attempt has its own bounded
	// http.Client.Timeout, so an unreachable fallback cannot extend the test
	// indefinitely or leak goroutines.
	SpeedTestURLFallback string `json:"speed_test_url_fallback,omitempty"`
	// Raw URL of the most recently connected config. Drives the "last"
	// badge in the table. Persisted so the badge survives restarts; only
	// one config carries it at a time.
	LastConnectedRaw   string `json:"last_connected_raw,omitempty"`
	// Favorite configs, keyed by their raw URL (stable across reload/sort).
	// Starred rows sort to the top in the default order.
	Favorites          []string `json:"favorites,omitempty"`
	// VerboseLogs raises the xray/sing-box log level from warning→info so the
	// Logs panel shows the detailed per-connection lines (like v2rayN). Takes
	// effect on the next connection.
	VerboseLogs        bool     `json:"verbose_logs,omitempty"`
	// LogTests, when on, emits a [test] line per ping/speed result into the
	// Logs panel. Off by default — bulk tests are noisy.
	LogTests           bool     `json:"log_tests,omitempty"`
	// Per-round ping timeout (ms) and speed-test duration (seconds).
	// Zero / unset → built-in defaults (pingTimeout / speedDuration consts).
	// Clamped to sane bounds inside the accessors. Note that the speed test
	// always runs a ping first to warm the tunnel and get a fresh delay —
	// PingTimeoutMs applies to both standalone ping tests and that
	// pre-speed warm-up.
	PingTimeoutMs      int    `json:"ping_timeout_ms,omitempty"`
	SpeedDurationSec   int    `json:"speed_duration_sec,omitempty"`
	// UI scale and language for the Settings / Tab settings dialogs.
	// Both apply to those modals only — the main window (tabs, table,
	// connection bar, title bar) is intentionally not affected, since
	// that's where pixel-precise typography matters most. ModalFontSize
	// is the px value driving the --modal-fs-base CSS variable; default
	// 11. Language uses BCP-47-ish short codes — "" / "en" / "ru".
	ModalFontSize      int    `json:"modal_font_size,omitempty"`
	Language           string `json:"language,omitempty"`
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

	// ── DNS leak protection (1.5.0) ──────────────────────────────────
	// All zero-valued → 1.4.0 behaviour. Opt-in for now; once we have
	// field reports we'll consider flipping defaults in 1.6.0.
	//
	// DNSLeakProtection is the master switch. When ON we install a real
	// DNS routing block in sing-box, hijack port 53, and turn on
	// strict_route so the WFP filter (Windows) prevents anything from
	// escaping. When OFF the program behaves like 1.4.0.
	// On by default (see appSettings initialiser). No omitempty: an explicit
	// "off" must round-trip to disk, otherwise it would silently revert to the
	// default on next launch.
	DNSLeakProtection  bool   `json:"dns_leak_protection"`
	// KillSwitch only takes effect when DNSLeakProtection is on. With
	// strict_route=true, sing-box already drops traffic that can't go
	// through the tunnel; the kill-switch wording in UI tells the user
	// this is happening. (1.6.0 will add Windows Firewall rules as
	// belt-and-braces.)
	KillSwitch         bool   `json:"kill_switch,omitempty"`
	// BlockLAN is inverted: zero/false = LAN traffic allowed direct.
	// When true the ip_is_private→direct rule is removed and even
	// 192.168.x.x goes through the tunnel.
	BlockLAN           bool   `json:"block_lan,omitempty"`
	// FakeIPDisabled is also inverted: zero/false = FakeIP enabled
	// (when DNSLeakProtection is on). FakeIP returns 198.18.0.0/15
	// pseudo-addresses for A/AAAA queries; real resolution happens
	// once a connection is made through the proxy outbound. Turning
	// it off falls back to real DNS via the proxy detour — slower
	// but more compatible with picky apps (P2P, WebRTC, custom
	// resolvers).
	// FakeIP is OFF by default → FakeIPDisabled defaults to true (see
	// appSettings initialiser). No omitempty for the same round-trip reason as
	// DNSLeakProtection.
	FakeIPDisabled     bool   `json:"fakeip_disabled"`
	// DNS server overrides. Empty falls back to the built-in default
	// for the slot (see dnsServerOr* helpers).
	BootstrapDNS       string `json:"bootstrap_dns,omitempty"` // plain UDP, IP-only
	DirectDNS          string `json:"direct_dns,omitempty"`    // for bypass traffic
	RemoteDNS          string `json:"remote_dns,omitempty"`    // through proxy
	// StaticHosts is a Windows-hosts-file-style domain→IP map.
	// Resolved before any DNS server is asked. Useful for hard-coded
	// VPN-server resolution, custom CNAME-like redirects, or
	// emergency bypass when DNS infrastructure is unreliable.
	StaticHosts        map[string]string `json:"static_hosts,omitempty"`
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

// speedFallbackDefaultURL is the fallback speed-test endpoint used when the
// user hasn't changed the setting (CacheFly). The fallback is on by default —
// it kicks in only when the primary URL returns HTTP 429.
const speedFallbackDefaultURL = "http://cachefly.cachefly.net/100mb.test"

// currentSpeedFallbackURL returns the fallback speed-test URL. The stored
// value distinguishes three cases: "__none" → fallback explicitly disabled
// (returns ""), "" / unset → the CacheFly default (on by default), anything
// else → that custom/preset URL.
func currentSpeedFallbackURL() string {
	settingsMu.RLock()
	u := strings.TrimSpace(appSettings.SpeedTestURLFallback)
	settingsMu.RUnlock()
	switch u {
	case "__none":
		return ""
	case "":
		return speedFallbackDefaultURL
	}
	return u
}

// xrayLogLevel / singboxLogLevel return the log verbosity for generated
// configs. Default is quiet (warning/warn); the "Verbose logs" setting bumps
// both to info so the Logs panel shows per-connection detail. Read under
// settingsMu like the other appSettings accessors.
func xrayLogLevel() string {
	settingsMu.RLock()
	v := appSettings.VerboseLogs
	settingsMu.RUnlock()
	if v {
		return "info"
	}
	return "warning"
}

func singboxLogLevel() string {
	settingsMu.RLock()
	v := appSettings.VerboseLogs
	settingsMu.RUnlock()
	if v {
		return "info"
	}
	return "warn"
}

// xrayLogConfig builds the xray "log" stanza. We deliberately KEEP the access
// log on in quiet (non-verbose) mode: its one-line-per-connection entries
// ("accepted host:port [in -> out]") are a useful summary of what's routed
// where (proxy vs direct), without the info-level transport spam. quiet =
// warnings/errors + access; verbose (loglevel "info") adds the per-connection
// internals ("creating connection", "taking detour", "sniffed domain", …).
// The rate-limit gate keeps even a busy TUN session bounded, and the
// server-IP route carve-out prevents the old connection-storm flood.
func xrayLogConfig() map[string]interface{} {
	return map[string]interface{}{"loglevel": xrayLogLevel()}
}

// recordLastConnected remembers the raw of the most recently connected
// config so the UI can tag exactly one row with a "last" badge. Persisted
// (saveSettings) so the badge survives a restart; a no-op when the raw is
// already current to avoid needless disk writes on reconnect.
func recordLastConnected(raw string) {
	if raw == "" {
		return
	}
	settingsMu.Lock()
	changed := appSettings.LastConnectedRaw != raw
	appSettings.LastConnectedRaw = raw
	settingsMu.Unlock()
	if changed {
		saveSettings()
	}
}

// Sane bounds for the two test-duration knobs. The lower bounds protect
// the tests from being so short they always fail (one round-trip needs at
// least a few hundred ms over a real proxy chain). The upper bounds keep
// "ping all" / "speed all" from blocking forever on dead servers.
const (
	minPingTimeoutMs    = 200
	maxPingTimeoutMs    = 10000
	minSpeedDurationSec = 1
	maxSpeedDurationSec = 60
)

// currentPingTimeout returns the per-round ping timeout. Zero / unset /
// out-of-range falls back to the compile-time pingTimeout default. Same
// value is used for the warm-up ping inside speed tests, so changing it
// affects both ping-only and ping→speed flows.
func currentPingTimeout() time.Duration {
	settingsMu.RLock()
	ms := appSettings.PingTimeoutMs
	settingsMu.RUnlock()
	if ms < minPingTimeoutMs || ms > maxPingTimeoutMs {
		return pingTimeout
	}
	return time.Duration(ms) * time.Millisecond
}

// currentSpeedDuration returns the speed-test download window. Zero / unset
// / out-of-range falls back to the compile-time speedDuration default.
func currentSpeedDuration() time.Duration {
	settingsMu.RLock()
	s := appSettings.SpeedDurationSec
	settingsMu.RUnlock()
	if s < minSpeedDurationSec || s > maxSpeedDurationSec {
		return speedDuration
	}
	return time.Duration(s) * time.Second
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

// ── DNS leak protection accessors ─────────────────────────────────
//
// Built-in defaults chosen for Russia 2026:
//
//   bootstrap = 9.9.9.9 (Quad9). Plain UDP/IP, used to resolve the VPN
//     server hostname on startup before any tunnel exists. Quad9 is
//     non-profit, Swiss, and historically unblocked in RU. Cloudflare
//     was thrown off the list because of the 2025 throttling.
//
//   direct = 77.88.8.8 (Yandex). Used only for bypass traffic (RU
//     sites, custom direct domains). Yandex always works inside RU
//     and privacy doesn't matter for direct traffic — those queries
//     would otherwise go to the user's ISP anyway.
//
//   remote = https://1.1.1.1/dns-query (Cloudflare DoH over IP).
//     Used for VPN-tunnelled traffic, so RU-level throttling is
//     irrelevant — the request goes through the proxy. We use the
//     IP-only DoH URL to avoid the meta-DNS chicken-and-egg of
//     "resolve dns.cloudflare.com first".
const (
	defaultBootstrapDNS = "9.9.9.9"
	defaultDirectDNS    = "77.88.8.8"
	defaultRemoteDNS    = "https://1.1.1.1/dns-query"
)

func dnsLeakProtectionEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.DNSLeakProtection
}

func killSwitchEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	// Kill-switch only makes sense with strict_route, which is only
	// turned on when DNSLeakProtection is on. The UI greys this out
	// in that case but we also defend in code.
	return appSettings.DNSLeakProtection && appSettings.KillSwitch
}

func allowLANTraffic() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return !appSettings.BlockLAN
}

func fakeIPEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	// FakeIP is meaningful only when leak protection is on. When off,
	// sing-box doesn't run its own DNS module at all.
	return appSettings.DNSLeakProtection && !appSettings.FakeIPDisabled
}

func currentBootstrapDNS() string {
	settingsMu.RLock()
	v := strings.TrimSpace(appSettings.BootstrapDNS)
	settingsMu.RUnlock()
	if v == "" {
		return defaultBootstrapDNS
	}
	return v
}
func currentDirectDNS() string {
	settingsMu.RLock()
	v := strings.TrimSpace(appSettings.DirectDNS)
	settingsMu.RUnlock()
	if v == "" {
		return defaultDirectDNS
	}
	return v
}
func currentRemoteDNS() string {
	settingsMu.RLock()
	v := strings.TrimSpace(appSettings.RemoteDNS)
	settingsMu.RUnlock()
	if v == "" {
		return defaultRemoteDNS
	}
	return v
}

// staticHostsSnapshot returns a defensive copy of the user-defined
// hosts map. Empty when none are set. The map is small (rarely more
// than a few entries) so the copy is cheap.
func staticHostsSnapshot() map[string]string {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	if len(appSettings.StaticHosts) == 0 {
		return nil
	}
	out := make(map[string]string, len(appSettings.StaticHosts))
	for k, v := range appSettings.StaticHosts {
		k = strings.TrimSpace(strings.ToLower(k))
		v = strings.TrimSpace(v)
		if k != "" && v != "" {
			out[k] = v
		}
	}
	return out
}

// Defaults: DNS leak protection ON, FakeIP OFF (FakeIPDisabled=true) — picked
// for safer out-of-the-box TUN behaviour (DNS through the tunnel, real DoH
// rather than FakeIP for app compatibility).
var appSettings = AppSettings{SourcesEnabled: true, DNSLeakProtection: true, FakeIPDisabled: true}
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

// excludeColumns is the canonical set of columns the exclude-filter UI can
// target. Anything else in the column position of a "col:val" rule is
// treated as legacy (name-only) data. Keep this in sync with the dropdown
// options in the tab-settings modal.
var excludeColumns = map[string]struct{}{
	"name":      {},
	"type":      {},
	"host":      {},
	"transport": {},
	"security":  {},
}

// parseExcludeRule splits a stored exclude-filter entry into (column, value).
//
// Encoding:
//   - New form:   "column:value"  where column is one of excludeColumns.
//   - Legacy:     "value"         (no colon, or colon-prefix isn't a known
//                                  column) — defaults to column="name" so
//                                  pre-existing user data keeps working
//                                  without a migration step.
//
// The colon split is on the FIRST ':' only — values can contain colons
// without escaping ("name:foo:bar" filters names containing "foo:bar").
func parseExcludeRule(s string) (column, value string) {
	if i := strings.Index(s, ":"); i > 0 {
		col := strings.ToLower(s[:i])
		if _, ok := excludeColumns[col]; ok {
			return col, s[i+1:]
		}
	}
	return "name", s
}

// displayProtocol returns the protocol label as the user sees it in the UI:
// "ss2022" is split out from the backend's unified "ss" Kind by inspecting
// the cipher. Keep in sync with chipProto() in the JS layer.
func displayProtocol(kind, security string) string {
	if kind == "ss" && strings.HasPrefix(security, "2022-blake3-") {
		return "ss2022"
	}
	return kind
}

// shouldSkip reports whether a config row should be hidden because it
// matches any of the per-tab exclude rules. Each rule is "column:value"
// (or bare "value" for legacy name-only rules — see parseExcludeRule).
// A row is skipped if ANY rule's value is a (case-insensitive) substring
// of the corresponding column's value on the row.
//
// The `kind` parameter is the backend protocol id ("vless", "ss", …); the
// type-column match uses displayProtocol so a "ss2022" filter actually
// targets only the SS2022 ciphers, not legacy SS.
func shouldSkip(name, kind, host, network, security string, rules []string) bool {
	if len(rules) == 0 {
		return false
	}
	lowName := strings.ToLower(name)
	lowType := strings.ToLower(displayProtocol(kind, security))
	lowHost := strings.ToLower(host)
	lowNet := strings.ToLower(network)
	lowSec := strings.ToLower(security)
	for _, r := range rules {
		col, val := parseExcludeRule(r)
		val = strings.ToLower(strings.TrimSpace(val))
		if val == "" {
			continue
		}
		var hay string
		switch col {
		case "type":
			hay = lowType
		case "host":
			hay = lowHost
		case "transport":
			hay = lowNet
		case "security":
			hay = lowSec
		default: // "name" + legacy
			hay = lowName
		}
		if strings.Contains(hay, val) {
			return true
		}
	}
	return false
}

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

// ─────────────────────────── log store ───────────────────────────
//
// In-memory ring buffer of recent log lines, shown in the UI's Logs panel.
// Sources: xray/sing-box stderr from the active connection ("xray"/"singbox")
// and Vair's own diagnostics ("vair", via vlog). Each new line is also pushed
// to clients as a lossy SSE "log" event so the panel updates live without
// blocking the broadcast path. The buffer is session-only (not persisted).

type logLine struct {
	T   int64  `json:"t"`   // unix millis
	Lvl string `json:"lvl"` // error | warn | info | raw
	Src string `json:"src"` // xray | singbox | vair
	Msg string `json:"msg"`
}

type logStore struct {
	mu      sync.Mutex
	lines   []logLine // ring buffer for the /api/logs snapshot
	pending []logLine // lines added since the last SSE flush
	cap     int
	// Token-bucket rate limit for high-volume core output (see gate). A
	// verbose TUN connection can emit tens of thousands of "creating
	// connection" lines per second; without a cap they pile up in the ring,
	// the SSE batches, and the client's 1024-slot channel, spiking CPU and
	// RAM into the gigabytes. error/warn lines bypass the limit so failures
	// are never dropped.
	rlStart   time.Time
	rlCount   int
	rlDropped int
}

const (
	logRateInterval = 250 * time.Millisecond
	logRateLimit    = 200 // info/raw lines per interval (~800/s) before dropping
)

var logs = &logStore{cap: 2000}

// ansiRe matches ANSI SGR colour escapes (e.g. sing-box on Windows still
// emits them even when stderr is piped). We strip them before storing so the
// Logs panel shows clean text instead of "[31mERROR[0m".
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

func stripANSI(s string) string {
	// Fast path: most lines (all of xray's) carry no escapes, so skip the
	// regexp entirely unless an ESC byte is actually present. Matters under a
	// verbose flood where this runs tens of thousands of times per second.
	if strings.IndexByte(s, 0x1b) < 0 {
		return s
	}
	return ansiRe.ReplaceAllString(s, "")
}

// add records a line. It does NOT broadcast directly: under verbose logging a
// busy core (especially TUN, which routes all system traffic) can emit
// thousands of lines per second, and one SSE event per line froze the UI
// (every event = a JSON marshal on the server + a parse and DOM write on the
// client). Instead lines accumulate in `pending` and a ticker flushes them as
// a single batched event a few times per second — bounded work regardless of
// log volume. See logFlushLoop.
// push appends a line to both the ring buffer and the pending SSE batch.
// Caller must hold l.mu.
func (l *logStore) push(ln logLine) {
	l.lines = append(l.lines, ln)
	if len(l.lines) > 2*l.cap {
		// Amortised trim: let the slice grow to 2× cap, then drop the oldest
		// half in one copy. This keeps the per-add cost O(1) under a flood
		// instead of copying `cap` elements on every single line.
		l.lines = append(l.lines[:0], l.lines[len(l.lines)-l.cap:]...)
	}
	l.pending = append(l.pending, ln)
	if len(l.pending) > l.cap {
		// A flood between flushes: keep only the newest cap lines for the live
		// stream (the panel re-fetches the full snapshot on open anyway).
		l.pending = append(l.pending[:0], l.pending[len(l.pending)-l.cap:]...)
	}
}

// add stores a pre-classified line (vlog/tlog and result summaries). Not
// rate-limited — these are low-volume and always worth keeping.
func (l *logStore) add(src, lvl, msg string) {
	msg = stripANSI(strings.TrimRight(msg, "\r\n"))
	if msg == "" {
		return
	}
	ln := logLine{T: time.Now().UnixMilli(), Lvl: lvl, Src: src, Msg: msg}
	l.mu.Lock()
	l.push(ln)
	l.mu.Unlock()
}

// gate advances the rate-limit window and reports whether a high-volume core
// line should be kept. error/warn lines (isErrWarn) always pass; info/raw
// lines are capped at logRateLimit per window. Callers MUST evaluate this on
// the raw bytes BEFORE allocating a string (scanner.Text()), so each dropped
// line under a verbose flood costs almost nothing — no string, no GC churn.
// When a window rolls over with drops, one summary line is pushed so the user
// knows output was suppressed.
func (l *logStore) gate(isErrWarn bool) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.rlStart) >= logRateInterval {
		if l.rlDropped > 0 {
			l.push(logLine{T: now.UnixMilli(), Lvl: "warn", Src: "vair",
				Msg: fmt.Sprintf("… %d log lines suppressed (too fast — lower verbosity to see all)", l.rlDropped)})
		}
		l.rlStart = now
		l.rlCount = 0
		l.rlDropped = 0
	}
	if isErrWarn {
		return true
	}
	if l.rlCount >= logRateLimit {
		l.rlDropped++
		return false
	}
	l.rlCount++
	return true
}

// flush emits the lines accumulated since the last flush as one batched,
// lossy SSE "log" event (payload is an array). Returns quickly when idle.
func (l *logStore) flush() {
	l.mu.Lock()
	if len(l.pending) == 0 {
		l.mu.Unlock()
		return
	}
	batch := make([]logLine, len(l.pending))
	copy(batch, l.pending)
	l.pending = l.pending[:0]
	l.mu.Unlock()
	state.broadcast(SSEEvent{Type: "log", Payload: batch, Lossy: true})
}

// logFlushLoop drives periodic batched delivery of buffered log lines.
func logFlushLoop() {
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		logs.flush()
	}
}

func (l *logStore) snapshot() []logLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.lines)
	if n > l.cap {
		n = l.cap
	}
	out := make([]logLine, n)
	copy(out, l.lines[len(l.lines)-n:])
	return out
}

func (l *logStore) clear() {
	l.mu.Lock()
	// Drop the backing arrays (not just reslice) so a buffer that grew under a
	// flood is actually reclaimed by the GC.
	l.lines = nil
	l.pending = nil
	l.mu.Unlock()
}

// parseLevel classifies a raw stderr line by source so the UI can colour it.
// xray uses bracketed tags ([Warning]/[Info]/[Error]); sing-box prints the
// level word (WARN/INFO/ERROR/FATAL). Unrecognised lines are "raw".
func parseLevel(src, line string) string {
	// Match xray's capitalised tags ([Error]/[Warning]/[Info]) and sing-box's
	// uppercase words (ERROR/WARN/INFO/FATAL) WITHOUT allocating an uppercased
	// copy of every line — this runs per line under a verbose flood, so the
	// old strings.ToUpper was a real GC/CPU cost.
	switch {
	case strings.Contains(line, "[Error]") || strings.Contains(line, "[Fatal]") || strings.Contains(line, "ERROR") || strings.Contains(line, "FATAL") || strings.Contains(line, "PANIC") || strings.Contains(line, "panic"):
		return "error"
	case strings.Contains(line, "[Warning]") || strings.Contains(line, "WARN"):
		return "warn"
	case strings.Contains(line, "[Info]") || strings.Contains(line, "INFO"):
		return "info"
	}
	return "raw"
}

// errWarnTokens mirrors parseLevel's error/warn cases, pre-converted to bytes
// so the rate-limit gate can classify scanner.Bytes() WITHOUT allocating a
// string. Under a verbose flood the gate runs on every line; only the lines we
// actually keep get a string (scanner.Text()) allocated.
var errWarnTokens = [][]byte{
	[]byte("[Error]"), []byte("[Fatal]"), []byte("ERROR"), []byte("FATAL"),
	[]byte("PANIC"), []byte("panic"), []byte("[Warning]"), []byte("WARN"),
}

func levelErrWarnBytes(b []byte) bool {
	for _, t := range errWarnTokens {
		if bytes.Contains(b, t) {
			return true
		}
	}
	return false
}

// vlog records a Vair diagnostic: still printed to stderr (for console /
// LEGACY runs) and also pushed into the log store / SSE stream so it shows
// in the UI Logs panel. level is "error" | "warn" | "info".
func vlog(level, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, msg)
	logs.add("vair", level, msg)
}

// logTestsEnabled reports whether the "Log speed/ping tests" setting is on.
func logTestsEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.LogTests
}

// nodeLogLabel is a short human label for a node used to prefix its test-core
// log lines, so interleaved output from concurrent probes stays attributable.
func nodeLogLabel(n *Node) string {
	if n == nil {
		return "?"
	}
	if s := strings.TrimSpace(n.Name); s != "" {
		return s
	}
	if n.Host != "" {
		return fmt.Sprintf("%s:%d", n.Host, n.Port)
	}
	return string(n.Kind)
}

// tlog records a ping/speed test result into the Logs panel, but only when
// the "Log speed/ping tests" setting is on (off by default — bulk tests can
// produce hundreds of lines). Tagged with the "test" source so it can be
// filtered separately from connection/core output.
func tlog(format string, args ...interface{}) {
	if !logTestsEnabled() {
		return
	}
	logs.add("test", "info", fmt.Sprintf(format, args...))
}

// lineSink is an io.Writer that splits a test core's output stream into lines
// and pushes each into the Logs panel under the "test" source, prefixed with
// the config name (bulk tests run many cores concurrently, so the name keeps
// interleaved lines attributable). engine is "xray"/"singbox" for level
// classification. Used only while the LogTests setting is on.
type lineSink struct {
	engine, name string
	buf          []byte
}

func (w *lineSink) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		raw := w.buf[:i]
		keep := logs.gate(levelErrWarnBytes(raw))
		var line string
		if keep {
			line = strings.TrimRight(string(raw), "\r")
		}
		w.buf = w.buf[i+1:]
		if keep && strings.TrimSpace(line) != "" {
			logs.add("test", parseLevel(w.engine, line), "["+w.name+"] "+line)
		}
	}
	return len(p), nil
}

// pumpProcLog reads a child process's stderr pipe line-by-line into the log
// store, tagged by source ("xray"/"singbox"). It also feeds each line to the
// optional sink (used by the connection paths to keep a rolling tail for the
// crash-error message). Returns when the pipe closes (process exit).
func pumpProcLog(src string, pipe io.Reader, sink func(string)) {
	if pipe == nil {
		return
	}
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		// Drop/keep decision on the raw bytes first — no string allocation for
		// the lines we drop under a verbose flood (the bulk of them).
		if !logs.gate(levelErrWarnBytes(scanner.Bytes())) {
			continue
		}
		line := scanner.Text()
		if sink != nil {
			sink(line)
		}
		logs.add(src, parseLevel(src, line), line)
	}
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

// ─────────────────────────── xray config (test + System Proxy) ───
//
// All xray config assembly lives in protocols.go now:
//   buildXrayConfigForNode(n, httpPort, socksPort) dispatches on n.Kind and
//   stitches together xrayShell / xrayStreamSettings / xrayRoutingForProxy
//   plus the per-protocol xrayOutboundXxx builder.



func buildHybridTUNConfig(ifaceName, serverIP string, xrayHTTPPort, xraySocksPort int, blockQUIC bool) map[string]interface{} {
	// Hybrid TUN: sing-box routes traffic, xray handles VLESS protocol.
	//
	// Two operating modes, gated by AppSettings.DNSLeakProtection:
	//
	// 1. Legacy mode (default, leak-prone): strict_route=false, DNS just
	//    references the OS resolver. System DNS packets can escape
	//    through the physical NIC. Matches 1.4.0 behaviour exactly so
	//    upgrades don't break working setups.
	//
	// 2. Protected mode: strict_route=true (WFP filter on Windows blocks
	//    port 53 outside TUN), proper DNS routing block with three
	//    distinct servers (bootstrap, direct, remote), and either
	//    FakeIP (default) or "real DNS via proxy detour" for the
	//    actual tunnelled-traffic queries.
	//
	// All knobs come from settings — see currentBootstrapDNS / etc.
	leakProtect := dnsLeakProtectionEnabled()
	useFakeIP := fakeIPEnabled()

	dns := buildDNSBlock(leakProtect, useFakeIP)

	tun := map[string]interface{}{
		"type":           "tun",
		"tag":            "tun-in",
		"interface_name": ifaceName,
		"address":        []string{"172.19.0.1/30"},
		"mtu":            currentMTU(),
		"auto_route":     true,
		"strict_route":   leakProtect, // WFP-filter on Windows when true
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

	// Routing rules are shared with the pure-sing-box path; build them via
	// the common helper. true = include the xray-process carve-out (this is
	// the hybrid path, an xray child is dialling the real server).
	rules := singboxRoutingRules(true)

	// Exclude the real VPN server IP from the tunnel. In hybrid mode sing-box
	// can't auto-exclude it (its outbound is a local SOCKS to 127.0.0.1 — the
	// real server is hidden inside the xray child), so without this the xray
	// child's own connection to the server can be re-captured by the TUN and
	// looped back through the proxy. That loop manifests as an endless storm
	// of "creating connection to <server>" lines and 100% CPU even while idle.
	// The process_name carve-out above is meant to prevent it, but Windows
	// find_process is unreliable; pinning the server IP to direct is the
	// bulletproof backstop. Prepended so it wins over `final: proxy`.
	if ip := net.ParseIP(serverIP); ip != nil {
		bits := "/32"
		if ip.To4() == nil {
			bits = "/128"
		}
		rules = append([]interface{}{
			map[string]interface{}{"ip_cidr": []string{serverIP + bits}, "outbound": "direct"},
		}, rules...)
	}

	// Block QUIC (UDP/443) when the node uses an XTLS flow (xtls-rprx-vision).
	// XTLS-Vision rejects UDP/443 ("XTLS rejected UDP/443 traffic"), so in TUN
	// mode a browser's HTTP/3 (QUIC) requests die and the site fails to load —
	// even though it works in proxy mode (where the browser only speaks TCP to
	// the HTTP proxy). Rejecting UDP/443 makes the browser fall back to TCP /
	// HTTP-2, which the tunnel handles fine. This is what v2rayN does by
	// default. Appended after the bypass rules so direct-routed QUIC (e.g. RU
	// sites) is unaffected; only proxy-bound QUIC is rejected.
	if blockQUIC {
		rules = append(rules, map[string]interface{}{
			"network": "udp", "port": 443, "action": "reject",
		})
	}

	// ruSitesDirect is still needed below to decide whether the RU rule_set
	// definitions get attached to the route.
	settingsMu.RLock()
	ruSitesDirect := appSettings.RuSitesDirect
	settingsMu.RUnlock()

	// default_domain_resolver picks the DNS server used when sing-box
	// itself needs to resolve a name in a routing rule (e.g. the IP
	// for a CIDR-matching geosite). In legacy mode → OS resolver. In
	// protected mode → our bootstrap server (plain UDP, no detour),
	// so name resolution for sing-box's own internal needs can never
	// loop through the proxy.
	defaultResolver := "dns-local"
	if leakProtect {
		defaultResolver = "dns-bootstrap"
	}
	route := map[string]interface{}{
		"auto_detect_interface":   true,
		"default_domain_resolver": defaultResolver,
		"find_process":            true,
		"rules":                   rules,
		"final":                   "proxy",
	}

	if ruSitesDirect {
		route["rule_set"] = singboxRuRuleSet()
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

// buildDNSBlock returns the sing-box "dns" configuration block.
//
// legacy mode (leakProtect=false):
//   One server, "dns-local" of type "local". sing-box defers to the
//   OS resolver. DNS leaks are possible because port 53 packets the
//   OS resolver sends out are not forced into TUN (strict_route is
//   false in this mode). This is what 1.4.0 shipped with — kept for
//   compatibility.
//
// protected mode (leakProtect=true): three real DNS servers plus an
// optional FakeIP server used via rule (never as `final`).
//
//   * dns-bootstrap (plain UDP, no detour) — sing-box's internal
//     fallback for routing-time resolutions. No `detour: direct`
//     here: in sing-box 1.13+ that errors out as
//     "detour to an empty direct outbound makes no sense". Without
//     a detour, sing-box dials the IP through OS routing, with
//     its own internal carve-out from the strict_route WFP filter
//     that blocks UDP/53 for other processes.
//   * dns-direct (plain UDP, no detour) — for bypass traffic
//     (geosite-ru, custom direct domains). Same shape as bootstrap.
//   * dns-remote (DoH over proxy) — for tunnelled traffic when
//     FakeIP is disabled, AND always as the `final` server so any
//     non-A/AAAA query (PTR, MX, SRV…) gets a real answer through
//     the tunnel. FakeIP cannot be `final` — sing-box 1.13+ rejects
//     that with "default server cannot be fakeip".
//   * dns-fakeip (FakeIP, optional) — used only via a rule that
//     matches `query_type` A/AAAA. Returns 198.18.0.0/15 pseudo-
//     addresses immediately; real resolution happens once the
//     connection is dialled through the proxy.
//
// Rule ordering (matters):
//   1. Static hosts (predefined map) — highest priority.
//   2. RU bypass + user-direct-domains → dns-direct.
//   3. If FakeIP enabled: A/AAAA → dns-fakeip.
//   4. Everything else falls through to `final = dns-remote`.
func buildDNSBlock(leakProtect, useFakeIP bool) map[string]interface{} {
	if !leakProtect {
		return map[string]interface{}{
			"servers": []interface{}{
				map[string]interface{}{"tag": "dns-local", "type": "local"},
			},
			"final":             "dns-local",
			"independent_cache": true,
		}
	}

	servers := []interface{}{
		// Bootstrap: plain UDP, no detour. sing-box uses its own
		// kernel-level carve-out to escape strict_route's WFP filter
		// for these internal queries.
		map[string]interface{}{
			"tag":    "dns-bootstrap",
			"type":   "udp",
			"server": bootstrapDNSIP(),
		},
		// Direct: same shape, used for bypass traffic queries.
		map[string]interface{}{
			"tag":    "dns-direct",
			"type":   "udp",
			"server": directDNSIP(),
		},
		// Remote: DoH through the proxy outbound. Always present
		// (acts as `final`) regardless of FakeIP setting.
		parseRemoteDNSServer(),
	}

	if useFakeIP {
		servers = append(servers, map[string]interface{}{
			"tag":         "dns-fakeip",
			"type":        "fakeip",
			"inet4_range": "198.18.0.0/15",
			// IPv6 fake range left out for now — adding it requires
			// also routing the range through TUN. v6 isn't always
			// available anyway; v4 is sufficient for the common case.
		})
	}

	rules := []interface{}{}

	// 1. Static hosts: hard-coded answers, checked before everything via
	// sing-box's "hosts" DNS server type. One server with a predefined
	// map is more efficient than one server per host.
	if hosts := staticHostsSnapshot(); len(hosts) > 0 {
		predefined := make(map[string]interface{}, len(hosts))
		var domainList []string
		for domain, ip := range hosts {
			predefined[domain] = []string{ip}
			domainList = append(domainList, domain)
		}
		servers = append(servers, map[string]interface{}{
			"tag":        "dns-hosts",
			"type":       "hosts",
			"predefined": predefined,
		})
		rules = append(rules, map[string]interface{}{
			"domain": domainList,
			"server": "dns-hosts",
		})
	}

	// 2. Direct bypass: RU geosite + user-defined direct domains.
	settingsMu.RLock()
	ruSitesDirect := appSettings.RuSitesDirect
	directDomains := make([]string, len(appSettings.DirectDomains))
	copy(directDomains, appSettings.DirectDomains)
	settingsMu.RUnlock()
	if ruSitesDirect {
		rules = append(rules, map[string]interface{}{
			"rule_set": "geosite-ru",
			"server":   "dns-direct",
		})
	}
	if len(directDomains) > 0 {
		var suffixes []string
		for _, d := range directDomains {
			d = strings.TrimSpace(d)
			if d != "" {
				suffixes = append(suffixes, d)
			}
		}
		if len(suffixes) > 0 {
			rules = append(rules, map[string]interface{}{
				"domain_suffix": suffixes,
				"server":        "dns-direct",
			})
		}
	}

	// 3. FakeIP for the common case: A/AAAA queries for tunnelled
	// hostnames. Must be a rule, not final — sing-box rejects fakeip
	// in the final slot.
	if useFakeIP {
		rules = append(rules, map[string]interface{}{
			"query_type": []string{"A", "AAAA"},
			"server":     "dns-fakeip",
		})
	}

	return map[string]interface{}{
		"servers":           servers,
		"rules":             rules,
		"final":             "dns-remote",
		"independent_cache": true,
		"strategy":          "ipv4_only", // avoid AAAA round-trips
	}
}

// bootstrapDNSIP returns just the IP component of the bootstrap DNS
// setting. If the user wrote a full URL like "https://9.9.9.9/...",
// strip it back to the host so sing-box's "udp" server type can use
// it directly.
func bootstrapDNSIP() string {
	v := currentBootstrapDNS()
	if ip := stripURLToHost(v); ip != "" {
		return ip
	}
	return defaultBootstrapDNS
}
func directDNSIP() string {
	v := currentDirectDNS()
	if ip := stripURLToHost(v); ip != "" {
		return ip
	}
	return defaultDirectDNS
}

// stripURLToHost extracts the host part of a URL or returns the input
// unchanged if it's already a bare host. Used because the bootstrap
// slot is plain UDP — even if the user enters "https://9.9.9.9/dns-
// query" we want just "9.9.9.9".
func stripURLToHost(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.Contains(v, "://") {
		// Already bare. Strip any trailing path/port — we want only host.
		if i := strings.IndexAny(v, ":/"); i >= 0 {
			return v[:i]
		}
		return v
	}
	// Has scheme. Parse and take Host (which excludes port; that's fine
	// for UDP DNS since we only support the standard :53).
	u, err := url.Parse(v)
	if err != nil {
		return ""
	}
	h := u.Hostname()
	return h
}

// parseRemoteDNSServer constructs the sing-box server-config map for
// the remote DNS slot. Accepts plain IP, IP with scheme, or full DoH
// URL — sing-box server types are picked accordingly.
func parseRemoteDNSServer() map[string]interface{} {
	v := strings.TrimSpace(currentRemoteDNS())
	base := map[string]interface{}{
		"tag":    "dns-remote",
		"detour": "proxy",
	}
	if strings.HasPrefix(v, "https://") {
		base["type"] = "https"
		// sing-box's "https" type needs a server hostname/IP, not a
		// full URL. Parse it apart.
		if u, err := url.Parse(v); err == nil {
			base["server"] = u.Hostname()
			if u.Path != "" && u.Path != "/" {
				base["path"] = u.Path
			}
		} else {
			base["server"] = v
		}
		return base
	}
	if strings.HasPrefix(v, "tls://") {
		base["type"] = "tls"
		base["server"] = strings.TrimPrefix(v, "tls://")
		return base
	}
	// Default: plain UDP.
	base["type"] = "udp"
	base["server"] = stripURLToHost(v)
	if base["server"] == "" {
		base["server"] = "1.1.1.1"
	}
	return base
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

func withXray(n *Node, ttl time.Duration, fn func(httpPort int, tr *http.Transport) error) error {
	httpPort, err := findFreePort()
	if err != nil {
		return fmt.Errorf("no free port")
	}
	cfg := buildXrayConfigForNode(n, httpPort, -1)
	if cfg == nil {
		return fmt.Errorf("xray: unsupported protocol %s", n.Kind)
	}
	traceTest := logTestsEnabled()
	if traceTest {
		// Full diagnostics for this probe: force info level and re-enable the
		// access log so failure reasons (dial timeouts, TLS handshake errors,
		// …) show in the panel regardless of the global Verbose setting.
		cfg["log"] = map[string]interface{}{"loglevel": "info"}
	}
	tmpPath, err := writeTempJSON(cfg, "xray-test")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	defer os.Remove(tmpPath)
	ctx, cancel := context.WithTimeout(context.Background(), ttl)
	defer cancel()
	cmd := exec.CommandContext(ctx, state.xrayBin, "run", "-c", tmpPath)
	var stderrBuf strings.Builder
	if traceTest {
		// xray logs to stdout; also tee stderr into the panel while keeping a
		// copy in stderrBuf for the crash-error message below.
		label := nodeLogLabel(n)
		cmd.Stdout = &lineSink{engine: "xray", name: label}
		cmd.Stderr = io.MultiWriter(&stderrBuf, &lineSink{engine: "xray", name: label})
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = &stderrBuf
	}
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

// withSingbox is the sing-box mirror of withXray: it spins up a throwaway
// sing-box process exposing a local HTTP proxy on a free port, waits for the
// port to open, then hands a ready *http.Transport to fn for the duration of
// ttl. Used by the ping/speed runners for UDP-family nodes (Hysteria2/TUIC)
// that xray can't dial. Same lifecycle contract as withXray — process is
// killed and PID untracked on return regardless of outcome.
func withSingbox(n *Node, ttl time.Duration, fn func(httpPort int, tr *http.Transport) error) error {
	if state.singboxBin == "" {
		return fmt.Errorf("sing-box not found (pass its path as 2nd arg)")
	}
	httpPort, err := findFreePort()
	if err != nil {
		return fmt.Errorf("no free port")
	}
	cfg := buildSingboxTestConfig(n, httpPort)
	if cfg == nil {
		return fmt.Errorf("sing-box: unsupported protocol %s", n.Kind)
	}
	traceTest := logTestsEnabled()
	if traceTest {
		// Full diagnostics: force info level so failure reasons appear.
		cfg["log"] = map[string]interface{}{"level": "info", "timestamp": true}
	}
	tmpPath, err := writeTempJSON(cfg, "singbox-test")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	defer os.Remove(tmpPath)
	ctx, cancel := context.WithTimeout(context.Background(), ttl)
	defer cancel()
	cmd := exec.CommandContext(ctx, state.singboxBin, "run", "-c", tmpPath)
	var stderrBuf strings.Builder
	if traceTest {
		// sing-box logs to stderr; tee it into the panel and keep a copy in
		// stderrBuf for the crash-error message below.
		label := nodeLogLabel(n)
		cmd.Stdout = &lineSink{engine: "singbox", name: label}
		cmd.Stderr = io.MultiWriter(&stderrBuf, &lineSink{engine: "singbox", name: label})
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = &stderrBuf
	}
	hideProcess(cmd)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("sing-box start: %w", err)
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
		portResult <- waitForPort(httpPort, time.Now().Add(singboxStartupTimeout))
	}()
	select {
	case exitErr := <-exitCh:
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg == "" {
			if exitErr != nil {
				errMsg = exitErr.Error()
			} else {
				errMsg = "exited unexpectedly"
			}
		}
		if len(errMsg) > 160 {
			errMsg = "..." + errMsg[len(errMsg)-160:]
		}
		return fmt.Errorf("sing-box: %s", errMsg)
	case ready := <-portResult:
		if !ready {
			return fmt.Errorf("sing-box: port not ready after %s", singboxStartupTimeout)
		}
	}
	return fn(httpPort, makeSharedTransport(httpPort))
}

// withEngine dispatches a throwaway-proxy probe to the right backend based on
// the node's protocol: xray for the TCP-family, sing-box for the UDP-family.
// Ping/speed runners call this instead of withXray directly so a mixed list
// (VLESS + Hysteria2 + …) just works without per-call-site branching.
func withEngine(n *Node, ttl time.Duration, fn func(httpPort int, tr *http.Transport) error) error {
	if engineForNode(n) == "singbox" {
		return withSingbox(n, ttl, fn)
	}
	return withXray(n, ttl, fn)
}

// ─────────────────────────── system proxy ────────────────────────

// proxyLockPath marks that WE currently have the Windows system proxy
// enabled. Written when we set it, removed when we unset it. If it survives
// to the next startup, the app was killed / the PC shut down without a
// clean disconnect — see clearStaleProxy.
func proxyLockPath() string { return filepath.Join(tabsDir(), "proxy.active") }

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
	// Record that the proxy is ours and active, so a dirty shutdown can be
	// detected and recovered on next launch.
	os.MkdirAll(tabsDir(), 0755) //nolint:errcheck
	os.WriteFile(proxyLockPath(), []byte(addr), 0644) //nolint:errcheck
	runHidden("rundll32.exe", "inetcpl.cpl,ClearMyTracksByProcess", "8").Run() //nolint:errcheck
	return nil
}

func unsetSystemProxy() {
	rp := `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	runHidden("reg", "add", rp, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "0", "/f").Run() //nolint:errcheck
	os.Remove(proxyLockPath()) //nolint:errcheck
}

// clearStaleProxy runs once at startup. If the proxy-lock file from a prior
// session is still present, the app exited (or the machine powered off)
// while the Windows system proxy was pointed at a localhost port that no
// longer has anything listening — which breaks ALL internet until cleared.
// We restore connectivity by disabling the proxy. A clean disconnect
// removes the lock, so this is a no-op in the normal case.
func clearStaleProxy() {
	if _, err := os.Stat(proxyLockPath()); err == nil {
		unsetSystemProxy()
	}
}

// ─────────────────────────── connection manager ──────────────────

func startProxyConnection(entry *ConfigEntry) {
	cm := state.conn
	stopConnectionLocked(cm)

	state.mu.RLock()
	connTab := state.activeTab
	state.mu.RUnlock()

	n, err := parseNode(entry.Raw)
	if err != nil {
		setConnError(cm, entry, err.Error(), connTab)
		return
	}
	entry.mu.Lock()
	entry.Protocol = string(n.Kind)
	entry.mu.Unlock()

	if reason := nodeUnsupportedReason(n); reason != "" {
		setConnError(cm, entry, reason, connTab)
		return
	}

	// Pre-resolve the VPN server hostname on the Go side so xray gets a
	// numeric IP for its outbound. Cheap, idempotent (no-op if the host
	// is already an IP literal), and lets the kill-switch path actually
	// boot — when strict_route is on, xray can't reach the OS resolver
	// on UDP/53 to do this resolution itself.
	if err := preResolveHost(n); err != nil {
		setConnError(cm, entry, "resolve server: "+err.Error(), connTab)
		return
	}

	cm.mu.Lock()
	cm.state = ConnState{Status: ConnConnecting, Mode: ModeProxy, EntryIndex: entry.Index, EntryName: n.Name, ConnTab: connTab, ConnRaw: entry.Raw}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})

	// Engine branch: TCP-family (VLESS/VMess/Trojan/SS) goes through xray;
	// UDP-family (Hysteria2/TUIC) goes through a pure-sing-box process.
	// Both converge on finalizeProxyConnection for the counter/system-proxy
	// /state wiring.
	if engineForNode(n) == "singbox" {
		startProxyConnectionSingbox(cm, entry, n, connTab)
		return
	}
	startProxyConnectionXray(cm, entry, n, connTab)
}

// startProxyConnectionXray runs the xray-backed proxy path: spawn xray on
// internal HTTP+SOCKS ports, wait for readiness (or an early crash), then
// hand off to finalizeProxyConnection for the shared post-spawn wiring.
func startProxyConnectionXray(cm *connManager, entry *ConfigEntry, n *Node, connTab string) {
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

	xrayCfg := buildXrayConfigForNode(n, intHTTPPort, intSOCKSPort)
	if xrayCfg == nil {
		setConnError(cm, entry, fmt.Sprintf("xray: unsupported protocol %s", n.Kind))
		return
	}
	tmpPath, err := writeTempJSON(xrayCfg, "xray-conn")
	if err != nil {
		setConnError(cm, entry, err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, state.xrayBin, "run", "-c", tmpPath)
	hideProcess(cmd)

	// xray writes its console log to STDOUT (unlike sing-box, which uses
	// stderr), so capture both. Stream them into the Logs panel and keep a
	// short tail so we can still report the real error if xray crashes at
	// startup.
	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()
	var tailMu sync.Mutex
	var tail []string
	sink := func(line string) {
		tailMu.Lock()
		tail = append(tail, line)
		if len(tail) > 6 {
			tail = tail[len(tail)-6:]
		}
		tailMu.Unlock()
	}

	if err = cmd.Start(); err != nil {
		cancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "xray start: "+err.Error())
		return
	}

	go pumpProcLog("xray", stdoutPipe, sink)
	go pumpProcLog("xray", stderrPipe, sink)

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
		// xray exited before the port opened — report the last stderr line.
		cancel()
		os.Remove(tmpPath)
		tailMu.Lock()
		errMsg := ""
		for i := len(tail) - 1; i >= 0; i-- {
			if s := strings.TrimSpace(tail[i]); s != "" {
				errMsg = s
				break
			}
		}
		tailMu.Unlock()
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

	// xray is now listening on the internal ports — hand off to the shared
	// post-spawn block (byte counters, system proxy, ConnState, tickers).
	finalizeProxyConnection(cm, entry, n.Name, connTab, cmd, cancel, tmpPath,
		httpPort, socksPort, intHTTPPort, intSOCKSPort)
}

// finalizeProxyConnection performs the post-spawn steps shared by every
// proxy backend (xray or sing-box): stand up the byte-counting forwarders
// in front of the engine's internal HTTP/SOCKS ports, point the system
// proxy at httpPort, record the connected ConnState, and start the uptime
// /stats tickers. On forwarder failure it tears the just-spawned engine
// down (mainCancel + tmp cleanup) and reports the error. The engine
// process must already be listening on intHTTPPort/intSOCKSPort.
func finalizeProxyConnection(cm *connManager, entry *ConfigEntry, name, connTab string,
	mainCmd *exec.Cmd, mainCancel context.CancelFunc, tmpPath string,
	httpPort, socksPort, intHTTPPort, intSOCKSPort int) {

	counter := &trafficCounter{}
	fwdCtx, fwdCancel := context.WithCancel(context.Background())
	if _, err := startCountingForwarder(fwdCtx, httpPort, intHTTPPort, counter, "proxy-http"); err != nil {
		fwdCancel()
		mainCancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "proxy http counter: "+err.Error())
		return
	}
	if _, err := startCountingForwarder(fwdCtx, socksPort, intSOCKSPort, counter, "proxy-socks"); err != nil {
		fwdCancel()
		mainCancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "proxy socks counter: "+err.Error())
		return
	}

	if err := setSystemProxy(httpPort); err != nil {
		fmt.Fprintf(os.Stderr, "⚠  setSystemProxy: %v\n", err)
	}

	cm.mu.Lock()
	cm.cmd = mainCmd
	cm.cancel = mainCancel
	cm.tmpCfg = tmpPath
	cm.counter = counter
	cm.fwdCancel = fwdCancel
	cm.state = ConnState{
		Status: ConnConnected, Mode: ModeProxy, ConnTab: connTab, ConnRaw: entry.Raw,
		EntryIndex: entry.Index, EntryName: name,
		HTTPPort: httpPort, SOCKSPort: socksPort,
		StartedAt: time.Now(),
	}
	cm.mu.Unlock()
	recordLastConnected(entry.Raw)
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	startUptimeTicker(cm)
	startStatsTicker(cm)
	vlog("info", "connected (proxy): %s — HTTP :%d / SOCKS :%d", name, httpPort, socksPort)
}

// startProxyConnectionSingbox runs the pure-sing-box proxy path for the
// UDP-family protocols (Hysteria2/TUIC) that xray can't dial. Same shape as
// the xray path — spawn sing-box on internal HTTP+SOCKS ports, wait for the
// HTTP port (or an early crash), then hand off to finalizeProxyConnection.
// The byte counters work here exactly as in the xray path: proxy mode has a
// real local HTTP/SOCKS hop to instrument (unlike pure-sing-box TUN).
func startProxyConnectionSingbox(cm *connManager, entry *ConfigEntry, n *Node, connTab string) {
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
	intHTTPPort, e1 := findFreePort()
	if e1 != nil {
		setConnError(cm, entry, "no free port for sing-box http")
		return
	}
	intSOCKSPort, e2 := findFreePort()
	if e2 != nil {
		setConnError(cm, entry, "no free port for sing-box socks")
		return
	}

	cfg := buildSingboxProxyConfig(n, intHTTPPort, intSOCKSPort)
	if cfg == nil {
		setConnError(cm, entry, fmt.Sprintf("sing-box: unsupported protocol %s", n.Kind))
		return
	}
	tmpPath, err := writeTempJSON(cfg, "singbox-conn")
	if err != nil {
		setConnError(cm, entry, err.Error())
		return
	}
	if debugPath := filepath.Join(tabsDir(), "last-singbox-proxy.json"); true {
		os.MkdirAll(tabsDir(), 0755)
		data, _ := json.MarshalIndent(cfg, "", "  ")
		os.WriteFile(debugPath, data, 0644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, state.singboxBin, "run", "-c", tmpPath)
	cmd.Stdout = io.Discard
	hideProcess(cmd)
	stderrPipe, _ := cmd.StderrPipe()
	var tailMu sync.Mutex
	var tail []string

	if err = cmd.Start(); err != nil {
		cancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "sing-box start: "+err.Error())
		return
	}

	go pumpProcLog("singbox", stderrPipe, func(line string) {
		tailMu.Lock()
		tail = append(tail, line)
		if len(tail) > 6 {
			tail = tail[len(tail)-6:]
		}
		tailMu.Unlock()
	})

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	portResult := make(chan bool, 1)
	go func() {
		portResult <- waitForPort(intHTTPPort, time.Now().Add(singboxConnTimeout))
	}()

	select {
	case exitErr := <-exitCh:
		cancel()
		os.Remove(tmpPath)
		tailMu.Lock()
		errMsg := ""
		for i := len(tail) - 1; i >= 0; i-- {
			if s := strings.TrimSpace(tail[i]); s != "" {
				errMsg = s
				break
			}
		}
		tailMu.Unlock()
		if errMsg == "" {
			if exitErr != nil {
				errMsg = exitErr.Error()
			} else {
				errMsg = "sing-box exited unexpectedly"
			}
		}
		if len(errMsg) > 200 {
			errMsg = "..." + errMsg[len(errMsg)-200:]
		}
		setConnError(cm, entry, "sing-box: "+errMsg)
		return
	case ready := <-portResult:
		if !ready {
			cancel()
			os.Remove(tmpPath)
			setConnError(cm, entry, "sing-box: port not ready after timeout")
			return
		}
	}

	finalizeProxyConnection(cm, entry, n.Name, connTab, cmd, cancel, tmpPath,
		httpPort, socksPort, intHTTPPort, intSOCKSPort)
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

	n, err := parseNode(entry.Raw)
	if err != nil {
		setConnError(cm, entry, err.Error())
		return
	}
	entry.mu.Lock()
	entry.Protocol = string(n.Kind)
	entry.mu.Unlock()

	if reason := nodeUnsupportedReason(n); reason != "" {
		setConnError(cm, entry, reason)
		return
	}

	// Pre-resolve the VPN server hostname so xray gets a numeric IP for
	// its outbound. Critical in protected mode: strict_route + WFP
	// filter would block xray's UDP/53 attempt to the OS resolver, and
	// the tunnel would deadlock at startup waiting for DNS. Cheap
	// no-op in all other cases.
	if err := preResolveHost(n); err != nil {
		setConnError(cm, entry, "resolve server: "+err.Error())
		return
	}

	cm.mu.Lock()
	cm.state = ConnState{Status: ConnConnecting, Mode: ModeTUN, EntryIndex: entry.Index, EntryName: n.Name, ConnTab: connTab, ConnRaw: entry.Raw}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})

	// Unique interface name per session avoids "file already exists".
	// Even if Windows hasn't fully cleaned the previous adapter kernel-side,
	// a new name means sing-box never conflicts with the old one.
	tunIfaceName := fmt.Sprintf("xc-tun-%d", time.Now().Unix()%10000)

	// Engine branch. TCP-family protocols use the hybrid path (sing-box TUN
	// front-end → xray outbound). UDP-family (Hysteria2/TUIC) run as a
	// single pure-sing-box process: cm.cmd = sing-box, cm.xrayCmd = nil.
	// stopConnectionLocked already no-ops on a nil xrayCmd, so teardown
	// needs no special-casing.
	if engineForNode(n) == "singbox" {
		startTUNConnectionSingbox(cm, entry, n, connTab, tunIfaceName)
		return
	}
	startTUNConnectionHybrid(cm, entry, n, connTab, tunIfaceName)
}

// startTUNConnectionHybrid runs the hybrid TUN path for the TCP-family
// protocols: sing-box owns the TUN device and routing, an xray child holds
// the actual protocol outbound, and a byte-counter forwarder sits between
// them so per-session traffic stats work. This is the original 1.4.0 TUN
// implementation, unchanged behaviourally.
func startTUNConnectionHybrid(cm *connManager, entry *ConfigEntry, n *Node, connTab, tunIfaceName string) {
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
	xrayCfg := buildXrayConfigForNode(n, xrayHTTPPort, xraySocksPort)
	if xrayCfg == nil {
		setConnError(cm, entry, fmt.Sprintf("xray: unsupported protocol %s", n.Kind))
		return
	}
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
	// xray logs to stdout (sing-box uses stderr) — capture both into the panel.
	xrayStdoutPipe, _ := xrayCmd.StdoutPipe()
	xrayStderrPipe, _ := xrayCmd.StderrPipe()
	hideProcess(xrayCmd)
	if err = xrayCmd.Start(); err != nil {
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "xray hybrid start: "+err.Error())
		return
	}
	go pumpProcLog("xray", xrayStdoutPipe, nil)
	go pumpProcLog("xray", xrayStderrPipe, nil)
	// Wait for xray port
	if !waitForPort(xrayHTTPPort, time.Now().Add(xrayStartupTimeout)) {
		xrayCmd.Process.Kill() //nolint:errcheck
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "xray hybrid: port not ready")
		return
	}
	vlog("info", "hybrid TUN: xray proxy on :%d/%d for %s", xrayHTTPPort, xraySocksPort, n.Network)

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
	// 2. Build sing-box TUN config routing through xray proxy (via counter).
	// VLESS with an XTLS flow (xtls-rprx-vision) can't carry QUIC/UDP-443 —
	// block it so browsers fall back to TCP instead of failing on HTTPS sites.
	blockQUIC := n.Vless != nil && strings.TrimSpace(n.Vless.Flow) != ""
	cfg := buildHybridTUNConfig(tunIfaceName, n.Host, xrayHTTPPort, counterSocksPort, blockQUIC)
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
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			// Gate on raw bytes before allocating — and cap the crash-tail
			// slice. Without the cap, stderrLines grew unbounded under a
			// verbose TUN flood (the source of the ~1 GB RAM spike).
			if !logs.gate(levelErrWarnBytes(scanner.Bytes())) {
				continue
			}
			line := scanner.Text()
			stderrMu.Lock()
			stderrLines = append(stderrLines, line)
			if len(stderrLines) > 60 {
				stderrLines = append(stderrLines[:0], stderrLines[len(stderrLines)-50:]...)
			}
			stderrMu.Unlock()
			if logFile != nil {
				fmt.Fprintln(logFile, line)
			}
			logs.add("singbox", parseLevel("singbox", line), line)
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
		EntryIndex: entry.Index, EntryName: n.Name,
		TUNIface: tunIfaceName, StartedAt: time.Now(),
	}
	cm.mu.Unlock()
	recordLastConnected(entry.Raw)
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	startUptimeTicker(cm)
	startStatsTicker(cm)
}

// startTUNConnectionSingbox runs the pure-sing-box TUN path for the
// UDP-family protocols (Hysteria2/TUIC). A single sing-box process owns the
// TUN device AND holds the protocol outbound — there is no xray child and
// no local SOCKS hop, so the byte counter cannot be inserted and traffic
// stats are unavailable for the session (the UI surfaces this). cm.xrayCmd
// stays nil; stopConnectionLocked already no-ops on a nil xrayCmd.
func startTUNConnectionSingbox(cm *connManager, entry *ConfigEntry, n *Node, connTab, tunIfaceName string) {
	cfg := buildSingboxTUNConfig(n, tunIfaceName)
	if cfg == nil {
		setConnError(cm, entry, fmt.Sprintf("sing-box: unsupported protocol %s", n.Kind))
		return
	}
	tmpPath, err := writeTempJSON(cfg, "singbox-tun")
	if err != nil {
		setConnError(cm, entry, "singbox config write: "+err.Error())
		return
	}
	fmt.Printf("ℹ  pure sing-box TUN config: %s\n", tmpPath)
	if debugPath := filepath.Join(tabsDir(), "last-singbox-tun.json"); true {
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
		setConnError(cm, entry, "sing-box start: "+err.Error())
		return
	}

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	var stderrLines []string
	var stderrMu sync.Mutex
	logPath := filepath.Join(tabsDir(), "last-singbox.log")
	os.MkdirAll(tabsDir(), 0755)
	logFile, _ := os.Create(logPath)
	go func() {
		if stderrPipe == nil {
			return
		}
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			// Gate on raw bytes before allocating — and cap the crash-tail
			// slice. Without the cap, stderrLines grew unbounded under a
			// verbose TUN flood (the source of the ~1 GB RAM spike).
			if !logs.gate(levelErrWarnBytes(scanner.Bytes())) {
				continue
			}
			line := scanner.Text()
			stderrMu.Lock()
			stderrLines = append(stderrLines, line)
			if len(stderrLines) > 60 {
				stderrLines = append(stderrLines[:0], stderrLines[len(stderrLines)-50:]...)
			}
			stderrMu.Unlock()
			if logFile != nil {
				fmt.Fprintln(logFile, line)
			}
			logs.add("singbox", parseLevel("singbox", line), line)
		}
		if logFile != nil {
			logFile.Close()
		}
	}()

	select {
	case <-exitCh:
		cancel()
		os.Remove(tmpPath)
		stderrMu.Lock()
		lines := stderrLines
		stderrMu.Unlock()
		errMsg := "sing-box crashed at startup"
		for i := len(lines) - 1; i >= 0 && i >= len(lines)-3; i-- {
			if lines[i] != "" {
				errMsg = lines[i]
				break
			}
		}
		if len(errMsg) > 180 {
			errMsg = "..." + errMsg[len(errMsg)-180:]
		}
		setConnError(cm, entry, errMsg)
		return
	case <-time.After(tunStartupTimeout):
	}

	cm.mu.Lock()
	cm.cmd = cmd
	cm.cancel = cancel
	cm.tmpCfg = tmpPath
	// No xray child, no byte counter in the pure-sing-box TUN path.
	cm.xrayCmd = nil
	cm.xrayCancel = nil
	cm.xrayTmpCfg = ""
	cm.counter = nil
	cm.fwdCancel = nil
	cm.state = ConnState{
		Status: ConnConnected, Mode: ModeTUN, ConnTab: connTab, ConnRaw: entry.Raw,
		EntryIndex: entry.Index, EntryName: n.Name,
		TUNIface: tunIfaceName, StartedAt: time.Now(),
		StatsUnavailable: true,
	}
	cm.mu.Unlock()
	recordLastConnected(entry.Raw)
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	startUptimeTicker(cm)
	// startStatsTicker self-terminates immediately on a nil counter — no
	// traffic stats for this path, by design (see buildSingboxTUNConfig).
}

func stopConnection() {
	stopConnectionLocked(state.conn)
	state.broadcast(SSEEvent{Type: "conn_update", Payload: state.conn.snap()})
	// A verbose connection can churn a lot of transient heap (log lines, JSON
	// batches). Go won't hand that back to the OS for ~5 min on its own, which
	// is why RSS stayed elevated after disconnect. Force it back now.
	debug.FreeOSMemory()
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
	vlog("error", "connect %s: %s", name, msg)
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
			// Lossy — stats_update fires every second (or faster) and the
			// next pulse fully supersedes this one's byte tallies. Worst
			// case under buffer pressure the on-screen counter pauses for
			// one tick; never matters because the next tick replaces it.
			state.broadcast(SSEEvent{Type: "stats_update", Payload: statsSnapshot(counter), Lossy: true})
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
	pt := currentPingTimeout()
	wc := &http.Client{Transport: tr, Timeout: warmupTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	w, err := wc.Get(url)
	if err != nil {
		return -1, fmt.Errorf("warmup: %w", err)
	}
	io.Copy(io.Discard, w.Body)
	w.Body.Close()
	mc := &http.Client{Transport: tr, Timeout: pt,
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

// minSpeedBytes is the floor for a "real" measurement. SS configs (and
// occasionally others) can finish the request with a 200 OK that carries
// only a few KB before the upstream closes — typically when the SS server
// rejects the relay after the cipher handshake. Dividing those few KB by a
// near-zero elapsed-time produces fantasy mbps numbers (200+ MB/s on a
// 10 Mbps line). We require at least this much body data to call it valid.
const minSpeedBytes int64 = 256 * 1024

// withCacheBuster appends a unique query param so a CDN / proxy / ISP cache
// along the path can't answer with a cached or short-circuited body — the
// speed test must measure a real transfer. Unknown query params are ignored
// by the bundled presets (Cloudflare __down, cachefly, ovh), and this is the
// fix for the "response too fast — upstream cache or proxy short-circuit"
// rejections that used to drop otherwise-fine proxies.
func withCacheBuster(u string) string {
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "vcb=" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// err429 marks a measurement that ended because the upstream answered with
// HTTP 429. The outer measureSpeed checks for it via errors.Is to decide
// whether to fall back to a secondary URL.
var err429 = errors.New("HTTP 429")

// measureSpeed runs one speed test, with an optional fallback to a second
// URL ONLY when the primary returns HTTP 429. Each attempt is its own
// bounded request — same http.Client.Timeout and same defer-close — so a
// fallback can never extend the test indefinitely or leak the connection
// (the failure mode that caused tests to hang on "connecting…" in a
// previous attempt at this feature).
func measureSpeed(tr *http.Transport, onProgress func(float64)) (float64, error) {
	primary := currentSpeedURL()
	mbps, err := measureSpeedOne(tr, primary, onProgress)
	if err == nil {
		return mbps, nil
	}
	// Only retry on a clean 429 from the primary. Connect errors, slow
	// responses, etc. stay as the user-visible error.
	if !errors.Is(err, err429) {
		return mbps, err
	}
	fb := currentSpeedFallbackURL()
	if fb == "" || fb == primary {
		return mbps, err
	}
	return measureSpeedOne(tr, fb, onProgress)
}

// measureSpeedOne is a single attempt: one HTTP request, bounded by
// Client.Timeout, body always closed via defer. Returned errors are
// terminal — there's no inner retry that could spin forever.
func measureSpeedOne(tr *http.Transport, urlStr string, onProgress func(float64)) (float64, error) {
	sd := currentSpeedDuration()
	// Hard wall-clock cap on the whole download. Client.Timeout covers the
	// full request including body read, so a hung upstream can't pin the
	// goroutine past sd+5s — even on SS configs where xray's CONNECT
	// succeeds but the relayed stream stalls indefinitely.
	sc := &http.Client{Transport: tr, Timeout: sd + 5*time.Second}
	req, err := http.NewRequest("GET", withCacheBuster(urlStr), nil)
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
	if resp.StatusCode == 429 {
		// Wrap the sentinel so measureSpeed can detect 429 unambiguously
		// without string-matching on the error message.
		return 0, fmt.Errorf("%w", err429)
	}
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 64*1024)
	var total int64
	start := time.Now()
	deadline := start.Add(sd)
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
	elapsed := time.Since(start)
	if total == 0 {
		return 0, fmt.Errorf("no data received")
	}
	// Only one floor now: too few bytes to measure. A proxy that relays
	// just a few KB before EOF is genuinely broken (the classic SS
	// "accept CONNECT, reset the stream" failure). The old "response too
	// fast" floor was dropped — with the cache-buster above defeating
	// cached/short-circuit replies and a large default file filling the
	// window, a short-but->=256KB sample now means a real (fast) proxy,
	// not a fake one, so we report it instead of rejecting it.
	if total < minSpeedBytes {
		return 0, fmt.Errorf("tiny response (%d B) — proxy relay closed early", total)
	}
	// Guard the divisor: a >=256KB burst can still arrive in well under a
	// millisecond over a fast local hop. Clamp so the ratio stays finite.
	secs := elapsed.Seconds()
	if secs < 0.001 {
		secs = 0.001
	}
	return float64(total) / secs / 1024 / 1024, nil
}

// ─────────────────────────── entry runners ───────────────────────

func runPingForEntry(entry *ConfigEntry) {
	n, err := parseNode(entry.Raw)
	if err != nil {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = err.Error()
		entry.mu.Unlock()
		return
	}
	entry.mu.Lock()
	entry.Protocol = string(n.Kind)
	entry.mu.Unlock()
	if reason := nodeUnsupportedReason(n); reason != "" {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = reason
		entry.mu.Unlock()
		return
	}
	ttl := startupTimeout + warmupTimeout + currentPingTimeout()*time.Duration(pingRounds) + 3*time.Second
	if err = withEngine(n, ttl, func(_ int, tr *http.Transport) error {
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
	entry.mu.Lock()
	ps, delay, perr, nm := entry.PingStatus, entry.Delay, entry.PingErr, entry.Name
	entry.mu.Unlock()
	if ps == StatusOK {
		tlog("ping ok: %s — %dms", nm, delay)
	} else {
		tlog("ping failed: %s — %s", nm, perr)
	}
}

// runSpeedForEntry always does ping→speed in a single xray session.
// Per-row ⬇ speed button should re-measure ping even if already tested,
// because conditions may have changed and speed result is meaningless without fresh ping.
func runSpeedForEntry(entry *ConfigEntry, tabID string) {
	n, err := parseNode(entry.Raw)
	if err != nil {
		entry.mu.Lock()
		entry.SpeedStatus = StatusFailed
		entry.SpeedErr = err.Error()
		entry.mu.Unlock()
		return
	}
	entry.mu.Lock()
	entry.Protocol = string(n.Kind)
	entry.mu.Unlock()
	if reason := nodeUnsupportedReason(n); reason != "" {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = reason
		entry.SpeedStatus = StatusFailed
		entry.SpeedErr = reason
		entry.SpeedLive = 0
		entry.mu.Unlock()
		return
	}
	ttl := startupTimeout + warmupTimeout + currentPingTimeout()*time.Duration(pingRounds) + currentSpeedDuration() + 10*time.Second
	if err = withEngine(n, ttl, func(_ int, tr *http.Transport) error {
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
			// Lossy: a later live callback (every ~250ms) supersedes this
			// one. Dropping under buffer pressure is harmless; the FINAL
			// terminal update below is reliable.
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID, Lossy: true})
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
		// withXray failed (e.g. xray exited with "exit status N" before
		// measurePing could even run). The bulk caller sets PingStatus=
		// TestingPing right before invoking us — so without this branch
		// the row sits on the blinking "ping" pill forever even though
		// the test is finished. Mirror the same reset for the speed side.
		if entry.PingStatus == StatusTestingPing {
			entry.PingStatus = StatusFailed
			entry.Delay = -1
			entry.PingErr = shortErr(err.Error())
		}
		entry.SpeedStatus = StatusFailed
		entry.SpeedMBps = 0
		entry.SpeedLive = 0
		entry.SpeedErr = shortErr(err.Error())
		entry.mu.Unlock()
	}
	logTestResult(entry)
}

// logTestResult emits one [test] line summarising the entry's current ping +
// speed outcome (gated by the LogTests setting, via tlog).
func logTestResult(entry *ConfigEntry) {
	entry.mu.Lock()
	ps, delay, perr := entry.PingStatus, entry.Delay, entry.PingErr
	ss, mbps, serr, nm := entry.SpeedStatus, entry.SpeedMBps, entry.SpeedErr, entry.Name
	entry.mu.Unlock()
	var b strings.Builder
	if ps == StatusOK {
		fmt.Fprintf(&b, "ping %dms", delay)
	} else {
		fmt.Fprintf(&b, "ping failed (%s)", perr)
	}
	switch ss {
	case StatusOK:
		fmt.Fprintf(&b, ", speed %.2f MB/s", mbps)
	case StatusSkipped:
		fmt.Fprintf(&b, ", speed skipped")
	case StatusFailed:
		fmt.Fprintf(&b, ", speed failed (%s)", serr)
	}
	tlog("%s — %s", nm, b.String())
}

func runPingAndSpeedForEntry(entry *ConfigEntry, tabID string) {
	n, err := parseNode(entry.Raw)
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
	entry.mu.Lock()
	entry.Protocol = string(n.Kind)
	entry.mu.Unlock()
	if reason := nodeUnsupportedReason(n); reason != "" {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = reason
		entry.SpeedStatus = StatusSkipped
		entry.SpeedErr = reason
		entry.mu.Unlock()
		return
	}
	ttl := startupTimeout + warmupTimeout + currentPingTimeout()*time.Duration(pingRounds) + currentSpeedDuration() + 10*time.Second
	if err = withEngine(n, ttl, func(_ int, tr *http.Transport) error {
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
			// Lossy — see runSpeedForEntry's identical callback for why.
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID, Lossy: true})
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
	logTestResult(entry)
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
// onlyIndices is an ordered list of entry indices to test, in the exact
// order they should be processed (typically the on-screen sortedList).
// Pass nil to test every entry in state.entries order.
func runPingAll(onlyIndices []int) {
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

	// Restrict to onlyIndices, preserving the client-supplied order so that
	// tests fire in the exact order the rows appear on screen.
	var entries []*ConfigEntry
	if onlyIndices != nil {
		byIdx := make(map[int]*ConfigEntry, len(allEntries))
		for _, e := range allEntries {
			byIdx[e.Index] = e
		}
		for _, idx := range onlyIndices {
			if e, ok := byIdx[idx]; ok {
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
	// Acquire the semaphore in the main loop BEFORE spawning the goroutine.
	// This makes the loop block in input order, so the next entry that runs
	// is always the next one in the sortedList we received — the on-screen
	// order. (The previous design spawned every goroutine immediately and
	// let them race for sem slots, which made the visible test order look
	// random for any concurrency > 1.)
	for _, e := range entries {
		if isTestCancelled(cancelCh) { break }
		state.mu.RLock()
		cancelled := state.cancelledTabs[tabID]
		state.mu.RUnlock()
		if cancelled { break }
		sem <- struct{}{}
		if isTestCancelled(cancelCh) { <-sem; break }
		wg.Add(1)
		go func(ent *ConfigEntry) {
			defer wg.Done()
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
			// Terminal entry update — reliable (this is the row's final
			// status; missing it leaves the UI on a stale "testing" pill).
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			// Bulk-progress tick is lossy: only the latest done/total matters
			// for the progress bar, and bulk_ping_done at the end is reliable.
			state.broadcast(SSEEvent{Type: "bulk_ping_progress", Payload: map[string]interface{}{"done": n, "total": int64(len(entries))}, Tab: tabID, Lossy: true})
		}(e)
	}
	wg.Wait()
	// Reconciliation pass: re-broadcast every tested entry's snapshot in
	// case any mid-flight reliable entry_update was dropped because a
	// slow SSE consumer hit the 2-second cap. Repeated updates are
	// idempotent on the client (onUpdate does an upsert by index), and
	// this guarantees the UI converges on the server's truth without
	// the user having to hit RELOAD. Sweep also sanity-checks for any
	// entry still in TestingPing — force-fails if so (defence in depth;
	// the per-entry watchdog should already have done this).
	// Skip the reconcile re-broadcast if the run was cancelled. On a cancel
	// triggered by RELOAD, the reload re-broadcasts a fresh "loaded" set;
	// a reconcile firing afterwards would re-assert every old result on top
	// of the reset table (the "results reappear a second later" bug). The
	// in-flight workers above already broadcast their own terminal status,
	// so a normal (uncancelled) finish still converges without this sweep.
	if !isTestCancelled(cancelCh) {
		reconcileBulkResults(entries, tabID, false)
	}
	state.broadcast(SSEEvent{Type: "bulk_ping_done", Tab: tabID})
}

// runSpeedAll mirrors runPingAll: when onlyIndices is non-nil, only those
// entries are tested (for FILTER-aware testing).
func runSpeedAll(onlyIndices []int) {
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
		byIdx := make(map[int]*ConfigEntry, len(allEntries))
		for _, e := range allEntries {
			byIdx[e.Index] = e
		}
		for _, idx := range onlyIndices {
			if e, ok := byIdx[idx]; ok {
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
	// See runPingAll for the rationale on sem-before-spawn: tests fire in
	// the order the client sent (on-screen order) instead of randomly.
	for _, e := range entries {
		if isTestCancelled(cancelCh) { break }
		state.mu.RLock()
		cancelled := state.cancelledTabs[tabID]
		state.mu.RUnlock()
		if cancelled { break }
		sem <- struct{}{}
		if isTestCancelled(cancelCh) { <-sem; break }
		wg.Add(1)
		go func(ent *ConfigEntry) {
			defer wg.Done()
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
			// Watchdog: runSpeedForEntry should have set a terminal status
			// (ok/failed/skipped). If for any reason it didn't, force-fail
			// the row so the UI doesn't sit on "connecting…" forever.
			// Mirrors the same guard at the end of /api/speed-one.
			ent.mu.Lock()
			if ent.SpeedStatus == StatusTestingSpeed {
				ent.SpeedStatus = StatusFailed
				if ent.SpeedErr == "" {
					ent.SpeedErr = "no result"
				}
				ent.SpeedLive = 0
			}
			// Sibling watchdog: PingStatus is set to TestingPing right before
			// the call. If xray exited early ("exit status N") and the inner
			// measurePing branch never ran, PingStatus stays stuck. Force-fail
			// so the row doesn't blink "ping" until the whole bulk completes.
			if ent.PingStatus == StatusTestingPing {
				ent.PingStatus = StatusFailed
				ent.Delay = -1
				if ent.PingErr == "" {
					ent.PingErr = "no result"
				}
			}
			ent.mu.Unlock()
			n := atomic.AddInt64(&done, 1)
			// Terminal entry update — reliable. This is the very fix point
			// for the "connecting…" / "ping" stuck-pill class of bugs.
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			// Progress tick is lossy — only the latest done/total matters,
			// and bulk_speed_done at the end is the reliable terminal.
			state.broadcast(SSEEvent{Type: "bulk_speed_progress", Payload: map[string]interface{}{"done": n, "total": int64(len(entries))}, Tab: tabID, Lossy: true})
		}(e)
	}
	wg.Wait()
	// See runPingAll for the rationale — covers the "stuck connecting…"
	// class of bugs where a slow SSE consumer drops a terminal event.
	// Skipped on cancel so a RELOAD-triggered stop doesn't re-assert old
	// speed results on top of the freshly-reset table.
	if !isTestCancelled(cancelCh) {
		reconcileBulkResults(entries, tabID, true)
	}
	state.broadcast(SSEEvent{Type: "bulk_speed_done", Tab: tabID})
}

// reconcileBulkResults walks every entry tested in a bulk run and
// re-broadcasts its final snapshot (reliably). If `includeSpeed` is true
// the sweep also force-fails any entry stuck on SpeedStatus=TestingSpeed;
// without it only PingStatus stuck states are corrected (suits bulk ping
// which doesn't touch SpeedStatus). The function is cheap — one mu lock
// per entry plus a broadcast — and it's the safety net that lets us keep
// the high-frequency mid-flight progress events lossy without ever
// leaving a row stranded on a stale pill.
func reconcileBulkResults(entries []*ConfigEntry, tabID string, includeSpeed bool) {
	for _, e := range entries {
		e.mu.Lock()
		if e.PingStatus == StatusTestingPing {
			e.PingStatus = StatusFailed
			e.Delay = -1
			if e.PingErr == "" {
				e.PingErr = "no result"
			}
		}
		if includeSpeed && e.SpeedStatus == StatusTestingSpeed {
			e.SpeedStatus = StatusFailed
			e.SpeedLive = 0
			if e.SpeedErr == "" {
				e.SpeedErr = "no result"
			}
		}
		e.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: e.snap(), Tab: tabID})
	}
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
		// Extract a proxy URL from anywhere in the line: scan for the
		// earliest occurrence of any recognised scheme so lines like
		// "  <some prefix> trojan://... <suffix>" still parse correctly.
		bestIdx := -1
		for _, s := range nodeSchemes {
			if i := strings.Index(line, s); i >= 0 && (bestIdx < 0 || i < bestIdx) {
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			continue
		}
		line = line[bestIdx:]
		// Trim trailing garbage (spaces, markdown links etc)
		if sp := strings.IndexAny(line, " \t\r"); sp > 0 {
			line = line[:sp]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		e := &ConfigEntry{Raw: line, PingStatus: StatusPending, Delay: -1, SpeedStatus: StatusPending}
		n, parseErr := parseNode(line)
		if parseErr != nil {
			e.Name = line[:minInt(40, len(line))]
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
	// 1024-slot buffer absorbs bursts from bulk ping/speed runs (5 concurrent
	// runners × 4 live-callbacks/sec × few seconds of slack) without forcing
	// the broadcast path into its 50ms soft-block fallback.
	ch := make(chan SSEEvent, 1024)
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

// handlePingConnected re-pings the currently connected config regardless of
// which tab the UI is showing. The browser path (pingOne) needs an index in
// the active tab; when the connected config lives on another tab the chip
// calls this instead. We locate the entry by its raw URL (its connection
// tab first, then any tab) and ping it, broadcasting under that entry's tab.
func handlePingConnected(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	cs := state.conn.snap()
	if cs.Status != ConnConnected || cs.ConnRaw == "" {
		w.WriteHeader(200)
		w.Write([]byte("not connected"))
		return
	}
	state.mu.RLock()
	var entry *ConfigEntry
	var tabID string
	if ents, ok := state.tabEntries[cs.ConnTab]; ok {
		for _, e := range ents {
			if e.Raw == cs.ConnRaw {
				entry, tabID = e, cs.ConnTab
				break
			}
		}
	}
	if entry == nil {
		for tid, ents := range state.tabEntries {
			for _, e := range ents {
				if e.Raw == cs.ConnRaw {
					entry, tabID = e, tid
					break
				}
			}
			if entry != nil {
				break
			}
		}
	}
	state.mu.RUnlock()
	if entry == nil {
		w.WriteHeader(200)
		w.Write([]byte("entry not found"))
		return
	}
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
					fmt.Fprintf(os.Stderr, "⚠ ping connected panic: %v\n", r)
				}
			}()
			runPingForEntry(entry)
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
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
		// Same protection for PingStatus: runSpeedForEntry sets PingStatus
		// inside the withXray callback. If xray crashes before that branch
		// runs and PingStatus was already TestingPing (e.g. a prior ping was
		// in flight when speed was clicked), force-fail it too.
		if entry.PingStatus == StatusTestingPing {
			entry.PingStatus = StatusFailed
			entry.Delay = -1
			if entry.PingErr == "" {
				entry.PingErr = "timeout"
			}
		}
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	}()
}

// parseFilterIndices reads an optional JSON body of the form
// {"indices":[0,3,5,...]} and returns it as an ordered slice. Order matters:
// the bulk test runners iterate in this order and acquire the concurrency
// semaphore in sequence, so the user-visible test order matches whatever the
// client sent (which is the on-screen sortedList order).
// Returns nil if no body or the body is empty/invalid — caller treats nil as
// "test everything" for backwards compatibility.
func parseFilterIndices(r *http.Request) []int {
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
	return req.Indices
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

	// Apply server-side dedup in-place when the tab is in "delete" mode.
	// Without this, dupes pasted into a delete-mode tab silently accumulated
	// because "delete" was only ever applied during fetchTabURLs / explicit
	// mode transition — paste went straight through. "hide" mode kept working
	// because it's a JS view filter, evaluated on every render.
	// Re-index so positions are contiguous after dedup.
	var dedupMode string
	for _, t := range state.tabs {
		if t.ID == id {
			dedupMode = t.DedupMode
			break
		}
	}
	if dedupMode == "delete" {
		existing = dedupByBody(existing)
		for i, e := range existing {
			e.Index = i
		}
	}

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
			if !shouldSkip(e.Name, e.Protocol, e.Host, e.Network, e.Security, excludeFilter) {
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
/* Ping chip in the connection bar — shows the connected config's latest
   delay; click to re-ping it. Colour mirrors the table ping pills. */
.cping{flex-shrink:0;cursor:pointer;font-size:11px;font-weight:700;padding:2px 9px;border-radius:99px;border:1px solid var(--border2);color:var(--dim);user-select:none;white-space:nowrap;transition:color .15s,border-color .15s}
.cping:hover{border-color:var(--accent);color:var(--accent)}
.cping.ok{color:var(--green);border-color:rgba(80,200,120,.4)}
.cping.failed{color:var(--red);border-color:rgba(220,80,80,.4)}
.cping.testing{color:var(--accent);border-color:rgba(232,197,71,.4)}

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

/* tabs — live in the toolbar next to filter/type/sort. The toolbar uses
   align-items:flex-start (see .toolbar below) so when the tab-bar grows
   taller (extra rows of tabs wrap downward), the filter/type/sort
   controls stay anchored at the top-right of the toolbar — only the
   tab-bar's slot grows; the other controls don't shift down with it.
   flex:0 1 auto = take exactly the natural max-content width when tabs
   fit (so the last tab sits flush against the 10px gap before the
   filter label — no empty trailing space inside the tab-bar's slot),
   but allow shrinking when over-full so the INTERNAL flex-wrap kicks
   in and tabs flow onto new rows downward instead of pushing the
   right-side controls off the row. */
.tab-bar{display:flex;gap:2px;align-items:center;flex-wrap:wrap;row-gap:3px;flex:0 1 auto;min-width:0}
.tab-btn{
  all:unset;cursor:pointer;font-family:var(--font);font-size:10px;font-weight:700;
  box-sizing:border-box;height:22px;padding:0 7px;border-radius:3px;color:var(--dim);border:1px solid var(--border2);
  transition:all .15s;white-space:nowrap;text-transform:uppercase;letter-spacing:.05em;
  display:inline-flex;align-items:center;gap:3px;
}
.tab-btn:hover{color:var(--text);border-color:var(--dim)}
.tab-btn.active{color:var(--accent);border-color:var(--accent)}
.tab-add{
  all:unset;cursor:pointer;font-family:var(--font);font-size:10px;font-weight:700;
  box-sizing:border-box;height:22px;padding:0;min-width:20px;display:flex;align-items:center;justify-content:center;
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
/* Modal text scale.
 * A single CSS variable on :root drives every font-size in the Settings
 * and Tab settings modals. The user can grow or shrink modal typography
 * from one input; the main window (table, header, conn bar, title bar)
 * does NOT use these classes, so it stays at its original sizes
 * regardless. Default 11px matches what the program shipped with. */
:root{ --modal-fs-base: 11px; }
.modal-box{
  background:var(--bg2);border:1px solid var(--border2);border-radius:6px;
  padding:18px 22px;
  /* One consistent width for every modal in the app (Settings, Tab
     Settings, Sources, Running Processes). 560px is comfortable for
     long DNS URLs and the static hosts textarea without being absurd.
     min(...) keeps the modal inside the program window when it's
     dragged narrow — 92vw means ≤92% of the webview width, so a
     500px-wide window still gets a 460px-wide modal. */
  width:min(560px, 92vw);
  /* Cap to viewport so long settings don't push the close button off-screen.
     Falling back to scroll keeps the close button reachable on any window
     size — including small mobile-style aspect ratios. */
  max-height:85vh;overflow-y:auto;
}
.modal-title{font-size:calc(var(--modal-fs-base) + 2px);font-weight:700;color:var(--accent);margin-bottom:14px;text-transform:uppercase;letter-spacing:.06em}
.modal-label{font-size:calc(var(--modal-fs-base) - 1px);color:var(--dim);text-transform:uppercase;letter-spacing:.08em;margin-bottom:4px}
.modal-input{
  width:100%;box-sizing:border-box;background:var(--bg3);border:1px solid var(--border2);border-radius:3px;
  color:var(--text);font-family:var(--font);font-size:calc(var(--modal-fs-base) + 1px);padding:6px 10px;outline:none;
  margin-bottom:12px;
}
/* Textareas inherit modal-input width but the browser default min-width
   would otherwise track the textarea's intrinsic content size. Force
   100% with box-sizing, and only allow vertical resize so the user
   can't drag the modal wider by accident. */
textarea.modal-input{resize:vertical;min-width:0}
.modal-input:focus{border-color:var(--accent)}
.modal-btns{display:flex;gap:8px;justify-content:flex-end;margin-top:6px}
.modal-row{display:flex;align-items:center;justify-content:space-between;margin-bottom:12px}
.modal-row-label{font-size:var(--modal-fs-base);color:var(--text)}
.toggle{position:relative;width:36px;height:20px;cursor:pointer;flex-shrink:0}
.toggle input{display:none}
.toggle-track{position:absolute;inset:0;background:var(--dim2);border-radius:10px;transition:.2s}
.toggle input:checked+.toggle-track{background:var(--accent)}
.toggle-thumb{position:absolute;top:2px;left:2px;width:16px;height:16px;background:#fff;border-radius:50%;transition:.2s}
.toggle input:checked~.toggle-thumb{left:18px}
.chips-wrap{display:flex;flex-wrap:wrap;gap:4px;margin-bottom:10px;min-height:26px;padding:4px 6px;background:var(--bg3);border:1px solid var(--border2);border-radius:3px}
.chip{display:inline-flex;align-items:center;gap:3px;font-size:calc(var(--modal-fs-base) - 1px);font-weight:700;
  padding:2px 6px 2px 8px;border-radius:99px;background:rgba(232,197,71,.12);color:var(--accent);border:1px solid rgba(232,197,71,.3)}
.chip-tag{font-size:calc(var(--modal-fs-base) - 3px);text-transform:uppercase;letter-spacing:.04em;opacity:.85;font-weight:800;margin-right:2px}
.chip.col-name{background:rgba(232,197,71,.12);color:var(--accent);border-color:rgba(232,197,71,.3)}
.chip.col-type{background:rgba(96,165,250,.14);color:#7bb6ff;border-color:rgba(96,165,250,.35)}
.chip.col-host{background:rgba(132,204,22,.14);color:#9fd84a;border-color:rgba(132,204,22,.35)}
.chip.col-transport{background:rgba(168,85,247,.14);color:#b387f0;border-color:rgba(168,85,247,.35)}
.chip.col-security{background:rgba(244,114,114,.14);color:#f08a8a;border-color:rgba(244,114,114,.35)}
.chip-x{cursor:pointer;opacity:.5;font-size:calc(var(--modal-fs-base) - 2px)}.chip-x:hover{opacity:1;color:var(--red)}
.chip-input{border:0;background:transparent;color:var(--text);font-family:var(--font);font-size:var(--modal-fs-base);outline:none;flex:1;min-width:80px}
/* Exclude filter — one labelled chip box per column. Values are added with
   Enter (same UX as "Custom domains without VPN"). Labels match the plain
   "Deduplicate duplicate configs" style (no caps, no per-column palette). */
.ef-fields{display:flex;flex-direction:column;gap:12px}
.ef-field{display:flex;flex-direction:column;gap:4px}
.ef-field-tag{font-size:var(--modal-fs-base);color:var(--text)}
.ef-field .chips-wrap{margin-bottom:0}
.modal-hint{font-size:calc(var(--modal-fs-base) - 2px);color:var(--dim);margin-top:-6px;margin-bottom:10px}
/* The exclude-filter intro hint sits below a label, so the global -6px pull
   would clip it onto the label above. Reset it to a small positive gap. */
.modal-hint.ef-hint{margin-top:2px;margin-bottom:12px}
/* Tab-settings modal: render the all-caps section labels in the same plain
   style as the "Deduplicate duplicate configs" row label. */
#tab-modal-box .modal-label{text-transform:none;letter-spacing:0;color:var(--text);font-size:var(--modal-fs-base)}
.settings-section{margin-bottom:16px;padding-bottom:12px;border-bottom:1px solid var(--border2)}
.settings-section:last-child{border-bottom:0;margin-bottom:0;padding-bottom:0}
.section-header{font-size:var(--modal-fs-base);font-weight:700;color:var(--accent);text-transform:uppercase;letter-spacing:.08em;margin-bottom:10px}

/* Compact numeric input used in Settings (concurrency, refresh interval) */
.num-input{
  width:70px;margin-bottom:0;padding:4px 8px;font-size:var(--modal-fs-base);text-align:right;
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
  all:unset;cursor:pointer;font-family:var(--font);font-size:calc(var(--modal-fs-base) - 1px);font-weight:700;
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
  padding:5px 16px;display:flex;align-items:flex-start;gap:10px;flex-wrap:nowrap;
}
/* The right-side controls live inside a single flex item that aligns to
   the TOP of the toolbar (matching the first row of tabs). Internally
   it stays display:flex/align-items:center so the labels (.tl) and the
   filter input — which have different heights — line up neatly with each
   other. flex-shrink:0 keeps it from getting squeezed; the tab-bar
   (flex:0 1 auto) absorbs horizontal slack and overflows downward via
   its own flex-wrap, leaving this block parked at the top-right of the
   toolbar even when tabs grow into several rows below.
   margin-left:auto pins this block to the RIGHT edge of the toolbar so
   it never slides left when the tab count shrinks — it stays parked at
   top-right exactly as it did originally. The auto margin soaks up all
   horizontal slack when tabs are few (big gap, right-anchored); when
   tabs fill the row the slack is 0 and the toolbar's own 10px flex gap
   provides the separation (≈ the filter→type inter-control gap).
   Every interactive control (.tab-btn/.tab-add on the left, .finput/
   .proto-btn/.sort-btn here) is locked to the same 22px box height, so
   with the toolbar top-aligned the first tab row and this block share
   the exact same top AND bottom edge. */
.toolbar-right{
  display:flex;align-items:center;gap:10px;flex-shrink:0;flex-wrap:nowrap;
  margin-left:auto;
}
.tl{font-size:10px;color:var(--dim);text-transform:uppercase;letter-spacing:.1em;white-space:nowrap}
.finput{
  background:var(--bg3);border:1px solid var(--border2);border-radius:3px;
  color:var(--text);font-family:var(--font);font-size:12px;padding:0 9px;width:180px;outline:none;
  box-sizing:border-box;height:22px;
}
.finput:focus{border-color:var(--accent)}
.sort-group{display:flex;gap:4px}
.sort-btn{
  all:unset;cursor:pointer;font-family:var(--font);font-size:10px;
  box-sizing:border-box;height:22px;display:inline-flex;align-items:center;justify-content:center;
  padding:0 8px;border-radius:2px;color:var(--dim);border:1px solid var(--border2);transition:all .15s;
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
thead th.cpr{text-align:center}
thead th.ct {text-align:center}
thead th.cs {text-align:center}
thead th.cp2{text-align:center}
thead th.csp{text-align:center}
thead th.ca {text-align:center}
tbody tr{border-bottom:1px solid var(--border);transition:background .08s}
tbody tr:hover{background:var(--bg3)}
tbody tr.row-cp{background:#071a09!important;box-shadow:inset 3px 0 0 var(--green)}
tbody tr.row-ct{background:#060f1f!important;box-shadow:inset 3px 0 0 var(--blue)}
/* RELOAD highlight: newly-added configs glow green and fade out. The fade is
   driven by a single global --flash-alpha variable (animated in JS), NOT a
   per-row CSS animation — so re-rendering a row while scrolling reads the
   current alpha instead of restarting the animation (which made the glow
   "re-ignite" on scroll). */
tbody tr.row-new{background:rgba(80,200,120,var(--flash-alpha,0))!important;box-shadow:inset 3px 0 0 var(--green)}
.reload-toast{position:fixed;top:54px;left:50%;transform:translateX(-50%);background:var(--bg2);border:1px solid var(--border);border-radius:6px;padding:6px 16px;font-size:13px;font-weight:800;letter-spacing:.04em;color:var(--text);z-index:9999;box-shadow:0 4px 16px rgba(0,0,0,.45);transition:opacity .45s;opacity:1;pointer-events:none}
.reload-toast.fade{opacity:0}
/* ── Logs dock panel ── */
#log-panel{position:relative;flex-shrink:0;height:30vh;min-height:120px;display:flex;flex-direction:column;border-top:2px solid var(--border);background:var(--bg2)}
.log-resize{position:absolute;top:-3px;left:0;right:0;height:6px;cursor:ns-resize;z-index:10}
.log-resize:hover,.log-resize.dragging{background:var(--accent)}
.log-head{flex-shrink:0;display:flex;align-items:center;gap:8px;padding:5px 12px;border-bottom:1px solid var(--border2)}
.log-title{font-size:11px;font-weight:800;text-transform:uppercase;letter-spacing:.08em;color:var(--accent)}
.log-head .spacer{flex:1}
.log-sel{background:var(--bg3);border:1px solid var(--border2);color:var(--text);font-family:var(--font);font-size:10px;border-radius:3px;padding:1px 4px;outline:none;cursor:pointer}
.log-auto{display:flex;align-items:center;gap:4px;font-size:10px;color:var(--dim);text-transform:uppercase;letter-spacing:.04em;cursor:pointer;user-select:none}
.log-head .btn{padding:3px 9px;font-size:10px}
.log-view{flex:1;overflow-y:auto;padding:6px 12px;font-family:var(--font);font-size:11px;line-height:1.5;white-space:pre-wrap;word-break:break-word;background:var(--bg);user-select:text;-webkit-user-select:text;cursor:text}
.log-line{display:block}
.log-line .lt{color:var(--dim2)}
.log-line .ls{font-weight:700;margin:0 6px}
.log-line.lvl-error{color:#f08a8a}
.log-line.lvl-warn{color:var(--accent)}
.log-line.lvl-info{color:var(--dim)}
.log-line.lvl-raw{color:var(--text)}
.log-line .ls.src-xray{color:#7bb6ff}
.log-line .ls.src-singbox{color:#b387f0}
.log-line .ls.src-vair{color:#9fd84a}
.log-empty{color:var(--dim);font-style:italic}
#btn-logs.on{color:var(--accent);border-color:var(--accent)}
td{padding:5px 10px;vertical-align:middle;font-size:12px}
.ci{width:38px;color:var(--dim);font-size:11px;text-align:left}
.cpr{width:64px;text-align:center}
.cn{min-width:150px;max-width:210px;text-align:left}
.ch{max-width:155px;text-align:left}.ct{width:88px;text-align:center}.cs{width:72px;text-align:center}
.cp2{width:110px;text-align:center}
.csp{width:130px;text-align:center}
.ca{width:220px;text-align:right}

.nc{display:flex;flex-direction:column;gap:2px}
/* name + "last" badge on one row: name flexes/ellipsises, badge stays fixed
   and is pushed to the right edge (next to the host column). */
.nm-row{display:flex;align-items:center;gap:6px;min-width:0}
/* Favorite star — fixed at the left of the name row. */
.fav{flex:0 0 auto;cursor:pointer;color:var(--dim2);font-size:13px;line-height:1;user-select:none;transition:color .15s}
.fav:hover{color:var(--accent)}
.fav.on{color:var(--accent)}
.nm{flex:1 1 auto;min-width:0;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;color:var(--text)}
.nh{color:var(--dim);font-size:10px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
/* "last connected" badge — fixed size at the end of the name column; the
   adjacent name clips before it instead of overlapping. */
.last-badge{flex:0 0 auto;font-size:8px;font-weight:800;text-transform:uppercase;letter-spacing:.04em;color:var(--accent);background:rgba(232,197,71,.14);border:1px solid rgba(232,197,71,.35);border-radius:3px;padding:0 3px;line-height:13px}
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
.sb{font-size:10px;padding:1px 5px;border-radius:2px;color:var(--dim);
  display:inline-block;max-width:60px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;vertical-align:middle}
.sb.tls{color:var(--blue)}.sb.reality{color:var(--purple);font-weight:700}

.pb{font-size:10px;padding:1px 5px;border-radius:2px;border:1px solid var(--border);color:var(--dim);font-weight:600;text-transform:uppercase;letter-spacing:.04em;display:inline-block;max-width:54px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;vertical-align:middle}
.pb.vless{color:var(--accent);border-color:rgba(232,197,71,.4)}
.pb.vmess{color:#818cf8;border-color:rgba(129,140,248,.4)}
.pb.trojan{color:var(--red);border-color:rgba(248,113,113,.4)}
.pb.ss{color:var(--teal);border-color:rgba(45,212,191,.4)}
.pb.ss2022{color:var(--green);border-color:rgba(74,222,128,.4)}
.pb.hysteria2{color:var(--orange);border-color:rgba(251,146,60,.4)}
.pb.tuic{color:var(--purple);border-color:rgba(192,132,252,.4)}

/* No flex-wrap here: keeps the 8 pills in a single row so they don't
   blow out toolbar-right's fixed height (26px) and break the vertical
   alignment of the labels next to them. */
.proto-group{display:flex;gap:4px;flex-wrap:nowrap}
.proto-btn{
  all:unset;cursor:pointer;font-family:var(--font);font-size:10px;
  box-sizing:border-box;height:22px;display:inline-flex;align-items:center;justify-content:center;
  padding:0 8px;border-radius:2px;color:var(--dim);border:1px solid var(--border2);transition:all .15s;text-transform:lowercase;
}
.proto-btn:hover{color:var(--text);border-color:var(--dim)}
.proto-btn.active{color:var(--accent);border-color:var(--accent);background:rgba(232,197,71,.08)}
/* When Ctrl-clicking we may have several pills active at once. Tinted bg
   makes the selected set visually distinct from a single-select look. */
.proto-btn#proto-vless.active{color:var(--accent);border-color:var(--accent);background:rgba(232,197,71,.08)}
.proto-btn#proto-vmess.active{color:#a5b1ff;border-color:rgba(129,140,248,.7);background:rgba(129,140,248,.10)}
.proto-btn#proto-trojan.active{color:var(--red);border-color:rgba(248,113,113,.7);background:rgba(248,113,113,.10)}
.proto-btn#proto-ss.active{color:var(--teal);border-color:rgba(45,212,191,.7);background:rgba(45,212,191,.10)}
.proto-btn#proto-ss2022.active{color:var(--green);border-color:rgba(74,222,128,.7);background:rgba(74,222,128,.10)}
.proto-btn#proto-hysteria2.active{color:var(--orange);border-color:rgba(251,146,60,.7);background:rgba(251,146,60,.10)}
.proto-btn#proto-tuic.active{color:var(--purple);border-color:rgba(192,132,252,.7);background:rgba(192,132,252,.10)}

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
    <button class="btn ghost" id="btn-logs"      onclick="toggleLogs()" title="Logs">logs</button>
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
  <div class="toolbar-right">
    <span class="tl">filter</span>
    <input class="finput" id="fi" placeholder="name / host / type / transport…" oninput="applyFilter()">
    <span id="fc" style="font-size:11px;color:var(--dim)"></span>
    <span class="tl" style="margin-left:6px">type</span>
    <div class="proto-group" title="Click to filter by one type. Ctrl+click to multi-select.">
      <button class="proto-btn active" id="proto-all"       onclick="onProtoBtnClick(event,'')">all</button>
      <button class="proto-btn"        id="proto-vless"     onclick="onProtoBtnClick(event,'vless')">vless</button>
      <button class="proto-btn"        id="proto-vmess"     onclick="onProtoBtnClick(event,'vmess')">vmess</button>
      <button class="proto-btn"        id="proto-trojan"    onclick="onProtoBtnClick(event,'trojan')">trojan</button>
      <button class="proto-btn"        id="proto-ss"        onclick="onProtoBtnClick(event,'ss')">ss</button>
      <button class="proto-btn"        id="proto-ss2022"    onclick="onProtoBtnClick(event,'ss2022')">ss2022</button>
      <button class="proto-btn"        id="proto-hysteria2" onclick="onProtoBtnClick(event,'hysteria2')">hy2</button>
      <button class="proto-btn"        id="proto-tuic"      onclick="onProtoBtnClick(event,'tuic')">tuic</button>
    </div>
    <span class="tl" style="margin-left:6px">sort</span>
    <div class="sort-group">
      <button class="sort-btn active" id="sort-idx"   onclick="setSort('idx')">default</button>
      <button class="sort-btn"        id="sort-ping"  onclick="setSort('ping')">ping ↑</button>
      <button class="sort-btn"        id="sort-speed" onclick="setSort('speed')">speed ↓</button>
    </div>
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
      <th class="cpr">Type</th>
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

<!-- ── Logs dock panel (above the connection bar) ── -->
<div id="log-panel" style="display:none">
  <div class="log-resize" id="log-resize" title="Drag to resize"></div>
  <div class="log-head">
    <span class="log-title" id="log-title">Logs</span>
    <select class="log-sel" id="log-src" onchange="renderLogs()">
      <option value="">all</option>
      <option value="xray">xray</option>
      <option value="singbox">sing-box</option>
      <option value="vair">vair</option>
      <option value="test">test</option>
    </select>
    <select class="log-sel" id="log-lvl" onchange="renderLogs()">
      <option value="">all</option>
      <option value="info">info+</option>
      <option value="warn">warn+</option>
      <option value="error">error</option>
    </select>
    <label class="log-auto"><input type="checkbox" id="log-autoscroll" checked> <span id="log-auto-lbl">auto-scroll</span></label>
    <div class="spacer"></div>
    <button class="btn ghost" id="log-copy" onclick="copyLogs()">copy</button>
    <button class="btn ghost" id="log-clear" onclick="clearLogs()">clear</button>
    <button class="btn ghost" id="log-close" onclick="toggleLogs(false)" title="Close">&#10005;</button>
  </div>
  <div class="log-view" id="log-view"></div>
</div>

<!-- ── Connection Bar (bottom) ── -->
<div id="conn-bar">
  <div class="cdot" id="cdot"></div>
  <span id="clabel">DISCONNECTED</span>
  <span id="cdetail" style="flex:1;color:var(--dim)"></span>
  <span id="cping" class="cping" style="display:none" title="Click to re-check ping"></span>
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
// protoFilter is a Set of selected protocol IDs. Empty Set = "all" (no filter).
// Plain click on a pill replaces the selection; Ctrl/Meta + click toggles
// that pill in/out of the selection so the user can show several at once.
let protoFilter=new Set();
let connState={status:'idle',entry_index:-1,mode:'proxy'};
// Raw of the most recently connected config — drives the "last" badge.
// Seeded from settings on load (survives restarts) and updated live on
// every successful connection. Only one config carries the badge.
let lastConnectedRaw='';
let appInfo={singbox_available:false,is_admin:false,os:'windows'};
let appSettingsJS={sources_enabled:true, ru_sites_direct:false, direct_domains:[], direct_apps:[], tray_enabled:false, ping_concurrency:10, speed_concurrency:5, ping_test_url:'', speed_test_url:'', speed_test_url_fallback:'', ping_timeout_ms:0, speed_duration_sec:0, tun_mtu:0, stats_disabled:false, stats_total_up:0, stats_total_down:0, dns_leak_protection:false, kill_switch:false, block_lan:false, fakeip_disabled:false, bootstrap_dns:'', direct_dns:'', remote_dns:'', static_hosts:{}, modal_font_size:0, language:'', favorites:[], verbose_logs:false};

// ── i18n / UI prefs ─────────────────────────────────────────────────
// Modal-only translations. Main window stays English on purpose — table
// headers, status pills, tab bar, and titlebar are untouched.
// Keys ARE the English text. That doubles as the fallback when no
// translation exists and keeps t() callsites self-documenting.
const I18N = {
  ru: {
    // Section headers
    "Settings": "Настройки",
    // "Sources" is intentionally NOT translated — the user asked to keep
    // the section header as-is so the tab name and the settings section
    // stay visually consistent across languages.
    "Routing": "Маршрутизация",
    "Testing": "Тестирование",
    "Network": "Сеть",
    "Statistics": "Статистика",
    "Security": "Безопасность",
    "DNS": "DNS",
    "System": "Система",
    "Appearance": "Внешний вид",

    // Settings — labels / buttons
    "Enable Sources tab": "Включить вкладку «SOURCES»",
    "Russian sites without VPN": "Российские сайты без VPN",
    "Custom domains without VPN": "Свои домены без VPN",
    "Apps without VPN (TUN mode only)": "Приложения без VPN (только TUN)",
    "Browse running processes": "Просмотреть запущенные процессы",
    "Ping concurrency": "Параллельных ping-тестов",
    "Speed concurrency": "Параллельных speed-тестов",
    "Ping timeout (ms)": "Таймаут ping (мс)",
    "Speed test duration (s)": "Длительность speed-теста (с)",
    "Ping URL": "URL для ping",
    "Custom ping URL": "Свой URL для ping",
    "Speed URL": "URL для speed",
    "Custom speed URL": "Свой URL для speed",
    "Speed URL fallback": "Резервный URL для speed",
    "(used when the main URL returns HTTP 429)": "(используется, если основной URL возвращает HTTP 429)",
    "Custom speed fallback URL": "Свой резервный URL для speed",
    "None — no fallback": "Без резерва",
    "Pick \"None\" to disable the retry.": "Выберите «Без резерва», чтобы отключить повтор.",
    "TUN MTU": "TUN MTU",
    "Enable traffic statistics": "Считать трафик",
    "Lifetime total": "Итого за всё время",
    "reset total": "сбросить итог",
    "TUN DNS leak protection": "TUN: защита от утечек DNS",
    "TUN Kill-switch": "TUN: Kill-switch",
    "TUN Block LAN traffic": "TUN: блокировать LAN-трафик",
    "TUN FakeIP": "TUN FakeIP",
    "TUN Bootstrap DNS": "TUN Bootstrap DNS",
    "TUN Direct DNS": "TUN Direct DNS",
    "TUN Remote DNS": "TUN Remote DNS",
    "TUN Static hosts": "TUN: статические хосты",
    "Minimize to tray on close": "Сворачивать в трей при закрытии",
    "Verbose logs": "Подробные логи",
    "Raises xray/sing-box log detail (level info) so the Logs panel shows per-connection lines. Takes effect on next connection.":
      "Повышает детализацию логов xray/sing-box (уровень info), чтобы в панели логов были видны строки по каждому соединению. Применяется при следующем подключении.",
    "Log speed/ping tests": "Логировать тесты скорости/пинга",
    "Logs each ping/speed result plus the full core output during the test (so you can see why a config is unavailable). Off by default — bulk tests can be noisy.":
      "Логирует каждый результат пинга/скорости и полный вывод ядра во время теста (видно, почему конфигурация недоступна). По умолчанию выключено — массовые тесты могут быть шумными.",
    "Logs": "Логи",
    "Copy": "Копировать",
    "Clear": "Очистить",
    "Auto-scroll": "Автопрокрутка",
    "No logs yet — connect to a config to see core output.":
      "Логов пока нет — подключитесь к конфигу, чтобы увидеть вывод ядра.",
    "Settings font size (px)": "Размер текста в настройках (px)",
    "Language": "Язык",
    "close": "закрыть",
    "Data": "Данные",
    "Storage location": "Папка с данными",
    "Open folder": "Открыть",
    "Settings backup": "Резервная копия настроек",
    "Export": "Экспорт",
    "Import": "Импорт",
    "Exports tabs, tab settings and app settings to a JSON file. Import replaces the current state — useful when moving Vair to another computer.":
      "Экспортирует вкладки, настройки вкладок и приложения в JSON-файл. Импорт заменяет текущее состояние — удобно для переноса Vair на другой компьютер.",
    "Turn the toggle off to import only the app settings and keep your existing tabs.":
      "Выключите переключатель, чтобы импортировать только настройки приложения, оставив существующие вкладки.",
    "Import tabs and tab settings": "Импортировать вкладки и их настройки",
    "Replace current tabs and settings with the imported file? This cannot be undone.":
      "Заменить текущие вкладки и настройки данными из файла? Отменить это действие будет нельзя.",
    "Replace current app settings with the imported file? Tabs will not be touched.":
      "Заменить настройки приложения данными из файла? Вкладки затронуты не будут.",

    // Placeholders
    "e.g. vk.com, press Enter": "напр. vk.com, нажмите Enter",
    "e.g. chrome.exe, press Enter": "напр. chrome.exe, нажмите Enter",
    "e.g. Russia, press Enter": "напр. Russia, нажмите Enter",

    // Small annotations
    "(resolves VPN server; plain UDP)": "(резолвит сервер VPN; обычный UDP)",
    "(for RU bypass / direct domains)": "(для RU-обхода и direct-доменов)",
    "(through proxy; DoH URL or IP)": "(через прокси; DoH URL или IP)",
    "(domain → IP; one per line)": "(домен → IP; по одному на строку)",

    // Hints (paragraphs)
    "Route traffic to Russian domains and IPs directly, bypassing VPN. Takes effect on next connection.":
      "Трафик к российским доменам и IP идёт напрямую, минуя VPN. Применится при следующем подключении.",
    "Enter a domain — all its subdomains are included automatically. Takes effect on next connection.":
      "Введите домен — все его поддомены включаются автоматически. Применится при следующем подключении.",
    "Process names that bypass VPN. Only works in TUN mode (system proxy can't be excluded per-app at the OS level).":
      "Имена процессов, которые идут мимо VPN. Работает только в TUN-режиме (системный прокси нельзя исключить пер-приложение).",
    "Takes effect on next connection.": "Применится при следующем подключении.",
    "How many configs are pinged or speed-tested in parallel. Defaults: ping 10, speed 5. Takes effect on the next bulk test run.":
      "Сколько конфигов одновременно проверяется. По умолчанию: ping 10, speed 5. Действует при следующем массовом тесте.",
    "Ping timeout is per round (3 rounds run, best is reported) and also applies to the warm-up ping inside speed tests. Speed duration is how long the test downloads before computing throughput. Defaults: 1500 ms, 4 s.":
      "Таймаут ping применяется к одному раунду (всего 3 раунда, в результат идёт лучший) и к разогревочному ping внутри speed-теста. Длительность speed-теста — это время скачивания, после которого считается скорость. По умолчанию: 1500 мс, 4 с.",
    "Speed test runs for ~4 seconds regardless of file size, measuring throughput. Ping test accepts any HTTP response — pick whichever endpoint your provider routes best.":
      "Speed-тест работает заданное время (по умолчанию 4 с) независимо от размера файла. Ping принимает любой HTTP-ответ — выберите эндпоинт, который ваш провайдер маршрутизирует лучше.",
    "Default 9000 (jumbo frames). If you see download stalls or sites hanging, try 1500 or 1408. Takes effect on next connection.":
      "По умолчанию 9000 (jumbo). Если скачивания зависают или сайты не открываются — попробуйте 1500 или 1408. Применится при следующем подключении.",
    "Tracks bytes through the VPN tunnel in both modes. The lifetime total persists across sessions; the live session counter resets on every connect.":
      "Считает байты через VPN-туннель в обоих режимах. Итоговая сумма сохраняется между запусками; сессионный счётчик сбрасывается при каждом подключении.",
    "Forces all DNS queries through the tunnel using sing-box's built-in FakeIP. Without this, system DNS can escape through your ISP. Takes effect on next connection. Applies only to TUN mode.":
      "Принудительно отправляет все DNS-запросы через туннель, используя встроенный FakeIP в sing-box. Без этого DNS может утекать к провайдеру. Применится при следующем подключении. Работает только в TUN-режиме.",
    "Drops all traffic if the VPN goes down — no fallback to your physical network. Relies on the same strict-routing mechanism as DNS leak protection.":
      "Сбрасывает весь трафик, если VPN упал — без возврата к физической сети. Использует тот же механизм strict-routing, что и защита от утечек DNS.",
    "By default 192.168.x.x and similar private addresses bypass the VPN so printers, NAS, and router admin pages still work. Enable this to force LAN traffic through the tunnel too — usually breaks local services.":
      "По умолчанию 192.168.x.x и подобные приватные адреса идут мимо VPN — это нужно, чтобы работали принтеры, NAS и админка роутера. Включите, чтобы LAN-трафик тоже шёл через туннель — обычно ломает локальные сервисы.",
    "FakeIP returns pseudo-addresses instantly and resolves the real domain inside the tunnel — fastest, no leak. Turn off to use a real DoH server through the proxy (slower but more compatible with apps that do their own DNS).":
      "FakeIP мгновенно отдаёт псевдо-адрес, а реальное имя резолвится уже внутри туннеля — быстро и без утечек. Отключите, чтобы использовать настоящий DoH через прокси (медленнее, но совместимо с приложениями, у которых свой DNS).",
    "Leave blank for defaults: Quad9 / Yandex / Cloudflare-over-IP. Pick servers that work on your ISP for bootstrap and direct; remote goes through the tunnel so anything reachable from your VPN server works.":
      "Оставьте пустым для значений по умолчанию: Quad9 / Yandex / Cloudflare-over-IP. Для bootstrap и direct выбирайте серверы, которые работают у вашего провайдера; remote идёт через туннель, поэтому подходит всё, что доступно с VPN-сервера.",
    "Hard-coded answers checked before any DNS server. Useful for pinning the VPN server IP or working around broken DNS. Format: <code>domain ip</code> separated by spaces, one per line.":
      "Жёсткие ответы, проверяются до любого DNS-сервера. Удобно для прибивания IP VPN-сервера или обхода сломанного DNS. Формат: <code>домен ip</code> через пробел, по одному на строку.",
    "Increase or decrease the text size in the Settings and Tab settings modals only. The main window's typography is unchanged.":
      "Меняет размер текста только в окнах настроек программы и вкладок. Шрифты основного окна не меняются.",
    "Changes apply immediately when you reopen this dialog.":
      "Изменения применяются после повторного открытия диалога.",
    "Reset the lifetime traffic counter to 0? The current session is not affected.":
      "Сбросить общий счётчик трафика? Текущая сессия не затрагивается.",

    // openTabSettings
    "Tab Settings": "Настройки вкладки",
    // "SOURCES" stays as-is to match the untranslated section/tab name.
    "Sources Settings": "Настройки SOURCES",
    "Name": "Имя",
    "Source URLs (raw links, base64 subscriptions)": "URL источников (raw-ссылки, base64-подписки)",
    "+ add URL": "+ добавить URL",
    "Files (loaded in addition order, after URLs)": "Файлы (грузятся в порядке добавления, после URL)",
    "+ add file": "+ добавить файл",
    "Files are read from disk on every RELOAD, so edits propagate without re-adding. No size limit — only the path is stored.":
      "Файлы читаются с диска при каждом RELOAD, так что правки подхватываются без повторного добавления. Без лимита на размер — хранится только путь.",
    "Auto-refresh interval (minutes, 0 = off)": "Интервал автообновления (минуты, 0 = выкл.)",
    "Deduplicate duplicate configs": "Удалять повторяющиеся конфиги",
    "Off": "Выкл.",
    "Hide": "Скрыть",
    "Delete": "Удалить",
    "No deduplication": "Без дедупликации",
    "Hide duplicates from view (reversible)": "Скрыть дубликаты из вида (обратимо)",
    "Permanently delete duplicates": "Безвозвратно удалить дубликаты",
    "Exclude filter": "Фильтр исключений",
    "Configs matching any of these are hidden. Type a value and press Enter to add it; matching is a case-insensitive substring. Leave a column empty to disable it.":
      "Конфиги, совпадающие с любым из значений, скрываются. Введите значение и нажмите Enter, чтобы добавить его; сравнение — подстрока без учёта регистра. Оставьте столбец пустым, чтобы отключить его.",
    "Off: show everything. Hide: filter from view, reversible. Delete: permanently remove duplicate entries. Matching is by vless body (ignores the name).":
      "Off: показывать всё. Hide: скрыть из вида (обратимо). Delete: безвозвратно удалить дубликаты. Сравнение по vless-телу (имя игнорируется).",

    // Running processes picker
    "Running processes": "Запущенные процессы",
    "filter…": "фильтр…",
    "Click a process to add it to the Apps without VPN list.": "Кликните по процессу, чтобы добавить его в список «Приложения без VPN».",
    "refresh": "обновить",
    "already added": "уже добавлен",
    "more, refine filter": "ещё, уточните фильтр",
    "No process list available — only works in the desktop build.": "Список процессов недоступен — работает только в desktop-сборке.",
    "No matches": "Совпадений нет",

    // Tab settings — empty file list placeholder
    "No files added. Use the + add file button below.": "Файлы не добавлены. Используйте кнопку «+ добавить файл» ниже.",

    "cancel": "отмена",
    "save": "сохранить"
  }
};

function t(en){
  var lang = (appSettingsJS && appSettingsJS.language) || 'en';
  if(lang === 'en' || !lang) return en;
  var dict = I18N[lang];
  return (dict && Object.prototype.hasOwnProperty.call(dict, en)) ? dict[en] : en;
}

// applyUIPrefs writes the modal-font-size CSS variable from the current
// settings. Called from loadAppSettings on startup, and re-called when the
// user changes the size in Settings so other open modals (and any later
// opened ones) update without reopening. Default 11 matches the historical
// modal typography; out-of-range values fall back to the default to keep
// the UI legible if a saved value is corrupted.
function applyUIPrefs(){
  var fs = appSettingsJS && appSettingsJS.modal_font_size;
  if(!fs || fs < 9 || fs > 20) fs = 11;
  document.documentElement.style.setProperty('--modal-fs-base', fs+'px');
}
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
  else if(ev.type==='entry_update'){
    // Capture the connected config's ping for the conn-bar chip even when
    // this update is for a non-active tab; only feed the table on a match.
    noteConnPing(ev.payload);
    if(tabMatch) onUpdate(ev.payload);
  }
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
  else if(ev.type==='log')                           onLogBatch(ev.payload);
};
loadAppSettings();

// ── Logs panel ─────────────────────────────────────────────────────
// Client-side ring of recent log lines (mirrors the server cap). Fed live
// by SSE 'log' events; (re)filled from /api/logs whenever the panel opens.
const LOG_CAP=2000;
let logBuf=[];        // [{t,lvl,src,msg}]
let logPanelOpen=false;
// Severity rank for the "info+ / warn+ / error" filter.
const LOG_LVL_RANK={raw:0, info:1, warn:2, error:3};
function logSrcLabel(s){ return s==='singbox'?'sing-box':s; }
function logPassesFilter(l){
  var src=document.getElementById('log-src'); var lvl=document.getElementById('log-lvl');
  if(src && src.value && l.src!==src.value) return false;
  if(lvl && lvl.value){
    var min=LOG_LVL_RANK[lvl.value]||0;
    if((LOG_LVL_RANK[l.lvl]||0) < min) return false;
  }
  return true;
}
function fmtLogTime(ms){
  var d=new Date(ms||Date.now());
  function p(n){return (n<10?'0':'')+n;}
  return p(d.getHours())+':'+p(d.getMinutes())+':'+p(d.getSeconds());
}
function logLineHTML(l){
  var lvl=l.lvl||'raw';
  return '<span class="log-line lvl-'+lvl+'">'+
    '<span class="lt">'+fmtLogTime(l.t)+'</span>'+
    '<span class="ls src-'+(l.src||'vair')+'">['+x(logSrcLabel(l.src||'vair'))+']</span>'+
    x(l.msg||'')+'</span>';
}
// onLogBatch: the server batches log lines into one SSE event (a few per
// second) so a flooding core can't freeze the UI. Always buffer; if the panel
// is open, append the batch in a single DOM write and keep the view pinned to
// the bottom. Node count is capped so the view can't grow without bound.
var logDirty=false; // set when a batch was skipped (active selection) — forces a full resync next time
function onLogBatch(arr){
  if(!arr) return;
  if(!Array.isArray(arr)) arr=[arr]; // tolerate a single-object payload
  if(arr.length===0) return;
  for(var i=0;i<arr.length;i++) logBuf.push(arr[i]);
  if(logBuf.length>LOG_CAP) logBuf=logBuf.slice(logBuf.length-LOG_CAP);
  if(!logPanelOpen) return;
  var view=document.getElementById('log-view');
  if(!view) return;
  // Don't touch the DOM while the user is selecting text here — it would wipe
  // their selection mid-copy. Buffer silently and resync once it clears.
  if(selectionInLogPanel()){ logDirty=true; return; }
  var auto=document.getElementById('log-autoscroll');
  var atBottom=view.scrollHeight-view.scrollTop-view.clientHeight < 40;
  if(logDirty){ logDirty=false; renderLogs(); return; }
  var html='';
  for(var j=0;j<arr.length;j++){ if(logPassesFilter(arr[j])) html+=logLineHTML(arr[j]); }
  if(html){
    view.insertAdjacentHTML('beforeend', html);
    while(view.childElementCount>LOG_CAP) view.removeChild(view.firstChild);
  }
  if(auto && auto.checked && atBottom) view.scrollTop=view.scrollHeight;
}
// renderLogs rebuilds the whole view from the buffer (used on open and when
// a filter changes).
function renderLogs(){
  var view=document.getElementById('log-view');
  if(!view) return;
  var html='';
  for(var i=0;i<logBuf.length;i++){ if(logPassesFilter(logBuf[i])) html+=logLineHTML(logBuf[i]); }
  view.innerHTML=html || '<div class="log-empty">No logs yet — connect to a config to see core output.</div>';
  var auto=document.getElementById('log-autoscroll');
  if(auto && auto.checked) view.scrollTop=view.scrollHeight;
}
function toggleLogs(force){
  var panel=document.getElementById('log-panel');
  var btn=document.getElementById('btn-logs');
  if(!panel) return;
  var open=(typeof force==='boolean')?force:(panel.style.display==='none');
  logPanelOpen=open;
  panel.style.display=open?'flex':'none';
  if(btn) btn.classList.toggle('on', open);
  if(open){
    // Refill from the server so we don't miss lines emitted while closed.
    fetch('/api/logs').then(function(r){return r.json();}).then(function(arr){
      if(Array.isArray(arr)) logBuf=arr.slice(-LOG_CAP);
      renderLogs();
    }).catch(function(){ renderLogs(); });
  }
}
function copyLogs(){
  var lines=[];
  for(var i=0;i<logBuf.length;i++){
    var l=logBuf[i];
    if(!logPassesFilter(l)) continue;
    lines.push(fmtLogTime(l.t)+' ['+logSrcLabel(l.src||'vair')+'] '+(l.msg||''));
  }
  navigator.clipboard.writeText(lines.join('\n')).catch(function(){});
}
function clearLogs(){
  logBuf=[];
  renderLogs();
  fetch('/api/logs/clear',{method:'POST'}).catch(function(){});
}
function applyLogI18n(){
  // The Logs panel intentionally stays in English regardless of UI language
  // (it shows raw core output, so English labels keep it consistent). The
  // HTML defaults are already English — nothing to translate here.
}
// Drag the top edge of the Logs panel to resize it. Height is clamped to a
// sane range and kept as an inline style, so it persists while the panel is
// toggled open/closed within a session.
(function(){
  var handle=document.getElementById('log-resize');
  var panel=document.getElementById('log-panel');
  if(!handle||!panel) return;
  var startY=0, startH=0;
  function onMove(e){
    var dy=startY-e.clientY;           // drag up → taller
    var h=startH+dy;
    var max=Math.round(window.innerHeight*0.85);
    if(h<120) h=120;
    if(h>max) h=max;
    panel.style.height=h+'px';
  }
  function onUp(){
    handle.classList.remove('dragging');
    document.body.style.userSelect='';
    document.removeEventListener('mousemove',onMove);
    document.removeEventListener('mouseup',onUp);
  }
  handle.addEventListener('mousedown',function(e){
    startY=e.clientY;
    startH=panel.getBoundingClientRect().height;
    handle.classList.add('dragging');
    document.body.style.userSelect='none';
    document.addEventListener('mousemove',onMove);
    document.addEventListener('mouseup',onUp);
    e.preventDefault();
  });
})();

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
// Tracks the previous conn state so we can skip the expensive rebuildTable()
// call on the once-per-second uptime ticks. Without this, the table is
// torn down and re-rendered every second while connected, which resets
// the CSS :hover state on whatever row the cursor was on — visible as
// a flicker on every tick. The table only really needs to redraw when
// the connection status, mode, or which entry is connected actually
// changes; uptime alone goes nowhere near the table rows.
let prevConnSig = '';
function onConnUpdate(cs){
  // Reset the cached connected-config ping when we disconnect or when the
  // connected config changes, so the chip never shows a stale value.
  var prevRaw=connState&&connState.conn_raw;
  if(!cs||cs.status!=='connected'||cs.conn_raw!==prevRaw) connPing=null;
  connState=cs;
  // Remember the config we just connected to so it keeps the "last" badge
  // even after disconnect. Mirrors the server-side recordLastConnected.
  if(cs.status==='connected' && cs.conn_raw) lastConnectedRaw=cs.conn_raw;
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
        '<div class="pchip" style="pointer-events:none;color:var(--dim)">all traffic routed</div>'+
        (cs.stats_unavailable?'<div class="pchip" style="pointer-events:none;color:var(--orange)" title="Hysteria2/TUIC TUN runs as a single sing-box process with no local SOCKS hop, so per-session and lifetime traffic counters cannot be measured.">⚠ traffic stats unavailable</div>':'');
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

  // highlight row — findConnIdx matches by raw within the active tab, so the
  // connected row lights up on any tab that contains this config (-1 when it
  // isn't present here), not only the tab it was connected from.
  document.querySelectorAll('tbody tr.row-cp,tbody tr.row-ct').forEach(r=>{r.classList.remove('row-cp','row-ct')});
  if(cs.status==='connected'||cs.status==='connecting'){
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
  // Only rebuild the table when something that affects row rendering has
  // actually changed (status, mode, which entry is current, active tab).
  // uptime_sec ticks every second while connected and does not affect any
  // row — rebuilding for it caused hover-flicker on every tick.
  const sig = cs.status+'|'+cs.mode+'|'+cs.entry_index+'|'+(cs.conn_tab||'')+'|'+(cs.conn_raw||'');
  if(sig !== prevConnSig){
    prevConnSig = sig;
    rebuildTable();
  }
  updateConnPing();
}

// connPing caches the connected config's latest ping ({status, delay}) so
// the conn-bar chip can show it on EVERY tab — not just the tab that holds
// the config. Fed by noteConnPing from any entry_update matching conn_raw.
let connPing=null;
function noteConnPing(e){
  if(!e||!connState||connState.status!=='connected'||!connState.conn_raw)return;
  if(e.raw!==connState.conn_raw)return;
  connPing={status:e.ping_status, delay:e.delay};
  updateConnPing();
}

// updateConnPing renders the ping chip in the connection bar for the
// currently connected config. Shows whenever connected (on any tab). Value
// comes from the live connPing cache, falling back to the active-tab entry
// if present. Click re-pings: by index when the config is in the active
// tab, otherwise via the /api/ping/connected endpoint (server finds it).
function updateConnPing(){
  var el=document.getElementById('cping');
  if(!el)return;
  if(!connState||connState.status!=='connected'){
    el.style.display='none';
    return;
  }
  el.style.display='';
  el.onclick=function(){
    var idx=findConnIdx();
    if(idx>=0) pingOne(idx);
    else fetch('/api/ping/connected',{method:'POST'}).catch(function(){});
  };
  var st=connPing?connPing.status:null, dl=connPing?connPing.delay:null;
  if(st===null){
    var idx=findConnIdx();
    var e=(idx>=0)?entries[idx]:null;
    // Seed the cache from the active-tab entry so the value persists after
    // switching to a tab that doesn't contain this config.
    if(e){ st=e.ping_status; dl=e.delay; connPing={status:st, delay:dl}; }
  }
  if(st==='testing_ping'){ el.className='cping testing'; el.textContent='pinging…'; }
  else if(st==='ok'){ el.className='cping ok'; el.textContent=dl+' ms'; }
  else if(st==='failed'){ el.className='cping failed'; el.textContent='ping ✕'; }
  else { el.className='cping'; el.textContent='ping'; }
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
// prevRawsByTab remembers the raw set last seen for each tab so a RELOAD can
// diff against it and highlight what changed. flashNewIdx holds the indices
// of just-added configs to flash for a couple of seconds.
let prevRawsByTab={};
let flashNewIdx=new Set();
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

  // RELOAD change highlight: compare against this tab's previous raw set.
  // First time a tab is loaded in a session there's no baseline (no flash);
  // a later reload of the same tab flashes added rows and toasts +N/−M.
  var tabKey=activeTabId;
  var newRaws=list.map(function(e){return e.raw;});
  var prev=prevRawsByTab[tabKey];
  flashNewIdx=new Set();
  if(prev){
    var prevSet=new Set(prev), newSet=new Set(newRaws);
    var added=0, removed=0;
    list.forEach(function(e){ if(!prevSet.has(e.raw)){ flashNewIdx.add(e.index); added++; } });
    prev.forEach(function(r){ if(!newSet.has(r)) removed++; });
    if(added>0||removed>0) showReloadDelta(added,removed);
    if(flashNewIdx.size>0) startFlashFade();
  }
  prevRawsByTab[tabKey]=newRaws;

  rebuildTable();
}
// startFlashFade animates the global --flash-alpha from a peak down to 0 over
// ~2.6s. Because the rows read this variable (rather than running their own
// animation), the glow stays in sync across all flashed rows and does not
// restart when a row is re-created during virtual scrolling. At the end it
// clears the flash set and rebuilds once so the class is dropped.
let flashRAF=0;
function startFlashFade(){
  if(flashRAF) cancelAnimationFrame(flashRAF);
  var start=Date.now(), dur=2600, peak=0.28;
  var root=document.documentElement;
  function step(){
    var t=(Date.now()-start)/dur;
    if(t>=1){
      root.style.setProperty('--flash-alpha','0');
      flashRAF=0; flashNewIdx=new Set(); rebuildTable();
      return;
    }
    root.style.setProperty('--flash-alpha', (peak*(1-t)).toFixed(3));
    flashRAF=requestAnimationFrame(step);
  }
  flashRAF=requestAnimationFrame(step);
}
// showReloadDelta pops a brief toast with how many configs a reload added
// (+N) and removed (−M).
function showReloadDelta(added,removed){
  var parts=[];
  if(added>0)  parts.push('+'+added);
  if(removed>0)parts.push('−'+removed);
  if(parts.length===0)return;
  var t=document.createElement('div');
  t.className='reload-toast';
  t.textContent=parts.join('   ');
  document.body.appendChild(t);
  setTimeout(function(){ t.classList.add('fade'); },1900);
  setTimeout(function(){ t.remove(); },2400);
}
function onUpdate(e){
  entries[e.index]=e;
  // Keep sortedList in sync so virtual scroll renders fresh data when scrolling back
  for(var i=0;i<sortedList.length;i++){
    if(sortedList[i].index===e.index){ sortedList[i]=e; break; }
  }
  updateRow(e.index);
  recalcStats();
  // If this update is for the connected config, refresh the conn-bar ping chip.
  if(connState&&connState.status==='connected'&&e.raw&&connState.conn_raw===e.raw) updateConnPing();
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
// Ordered list of pill IDs; "" represents the "all" pill (empty selection).
const PROTO_PILLS=['','vless','vmess','trojan','ss','ss2022','hysteria2','tuic'];
// onProtoBtnClick is the routed handler for every type-filter pill. Plain
// click sets the selection to exactly that pill (or clears it when "all").
// Ctrl/Meta + click toggles the pill in/out of the current selection — this
// is how users build up a "vmess + ss + trojan" view without an extra menu.
// "all" + Ctrl is treated the same as plain "all" because mixing "all" with
// other pills has no coherent meaning.
function onProtoBtnClick(ev, p){
  var multi=ev && (ev.ctrlKey || ev.metaKey);
  if(p==='' || !multi){
    protoFilter=new Set();
    if(p) protoFilter.add(p);
  } else {
    if(protoFilter.has(p)) protoFilter.delete(p);
    else protoFilter.add(p);
  }
  saveProtoFilter();
  renderProtoPills();
  rebuildTable();
}
function renderProtoPills(){
  for(var i=0;i<PROTO_PILLS.length;i++){
    var s=PROTO_PILLS[i];
    var id=s===''?'proto-all':'proto-'+s;
    var el=document.getElementById(id);
    if(!el) continue;
    if(s===''){
      el.classList.toggle('active', protoFilter.size===0);
    } else {
      el.classList.toggle('active', protoFilter.has(s));
    }
  }
}
// Back-compat shim for any external caller (e.g. saved bookmarks, devtools,
// localStorage restore) that still passes a single protocol string.
function setProtoFilter(p){
  protoFilter=new Set();
  if(p) protoFilter.add(p);
  saveProtoFilter();
  renderProtoPills();
  rebuildTable();
}
// protoFilter persistence — survives reloads so the user's type-filter
// selection (incl. multi-select) sticks. localStorage can throw in some
// embedded WebView2 / private-mode contexts, so every access is guarded;
// a failure just degrades to the non-persistent behaviour silently.
var PROTO_FILTER_LS_KEY='vair.protoFilter';
function saveProtoFilter(){
  try{
    localStorage.setItem(PROTO_FILTER_LS_KEY, JSON.stringify(Array.from(protoFilter)));
  }catch(e){}
}
function loadProtoFilter(){
  try{
    var raw=localStorage.getItem(PROTO_FILTER_LS_KEY);
    if(!raw) return;
    var arr=JSON.parse(raw);
    if(!Array.isArray(arr)) return;
    var next=new Set();
    // Only accept IDs we still recognise — drops stale values if the
    // protocol list ever changes between versions.
    for(var i=0;i<arr.length;i++){
      if(PROTO_PILLS.indexOf(arr[i])>=0 && arr[i]!=='') next.add(arr[i]);
    }
    protoFilter=next;
  }catch(e){}
}
// chipProto derives the *display* protocol from an entry, distinguishing
// SS2022 from legacy SS by the cipher prefix. Backend reports both as "ss"
// because xray's outbound is identical — the split is a UI concern only.
function chipProto(e){
  var pr=(e.protocol||'vless').toLowerCase().replace(/[^a-z0-9]/g,'');
  if(pr==='ss' && (e.security||'').indexOf('2022-blake3-')===0) pr='ss2022';
  return pr;
}
// protoLabel maps the (full) protocol key to the short text shown in the
// TYPE column chip. Only "hysteria2" needs shortening — at 10px it overran
// the 64px column and bled into the Name cell. "hy2" matches the TYPE
// filter pill label exactly, so the chip and the filter stay consistent.
function protoLabel(pr){ return pr==='hysteria2' ? 'hy2' : pr; }
// Exclude-filter columns the user can target from the tab Settings modal.
// Keep in lockstep with excludeColumns in main.go shouldSkip.
var EXCLUDE_COLS=['name','type','host','transport','security'];
// parseExcludeRule splits a stored rule ("column:value") into {column,value}.
// Legacy bare strings (no colon, or unknown column) map to "name" so old
// tabs.json data keeps working without migration.
function parseExcludeRule(s){
  if(typeof s!=='string') return {column:'name',value:''};
  var i=s.indexOf(':');
  if(i>0){
    var col=s.substring(0,i).toLowerCase();
    if(EXCLUDE_COLS.indexOf(col)>=0){
      return {column:col,value:s.substring(i+1).trim().toLowerCase()};
    }
  }
  return {column:'name',value:s.trim().toLowerCase()};
}
// Example placeholders shown (greyed) in each column's chip input. One value
// per example — values are added one at a time with Enter, not comma lists.
var EXCLUDE_PLACEHOLDERS={
  name:'e.g. Russia',
  type:'e.g. vless',
  host:'e.g. example.com',
  transport:'e.g. tcp',
  security:'e.g. tls'
};
function applyFilter(){ filterText=document.getElementById('fi').value.toLowerCase(); rebuildTable(); }
function matches(e){
  // Per-tab dedup view filter — hide rows whose vless body appeared at an
  // earlier index. Only applies in "hide" mode; "delete" mode has already
  // removed those entries server-side, so they're not in the entries map.
  var tab=tabsList.find(function(t){return t.id===activeTabId;});
  if(tab && tab.dedup_mode==='hide' && dupBodyIndices.has(e.index)) return false;
  // Per-tab exclude filter — rules are "column:value" strings (legacy bare
  // strings are treated as name-column). Match is case-insensitive substring.
  var ef=tab&&tab.exclude_filter?tab.exclude_filter:[];
  if(ef.length>0){
    var fields={
      name:(e.name||'').toLowerCase(),
      type:chipProto(e),
      host:(e.host||'').toLowerCase(),
      transport:(e.network||'').toLowerCase(),
      security:(e.security||'').toLowerCase()
    };
    for(var i=0;i<ef.length;i++){
      var r=parseExcludeRule(ef[i]);
      if(!r.value) continue;
      var hay=fields[r.column]||fields.name;
      if(hay.indexOf(r.value)>=0) return false;
    }
  }
  // Type pill filter — empty Set means "all". Multi-select: row passes when
  // its chipProto is in the selected set. chipProto handles the SS / SS2022
  // split so the filter agrees with the chip the user sees.
  if(protoFilter && protoFilter.size>0){
    if(!protoFilter.has(chipProto(e))) return false;
  }
  if(!filterText)return true;
  return(e.name||'').toLowerCase().includes(filterText)||(e.host||'').toLowerCase().includes(filterText)
    ||(e.network||'').toLowerCase().includes(filterText)||(e.security||'').toLowerCase().includes(filterText)
    ||(e.protocol||'').toLowerCase().includes(filterText);
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
  // Per-mode comparator. The favorites-first wrapper below applies it within
  // each group, so favorites always sit on top but are still ordered by the
  // active sort (and so are the non-favorites beneath them).
  var cmp;
  if(sortMode==='ping'){
    cmp=function(a,b){if(a.delay>0&&b.delay>0)return a.delay-b.delay;if(a.delay>0)return -1;if(b.delay>0)return 1;return a.index-b.index;};
  }else if(sortMode==='speed'){
    // 1. Speed OK: sorted by speed descending
    // 2. Ping OK but speed failed/skipped: sorted by ping ascending
    // 3. Currently testing
    // 4. Ping failed: sorted by index
    var speedRank=function(e){
      if(e.speed_status==='ok' && e.speed_mbps>0) return 0;
      if(e.ping_status==='ok' && e.delay>0) return 1;
      if(e.speed_status==='testing_speed'||e.ping_status==='testing_ping') return 2;
      if(e.ping_status==='failed') return 3;
      return 4;
    };
    cmp=function(a,b){
      const ra=speedRank(a),rb=speedRank(b);
      if(ra!==rb) return ra-rb;
      if(ra===0) return b.speed_mbps-a.speed_mbps;
      if(ra===1) return a.delay-b.delay;
      return a.index-b.index;
    };
  }else{
    cmp=function(a,b){return a.index-b.index;};
  }
  // Favorites always float to the top — in EVERY sort mode — and are sorted
  // among themselves by the active comparator; non-favorites follow, also
  // sorted by it.
  list.sort(function(a,b){
    var fa=isFav(a.raw)?0:1, fb=isFav(b.raw)?0:1;
    if(fa!==fb) return fa-fb;
    return cmp(a,b);
  });
  sortedList = list;
  document.getElementById('fc').textContent=filterText?(list.length+'/'+Object.keys(entries).length):'';
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
  if(connState.status==='connected'||connState.status==='connecting'){
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

// pendingResort coalesces re-sort requests during bulk tests. While
// running ping all / speed all with sort = ping or speed, every row
// finishing its test would otherwise trigger a full rebuildTable — and
// since rebuildTable wipes the tbody, the :hover on whatever row the
// cursor was on flickered on each rebuild. Debouncing collapses N row
// finishes into one rebuild ~250 ms after the first; rows themselves
// have already been updated in-place by then, so the user sees fresh
// numbers immediately and only the positions catch up shortly after.
let pendingResort = null;
function scheduleResort(){
  if(pendingResort) return;
  pendingResort = setTimeout(function(){
    pendingResort = null;
    rebuildTable();
  }, 250);
}

function updateRow(idx){
  const e=entries[idx]; if(!e)return;
  const old=document.getElementById('r'+idx);
  if(!old){
    // Row is outside the virtual window. In sorted mode a finishing row
    // may need to be brought into view — schedule a debounced resort,
    // but only when the row has reached a terminal status (same rule
    // applied below for visible rows).
    if(sortMode!=='idx'){
      const stillTesting = e.ping_status==='testing_ping' || e.speed_status==='testing_speed';
      if(!stillTesting) scheduleResort();
    }
    return;
  }
  // In-place update of the row's contents — never destroys the <tr>
  // node itself, which is what kept :hover stable in idx mode (fix #3).
  // We extend the same approach to sorted modes here so live-speed
  // ticks and ping pill changes during a test stop flickering the
  // cursor row.
  const pos=parseInt(old.cells[0].textContent)||idx+1;
  const nr=buildRow(e,pos);
  old.className = nr.className;
  old.replaceChildren(...nr.childNodes);
  if((connState.status==='connected'||connState.status==='connecting')&&findConnIdx()===idx)
    old.classList.add(connState.mode==='tun'?'row-ct':'row-cp');
  // In sorted modes the row's position may have to change, but only
  // when its status is final — re-sorting on every live_speed progress
  // tick (~400 ms cadence) was the cause of the hover flicker before.
  // Debounced so a flurry of finishes during bulk tests collapses to
  // one rebuild.
  if(sortMode!=='idx'){
    const stillTesting = e.ping_status==='testing_ping' || e.speed_status==='testing_speed';
    if(!stillTesting) scheduleResort();
  }
}

function buildRow(e,pos){
  const tr=document.createElement('tr'); tr.id='r'+e.index;
  if(selectedRows.has(e.index)) tr.classList.add('selected');
  if(flashNewIdx.has(e.index)) tr.classList.add('row-new');
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
  // Connected highlight + disconnect button show on EVERY tab that holds
  // this config (matched by raw), not just the tab it was connected from.
  // Falling back to entry_index only makes sense within the connected tab
  // (indices aren't comparable across tabs), so that legacy branch keeps
  // the tab guard.
  const isConn=connState&&connState.status==='connected'&&(connState.conn_raw?connState.conn_raw===e.raw:(connState.entry_index===e.index&&(!connState.conn_tab||connState.conn_tab===activeTabId)));

  let connectBtn;
  if(isConn){
    connectBtn='<button class="btn sm-disc" onclick="doDisconnect()" title="Disconnect">disconnect</button>';
  } else {
    connectBtn='<button class="btn sm ghost" onclick="doConnect('+e.index+')" title="'+(selectedMode==='tun'?'TUN mode (all traffic)':'System Proxy (HTTP/SOCKS)')+'">connect</button>';
  }

  const pr=chipProto(e);
  // "last" badge: the single most-recently-connected config. Sits at the
  // RIGHT end of the NAME column (next to the host column). The name is a
  // flex item that shrinks/ellipsises while the badge stays fixed, so a long
  // config name clips instead of colliding with the badge.
  const isLast=e.raw && lastConnectedRaw && e.raw===lastConnectedRaw;
  const lastBadge=isLast?'<span class="last-badge" title="Last connected config">last</span>':'';
  // Favorite star: left of the name. stopPropagation so toggling doesn't also
  // select the row. Favorites float to the top in the default sort order.
  const favOn=isFav(e.raw);
  const favStar='<span class="fav'+(favOn?' on':'')+'" title="Favorite" onclick="event.stopPropagation();toggleFav('+e.index+')">'+(favOn?'★':'☆')+'</span>';
  tr.innerHTML=
    '<td class="ci">'+pos+'</td>'+
    '<td class="cpr"><span class="pb '+pr+'" title="'+x(pr)+'">'+x(protoLabel(pr))+'</span></td>'+
    '<td class="cn"><div class="nc"><div class="nm-row">'+favStar+'<span class="nm" title="'+x(e.name)+'">'+x(e.name)+'</span>'+lastBadge+'</div>'+
    '</div></td>'+
    '<td class="ch"><span class="nh">'+x(e.host||'')+(e.port?':'+e.port:'')+'</span></td>'+
    '<td class="ct"><span class="nb '+nc+'">'+x(e.network||'tcp')+'</span></td>'+
    '<td class="cs"><span class="sb '+(e.security||'none')+'" title="'+x(e.security||'none')+'">'+x(e.security||'none')+'</span></td>'+
    '<td class="cp2"><div class="vc"><span class="'+pp+'" title="'+x(pt)+'">'+pt+'</span></div></td>'+
    '<td class="csp"><div class="vc"><span class="'+sp+'" title="'+x(st)+'">'+st+'</span></div></td>'+
    '<td class="ca"><div class="act-cell">'+
      connectBtn+
      '<button class="btn sm ghost" title="Ping" onclick="pingOne('+e.index+')">ping</button>'+
      '<button class="btn sm ghost"  title="Speed" onclick="speedOne('+e.index+')">speed</button>'+
      '<button class="cpb" title="Copy URL" onclick="cpRaw(this,'+e.index+')">⎘</button>'+
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
// Collect indices of currently visible entries in on-screen (sortedList)
// order. sortedList is the result of matches() + the active sort, so the
// server tests rows in the same order the user sees them — including the
// default (idx) sort, which is what "test in the order on screen" means.
function visibleIndices(){
  return sortedList.map(function(e){ return e.index; });
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
  // The conn-bar ping chip is scoped to the connected config's tab — refresh
  // it so it hides/shows correctly after a tab switch.
  updateConnPing();
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
    // Count entries that failed ping or speed so the user knows how many the
    // bulk-delete will remove before clicking.
    var failed=0;
    Object.values(entries).forEach(function(en){ if(en.ping_status==='failed'||en.speed_status==='failed') failed++; });
    if(failed>0){
      m.innerHTML+='<div class="ctx-sep"></div>';
      m.innerHTML+='<div class="ctx-menu-item danger" onclick="deleteFailedRows();closeCtxMenu()">Delete failed ping/speed ('+failed+')</div>';
    }
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

// containsNodeURL mirrors looksLikeNodeURL on the Go side: any text that
// embeds one of the recognised proxy URL schemes is treated as paste-worthy.
function containsNodeURL(text){
  if(!text) return false;
  var schemes=['vless://','vmess://','trojan://','ss://','hysteria2://','hy2://','tuic://'];
  for(var i=0;i<schemes.length;i++){ if(text.indexOf(schemes[i])>=0) return true; }
  return false;
}

function pasteFromClipboard(){
  navigator.clipboard.readText().then(function(text){
    if(!containsNodeURL(text))return;
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
// selectionInLogPanel: true when the user has a live (non-collapsed) text
// selection anchored inside the Logs panel. Used so Ctrl+C / Ctrl+A defer to
// native browser behaviour there instead of the table's row-copy/select-all.
function selectionInLogPanel(){
  var panel=document.getElementById('log-panel');
  if(!panel || panel.style.display==='none') return false;
  var sel=window.getSelection&&window.getSelection();
  if(!sel || sel.rangeCount===0 || sel.isCollapsed) return false;
  return panel.contains(sel.anchorNode);
}
document.addEventListener('copy',function(e){
  // Inputs and textareas have their own selection — let the native copy
  // handle them so users can still copy text out of the URL/filter fields.
  var ae=document.activeElement;
  if(ae && (ae.tagName==='INPUT' || ae.tagName==='TEXTAREA')) return;
  // A highlighted selection in the Logs panel copies natively (lets the user
  // grab just the lines they dragged over) even if table rows are selected.
  if(selectionInLogPanel()) return;
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

// isFav / toggleFav: per-config favorites, keyed by raw URL so they survive
// reloads and re-sorts. Persisted in app settings; starred rows float to the
// top in the default sort order.
function isFav(raw){
  return !!(raw && appSettingsJS.favorites && appSettingsJS.favorites.indexOf(raw)>=0);
}
function toggleFav(idx){
  var e=entries[idx];
  if(!e||!e.raw)return;
  if(!appSettingsJS.favorites) appSettingsJS.favorites=[];
  var i=appSettingsJS.favorites.indexOf(e.raw);
  if(i>=0) appSettingsJS.favorites.splice(i,1);
  else appSettingsJS.favorites.push(e.raw);
  saveAppSettings();
  rebuildTable();
}

// deleteFailedRows removes every entry in the current tab whose ping OR
// speed test ended in failure. Same delete path as deleteSelectedRows, just
// a different index set. No-op on the main (Sources) tab — its entries are
// re-fetched, so deletions there wouldn't stick.
function deleteFailedRows(){
  if(activeTabId==='main')return;
  var indices=[];
  Object.values(entries).forEach(function(e){
    if(e.ping_status==='failed'||e.speed_status==='failed') indices.push(e.index);
  });
  if(indices.length===0)return;
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
    if(s.last_connected_raw) lastConnectedRaw=s.last_connected_raw;
    applyUIPrefs();
    applyLogI18n();
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

  ov.innerHTML='<div class="modal-box">'+
    '<div class="modal-title">'+t('Settings')+'</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">'+t('Sources')+'</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Enable Sources tab')+'</span>'+
        '<label class="toggle"><input type="checkbox" id="set-sources-on" '+(appSettingsJS.sources_enabled!==false?'checked':'')+' onchange="toggleSources(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
      '</div>'+
    '</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">'+t('Routing')+'</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Russian sites without VPN')+'</span>'+
        '<label class="toggle"><input type="checkbox" id="set-ru-direct" '+(appSettingsJS.ru_sites_direct?'checked':'')+' onchange="toggleRuSites(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
      '</div>'+
      '<div class="modal-hint">'+t('Route traffic to Russian domains and IPs directly, bypassing VPN. Takes effect on next connection.')+'</div>'+
      '<div class="modal-row" style="margin-top:10px;margin-bottom:4px">'+
        '<span class="modal-row-label">'+t('Custom domains without VPN')+'</span>'+
      '</div>'+
      '<div class="chips-wrap" id="domain-chips">'+domainChipsHtml+
        '<input class="chip-input" id="domain-input" placeholder="'+t('e.g. vk.com, press Enter')+'" onkeydown="domainChipKey(event)">'+
      '</div>'+
      '<div class="modal-hint">'+t('Enter a domain — all its subdomains are included automatically. Takes effect on next connection.')+'</div>'+
      '<div class="modal-row" style="margin-top:10px;margin-bottom:4px">'+
        '<span class="modal-row-label">'+t('Apps without VPN (TUN mode only)')+'</span>'+
      '</div>'+
      '<div class="chips-wrap" id="app-chips">'+appChipsHtml+
        '<input class="chip-input" id="app-input" placeholder="'+t('e.g. chrome.exe, press Enter')+'" onkeydown="appChipKey(event)">'+
      '</div>'+
      '<div class="modal-hint">'+t("Process names that bypass VPN. Only works in TUN mode (system proxy can't be excluded per-app at the OS level).")+' <a href="#" onclick="openProcessPicker(event)" style="color:var(--accent);text-decoration:underline">'+t('Browse running processes')+'</a>. '+t('Takes effect on next connection.')+'</div>'+
    '</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">'+t('Testing')+'</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Ping concurrency')+'</span>'+
        '<input class="modal-input num-input" id="set-ping-conc" type="number" min="1" max="200" value="'+(appSettingsJS.ping_concurrency||10)+'" onchange="updateConcurrency(\'ping\',this.value)">'+
      '</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Speed concurrency')+'</span>'+
        '<input class="modal-input num-input" id="set-speed-conc" type="number" min="1" max="100" value="'+(appSettingsJS.speed_concurrency||5)+'" onchange="updateConcurrency(\'speed\',this.value)">'+
      '</div>'+
      '<div class="modal-hint">'+t('How many configs are pinged or speed-tested in parallel. Defaults: ping 10, speed 5. Takes effect on the next bulk test run.')+'</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Ping timeout (ms)')+'</span>'+
        '<input class="modal-input num-input" id="set-ping-timeout" type="number" min="200" max="10000" step="100" value="'+(appSettingsJS.ping_timeout_ms||1500)+'" onchange="updatePingTimeout(this.value)">'+
      '</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Speed test duration (s)')+'</span>'+
        '<input class="modal-input num-input" id="set-speed-duration" type="number" min="1" max="60" value="'+(appSettingsJS.speed_duration_sec||4)+'" onchange="updateSpeedDuration(this.value)">'+
      '</div>'+
      '<div class="modal-hint">'+t('Ping timeout is per round (3 rounds run, best is reported) and also applies to the warm-up ping inside speed tests. Speed duration is how long the test downloads before computing throughput. Defaults: 1500 ms, 4 s.')+'</div>'+
      '<div class="modal-row" style="display:block;margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">'+t('Ping URL')+'</span>'+
        '<select class="modal-input" id="set-ping-url" style="margin-bottom:0" onchange="updateTestURL(\'ping\',this.value)">'+pingURLOptions()+'</select>'+
      '</div>'+
      '<div class="modal-row" id="ping-url-custom-row" style="display:'+(isCustomURL('ping')?'block':'none')+';margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">'+t('Custom ping URL')+'</span>'+
        '<input class="modal-input" id="set-ping-url-custom" style="margin-bottom:0" placeholder="https://..." value="'+x(isCustomURL('ping')?(appSettingsJS.ping_test_url||''):'')+'" onchange="updateTestURLCustom(\'ping\',this.value)">'+
      '</div>'+
      '<div class="modal-row" style="display:block;margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">'+t('Speed URL')+'</span>'+
        '<select class="modal-input" id="set-speed-url" style="margin-bottom:0" onchange="updateTestURL(\'speed\',this.value)">'+speedURLOptions()+'</select>'+
      '</div>'+
      '<div class="modal-row" id="speed-url-custom-row" style="display:'+(isCustomURL('speed')?'block':'none')+';margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">'+t('Custom speed URL')+'</span>'+
        '<input class="modal-input" id="set-speed-url-custom" style="margin-bottom:0" placeholder="https://..." value="'+x(isCustomURL('speed')?(appSettingsJS.speed_test_url||''):'')+'" onchange="updateTestURLCustom(\'speed\',this.value)">'+
      '</div>'+
      '<div class="modal-row" style="display:block;margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">'+t('Speed URL fallback')+' <span style="color:var(--muted);font-weight:400">'+t('(used when the main URL returns HTTP 429)')+'</span></span>'+
        '<select class="modal-input" id="set-speed-url-fallback" style="margin-bottom:0" onchange="updateSpeedFallback(this.value)">'+speedFallbackOptions()+'</select>'+
      '</div>'+
      '<div class="modal-row" id="speed-fallback-custom-row" style="display:'+(isCustomFallback()?'block':'none')+';margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">'+t('Custom speed fallback URL')+'</span>'+
        '<input class="modal-input" id="set-speed-url-fallback-custom" style="margin-bottom:0" placeholder="https://..." value="'+x(isCustomFallback()?(appSettingsJS.speed_test_url_fallback||''):'')+'" onchange="updateSpeedFallbackCustom(this.value)">'+
      '</div>'+
      '<div class="modal-hint">'+t('Speed test runs for ~4 seconds regardless of file size, measuring throughput. Ping test accepts any HTTP response — pick whichever endpoint your provider routes best.')+' '+t('Pick "None" to disable the retry.')+'</div>'+
    '</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">'+t('Network')+'</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('TUN MTU')+'</span>'+
        '<input class="modal-input num-input" id="set-mtu" type="number" min="576" max="9000" value="'+(appSettingsJS.tun_mtu||9000)+'" onchange="updateMTU(this.value)">'+
      '</div>'+
      '<div class="modal-hint">'+t('Default 9000 (jumbo frames). If you see download stalls or sites hanging, try 1500 or 1408. Takes effect on next connection.')+'</div>'+
    '</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">'+t('Statistics')+'</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Enable traffic statistics')+'</span>'+
        '<label class="toggle"><input type="checkbox" id="set-stats" '+(!appSettingsJS.stats_disabled?'checked':'')+' onchange="toggleStats(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
      '</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label" id="stats-total-label">'+t('Lifetime total')+': ↑'+fmtBytes(appSettingsJS.stats_total_up||0)+' ↓'+fmtBytes(appSettingsJS.stats_total_down||0)+'</span>'+
        '<button class="btn ghost sm" onclick="resetTotalStats()">'+t('reset total')+'</button>'+
      '</div>'+
      '<div class="modal-hint">'+t('Tracks bytes through the VPN tunnel in both modes. The lifetime total persists across sessions; the live session counter resets on every connect.')+'</div>'+
    '</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">'+t('Security')+'</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('TUN DNS leak protection')+'</span>'+
        '<label class="toggle"><input type="checkbox" id="set-dnslp" '+(appSettingsJS.dns_leak_protection?'checked':'')+' onchange="toggleDNSLeakProtection(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
      '</div>'+
      '<div class="modal-hint">'+t("Forces all DNS queries through the tunnel using sing-box's built-in FakeIP. Without this, system DNS can escape through your ISP. Takes effect on next connection. Applies only to TUN mode.")+'</div>'+
      '<div id="security-deps" style="'+(appSettingsJS.dns_leak_protection?'':'display:none')+'">'+
        '<div class="modal-row">'+
          '<span class="modal-row-label">'+t('TUN Kill-switch')+'</span>'+
          '<label class="toggle"><input type="checkbox" id="set-ks" '+(appSettingsJS.kill_switch?'checked':'')+' onchange="toggleKillSwitch(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
        '</div>'+
        '<div class="modal-hint">'+t('Drops all traffic if the VPN goes down — no fallback to your physical network. Relies on the same strict-routing mechanism as DNS leak protection.')+'</div>'+
        '<div class="modal-row">'+
          '<span class="modal-row-label">'+t('TUN Block LAN traffic')+'</span>'+
          '<label class="toggle"><input type="checkbox" id="set-blan" '+(appSettingsJS.block_lan?'checked':'')+' onchange="toggleBlockLAN(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
        '</div>'+
        '<div class="modal-hint">'+t('By default 192.168.x.x and similar private addresses bypass the VPN so printers, NAS, and router admin pages still work. Enable this to force LAN traffic through the tunnel too — usually breaks local services.')+'</div>'+
      '</div>'+
    '</div>'+
    '<div class="settings-section" id="dns-section" style="'+(appSettingsJS.dns_leak_protection?'':'display:none')+'">'+
      '<div class="section-header">'+t('DNS')+'</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('TUN FakeIP')+'</span>'+
        '<label class="toggle"><input type="checkbox" id="set-fakeip" '+(!appSettingsJS.fakeip_disabled?'checked':'')+' onchange="toggleFakeIP(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
      '</div>'+
      '<div class="modal-hint">'+t('FakeIP returns pseudo-addresses instantly and resolves the real domain inside the tunnel — fastest, no leak. Turn off to use a real DoH server through the proxy (slower but more compatible with apps that do their own DNS).')+'</div>'+
      '<div class="modal-row" style="display:block;margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">'+t('TUN Bootstrap DNS')+' <span style="color:var(--muted);font-weight:400">'+t('(resolves VPN server; plain UDP)')+'</span></span>'+
        '<input class="modal-input" id="set-bootstrap-dns" style="margin-bottom:0" placeholder="9.9.9.9" value="'+x(appSettingsJS.bootstrap_dns||'')+'" onchange="updateDNSServer(\'bootstrap\',this.value)">'+
      '</div>'+
      '<div class="modal-row" style="display:block;margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">'+t('TUN Direct DNS')+' <span style="color:var(--muted);font-weight:400">'+t('(for RU bypass / direct domains)')+'</span></span>'+
        '<input class="modal-input" id="set-direct-dns" style="margin-bottom:0" placeholder="77.88.8.8" value="'+x(appSettingsJS.direct_dns||'')+'" onchange="updateDNSServer(\'direct\',this.value)">'+
      '</div>'+
      '<div class="modal-row" style="display:block;margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">'+t('TUN Remote DNS')+' <span style="color:var(--muted);font-weight:400">'+t('(through proxy; DoH URL or IP)')+'</span></span>'+
        '<input class="modal-input" id="set-remote-dns" style="margin-bottom:0" placeholder="https://1.1.1.1/dns-query" value="'+x(appSettingsJS.remote_dns||'')+'" onchange="updateDNSServer(\'remote\',this.value)">'+
      '</div>'+
      '<div class="modal-hint">'+t('Leave blank for defaults: Quad9 / Yandex / Cloudflare-over-IP. Pick servers that work on your ISP for bootstrap and direct; remote goes through the tunnel so anything reachable from your VPN server works.')+'</div>'+
      '<div class="modal-row" style="display:block;margin-bottom:6px">'+
        '<span class="modal-row-label" style="display:block;margin-bottom:4px">'+t('TUN Static hosts')+' <span style="color:var(--muted);font-weight:400">'+t('(domain → IP; one per line)')+'</span></span>'+
        '<textarea class="modal-input" id="set-static-hosts" style="margin-bottom:0;min-height:60px;font-family:var(--mono);font-size:11px" placeholder="vpn.example.com 1.2.3.4" onchange="updateStaticHosts(this.value)">'+x(staticHostsToText(appSettingsJS.static_hosts))+'</textarea>'+
      '</div>'+
      '<div class="modal-hint">'+t('Hard-coded answers checked before any DNS server. Useful for pinning the VPN server IP or working around broken DNS. Format: <code>domain ip</code> separated by spaces, one per line.')+'</div>'+
    '</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">'+t('Appearance')+'</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Language')+'</span>'+
        '<select class="modal-input num-input" style="width:120px;text-align:left" id="set-lang" onchange="updateLanguage(this.value)">'+
          '<option value="en"'+(!appSettingsJS.language||appSettingsJS.language==="en"?" selected":"")+'>English</option>'+
          '<option value="ru"'+(appSettingsJS.language==="ru"?" selected":"")+'>Русский</option>'+
        '</select>'+
      '</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Settings font size (px)')+'</span>'+
        '<input class="modal-input num-input" id="set-modal-fs" type="number" min="9" max="20" value="'+(appSettingsJS.modal_font_size||11)+'" onchange="updateModalFontSize(this.value)">'+
      '</div>'+
      '<div class="modal-hint">'+t("Increase or decrease the text size in the Settings and Tab settings modals only. The main window's typography is unchanged.")+'</div>'+
    '</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">'+t('System')+'</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Minimize to tray on close')+'</span>'+
        '<label class="toggle"><input type="checkbox" id="set-tray" '+(appSettingsJS.tray_enabled?'checked':'')+' onchange="toggleTray(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
      '</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Verbose logs')+'</span>'+
        '<label class="toggle"><input type="checkbox" id="set-verbose-logs" '+(appSettingsJS.verbose_logs?'checked':'')+' onchange="toggleVerboseLogs(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
      '</div>'+
      '<div class="modal-hint">'+t('Raises xray/sing-box log detail (level info) so the Logs panel shows per-connection lines. Takes effect on next connection.')+'</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Log speed/ping tests')+'</span>'+
        '<label class="toggle"><input type="checkbox" id="set-log-tests" '+(appSettingsJS.log_tests?'checked':'')+' onchange="toggleLogTests(this.checked)"><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
      '</div>'+
      '<div class="modal-hint">'+t('Logs each ping/speed result plus the full core output during the test (so you can see why a config is unavailable). Off by default — bulk tests can be noisy.')+'</div>'+
    '</div>'+
    '<div class="settings-section">'+
      '<div class="section-header">'+t('Data')+'</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Storage location')+'</span>'+
        '<button class="btn ghost" onclick="openStorageLocation()">'+t('Open folder')+'</button>'+
      '</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Settings backup')+'</span>'+
        '<span style="display:inline-flex;gap:6px">'+
          '<button class="btn ghost" onclick="exportSettings()">'+t('Export')+'</button>'+
          '<button class="btn ghost" onclick="importSettings()">'+t('Import')+'</button>'+
        '</span>'+
      '</div>'+
      '<div class="modal-row">'+
        '<span class="modal-row-label">'+t('Import tabs and tab settings')+'</span>'+
        '<label class="toggle"><input type="checkbox" id="set-import-tabs" checked><span class="toggle-track"></span><span class="toggle-thumb"></span></label>'+
      '</div>'+
      '<input type="file" id="import-file" accept=".json,application/json" style="display:none" onchange="handleImportFile(event)">'+
      '<div class="modal-hint">'+t('Exports tabs, tab settings and app settings to a JSON file. Import replaces the current state — useful when moving Vair to another computer.')+' '+t('Turn the toggle off to import only the app settings and keep your existing tabs.')+'</div>'+
    '</div>'+
    '<div class="modal-btns">'+
      '<button class="btn ghost" onclick="document.getElementById(\'settings-modal\').remove()">'+t('close')+'</button>'+
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
  ov.innerHTML='<div class="modal-box">'+
    '<div class="modal-title">'+t('Running processes')+'</div>'+
    '<input class="modal-input" id="proc-filter" placeholder="'+t('filter…')+'" autofocus>'+
    '<div class="modal-hint" style="margin-top:0">'+t('Click a process to add it to the Apps without VPN list.')+'</div>'+
    '<div id="proc-list-box" style="max-height:300px;overflow-y:auto;border:1px solid var(--border2);border-radius:3px;background:var(--bg2);padding:4px"></div>'+
    '<div class="modal-btns">'+
      '<button class="btn ghost" onclick="refreshProcessCache();renderProcessList(document.getElementById(\'proc-filter\').value)">'+t('refresh')+'</button>'+
      '<button class="btn ghost" onclick="document.getElementById(\'proc-modal\').remove()">'+t('close')+'</button>'+
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
      x(n)+(already?'  ('+t('already added')+')':'')+
      '</div>';
    shown++;
    if(shown>500){ html+='<div style="padding:4px 8px;color:var(--dim);font-size:10px">… '+(names.length-shown)+' '+t('more, refine filter')+'</div>'; break; }
  }
  if(shown===0){
    html='<div style="padding:8px;color:var(--dim);font-size:11px;text-align:center">'+
      (names.length===0?t('No process list available — only works in the desktop build.'):t('No matches'))+
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
function toggleVerboseLogs(on){
  appSettingsJS.verbose_logs=on;
  saveAppSettings();
}
function toggleLogTests(on){
  appSettingsJS.log_tests=on;
  saveAppSettings();
}

// openStorageLocation asks the server to open %LOCALAPPDATA%\\vair in
// Explorer — that's where tabs.json, settings.json and the extracted
// xray/sing-box binaries live.
function openStorageLocation(){
  fetch('/api/storage/open',{method:'POST'}).then(function(r){
    if(!r.ok) r.text().then(function(t){ alert(t||'Could not open folder'); });
  }).catch(function(err){ alert(err); });
}

// exportSettings asks the server for the JSON document, then lets the user
// pick where to save it via a native Save As dialog. The browser-download
// path (anchor with the download attribute) is only a fallback when the
// native binding is unavailable — in WebView2 it would usually drop the
// file in the user's Downloads folder with no chance to choose.
function exportSettings(){
  fetch('/api/export').then(function(r){
    if(!r.ok) throw new Error('export failed: '+r.status);
    return r.text();
  }).then(function(text){
    // Build a timestamped suggested name; the user can change it in the dialog.
    function pad2(n){return (n<10?'0':'')+n;}
    var d=new Date();
    var stamp=d.getFullYear()+pad2(d.getMonth()+1)+pad2(d.getDate())+'_'+pad2(d.getHours())+pad2(d.getMinutes())+pad2(d.getSeconds());
    var name='vair_settings_'+stamp+'.json';
    if(typeof window._goSaveExport==='function'){
      return Promise.resolve(window._goSaveExport(name, text)).then(function(path){
        // Empty path == user cancelled. Nothing to do; no notification noise.
        return path;
      });
    }
    // Fallback: trigger a plain browser download.
    var blob=new Blob([text],{type:'application/json'});
    var url=URL.createObjectURL(blob);
    var a=document.createElement('a');
    a.href=url; a.download=name;
    document.body.appendChild(a); a.click();
    setTimeout(function(){ a.remove(); URL.revokeObjectURL(url); }, 0);
  }).catch(function(err){ alert('Export failed: '+err); });
}

// importSettings opens the hidden <input type="file">; the change handler
// (handleImportFile) reads the picked file and POSTs it to /api/import.
function importSettings(){
  var inp=document.getElementById('import-file');
  if(!inp) return;
  inp.value=''; // allow re-picking the same file
  inp.click();
}

function handleImportFile(ev){
  var f=ev.target.files && ev.target.files[0];
  if(!f) return;
  // Read the toggle BEFORE the confirm dialog so a dynamic-language remount
  // can't drop the element between us and the fetch.
  var tabsBox=document.getElementById('set-import-tabs');
  var includeTabs=tabsBox?tabsBox.checked:true;
  var msg=includeTabs
    ? t('Replace current tabs and settings with the imported file? This cannot be undone.')
    : t('Replace current app settings with the imported file? Tabs will not be touched.');
  if(!confirm(msg)){
    return;
  }
  var reader=new FileReader();
  reader.onload=function(){
    fetch('/api/import?tabs='+(includeTabs?'1':'0'),{
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body: reader.result
    }).then(function(r){
      if(!r.ok){
        r.text().then(function(t){ alert('Import failed: '+t); });
        return;
      }
      // Server has rebroadcast tabs/active_tab/loaded over SSE; force a
      // full re-render via reload to make sure every cached bit of UI
      // state (selectedRows, sortMode, modals) starts clean.
      location.reload();
    }).catch(function(err){ alert('Import failed: '+err); });
  };
  reader.onerror=function(){ alert('Could not read file'); };
  reader.readAsText(f);
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

// updatePingTimeout / updateSpeedDuration store the test-duration knobs.
// Bounds mirror minPingTimeoutMs / maxPingTimeoutMs / minSpeedDurationSec /
// maxSpeedDurationSec on the Go side; out-of-range values fall back to the
// compile-time defaults (1500 ms ping, 4 s speed) at measurement time.
function updatePingTimeout(raw){
  var v=parseInt(raw,10);
  if(isNaN(v) || v<200) v=200;
  if(v>10000) v=10000;
  appSettingsJS.ping_timeout_ms=v;
  var inp=document.getElementById('set-ping-timeout'); if(inp) inp.value=v;
  saveAppSettings();
}
function updateSpeedDuration(raw){
  var v=parseInt(raw,10);
  if(isNaN(v) || v<1) v=1;
  if(v>60) v=60;
  appSettingsJS.speed_duration_sec=v;
  var inp=document.getElementById('set-speed-duration'); if(inp) inp.value=v;
  saveAppSettings();
}

// updateModalFontSize / updateLanguage — Appearance controls. Both apply
// only to .modal-* DOM (Settings + Tab settings); the main window keeps
// its original typography and English labels by design. Bounds match
// applyUIPrefs: 9–20 px, default 11. Out-of-range values fall back to
// the default at apply time.
function updateModalFontSize(raw){
  var v=parseInt(raw,10);
  if(isNaN(v) || v<9) v=9;
  if(v>20) v=20;
  appSettingsJS.modal_font_size=v;
  var inp=document.getElementById('set-modal-fs'); if(inp) inp.value=v;
  applyUIPrefs();
  saveAppSettings();
}
function updateLanguage(code){
  // Empty / unknown → English (the source language of every literal in
  // openSettings / openTabSettings). The change takes effect when those
  // modals are reopened — reopening Settings right after picking a new
  // language is the path the user follows naturally to verify it.
  if(code!=='ru' && code!=='en') code='en';
  appSettingsJS.language=code;
  saveAppSettings(function(){
    // Reopen Settings so labels switch immediately. Tab settings re-render
    // on its own next open.
    var m=document.getElementById('settings-modal');
    if(m){ m.remove(); openSettings(); }
  });
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
  {url:'',                                                  label:'https://speed.cloudflare.com/__down?bytes=50000000 (default)'},
  {url:'https://speed.cloudflare.com/__down?bytes=10000000',  label:'https://speed.cloudflare.com/__down?bytes=10000000'},
  {url:'http://cachefly.cachefly.net/100mb.test',           label:'http://cachefly.cachefly.net/100mb.test'},
  {url:'https://proof.ovh.net/files/100Mb.dat',             label:'https://proof.ovh.net/files/100Mb.dat'}
];
// SPEED_PRESETS[0].url is intentionally empty for the *primary* dropdown
// (empty value tells the Go side "use the built-in default"). For the
// fallback dropdown, empty means "no retry" instead — so we need the
// literal default URL spelled out when listing/matching that preset there.
var SPEED_DEFAULT_URL='https://speed.cloudflare.com/__down?bytes=50000000';

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
// fallbackPresetURL returns the URL value for a SPEED_PRESETS entry as
// used in the *fallback* dropdown: the first preset (which has an empty
// URL in the primary list, meaning "use server default") gets remapped to
// the literal SPEED_DEFAULT_URL so it can actually be selected as a
// fallback.
function fallbackPresetURL(p){
  return p.url || SPEED_DEFAULT_URL;
}
// fallbackPresetLabel strips the "(default)" suffix when present. That
// suffix exists in the primary dropdown to signal "leave empty → use this
// URL", but in the fallback context it's just confusing — every option is
// a real URL the user can pick, no defaulting involved.
function fallbackPresetLabel(p){
  return (p.label||'').replace(/\s*\(default\)\s*$/,'');
}
// isCustomFallback: true when the saved fallback URL is set but doesn't
// match any SPEED_PRESETS entry — the dropdown should show "Custom URL…"
// selected and reveal the text input. Empty string means "None", not custom.
function isCustomFallback(){
  var cur = appSettingsJS.speed_test_url_fallback||'';
  if(cur==='' || cur==='__none') return false; // default (CacheFly) or disabled — not custom
  for(var i=0;i<SPEED_PRESETS.length;i++){
    if(fallbackPresetURL(SPEED_PRESETS[i])===cur) return false;
  }
  return true;
}
// speedFallbackOptions reuses SPEED_PRESETS but prepends an explicit
// "None" option (the default — fallback disabled). The first preset's
// empty URL is remapped to the actual Cloudflare default so the user can
// pick it as a fallback target (the label keeps "(default)" — that hints
// it's the same endpoint the primary slot falls back to when left blank).
function speedFallbackOptions(){
  var cur = appSettingsJS.speed_test_url_fallback||'';
  // Empty / unset = the default (CacheFly is on by default). Only the explicit
  // "__none" sentinel means the fallback is disabled.
  if(cur==='') cur='http://cachefly.cachefly.net/100mb.test';
  var isNone = (cur === '__none');
  var custom = isCustomFallback();
  var html='';
  html += '<option value="__none"'+(isNone?' selected':'')+'>'+t('None — no fallback')+'</option>';
  for(var i=0;i<SPEED_PRESETS.length;i++){
    var p = SPEED_PRESETS[i];
    var url = fallbackPresetURL(p);
    var sel = (!isNone && !custom && url===cur) ? ' selected' : '';
    var lbl = fallbackPresetLabel(p);
    // CacheFly is the built-in fallback default — mark it so in the list.
    if(url==='http://cachefly.cachefly.net/100mb.test') lbl += ' (default)';
    html += '<option value="'+x(url)+'"'+sel+'>'+x(lbl)+'</option>';
  }
  html += '<option value="__custom"'+(custom?' selected':'')+'>Custom URL…</option>';
  return html;
}
// updateSpeedFallback handles a dropdown change. "__none" clears the saved
// URL (fallback disabled); "__custom" reveals the text input but doesn't
// save until the user types and blurs; a preset saves its URL straight in.
function updateSpeedFallback(val){
  var customRow = document.getElementById('speed-fallback-custom-row');
  if(val==='__none'){
    if(customRow) customRow.style.display='none';
    // Persist the explicit "disabled" sentinel — empty would mean "default".
    appSettingsJS.speed_test_url_fallback = '__none';
    saveAppSettings();
    return;
  }
  if(val==='__custom'){
    if(customRow) customRow.style.display='block';
    var inp=document.getElementById('set-speed-url-fallback-custom');
    if(inp && !inp.value) inp.focus();
    return;
  }
  if(customRow) customRow.style.display='none';
  appSettingsJS.speed_test_url_fallback = val;
  saveAppSettings();
}
function updateSpeedFallbackCustom(raw){
  appSettingsJS.speed_test_url_fallback = (raw||'').trim();
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

// ── DNS leak protection / security toggles ─────────────────────────
// The DNS section is hidden when DNS leak protection is off, since the
// per-slot DNS server fields and FakeIP toggle are meaningless without
// the master switch. We toggle visibility instead of disabling so the
// section's defaults aren't visually misleading when irrelevant.

function toggleDNSLeakProtection(on){
  appSettingsJS.dns_leak_protection = !!on;
  // Kill-switch and Block LAN are TUN-only and depend on the same
  // strict-routing mechanism — hide both completely (rather than just
  // disable) when leak protection is off, so the UI doesn't show
  // settings that can't actually do anything.
  if(!on){
    if(appSettingsJS.kill_switch){
      appSettingsJS.kill_switch = false;
      var ks = document.getElementById('set-ks');
      if(ks) ks.checked = false;
    }
    if(appSettingsJS.block_lan){
      appSettingsJS.block_lan = false;
      var bl = document.getElementById('set-blan');
      if(bl) bl.checked = false;
    }
  }
  var deps = document.getElementById('security-deps');
  if(deps) deps.style.display = on ? '' : 'none';
  var dnsSec = document.getElementById('dns-section');
  if(dnsSec) dnsSec.style.display = on ? '' : 'none';
  saveAppSettings();
}
function toggleKillSwitch(on){
  appSettingsJS.kill_switch = !!on;
  saveAppSettings();
}
function toggleBlockLAN(on){
  appSettingsJS.block_lan = !!on;
  saveAppSettings();
}
function toggleFakeIP(on){
  // Storage is inverted (fakeip_disabled) so the JSON default state
  // (omitted/false) corresponds to FakeIP being ON when leak
  // protection is on — matches the recommended setting.
  appSettingsJS.fakeip_disabled = !on;
  saveAppSettings();
}
function updateDNSServer(slot, raw){
  var v = (raw||'').trim();
  if(slot === 'bootstrap') appSettingsJS.bootstrap_dns = v;
  else if(slot === 'direct') appSettingsJS.direct_dns = v;
  else if(slot === 'remote') appSettingsJS.remote_dns = v;
  saveAppSettings();
}

// Static hosts: stored as object {domain: ip}, edited in a textarea
// as one-per-line "domain ip" pairs (mirrors /etc/hosts ordering).
// Lines starting with # are comments. Whitespace around domain or
// IP is tolerated. Invalid lines are dropped silently on save —
// the parser is forgiving rather than yelling at the user.
function staticHostsToText(obj){
  if(!obj || typeof obj !== 'object') return '';
  var lines = [];
  Object.keys(obj).sort().forEach(function(k){
    lines.push(k + ' ' + obj[k]);
  });
  return lines.join('\n');
}
function updateStaticHosts(raw){
  var out = {};
  (raw||'').split(/\r?\n/).forEach(function(line){
    var t = line.trim();
    if(!t || t[0] === '#') return;
    var parts = t.split(/\s+/);
    if(parts.length < 2) return;
    var domain = parts[0].toLowerCase();
    var ip = parts[1];
    // Very loose validation — anything Go can parse counts as an IP
    // there, so we just reject obviously empty/whitespace values.
    if(domain && ip) out[domain] = ip;
  });
  appSettingsJS.static_hosts = out;
  saveAppSettings();
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
  // Same per-column exclude filter as the custom tabs. Legacy bare name
  // rules already stored on the main tab parse into the Name column.
  var efFieldsHtml=renderExcludeFields(ef);
  var ov=document.createElement('div');
  ov.className='modal-overlay';ov.id='tab-modal';
  ov.onclick=function(ev){if(ev.target===ov)ov.remove();};
  ov.innerHTML=
    '<div class="modal-box" id="tab-modal-box">'+
      '<div class="modal-title">'+t('Sources Settings')+'</div>'+
      '<div class="modal-label">'+t('Auto-refresh interval (minutes, 0 = off)')+'</div>'+
      '<input class="modal-input" id="ms-src-refresh" type="number" min="0" value="'+(tab&&tab.refresh_min||0)+'" style="width:80px;margin-bottom:10px">'+
      '<div class="modal-label">'+t('Exclude filter')+'</div>'+
      '<div class="modal-hint ef-hint">'+t('Configs matching any of these are hidden. Type a value and press Enter to add it; matching is a case-insensitive substring. Leave a column empty to disable it.')+'</div>'+
      '<div class="ef-fields" id="tab-filter-fields">'+efFieldsHtml+'</div>'+
      '<div class="modal-btns">'+
        '<button class="btn ghost" onclick="document.getElementById(\'tab-modal\').remove()">'+t('cancel')+'</button>'+
        '<button class="btn ghost" onclick="saveSourcesSettings()">'+t('save')+'</button>'+
      '</div>'+
    '</div>';
  document.body.appendChild(ov);
}
function saveSourcesSettings(){
  var refreshMin=parseInt(document.getElementById('ms-src-refresh').value)||0;
  var ef=collectExcludeFilter();
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
    wrap.innerHTML='<div class="modal-hint" style="margin:0 0 6px">'+t('No files added. Use the + add file button below.')+'</div>';
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
  var efFieldsHtml=renderExcludeFields(ef);

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
    '<div class="modal-box" id="tab-modal-box">'+
      '<div class="modal-title">'+t('Tab Settings')+'</div>'+
      '<div class="settings-section">'+
        '<div class="modal-label">'+t('Name')+'</div>'+
        '<input class="modal-input" id="ms-name" value="'+x(tab.name)+'" maxlength="40" style="margin-bottom:0">'+
      '</div>'+
      '<div class="settings-section">'+
        '<div class="modal-label">'+t('Source URLs (raw links, base64 subscriptions)')+'</div>'+
        '<div id="ms-urls">'+urlsHtml+'</div>'+
        '<button class="btn ghost" style="font-size:9px;margin:4px 0 12px" onclick="addURLRow()">'+t('+ add URL')+'</button>'+
        '<div class="modal-label">'+t('Files (loaded in addition order, after URLs)')+'</div>'+
        '<div id="ms-files"></div>'+
        '<button class="btn ghost" style="font-size:9px;margin:4px 0 8px" onclick="pickModalFiles()">'+t('+ add file')+'</button>'+
        '<div class="modal-hint">'+t('Files are read from disk on every RELOAD, so edits propagate without re-adding. No size limit — only the path is stored.')+'</div>'+
        '<div class="modal-label" style="margin-top:8px">'+t('Auto-refresh interval (minutes, 0 = off)')+'</div>'+
        '<input class="modal-input" id="ms-refresh" type="number" min="0" value="'+(tab.refresh_min||0)+'" style="width:80px;margin-bottom:0">'+
      '</div>'+
      '<div class="settings-section">'+
        '<div class="modal-row" style="margin-bottom:6px;align-items:flex-end">'+
          '<span class="modal-row-label">'+t('Deduplicate duplicate configs')+'</span>'+
          renderDedupSeg(tab.dedup_mode||(tab.dedup?'hide':''))+
        '</div>'+
        '<div class="modal-hint">'+t('Off: show everything. Hide: filter from view, reversible. Delete: permanently remove duplicate entries. Matching is by vless body (ignores the name).')+'</div>'+
      '</div>'+
      '<div class="settings-section">'+
        '<div class="modal-label">'+t('Exclude filter')+'</div>'+
        '<div class="modal-hint ef-hint">'+t('Configs matching any of these are hidden. Type a value and press Enter to add it; matching is a case-insensitive substring. Leave a column empty to disable it.')+'</div>'+
        '<div class="ef-fields" id="tab-filter-fields">'+efFieldsHtml+'</div>'+
      '</div>'+
      '<div class="modal-btns">'+
        '<button class="btn ghost" onclick="document.getElementById(\'tab-modal\').remove()">'+t('cancel')+'</button>'+
        '<button class="btn ghost" onclick="saveTabSettings(\''+tabId+'\')">'+t('save')+'</button>'+
      '</div>'+
    '</div>';
  document.body.appendChild(ov);
  renderFileList();
  document.getElementById('ms-name').focus();
  document.getElementById('ms-name').select();
}
// efChipHtml renders one stored value as a removable chip.
function efChipHtml(v){
  return '<span class="chip" data-v="'+x(v)+'">'+x(v)+
    '<span class="chip-x" onclick="removeEfChip(this)">x</span></span>';
}
// renderExcludeFields builds one labelled chip box per column, pre-filled
// with the values already stored on the tab. Values are added by typing and
// pressing Enter (same UX as "Custom domains without VPN"); each chip has an
// x to remove it. saveTabSettings / saveSourcesSettings read the chips back
// into "col:value" rules. Legacy bare name rules parse into the Name column.
function renderExcludeFields(rules){
  var byCol={name:[],type:[],host:[],transport:[],security:[]};
  if(rules && rules.length){
    for(var i=0;i<rules.length;i++){
      var r=parseExcludeRule(rules[i]);
      if(!r.value) continue;
      byCol[r.column].push(r.value);
    }
  }
  var html='';
  for(var k=0;k<EXCLUDE_COLS.length;k++){
    var col=EXCLUDE_COLS[k];
    var list=byCol[col];
    var label=col.charAt(0).toUpperCase()+col.slice(1);
    var chips='';
    for(var j=0;j<list.length;j++) chips+=efChipHtml(list[j]);
    html+='<div class="ef-field" data-col="'+col+'">'+
      '<div class="ef-field-tag">'+label+'</div>'+
      '<div class="chips-wrap" data-col="'+col+'">'+chips+
        '<input class="chip-input ef-chip-input" data-col="'+col+'" placeholder="'+x(EXCLUDE_PLACEHOLDERS[col])+'" onkeydown="efChipKey(event)">'+
      '</div>'+
    '</div>';
  }
  return html;
}
// efChipKey turns the typed value into a chip on Enter. Modal-local only —
// nothing is persisted until the user presses Save (which reads the chips).
function efChipKey(ev){
  if(ev.key!=='Enter')return;
  ev.preventDefault();
  var inp=ev.target;
  var val=inp.value.trim();
  if(!val)return;
  var wrap=inp.parentElement;
  var existing=wrap.querySelectorAll('.chip');
  for(var i=0;i<existing.length;i++){
    if((existing[i].getAttribute('data-v')||'').toLowerCase()===val.toLowerCase()){ inp.value=''; return; }
  }
  inp.insertAdjacentHTML('beforebegin', efChipHtml(val));
  inp.value='';
}
function removeEfChip(el){
  var chip=el.parentElement;
  if(chip) chip.remove();
}
// collectExcludeFilter reads every chip (plus any half-typed leftover in the
// inputs) back into the stored "col:value" rule list, de-duplicated and in
// column-display order so reopening the modal is stable.
function collectExcludeFilter(){
  var ef=[];
  var seen={};
  document.querySelectorAll('#tab-filter-fields .chips-wrap').forEach(function(wrap){
    var col=wrap.getAttribute('data-col');
    var push=function(v){
      v=(v||'').trim();
      if(!v) return;
      var stored=col+':'+v;
      if(seen[stored]) return;
      seen[stored]=1;
      ef.push(stored);
    };
    wrap.querySelectorAll('.chip').forEach(function(c){ push(c.getAttribute('data-v')); });
    var inp=wrap.querySelector('.ef-chip-input');
    if(inp) push(inp.value); // don't lose a value the user forgot to Enter
  });
  return ef;
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
    {v:'',       l:t('Off')},
    {v:'hide',   l:t('Hide')},
    {v:'delete', l:t('Delete')}
  ];
  var html='<div class="seg-group" id="ms-dedup-seg" role="radiogroup" aria-label="'+t('Deduplicate duplicate configs')+'">';
  for(var i=0;i<modes.length;i++){
    var m=modes[i];
    var active=(currentMode===m.v)?' active':'';
    var titleText=m.v===''?t('No deduplication'):m.v==='hide'?t('Hide duplicates from view (reversible)'):t('Permanently delete duplicates');
    html+='<button type="button" class="seg-btn'+active+'" data-mode="'+m.v+'" '+
      'onclick="selectDedupMode(this)" '+
      'title="'+titleText+'">'+
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
  // Exclude filter: chips added via Enter, read back into "col:value" rules.
  var ef=collectExcludeFilter();
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
  // Ctrl+A: select every row currently on-screen. sortedList is the
  // filter+sort result that the table actually renders, so this naturally
  // respects FILTER, TYPE pills, and the per-tab Exclude filter — copying
  // afterwards never includes hidden configs.
  if(e.ctrlKey&&(e.key==='a'||e.key==='A')){
    if(document.activeElement&&(document.activeElement.tagName==='INPUT'||document.activeElement.tagName==='TEXTAREA'))return;
    // If the user is selecting inside the Logs panel, Ctrl+A selects all the
    // log text there instead of the table rows.
    if(selectionInLogPanel()){
      var view=document.getElementById('log-view');
      if(view){
        e.preventDefault();
        e.stopPropagation();
        var r=document.createRange();
        r.selectNodeContents(view);
        var s=window.getSelection();
        s.removeAllRanges();
        s.addRange(r);
      }
      return;
    }
    e.preventDefault();
    e.stopPropagation();
    selectedRows.clear();
    sortedList.forEach(en=>selectedRows.add(en.index));
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
  if(!containsNodeURL(text))return;
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

// Restore the persisted type-filter selection and reflect it in the pills.
// Runs at end-of-script so PROTO_PILLS / renderProtoPills are defined (no
// temporal-dead-zone) and the proto-* buttons exist in the DOM. The table
// is (re)built from SSE-driven loads, which call matches() → protoFilter,
// so no explicit rebuildTable() is needed here.
loadProtoFilter();
renderProtoPills();

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

// handleStorageOpen reveals %LOCALAPPDATA%\vair in Explorer so the user
// can see/back up tabs.json, settings.json, and the extracted xray/sing-box
// binaries. Failures are reported as a 500 with the error text so the UI
// can flash a notification.
func handleStorageOpen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if err := openStorageLocation(tabsDir()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// handleLogs returns the current log buffer as JSON — used by the Logs panel
// to fill itself on open. Live updates afterwards arrive via the SSE "log".
func handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs.snapshot())
}

// handleLogsClear empties the log buffer (the panel's "Clear" button).
func handleLogsClear(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	logs.clear()
	go debug.FreeOSMemory() // hand the buffer's heap back to the OS
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// settingsExport is the on-disk file format produced by /api/export and
// consumed by /api/import. Schema version is checked on import; bump it
// whenever a field changes shape so old exports either keep working or
// fail with a clear message instead of silently corrupting state.
type settingsExport struct {
	Version     int             `json:"version"`
	ExportedAt  string          `json:"exported_at"`
	AppName     string          `json:"app"`
	AppSettings AppSettings     `json:"app_settings"`
	Tabs        []persistedTab  `json:"tabs"`
}

const settingsExportVersion = 1

// buildSettingsExport snapshots tabs + their current config entries + the
// app settings into a single document. We send config entries (not just
// source URLs) because a tab might be a hand-pasted collection with no
// source URL — round-tripping it through export/import has to preserve
// that data.
func buildSettingsExport() settingsExport {
	state.mu.RLock()
	var tabs []persistedTab
	for _, t := range state.tabs {
		pt := persistedTab{
			ID: t.ID, Name: t.Name,
			SourceURLs: t.SourceURLs, SourceFiles: t.SourceFiles,
			RefreshMin: t.RefreshMin, ExcludeFilter: t.ExcludeFilter,
			DedupMode: t.DedupMode,
		}
		if !t.IsMain {
			// Snapshot the raw config strings for pasted tabs so the import
			// on another machine sees the same configs without needing the
			// original source URL to be reachable.
			for _, e := range state.tabEntries[t.ID] {
				if e != nil && e.Raw != "" {
					pt.Configs = append(pt.Configs, e.Raw)
				}
			}
		}
		tabs = append(tabs, pt)
	}
	state.mu.RUnlock()
	settingsMu.RLock()
	settingsCopy := appSettings
	settingsMu.RUnlock()
	return settingsExport{
		Version:     settingsExportVersion,
		ExportedAt:  time.Now().UTC().Format(time.RFC3339),
		AppName:     "Vair",
		AppSettings: settingsCopy,
		Tabs:        tabs,
	}
}

func handleSettingsExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	data, err := json.MarshalIndent(buildSettingsExport(), "", "  ")
	if err != nil {
		http.Error(w, "marshal: "+err.Error(), 500)
		return
	}
	filename := fmt.Sprintf("vair_settings_%s.json", time.Now().Format("20060102_150405"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(200)
	w.Write(data)
}

// handleSettingsImport accepts a settingsExport JSON payload and applies it
// in place of the current state. The on-disk tabs.json / settings.json get
// rewritten, the in-memory state is rebuilt, and clients are told to
// reload via the SSE channel so the UI re-renders with the new tabs.
func handleSettingsImport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	// tabs=0 → import only the app settings; leave the user's existing tabs
	// untouched. Default (no param, or anything except "0") is full import.
	includeTabs := r.URL.Query().Get("tabs") != "0"

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	var imp settingsExport
	if err := json.Unmarshal(body, &imp); err != nil {
		http.Error(w, "parse JSON: "+err.Error(), 400)
		return
	}
	if imp.Version == 0 || imp.Version > settingsExportVersion {
		http.Error(w, fmt.Sprintf("unsupported export version %d (expected %d)", imp.Version, settingsExportVersion), 400)
		return
	}
	if includeTabs && len(imp.Tabs) == 0 {
		http.Error(w, "no tabs in export", 400)
		return
	}

	// Take ownership of the new state. Stop any running tests so they
	// don't reference about-to-be-replaced *ConfigEntry pointers.
	if atomic.LoadInt32(&state.pingRunning) == 1 {
		cancelPingAll()
	}
	if atomic.LoadInt32(&state.speedRunning) == 1 {
		cancelSpeedAll()
	}
	stopConnection()

	// Replace app settings (atomic on disk + in memory).
	settingsMu.Lock()
	appSettings = imp.AppSettings
	settingsMu.Unlock()
	saveSettings()

	// App-settings-only import: skip the tab rebuild entirely. The SSE push
	// at the end still refreshes any UI that watches app_settings.
	if !includeTabs {
		state.broadcast(SSEEvent{Type: "app_info", Payload: map[string]interface{}{
			"singbox_available": state.singboxBin != "",
			"is_admin":          checkAdmin(),
			"os":                "windows",
		}})
		w.WriteHeader(200)
		fmt.Fprintf(w, `{"tabs":0,"app_settings_only":true}`)
		return
	}

	// Rebuild tabs in memory from the imported document. Mirrors loadTabs
	// (which reads the same persistedTab shape) but on injected data
	// instead of tabs.json.
	state.mu.Lock()
	state.tabs = []Tab{{ID: "main", Name: "Sources", IsMain: true, Closable: false}}
	state.tabEntries = make(map[string][]*ConfigEntry)
	state.entries = nil
	for _, pt := range imp.Tabs {
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
		mode := pt.DedupMode
		if mode == "" && pt.Dedup {
			mode = "hide"
		}
		urls := pt.SourceURLs
		if len(urls) == 0 && pt.SourceURL != "" {
			urls = []string{pt.SourceURL}
		}
		tab := Tab{
			ID: pt.ID, Name: pt.Name, IsMain: false, Closable: true,
			SourceURLs: urls, SourceFiles: pt.SourceFiles,
			RefreshMin: pt.RefreshMin, ExcludeFilter: pt.ExcludeFilter, DedupMode: mode,
		}
		state.tabs = append(state.tabs, tab)
		state.tabEntries[tab.ID] = parseConfigLines(strings.Join(pt.Configs, "\n"))
	}
	// Make sure the active tab still exists; if the imported set dropped it,
	// fall back to "main".
	activeOK := false
	for _, t := range state.tabs {
		if t.ID == state.activeTab {
			activeOK = true
			break
		}
	}
	if !activeOK {
		state.activeTab = "main"
	}
	state.entries = state.tabEntries[state.activeTab]
	state.mu.Unlock()
	saveTabs()

	// Push the new tab list, active tab, and a "loaded" snapshot of the
	// active tab's entries so every connected client refreshes without
	// needing a page reload.
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	state.broadcast(SSEEvent{Type: "active_tab", Payload: state.activeTab})
	if cur := state.entries; cur != nil {
		snaps := make([]ConfigEntry, len(cur))
		for i, e := range cur {
			snaps[i] = e.snap()
		}
		state.broadcast(SSEEvent{Type: "loaded", Payload: snaps, Tab: state.activeTab})
	}

	w.WriteHeader(200)
	fmt.Fprintf(w, `{"tabs":%d}`, len(imp.Tabs))
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
	http.HandleFunc("/api/ping/connected", handlePingConnected)
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
	http.HandleFunc("/api/storage/open", handleStorageOpen)
	http.HandleFunc("/api/export", handleSettingsExport)
	http.HandleFunc("/api/import", handleSettingsImport)
	http.HandleFunc("/api/logs", handleLogs)
	http.HandleFunc("/api/logs/clear", handleLogsClear)
	go logFlushLoop()
}

func httpListenAndServe() error {
	addr := fmt.Sprintf(":%d", webPort)
	// The elevated relaunch (restartAsAdmin) starts the new instance before
	// the old one has fully released the port. Retry the bind for a few
	// seconds so the admin handoff doesn't kill the fresh process with a
	// "address already in use" error.
	var ln net.Listener
	var err error
	for i := 0; i < 40; i++ {
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	if err := http.Serve(ln, nil); err != nil {
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
