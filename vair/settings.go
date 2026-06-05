package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────── settings ──────────────────────────────

type AppSettings struct {
	SourcesEnabled bool     `json:"sources_enabled"`
	RuSitesDirect  bool     `json:"ru_sites_direct"`
	DirectDomains  []string `json:"direct_domains"`
	DirectApps     []string `json:"direct_apps"`
	// DirectDomainsDisabled / DirectAppsDisabled turn OFF the split-tunnel routing
	// for the saved "Custom domains without VPN" / "Apps without VPN" lists
	// WITHOUT clearing them — the lists persist but aren't applied to the
	// generated routing. Inverted bool so the JSON zero value (omitted) means
	// "enabled": both features default ON.
	DirectDomainsDisabled bool `json:"direct_domains_disabled,omitempty"`
	DirectAppsDisabled    bool `json:"direct_apps_disabled,omitempty"`
	// RoutingMode selects the traffic policy:
	//   "bypass_ru"    — everything through the VPN except Russian sites (legacy
	//                    RuSitesDirect=true). RU geosite/geoip go direct.
	//   "only_blocked" — direct by default, only resources BLOCKED in Russia go
	//                    through the VPN (runetfreedom ru-blocked rule-sets).
	//   "proxy_all"    — everything through the VPN (legacy RuSitesDirect=false).
	// Unset is derived from the legacy RuSitesDirect toggle (see routingMode()),
	// so old settings files keep their behaviour without an explicit migration.
	RoutingMode string `json:"routing_mode,omitempty"`
	// ProxyDomains is the manual "Custom domains THROUGH VPN" list (mirror of
	// DirectDomains); meaningful in only_blocked mode. ProxyDomainsDisabled keeps
	// the list but stops applying it.
	ProxyDomains         []string `json:"proxy_domains,omitempty"`
	ProxyDomainsDisabled bool     `json:"proxy_domains_disabled,omitempty"`
	// BlocklistURL is an optional user-supplied plain-text domain list (one
	// suffix per line) fetched + auto-updated and routed through the VPN in
	// addition to the bundled runetfreedom list (only_blocked mode).
	BlocklistURL string `json:"blocklist_url,omitempty"`
	TrayEnabled  bool   `json:"tray_enabled"`
	// Per-user concurrency overrides for bulk tests. Zero / unset falls back
	// to the defaults below. Capped at sane upper bounds inside the
	// accessors so a fat-fingered "9999" doesn't melt the local network.
	PingConcurrency  int `json:"ping_concurrency,omitempty"`
	SpeedConcurrency int `json:"speed_concurrency,omitempty"`
	// Customisable test endpoints. Empty → use built-in defaults
	// (pingTestURLDefault / speedTestURLDefault). The dropdown in
	// Settings → Testing lets the user pick a preset or "Custom URL".
	PingTestURL  string `json:"ping_test_url,omitempty"`
	SpeedTestURL string `json:"speed_test_url,omitempty"`
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
	LastConnectedRaw string `json:"last_connected_raw,omitempty"`
	// Favorite configs, keyed by their raw URL (stable across reload/sort).
	// Starred rows sort to the top in the default order.
	Favorites []string `json:"favorites,omitempty"`
	// VerboseLogs raises the xray/sing-box log level from warning→info so the
	// Logs panel shows the detailed per-connection lines (like v2rayN). Takes
	// effect on the next connection.
	VerboseLogs bool `json:"verbose_logs,omitempty"`
	// LogTests, when on, emits a [test] line per ping/speed result into the
	// Logs panel. Off by default — bulk tests are noisy.
	LogTests bool `json:"log_tests,omitempty"`
	// Per-round ping timeout (ms) and speed-test duration (seconds).
	// Zero / unset → built-in defaults (pingTimeout / speedDuration consts).
	// Clamped to sane bounds inside the accessors. Note that the speed test
	// always runs a ping first to warm the tunnel and get a fresh delay —
	// PingTimeoutMs applies to both standalone ping tests and that
	// pre-speed warm-up.
	PingTimeoutMs    int `json:"ping_timeout_ms,omitempty"`
	SpeedDurationSec int `json:"speed_duration_sec,omitempty"`
	// WarmupTimeoutMs bounds the un-measured warm-up request that primes the
	// tunnel (TCP + TLS/Reality handshake) before the timed ping rounds run.
	// A server that's slow to *establish* but fast once up (distant endpoint,
	// CDN, Reality) can exceed a tight warm-up and get falsely marked "timeout"
	// even though it works — so this is generous by default (4 s). Zero / unset
	// / out-of-range → the built-in warmupTimeout default. Clamped in
	// currentWarmupTimeout.
	WarmupTimeoutMs int `json:"warmup_timeout_ms,omitempty"`
	// UI scale and language for the Settings / Tab settings dialogs.
	// Both apply to those modals only — the main window (tabs, table,
	// connection bar, title bar) is intentionally not affected, since
	// that's where pixel-precise typography matters most. ModalFontSize
	// is the px value driving the --modal-fs-base CSS variable; default
	// 11. Language uses BCP-47-ish short codes — "" / "en" / "ru".
	ModalFontSize int    `json:"modal_font_size,omitempty"`
	Language      string `json:"language,omitempty"`
	// Theme is the UI colour scheme: "" / "dark" (default) or "light". Applied
	// client-side by toggling body.theme-light; the server only stores it.
	Theme string `json:"theme,omitempty"`
	// WindowSizePct is the default window size as a percent of the current
	// monitor's work area (clamped 40–100, capped 1440×920 / floored 900×600 in
	// idealWindowSize). 0 / unset → 80 (the historical default). Applied at
	// launch and on un-maximize, recomputed per monitor so it's deterministic.
	WindowSizePct int `json:"window_size_pct,omitempty"`
	// TUN adapter MTU. Zero / unset / out-of-range → 9000 (current default).
	// Recommended values: 9000 (default, jumbo) or 1500 / 1408 if a slow
	// network can't handle big frames.
	TUNMTU int `json:"tun_mtu,omitempty"`
	// Traffic statistics. Inverted bool so the JSON zero value (omitted /
	// false) means "enabled" — matches the default-on UI.
	StatsDisabled bool `json:"stats_disabled,omitempty"`
	// Lifetime traffic counters, persisted between sessions. Bytes.
	StatsTotalUp   int64 `json:"stats_total_up,omitempty"`
	StatsTotalDown int64 `json:"stats_total_down,omitempty"`

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
	DNSLeakProtection bool `json:"dns_leak_protection"`
	// KillSwitch only takes effect when DNSLeakProtection is on. With
	// strict_route=true, sing-box already drops traffic that can't go
	// through the tunnel; the kill-switch wording in UI tells the user
	// this is happening. (1.6.0 will add Windows Firewall rules as
	// belt-and-braces.)
	KillSwitch bool `json:"kill_switch,omitempty"`
	// BlockLAN is inverted: zero/false = LAN traffic allowed direct.
	// When true the ip_is_private→direct rule is removed and even
	// 192.168.x.x goes through the tunnel.
	BlockLAN bool `json:"block_lan,omitempty"`
	// SocksAuth protects the local SOCKS5 proxy (proxy mode) with a
	// username/password so other local apps can't use it or probe the VPN server.
	// OFF by default. When ON, the proxy SOCKS listener requires SocksUser/SocksPass
	// (generated at random if unset; editable / resettable from Settings). The
	// internal TUN handoff keeps its own separate auth regardless of this setting.
	SocksAuth bool   `json:"socks_auth,omitempty"`
	SocksUser string `json:"socks_user,omitempty"`
	SocksPass string `json:"socks_pass,omitempty"`
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
	FakeIPDisabled bool `json:"fakeip_disabled"`
	// DNS server overrides. Empty falls back to the built-in default
	// for the slot (see dnsServerOr* helpers).
	BootstrapDNS string `json:"bootstrap_dns,omitempty"` // plain UDP, IP-only
	DirectDNS    string `json:"direct_dns,omitempty"`    // for bypass traffic
	RemoteDNS    string `json:"remote_dns,omitempty"`    // through proxy
	// StaticHosts is a Windows-hosts-file-style domain→IP map.
	// Resolved before any DNS server is asked. Useful for hard-coded
	// VPN-server resolution, custom CNAME-like redirects, or
	// emergency bypass when DNS infrastructure is unreliable.
	StaticHosts map[string]string `json:"static_hosts,omitempty"`

	// ── Auto-connect / auto-switch (failover) (1.5.0) ────────────────
	// AutoConnect is the master switch for the whole feature. When OFF
	// the supervisor does nothing (zero overhead — no health probes). When
	// ON it implies connect-on-startup AND failover-while-connected — both
	// the separate toggles below were removed in favour of this single switch.
	AutoConnect bool `json:"auto_connect,omitempty"`
	// AutoConnectOnStart is DEPRECATED (the toggle was removed). AutoConnect now
	// always connects on startup. Retained only so old settings files round-trip
	// without error; no longer read.
	AutoConnectOnStart bool `json:"auto_connect_on_start,omitempty"`
	// AutoSwitch is DEPRECATED (the toggle was removed). Failover is now intrinsic
	// to AutoConnect (always on when the feature is on). Retained only so old
	// settings files round-trip without error; no longer read.
	AutoSwitch bool `json:"auto_switch,omitempty"`
	// AutoTabs is the candidate pool: the set of tab IDs whose configs
	// auto-connect / failover may choose from. Empty ⇒ defaults to the
	// main "Sources" tab (["main"]).
	AutoTabs []string `json:"auto_tabs,omitempty"`
	// AutoMode selects the connection mode for auto actions:
	// "" (remember last) / "proxy" / "tun". "tun" downgrades to "proxy"
	// when not running as admin.
	AutoMode string `json:"auto_mode,omitempty"`
	// AutoHealthSec is the live-tunnel probe interval in seconds
	// (default 15, clamped ≥5 at use-site).
	AutoHealthSec int `json:"auto_health_sec,omitempty"`
	// AutoFailThreshold is the number of consecutive failed probes before
	// a failover switch is triggered (default 2).
	AutoFailThreshold int `json:"auto_fail_threshold,omitempty"`
	// AutoPingRefresh re-pings the candidate pool after a config-list refresh
	// (auto-refresh / manual / startup) so failover can rank the fresh list by
	// real delay. Default ON; turn off to avoid the extra test traffic. No
	// omitempty: a default-true bool must persist an explicit false.
	AutoPingRefresh bool `json:"auto_ping_refresh"`
	// AutoMaxLatencyMs is the live-probe round-trip budget in milliseconds.
	// When AutoSwitch is on and the live link stays reachable but slower than
	// this for AutoFailThreshold checks in a row, the app fails over to the
	// fastest candidate. Default 500. 0 = disabled (only a dead link triggers
	// failover). No omitempty: a non-zero default must let an explicit 0 (the
	// user disabling the speed check) survive a restart.
	AutoMaxLatencyMs int `json:"auto_max_latency_ms"`
	// AutoRankBySpeed orders ping-OK candidates by descending measured download
	// speed (SpeedMBps) instead of ascending ping delay. Configs without a
	// speed result fall back to delay order. Default off.
	AutoRankBySpeed bool `json:"auto_rank_by_speed,omitempty"`
	// LastConnectedMode records the mode ("proxy"/"tun") of the most
	// recent connection, so AutoMode="" (remember last) can reuse it.
	LastConnectedMode string `json:"last_connected_mode,omitempty"`
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
func recordLastConnected(raw string, mode ConnMode) {
	if raw == "" {
		return
	}
	settingsMu.Lock()
	changed := appSettings.LastConnectedRaw != raw || appSettings.LastConnectedMode != string(mode)
	appSettings.LastConnectedRaw = raw
	if mode != "" {
		appSettings.LastConnectedMode = string(mode)
	}
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
	// Warm-up can legitimately take longer than a ping round (it includes the
	// full TCP + TLS/Reality handshake), so its upper bound is higher.
	minWarmupTimeoutMs = 200
	maxWarmupTimeoutMs = 20000
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

// currentWarmupTimeout returns the warm-up request timeout — the un-measured
// priming request that establishes the tunnel before the timed ping rounds.
// Zero / unset / out-of-range falls back to the compile-time warmupTimeout
// default (4 s). Raising this is the fix for "slow to connect but works" configs
// that v2rayN reports as alive while a tight warm-up here marks them "timeout".
func currentWarmupTimeout() time.Duration {
	settingsMu.RLock()
	ms := appSettings.WarmupTimeoutMs
	settingsMu.RUnlock()
	if ms < minWarmupTimeoutMs || ms > maxWarmupTimeoutMs {
		return warmupTimeout
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

// currentWindowSizePct returns the default-window-size percent of the current
// monitor's work area. Zero / unset / out-of-range → 80 (historical default).
// Clamped to [40, 100]; the absolute caps/floors live in idealWindowSize.
func currentWindowSizePct() int {
	settingsMu.RLock()
	p := appSettings.WindowSizePct
	settingsMu.RUnlock()
	if p < 40 || p > 100 {
		return 80
	}
	return p
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
//	bootstrap = 9.9.9.9 (Quad9). Plain UDP/IP, used to resolve the VPN
//	  server hostname on startup before any tunnel exists. Quad9 is
//	  non-profit, Swiss, and historically unblocked in RU. Cloudflare
//	  was thrown off the list because of the 2025 throttling.
//
//	direct = 77.88.8.8 (Yandex). Used only for bypass traffic (RU
//	  sites, custom direct domains). Yandex always works inside RU
//	  and privacy doesn't matter for direct traffic — those queries
//	  would otherwise go to the user's ISP anyway.
//
//	remote = https://1.1.1.1/dns-query (Cloudflare DoH over IP).
//	  Used for VPN-tunnelled traffic, so RU-level throttling is
//	  irrelevant — the request goes through the proxy. We use the
//	  IP-only DoH URL to avoid the meta-DNS chicken-and-egg of
//	  "resolve dns.cloudflare.com first".
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

// routingMode returns the effective traffic policy: "bypass_ru", "only_blocked"
// or "proxy_all". When RoutingMode is unset (old settings file / fresh install)
// it's derived from the legacy RuSitesDirect toggle so behaviour is preserved.
func routingMode() string {
	settingsMu.RLock()
	m := appSettings.RoutingMode
	ruDirect := appSettings.RuSitesDirect
	settingsMu.RUnlock()
	switch m {
	case "bypass_ru", "only_blocked", "proxy_all":
		return m
	}
	if ruDirect {
		return "bypass_ru"
	}
	return "proxy_all"
}

// effectiveProxyDomains returns the manual "through VPN" domain list, or nil when
// the list is toggled off. Mirror of the DirectDomains handling.
func effectiveProxyDomains() []string {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	if appSettings.ProxyDomainsDisabled {
		return nil
	}
	out := make([]string, 0, len(appSettings.ProxyDomains))
	for _, d := range appSettings.ProxyDomains {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// blocklistURL returns the trimmed custom blocklist URL ("" = none).
func blocklistURL() string {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return strings.TrimSpace(appSettings.BlocklistURL)
}

// proxySocksCreds returns the username/password the user-facing SOCKS5 proxy
// (proxy mode) should require. Returns ("","") when SOCKS auth is off — the
// listener then accepts connections without credentials. When on, it uses the
// user's custom credentials, falling back to the per-launch random ones if unset.
func proxySocksCreds() (user, pass string) {
	settingsMu.RLock()
	on := appSettings.SocksAuth
	u := strings.TrimSpace(appSettings.SocksUser)
	p := strings.TrimSpace(appSettings.SocksPass)
	settingsMu.RUnlock()
	if !on {
		return "", ""
	}
	if u == "" {
		u = proxyAuthUser
	}
	if p == "" {
		p = proxyAuthPass
	}
	return u, p
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
var appSettings = AppSettings{SourcesEnabled: true, DNSLeakProtection: true, FakeIPDisabled: true, AutoHealthSec: 15, AutoFailThreshold: 2, AutoSwitch: true, AutoPingRefresh: true, AutoMaxLatencyMs: 300}
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
