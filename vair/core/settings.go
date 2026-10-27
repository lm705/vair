package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// trimBOM drops a leading UTF-8 byte-order mark.
//
// Every JSON file this program reads back can have been through an editor, and
// several Windows tools write that mark by default — Notepad once did,
// PowerShell 5.1's Set-Content -Encoding utf8 still does. encoding/json rejects
// it as a syntax error at byte zero, so the file "cannot be read" while looking
// perfectly correct on screen, and the whole configuration falls back to
// defaults. Three bytes off the front is the entire fix; the mark carries no
// information for a file whose encoding is already fixed at UTF-8.
func trimBOM(b []byte) []byte { return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}) }

// ─────────────────────────── settings ──────────────────────────────

// UnlockServiceDef is one saved service-check entry. It is the settings shape of
// core.unlockService, kept separate so the on-disk format does not move every
// time the checker grows a field.
type UnlockServiceDef struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	OpenIf    []string `json:"open_if,omitempty"`
	BlockedIf []string `json:"blocked_if,omitempty"`
}

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
	// BlockDomains is the "Block" rule list (dropped outbound — ads/telemetry/
	// unwanted hosts). Same entry syntax as the direct/proxy lists (domain, IP/
	// CIDR, full:/regexp:/domain:/geosite:/geoip: prefixes). Applied at the
	// highest user priority (block → direct → proxy → mode rules).
	BlockDomains         []string `json:"block_domains,omitempty"`
	BlockDomainsDisabled bool     `json:"block_domains_disabled,omitempty"`
	// RoutingMode selects the traffic policy:
	//   "bypass_countries" — everything through the VPN except the sites of the
	//                    countries in BypassCountries (their geosite/geoip go
	//                    direct). "bypass_ru" is the pre-2.1 name for the same
	//                    mode fixed to Russia (legacy RuSitesDirect=true).
	//   "only_blocked" — direct by default, only resources BLOCKED in Russia go
	//                    through the VPN (runetfreedom ru-blocked rule-sets).
	//   "only_blocked_cn" — direct by default, only resources BLOCKED in China go
	//                    through the VPN (the community GFW list, Loyalsoldier).
	//   "only_selected" — direct by default, only the user's own lists through.
	//   "proxy_all"    — everything through the VPN (legacy RuSitesDirect=false).
	// Unset is derived from the legacy RuSitesDirect toggle (see routingMode()),
	// so old settings files keep their behaviour without an explicit migration.
	RoutingMode string `json:"routing_mode,omitempty"`
	// BypassCountries is the country set for "bypass_countries": ISO codes from
	// bypassCountryOrder ("ru","cn","ir","kz") whose sites go DIRECT.
	BypassCountries []string `json:"bypass_countries,omitempty"`
	// ProxyDomains is the manual "Custom domains THROUGH VPN" list (mirror of
	// DirectDomains); meaningful in only_blocked mode. ProxyDomainsDisabled keeps
	// the list but stops applying it.
	ProxyDomains         []string `json:"proxy_domains,omitempty"`
	ProxyDomainsDisabled bool     `json:"proxy_domains_disabled,omitempty"`
	// BlocklistURL is the pre-2.1 SINGLE "through VPN (URL)" list. Kept for
	// migration only: the UI moves it into BlocklistURLs on first edit, and
	// blocklistURLs() falls back to it while it's still set.
	BlocklistURL string `json:"blocklist_url,omitempty"`
	// BlocklistURLs are the "through VPN (URL)" sources: one or more links to
	// plain-text domain lists, each fetched + auto-updated and routed through the
	// VPN. BlocklistURLsDisabled keeps the list but stops applying it.
	BlocklistURLs         []string `json:"blocklist_urls,omitempty"`
	BlocklistURLsDisabled bool     `json:"blocklist_urls_disabled,omitempty"`
	TrayEnabled           bool     `json:"tray_enabled"`
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
	// Fallback speed URL — used ONLY when the primary speed URL answers with
	// one of the SpeedFallbackOn statuses (429 by default). "" / unset → the
	// CacheFly default (on by default);
	// "__none" → fallback explicitly disabled; anything else → that URL. See
	// currentSpeedFallbackURL. Each attempt has its own bounded
	// http.Client.Timeout, so an unreachable fallback cannot extend the test
	// indefinitely or leak goroutines.
	SpeedTestURLFallback string `json:"speed_test_url_fallback,omitempty"`
	// SpeedFallbackOn is which HTTP statuses from the primary speed URL send the
	// test to the fallback: codes, ranges and classes, comma-separated —
	// "429, 403, 500-599" or "429, 5xx". "" means 429, the rate limit the
	// fallback was made for. Only answers from the test file's server count: a
	// connection that fails says something about the configuration, and a
	// second URL would not change it. See speedFallbackOn.
	SpeedFallbackOn string `json:"speed_fallback_on,omitempty"`
	// CheckIPURL is what the "check IP" button asks. Empty → the built-in
	// default. Any service that answers JSON will do: the reply is read by field
	// name, not by a shape (see CheckExit).
	CheckIPURL string `json:"check_ip_url,omitempty"`
	// Raw URL of the most recently connected config. Drives the "last"
	// badge in the table. Persisted so the badge survives restarts; only
	// one config carries it at a time.
	LastConnectedRaw string `json:"last_connected_raw,omitempty"`
	// LastConnectedChain is every hop of the last session when it was a chain,
	// entry first; empty for a single configuration. LastConnectedRaw holds
	// only the exit (the badge), and restoring from it alone brought the exit
	// back connected directly — the one thing a chain is there to avoid.
	LastConnectedChain []string `json:"last_connected_chain,omitempty"`
	// Favorite configs, keyed by their raw URL (stable across reload/sort).
	// Starred rows sort to the top in the default order.
	Favorites []string `json:"favorites,omitempty"`
	// UpdateDismissedVersion is the newest version the user chose "don't show
	// again" for on the startup update banner. The banner stays hidden until a
	// version strictly newer than this is published (see checkForUpdate.Notify).
	UpdateDismissedVersion string `json:"update_dismissed_version,omitempty"`
	// VerboseLogs raises the xray/sing-box log level from warning→info so the
	// Logs panel shows the detailed per-connection lines. Takes effect on the
	// next connection.
	VerboseLogs bool `json:"verbose_logs,omitempty"`
	// KillSwitchPersistent extends the kill-switch past the tunnel's own life.
	// The engine's strict_route stops when the engine does; this holds the block
	// at the firewall, so a crash, a kill or Vair closing does not quietly return
	// the machine to its unprotected connection. Off by default, and deliberately
	// separate from KillSwitch: it is the one setting here that can leave a
	// machine with no way out.
	KillSwitchPersistent bool `json:"kill_switch_persistent,omitempty"`
	// ProxyKillSwitch blocks everything that does not go through the proxy.
	//
	// Proxy mode has never had a leak story: only applications that honour the
	// system proxy come through us, and everything else has always gone straight
	// out. This is the same firewall block the TUN kill-switch uses, with the
	// tunnel's interface replaced by "the engine and loopback" — so what is left
	// is the proxy, and nothing else. Off by default, and it needs the same
	// privileges TUN does, which proxy mode otherwise never asks for.
	ProxyKillSwitch bool `json:"proxy_kill_switch,omitempty"`
	// WatchConnection turns on the health check for a connection AUTO is not
	// managing: the same probe, interval and failure threshold AUTO uses, ending
	// in "the server stopped responding" and a notification instead of a switch.
	// Off by default — it drops a connection on the strength of a probe, and that
	// is a decision to opt into rather than to inherit. Irrelevant while AUTO is
	// on: AUTO watches the live node itself and moves to another one.
	WatchConnection bool `json:"watch_connection,omitempty"`
	// ConnTracking lets the connections panel collect: a row per connection,
	// the bytes read to learn where it is going, the engine's log parsed for it.
	// Off by default — what it collects is a list of the sites visited, with the
	// program and the traffic, kept for the whole run and readable through the
	// web interface — so it starts only when the switch in the panel ("record")
	// is turned on. Replaces conn_tracking_off, which had it on by default.
	ConnTracking bool `json:"conn_tracking,omitempty"`
	// RuleStats, when on, counts how often each line of the user's rule lists
	// actually fires, so the settings screen can say "never matched" about a rule
	// that has a typo in it. It costs a debug-level sing-box log — that is where
	// the engine names the rule it matched — so it is off unless asked for.
	RuleStats bool `json:"rule_stats,omitempty"`
	// UnlockServices is the service-check list once the user has edited it:
	// added their own, renamed one, pointed one somewhere else. Empty means
	// nothing has been touched and the built-in list is used as it shipped —
	// which is also what "restore the defaults" does, by emptying this.
	UnlockServices []UnlockServiceDef `json:"unlock_services,omitempty"`
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
	// Local proxy listen ports that other apps / the system proxy connect to
	// (they bind 127.0.0.1). Zero / unset / out-of-range → the built-in defaults
	// (connHTTPPort / connSOCKSPort). De-conflicted in currentProxyPorts; a change
	// takes effect on the next connection.
	HttpPort  int `json:"http_port,omitempty"`
	SocksPort int `json:"socks_port,omitempty"`
	// ProxyBind is the address the local HTTP/SOCKS proxy listeners bind to:
	//   "" / "127.0.0.1" → this machine only (default)
	//   "0.0.0.0"        → all interfaces (reachable from other devices on the LAN)
	//   any other IP     → a specific interface the user typed (custom)
	// Only expose beyond localhost on a trusted network: SOCKS keeps its random
	// per-launch credentials, HTTP has none. Applies on the next connection.
	// Supersedes the legacy ProxyAllowLAN bool (migrated on read in proxyBindHost).
	ProxyBind string `json:"proxy_bind,omitempty"`
	// Deprecated: use ProxyBind. Kept so an old settings.json still migrates.
	ProxyAllowLAN bool `json:"proxy_allow_lan,omitempty"`
	// NoSystemProxy: when on, connecting in proxy mode does NOT touch the
	// Windows system proxy — the local HTTP/SOCKS listeners still run, and only
	// apps explicitly pointed at them go through the tunnel ("non-system proxy"
	// mode). Off by default (the system proxy is set, as always).
	NoSystemProxy bool `json:"no_system_proxy,omitempty"`
	// ProxyGuardDisabled turns off the check that the OS proxy still points at
	// us while connected. Stored as the negative so the guard is on by default:
	// a hijacked setting sends traffic outside the tunnel while the app still
	// says "connected", which is the failure the user is least able to notice.
	// Worth turning off when another program is meant to own the setting.
	ProxyGuardDisabled bool `json:"proxy_guard_disabled,omitempty"`

	// Two text sizes and the language. ModalFontSize drives --modal-fs-base and
	// covers the dialogs — settings, tab settings, AUTO, connections, the
	// service check; default 12. AppFontSize drives --app-fs-base and covers the
	// main window — toolbar, tabs, table, connection bar, logs; default 13, the
	// size the window has always been.
	//
	// Two settings rather than one because the two have different room to grow:
	// a dialog is laid out by its content, while the table's columns have fixed
	// widths and the toolbar has one row to fit in.
	ModalFontSize int    `json:"modal_font_size,omitempty"`
	AppFontSize   int    `json:"app_font_size,omitempty"`
	Language      string `json:"language,omitempty"`
	// HideHints removes the explanatory paragraph under each setting in the
	// Settings, Tab-settings and auto-connect dialogs; an i mark beside each
	// name opens it instead. On by default since 2.7.0 (the user's call: the
	// dialogs are long, and a description is one click away). Not omitempty:
	// with the default on, an explicit "show them" is a false that has to be
	// written down, or the next start would read the default back.
	HideHints bool `json:"hide_hints"`
	// SettingsRev is the revision of the settings file, set by migrateSettings.
	// Zero in a file written before revisions existed. Program-owned: no
	// dialog writes it.
	SettingsRev int `json:"settings_rev,omitempty"`
	// Theme is the UI colour scheme: "" / "dark" (default) or "light". Applied
	// client-side by toggling body.theme-light; the server only stores it.
	Theme string `json:"theme,omitempty"`
	// TUN adapter MTU. Zero / unset / out-of-range → 9000 (current default).
	// Recommended values: 9000 (default, jumbo) or 1500 / 1408 if a slow
	// network can't handle big frames.
	TUNMTU int `json:"tun_mtu,omitempty"`
	// TUNStack is which TCP/IP stack sing-box runs inside the tunnel:
	// "" / "gvisor" (default), "mixed" or "system".
	//
	// It is the difference between "TUN costs a little CPU" and "TUN costs a
	// core". gVisor implements TCP in user space, in Go: every packet crossing
	// the tunnel is parsed, reassembled and re-emitted by the engine, which is
	// work proxy mode never does at all — there the application speaks SOCKS to
	// the engine and the operating system does the networking. "system" hands
	// TCP back to the OS and costs a fraction of it; "mixed" is system TCP with
	// gVisor's UDP. gVisor stays the default because it is the one that behaves
	// identically everywhere; the other two are worth trying on a machine where
	// the tunnel is expensive.
	TUNStack string `json:"tun_stack,omitempty"`
	// TunShareProxy also exposes Vair's HTTP/SOCKS proxy on the Proxy-access
	// address while a TUN connection is up, so another LAN device (a phone) can
	// route through this PC's tunnel. Off by default. Bind address + ports come
	// from ProxyBind / HttpPort / SocksPort; takes effect on the next connection.
	TunShareProxy bool `json:"tun_share_proxy,omitempty"`
	// ── Remote control over LAN (2.0.0) ─────────────────────────────
	// RemoteEnabled turns on an HTTP+SSE server on the LAN (port webPort) that
	// serves the same UI to a phone/browser in the same network. Off by default;
	// gated by RemoteToken. RemoteToken is a random secret generated on the first
	// enable — every request must present it (?key= or the auth header), so the
	// server ignores anyone on the network without the link/QR.
	RemoteEnabled bool   `json:"remote_enabled,omitempty"`
	RemoteToken   string `json:"remote_token,omitempty"`
	// RemotePort overrides the server port (0/unset → webPort, 19876). Lets 2.0
	// coexist with a running 1.10 release that already owns 19876.
	RemotePort int `json:"remote_port,omitempty"`
	// RemoteIP picks which of the machine's addresses the Settings UI puts in
	// the phone URL/QR (display only — the server listens on ALL interfaces).
	// Empty → the auto-ranked first LAN address.
	RemoteIP string `json:"remote_ip,omitempty"`

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
	// DNSLeakProtection is the master switch. When ON the names of the sites
	// behind the tunnel are resolved through it, and strict_route's WFP filter
	// (Windows) stops every other program asking a resolver outside it — the
	// lookups Windows otherwise sends to the physical adapter's resolver in
	// parallel. When OFF those names go to the system resolver.
	// On by default (see appSettings initialiser). No omitempty: an explicit
	// "off" must round-trip to disk, otherwise it would silently revert to the
	// default on next launch.
	DNSLeakProtection bool `json:"dns_leak_protection"`
	// KillSwitch (TUN, only meaningful with DNSLeakProtection on): Vair's own
	// block of everything that tries to leave OUTSIDE the tunnel, so nothing
	// falls back to the physical NIC (tunnelblock_windows.go; the helper's
	// nftables table on Linux). DNS is DNSLeakProtection's, not this. ON by
	// default. No omitempty — an explicit "off" must round-trip to disk.
	KillSwitch bool `json:"kill_switch"`
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
	// HttpAuth is the HTTP-proxy counterpart of SocksAuth: when ON, the local
	// HTTP proxy requires HttpUser/HttpPass (Basic auth). It applies to BOTH the
	// proxy-mode HTTP listener AND the TUN "share proxy over LAN" HTTP inbound.
	// OFF by default (the HTTP proxy stays open, as before).
	HttpAuth bool   `json:"http_auth,omitempty"`
	HttpUser string `json:"http_user,omitempty"`
	HttpPass string `json:"http_pass,omitempty"`
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
	// IPv6Mode decides what happens to IPv6 while a connection is up. Three
	// answers, because there are only three: keep it off the machine, carry it
	// through the tunnel, or let it go around. Empty = ipv6Blocked, which is
	// where users effectively already were — the DNS block asked for A records
	// only, so v6 rarely happened; what it could not stop was an application
	// with its own resolver, a literal address, or DNS leak protection turned
	// off, and those are the leaks this closes.
	IPv6Mode string `json:"ipv6_mode,omitempty"`
	// DNS server overrides. Empty falls back to the built-in default
	// for the slot (see dnsServerOr* helpers).
	BootstrapDNS string `json:"bootstrap_dns,omitempty"`
	// BootstrapStrict forbids the fallback to the system resolver when the
	// bootstrap server does not answer: the connection fails instead, and the
	// VPN server's name never reaches the provider's resolver.
	BootstrapStrict bool   `json:"bootstrap_strict,omitempty"` // plain UDP, IP-only
	DirectDNS       string `json:"direct_dns,omitempty"`       // for bypass traffic
	RemoteDNS       string `json:"remote_dns,omitempty"`       // through proxy
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

	// ReconnectOnStart brings back the connection that was up when the program
	// last closed. Off by default: starting a program is not the same as asking
	// it to send your traffic somewhere. It is also what makes the persistent
	// kill switch hold across a close — see core/restore.go.
	ReconnectOnStart bool `json:"reconnect_on_start,omitempty"`
	// LastSessionActive is whether a connection was up the last time anything
	// was recorded. Set on connect, cleared by an explicit disconnect, and
	// deliberately left alone by shutdown — which is what tells a start that
	// there is something to come back to.
	LastSessionActive bool `json:"last_session_active,omitempty"`
	// AutoPingRefresh re-pings the candidate pool after a config-list refresh
	// (auto-refresh / manual / startup) so failover can rank the fresh list by
	// real delay. Default ON; turn off to avoid the extra test traffic. No
	// omitempty: a default-true bool must persist an explicit false.
	AutoPingRefresh bool `json:"auto_ping_refresh"`
	// The switch tolerance: how much better a candidate has to be before AUTO
	// leaves a working connection, and how long it must stay before it may leave
	// at all. Without them AUTO flaps between servers that differ by noise.
	// Absent means the built-in default (20 ms / 15 % / 60 s); zero is a value.
	// They were plain numbers with omitempty, so a 0 typed into the panel was
	// never written, and "no wait" quietly became sixty seconds.
	AutoSwitchToleranceMs *int `json:"auto_switch_tolerance_ms,omitempty"`
	AutoSwitchSpeedPct    *int `json:"auto_switch_speed_pct,omitempty"`
	AutoMinDwellSec       *int `json:"auto_min_dwell_sec,omitempty"`
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
	// AutoRankStable ranks candidates on their measured history — typical delay
	// plus a charge for jitter and for failed probes — instead of on the last
	// measurement alone. Off by default: it changes which server AUTO picks, and
	// a change of that kind should be asked for rather than arrive with an
	// update. Needs a few measurements per server before it does anything.
	AutoRankStable bool `json:"auto_rank_stable,omitempty"`
	// AutoRankMode is what AUTO ranks candidates by, and it replaces the two
	// booleans above — which were never independent: speed decided and history
	// only broke ties, so two switches offered four states that meant three
	// things, one of them silently unreachable.
	//
	//	"delay"         the last measured ping (the original behaviour)
	//	"speed"         the last measured download speed
	//	"history"       typical delay, charged for jitter and for failed probes
	//	"speed_history" the speed a server can be relied on: typical less spread
	//
	// Empty means "derive it from the old pair", which autoRankMode does. The old
	// fields are still written, so a downgrade keeps the setting.
	AutoRankMode string `json:"auto_rank_mode,omitempty"`
	// AutoPreferLast makes AUTO try the last connected config first when it
	// connects from nothing — at start, when switched on, after a disconnect —
	// as long as that config is a candidate and passed its latest ping. Off by
	// default since 2.7.0: the first connection goes to the top of the ranking,
	// the row the panel's candidate list shows first. It used to be the only
	// behaviour, and a server the ranking had long since left behind kept being
	// chosen just for having been used. Failover always follows the ranking.
	AutoPreferLast bool `json:"auto_prefer_last,omitempty"`
	// LastConnectedMode records the mode ("proxy"/"tun") of the most
	// recent connection, so AutoMode="" (remember last) can reuse it.
	LastConnectedMode string `json:"last_connected_mode,omitempty"`
	// SelectedMode is the proxy/tun pill on the main window — the mode the user
	// is looking at, whether or not they have connected in it yet.
	//
	// It exists because "remember last" used to mean LastConnectedMode alone,
	// and the two can disagree for as long as someone flips the pill without
	// connecting: the window said proxy, the AUTO panel said proxy, and AUTO
	// brought servers up in TUN because that was the mode of a session from an
	// hour ago. The pill is what the user believes is selected, so that is what
	// "remember last" now reads first.
	SelectedMode string `json:"selected_mode,omitempty"`

	// ── TLS fragmentation (DPI evasion) ──────────────────────────────
	// TLSFragment, when on, routes the xray outbound's connection to its server
	// through a local "freedom" outbound that splits the TLS ClientHello into
	// pieces, so a stateful DPI can't match the SNI/handshake in one packet.
	// Applies only to the xray engine (TCP-family TLS protocols), in proxy and
	// hybrid-TUN alike (xray makes the real TLS dial in both). Off by default.
	// Length/Interval are xray's fragment ranges ("min-max" or a single number);
	// empty falls back to the defaults below.
	TLSFragment         bool   `json:"tls_fragment,omitempty"`
	TLSFragmentLength   string `json:"tls_fragment_length,omitempty"`
	TLSFragmentInterval string `json:"tls_fragment_interval,omitempty"`

	// TLSFingerprint is the uTLS ClientHello fingerprint xray mimics for TLS /
	// Reality nodes (chrome, firefox, safari, ios, edge, random, randomized).
	// A config's own fp= always wins; this is the fallback when it has none.
	// Without it xray uses Go's TLS stack, which DPI can fingerprint — so the
	// default is "chrome", the one most traffic looks like. "" → chrome.
	TLSFingerprint string `json:"tls_fingerprint,omitempty"`

	// AutostartEnabled launches Vair at Windows logon (HKCU Run key, started
	// minimized via the --autostart flag). Off by default. Reconciled with the
	// registry whenever settings are saved (see applyAutostart).
	AutostartEnabled bool `json:"autostart_enabled,omitempty"`

	// DeepLinkEnabled registers the vair:// URL scheme so clicking a
	// vair://import/<sub|config> link opens Vair and imports it. ON by default;
	// no omitempty so an explicit "off" round-trips to disk. Reconciled with the
	// registry on every settings save (see registerDeepLink).
	DeepLinkEnabled bool `json:"deep_link_enabled"`

	// LogToFile mirrors the Logs panel to a rotating file in the runtime dir
	// (buffered, rotated at 5 MiB, one old file kept). OFF by default: the
	// engines' access lines record which hosts were visited, so this is an
	// opt-in debugging aid, not a default.
	LogToFile bool `json:"log_to_file,omitempty"`
}

const (
	defaultPingConcurrency  = 10
	defaultSpeedConcurrency = 5
	// Each test runs an engine process of its own (about 18 MB): a high value
	// costs processor and memory, and the description says so. The ceilings
	// are the user's to choose within these bounds.
	maxPingConcurrency  = 200
	maxSpeedConcurrency = 100
)

// currentPingConcurrency returns the user's chosen ping concurrency, clamped
// to [1, maxPingConcurrency]. Used by runPingAll instead of a const so the
// setting is honored without a restart.
// currentProxyPorts returns the local HTTP/SOCKS proxy listen ports, falling back
// to the built-in defaults for unset/invalid values. The two are forced distinct
// and never collide with the web-UI port; on a conflict the offending one reverts
// to its default. Read on every connect, so a change applies on the next one.
func currentProxyPorts() (httpPort, socksPort int) {
	settingsMu.RLock()
	hp, sp := appSettings.HttpPort, appSettings.SocksPort
	settingsMu.RUnlock()
	// Not the remote-access port either: that listener is up from the start,
	// and a proxy on the same port could not bind.
	remote := RemotePortSetting()
	valid := func(p int) bool { return p >= 1 && p <= 65535 && p != webPort && p != remote }
	if (hp != 0 && !valid(hp)) || (sp != 0 && !valid(sp)) || (hp != 0 && hp == sp) {
		vlog("warning", "proxy ports %d/%d cannot be used (out of range, equal, or taken by remote access) — using %d/%d", hp, sp, connHTTPPort, connSOCKSPort)
	}
	if !valid(hp) {
		hp = connHTTPPort
	}
	if !valid(sp) {
		sp = connSOCKSPort
	}
	if hp == sp { // can't share one port
		hp, sp = connHTTPPort, connSOCKSPort
	}
	return hp, sp
}

// proxyBindHost is the address the HTTP/SOCKS proxy listeners bind to, from the
// ProxyBind setting (with the legacy ProxyAllowLAN bool migrated in). An unset
// value or an unparseable custom address falls back to 127.0.0.1 (local only).
// Read on every connect, so a change applies on the next one.
func proxyBindHost() string {
	settingsMu.RLock()
	b := strings.TrimSpace(appSettings.ProxyBind)
	lan := appSettings.ProxyAllowLAN
	settingsMu.RUnlock()
	if b == "" {
		if lan { // migrate old "allow LAN" toggle
			return "0.0.0.0"
		}
		return "127.0.0.1"
	}
	if net.ParseIP(b) == nil { // custom address must be a valid IP
		return "127.0.0.1"
	}
	return b
}

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

// validTestURL accepts what the tests can request: an http or https address
// with a host. Anything else failed every configuration it was used for — a
// missing scheme marked a whole tab "failed", where "Delete failed" would have
// removed working configurations — or, when it did not parse at all, stopped
// the program in the middle of PING ALL.
func validTestURL(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// sanitizeTestURLs keeps a settings save from storing a test address that
// cannot be requested: such a value is not saved and the one before it stays.
// Blank means the default; the fallback also takes its "__none" switch.
func sanitizeTestURLs(s *AppSettings, cur AppSettings) {
	fix := func(v *string, old string) {
		t := strings.TrimSpace(*v)
		switch {
		case t == "", t == "__none" && v == &s.SpeedTestURLFallback:
			*v = t
		case validTestURL(t):
			*v = t
		default:
			*v = old
		}
	}
	fix(&s.PingTestURL, cur.PingTestURL)
	fix(&s.SpeedTestURL, cur.SpeedTestURL)
	fix(&s.SpeedTestURLFallback, cur.SpeedTestURLFallback)
	fix(&s.CheckIPURL, cur.CheckIPURL)
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

// speedFallbackOn reports whether an HTTP status from the primary speed URL
// sends the test to the fallback, by the SpeedFallbackOn list.
func speedFallbackOn(status int) bool {
	if status <= 0 {
		return false
	}
	settingsMu.RLock()
	spec := appSettings.SpeedFallbackOn
	settingsMu.RUnlock()
	match, ok := parseStatusSet(spec)
	if !ok {
		return status == 429
	}
	return match(status)
}

// parseStatusSet reads a list of HTTP statuses: codes ("429"), ranges
// ("500-599") and classes ("5xx"), separated by commas, semicolons or spaces.
// Tokens that are none of these are skipped; ok is false when nothing usable
// is left, and the caller falls back to its default.
func parseStatusSet(spec string) (match func(int) bool, ok bool) {
	type span struct{ lo, hi int }
	var spans []span
	for _, tok := range strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ';' || unicode.IsSpace(r)
	}) {
		tok = strings.ToLower(strings.TrimSpace(tok))
		switch {
		case len(tok) == 3 && tok[1:] == "xx" && tok[0] >= '1' && tok[0] <= '5':
			base := int(tok[0]-'0') * 100
			spans = append(spans, span{base, base + 99})
		case strings.Contains(tok, "-"):
			a, b, _ := strings.Cut(tok, "-")
			lo, e1 := strconv.Atoi(a)
			hi, e2 := strconv.Atoi(b)
			if e1 == nil && e2 == nil && lo >= 100 && hi <= 599 && lo <= hi {
				spans = append(spans, span{lo, hi})
			}
		default:
			if n, err := strconv.Atoi(tok); err == nil && n >= 100 && n <= 599 {
				spans = append(spans, span{n, n})
			}
		}
	}
	if len(spans) == 0 {
		return nil, false
	}
	return func(code int) bool {
		for _, s := range spans {
			if code >= s.lo && code <= s.hi {
				return true
			}
		}
		return false
	}, true
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
	v, stats := appSettings.VerboseLogs, appSettings.RuleStats
	settingsMu.RUnlock()
	// Rule statistics need debug: "router: match[3] domain_suffix=… => route(…)"
	// is the only line where sing-box names the rule it matched, and it is not
	// printed at info.
	if stats {
		return "debug"
	}
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
func recordLastConnected(raw string, chain []string, mode ConnMode) {
	if raw == "" {
		return
	}
	settingsMu.Lock()
	appSettings.LastConnectedRaw = raw
	appSettings.LastConnectedChain = nil
	if len(chain) > 1 {
		appSettings.LastConnectedChain = append([]string(nil), chain...)
	}
	if mode != "" {
		appSettings.LastConnectedMode = string(mode)
	}
	// Written on every successful connect, not only when the configuration
	// changed: a start after a crash reads this to know there was a session to
	// bring back.
	appSettings.LastSessionActive = true
	settingsMu.Unlock()
	saveSettings()
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
// default (4 s). Raising this is the fix for "slow to connect but works"
// configs: a server with a long handshake is alive, and a tight warm-up here
// marks it "timeout".
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
	return effectiveMTU(m, ipv6Mode())
}

// effectiveMTU is the MTU the tunnel is given for a setting and an IPv6 mode.
//
// IPv6 needs 1280 on the link. With an IPv6 address on the tunnel — the
// default, "blocked" included — Windows refuses a smaller MTU and the tunnel
// does not start at all ("set ipv6 options: The parameter is incorrect"),
// which read like a broken engine rather than a setting.
func effectiveMTU(m int, v6mode string) int {
	if m < 576 || m > 9000 {
		return 9000
	}
	if m < 1280 && v6mode != ipv6Direct {
		return 1280
	}
	return m
}

// tunStack returns the TCP/IP stack for the tunnel inbound, defaulting to the
// one every platform behaves the same on. Anything unrecognised (an old file, a
// hand-edited settings.json) falls back to that default rather than handing the
// engine a word it will refuse to start on.
func tunStack() string {
	settingsMu.RLock()
	v := appSettings.TUNStack
	settingsMu.RUnlock()
	switch v {
	case "system", "mixed":
		return v
	}
	return "gvisor"
}

// RemoteConfig returns whether LAN remote control is enabled and its token.
func RemoteConfig() (enabled bool, token string) {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.RemoteEnabled, appSettings.RemoteToken
}

// broadcastSettings tells every window that the settings changed.
//
// There is more than one window: the detached AUTO panel is a second copy of
// the frontend, and it read the settings once, at startup. Switching the
// language (or the theme, or the hint density) in the main window left it as it
// was — most visibly the language, which simply stayed as it had been.
func broadcastSettings(s AppSettings) {
	state.broadcast(SSEEvent{Type: "settings_update", Payload: s})
}

// EnsureRemoteToken returns the remote-access token, generating and persisting a
// fresh random one the first time (so enabling remote access always has a
// secret to gate on). Idempotent.
func EnsureRemoteToken() string {
	settingsMu.Lock()
	if appSettings.RemoteToken == "" {
		appSettings.RemoteToken = randomHex(16)
	}
	tok := appSettings.RemoteToken
	settingsMu.Unlock()
	saveSettings()
	return tok
}

// RemotePortSetting returns the user-configured remote port (0 = use default).
func RemotePortSetting() int {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	if p := appSettings.RemotePort; p > 0 && p < 65536 {
		return p
	}
	return 0
}

// RegenerateRemoteToken replaces the remote-access token with a fresh random
// one (invalidating every existing link/QR/cookie) and returns it.
func RegenerateRemoteToken() string {
	settingsMu.Lock()
	appSettings.RemoteToken = randomHex(16)
	tok := appSettings.RemoteToken
	settingsMu.Unlock()
	saveSettings()
	return tok
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

func tunShareProxyEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.TunShareProxy
}

func noSystemProxyEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.NoSystemProxy
}

// persistentKillSwitchEnabled reports whether the firewall-level block is
// wanted. Meaningless without the kill-switch itself, so it follows it.
// proxyKillSwitchEnabled reports whether proxy mode should be fenced in.
func proxyKillSwitchEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.ProxyKillSwitch
}

func persistentKillSwitchEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.KillSwitchPersistent && appSettings.KillSwitch && appSettings.DNSLeakProtection
}

// The switch tolerance, with the defaults the numbers used to be hard-coded to.
func autoSwitchToleranceMs() int64 {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	if v := appSettings.AutoSwitchToleranceMs; v != nil && *v >= 0 {
		return int64(*v)
	}
	return 20
}

func autoSwitchSpeedFactor() float64 {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	if v := appSettings.AutoSwitchSpeedPct; v != nil && *v >= 0 {
		return 1 + float64(*v)/100
	}
	return 1.15
}

func autoMinDwell() time.Duration {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	if v := appSettings.AutoMinDwellSec; v != nil && *v >= 0 {
		return time.Duration(*v) * time.Second
	}
	return 60 * time.Second
}

// sanitizeAutoSettings keeps the AUTO numbers inside the ranges the panel
// shows. The panel's own limits were only on the arrows: a typed 1000 was saved
// as a 1000-second health check against a stated maximum of 600.
func sanitizeAutoSettings(s *AppSettings) {
	clamp := func(v, lo, hi int) int {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	if s.AutoHealthSec != 0 {
		s.AutoHealthSec = clamp(s.AutoHealthSec, 5, 600)
	}
	if s.AutoFailThreshold != 0 {
		s.AutoFailThreshold = clamp(s.AutoFailThreshold, 1, 10)
	}
	s.AutoMaxLatencyMs = clamp(s.AutoMaxLatencyMs, 0, 10000)
	for _, p := range []struct {
		v      **int
		lo, hi int
	}{{&s.AutoSwitchToleranceMs, 0, 5000}, {&s.AutoSwitchSpeedPct, 0, 500}, {&s.AutoMinDwellSec, 0, 3600}} {
		if *p.v != nil {
			n := clamp(**p.v, p.lo, p.hi)
			*p.v = &n
		}
	}
}

func killSwitchEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	// A block of all traffic that lets DNS leak is not a kill-switch: it
	// needs DNSLeakProtection. The UI greys this out in that case but we
	// also defend in code.
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
	case "only_blocked", "only_blocked_cn", "only_selected", "proxy_all":
		return m
	case "bypass_ru", "bypass_countries":
		// "bypass_ru" is the pre-2.1 name — same mode with the country set
		// hard-wired to Russia (see bypassCountries).
		return "bypass_countries"
	}
	if ruDirect {
		return "bypass_countries"
	}
	return "proxy_all"
}

// bypassCountryOrder is the canonical order countries appear in generated
// configs (deterministic output) and in the Settings UI.
var bypassCountryOrder = []string{"ru", "cn", "ir", "kz"}

// bypassCountries returns the effective country set for the bypass-countries
// routing mode: the stored list filtered to known codes in canonical order. The
// legacy "bypass_ru" mode (and the pre-mode RuSitesDirect bool) map to Russia,
// so upgraders keep their exact behaviour. An empty list is legitimate —
// nothing goes direct (behaves like proxy_all) until a country is picked.
func bypassCountries() []string {
	settingsMu.RLock()
	mode := appSettings.RoutingMode
	ruDirect := appSettings.RuSitesDirect
	list := append([]string(nil), appSettings.BypassCountries...)
	settingsMu.RUnlock()
	if mode == "bypass_ru" || (mode == "" && ruDirect) {
		return []string{"ru"}
	}
	set := map[string]bool{}
	for _, c := range list {
		set[strings.ToLower(strings.TrimSpace(c))] = true
	}
	var out []string
	for _, cc := range bypassCountryOrder {
		if set[cc] {
			out = append(out, cc)
		}
	}
	return out
}

// effectiveProxyDomains returns the manual "through VPN" domain list, or nil when
// the list is toggled off. Mirror of the DirectDomains handling.
func effectiveProxyDomains() []string {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	if appSettings.ProxyDomainsDisabled {
		return nil
	}
	return trimList(appSettings.ProxyDomains)
}

// effectiveDirectDomains returns the manual "without VPN" rule list, or nil when
// the list is toggled off.
func effectiveDirectDomains() []string {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	if appSettings.DirectDomainsDisabled {
		return nil
	}
	return trimList(appSettings.DirectDomains)
}

// Default xray TLS-fragment ranges. "tlshello" packets are split into chunks
// of `length` bytes with `interval` ms between them. These are the values in
// common use — conservative enough not to break throughput while still
// defeating one-packet SNI matching.
const (
	defaultTLSFragmentLength   = "100-200"
	defaultTLSFragmentInterval = "10-20"
)

// tlsFragmentEnabled reports whether TLS ClientHello fragmentation is on.
func tlsFragmentEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.TLSFragment
}

// The bounds of the two fragment settings.
//
// A length of 0 stops xray before it starts ("failed to build outbound config
// with tag fragment"), and a length past the size of a ClientHello (about
// 1.7 KB) cuts nothing: the handshake goes out whole, exactly as with the
// switch off, and nothing says so. 1000 still leaves two pieces. An interval is
// paid once per piece on every handshake — a dozen pieces at a second each
// would be twelve seconds per connection — so it stops at one second.
const (
	fragLengthMin, fragLengthMax     = 1, 1000
	fragIntervalMin, fragIntervalMax = 0, 1000
)

// normalizeFragmentSpec checks one of the fragment settings — a number, or a
// range "a-b" — against its bounds, and returns it the way the engine takes it:
// the low end first, a range of one number as that number. ok is false for
// anything else.
func normalizeFragmentSpec(s string, lo, hi int) (string, bool) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, "-")
	if len(parts) > 2 {
		return "", false
	}
	var nums []int
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || len(p) > 6 {
			return "", false
		}
		v := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				return "", false
			}
			v = v*10 + int(c-'0')
		}
		if v < lo || v > hi {
			return "", false
		}
		nums = append(nums, v)
	}
	if len(nums) == 2 && nums[0] > nums[1] {
		nums[0], nums[1] = nums[1], nums[0]
	}
	if len(nums) == 1 || nums[0] == nums[1] {
		return strconv.Itoa(nums[0]), true
	}
	return strconv.Itoa(nums[0]) + "-" + strconv.Itoa(nums[1]), true
}

// fragmentUpper is the high end of a normalized spec.
func fragmentUpper(spec string) int {
	if i := strings.IndexByte(spec, '-'); i >= 0 {
		spec = spec[i+1:]
	}
	v, _ := strconv.Atoi(spec)
	return v
}

// sanitizeFragmentSettings keeps a settings save from storing a fragment value
// the engine would refuse or ignore: such a value is not saved, the one before
// it stays, and the window says so under the field. Blank means the default.
func sanitizeFragmentSettings(s *AppSettings, cur AppSettings) {
	fix := func(v *string, old string, lo, hi int) {
		if strings.TrimSpace(*v) == "" {
			*v = ""
			return
		}
		if n, ok := normalizeFragmentSpec(*v, lo, hi); ok {
			*v = n
		} else {
			*v = old
		}
	}
	fix(&s.TLSFragmentLength, cur.TLSFragmentLength, fragLengthMin, fragLengthMax)
	fix(&s.TLSFragmentInterval, cur.TLSFragmentInterval, fragIntervalMin, fragIntervalMax)
}

// currentTLSFragmentParams returns the validated length/interval ranges, falling
// back to the defaults when unset or malformed.
func currentTLSFragmentParams() (length, interval string) {
	settingsMu.RLock()
	l := appSettings.TLSFragmentLength
	iv := appSettings.TLSFragmentInterval
	settingsMu.RUnlock()
	var ok bool
	if l, ok = normalizeFragmentSpec(l, fragLengthMin, fragLengthMax); !ok {
		l = defaultTLSFragmentLength
	}
	if iv, ok = normalizeFragmentSpec(iv, fragIntervalMin, fragIntervalMax); !ok {
		iv = defaultTLSFragmentInterval
	}
	return l, iv
}

// applySingboxFragment is applyXrayFragment for an outbound that runs on
// sing-box. sing-box has no length or interval: it cuts the ClientHello inside
// the server name, into separate TLS records sent as separate packets, and
// waits between them. The wait is the high end of the interval setting —
// without one it waits 500 ms per handshake on Windows, where it cannot read
// the moment the first piece is acknowledged.
//
// Only an outbound that dials its server itself: a hop reached through
// another one (detour) is inside the tunnel, where nobody inspects it.
func applySingboxFragment(out map[string]interface{}) {
	if out == nil || !tlsFragmentEnabled() {
		return
	}
	switch out["type"] {
	case "vless", "trojan", "vmess", "http":
	default:
		return // QUIC (Hysteria2, TUIC) has no TCP handshake to cut
	}
	if _, chained := out["detour"]; chained {
		return
	}
	tls, _ := out["tls"].(map[string]interface{})
	if tls == nil || tls["enabled"] != true {
		return
	}
	_, interval := currentTLSFragmentParams()
	delay := fragmentUpper(interval)
	if delay < 1 {
		delay = 1 // 0 would read as "unset", i.e. 500 ms
	}
	tls["fragment"] = true
	tls["record_fragment"] = true
	tls["fragment_fallback_delay"] = strconv.Itoa(delay) + "ms"
}

// validTLSFingerprints are the uTLS fingerprints xray accepts. Kept as a set so
// a stale/garbage setting can't be injected into the generated config.
var validTLSFingerprints = map[string]bool{
	"chrome": true, "firefox": true, "safari": true, "ios": true, "android": true,
	"edge": true, "360": true, "qq": true, "random": true, "randomized": true,
}

// currentTLSFingerprint returns the configured uTLS fingerprint, defaulting to
// "chrome" when unset or invalid.
func currentTLSFingerprint() string {
	settingsMu.RLock()
	fp := strings.TrimSpace(appSettings.TLSFingerprint)
	settingsMu.RUnlock()
	if !validTLSFingerprints[fp] {
		return "chrome"
	}
	return fp
}

// blocklistURL returns the trimmed custom blocklist URL ("" = none).
// blocklistURLs returns the effective "through VPN (URL)" source list: nil when
// toggled off; otherwise the trimmed BlocklistURLs, falling back to the legacy
// single BlocklistURL while the user hasn't migrated it.
func blocklistURLs() []string {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	if appSettings.BlocklistURLsDisabled {
		return nil
	}
	out := trimList(appSettings.BlocklistURLs)
	if len(out) == 0 {
		if u := strings.TrimSpace(appSettings.BlocklistURL); u != "" {
			out = []string{u}
		}
	}
	return out
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

// proxyHttpCreds returns the username/password the user-facing HTTP proxy should
// require, mirroring proxySocksCreds. Returns ("","") when HTTP auth is off — the
// listener then accepts connections without credentials (the historical default).
// When on, it uses the user's credentials, falling back to the per-launch random
// ones if a field was left blank. Applies to both the proxy-mode HTTP listener
// and the TUN "share proxy over LAN" HTTP inbound.
func proxyHttpCreds() (user, pass string) {
	settingsMu.RLock()
	on := appSettings.HttpAuth
	u := strings.TrimSpace(appSettings.HttpUser)
	p := strings.TrimSpace(appSettings.HttpPass)
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
	var bad []string
	for k, v := range appSettings.StaticHosts {
		k = strings.TrimSpace(strings.ToLower(k))
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		// An address that is not one is left out. It used to go into the
		// engine's DNS module as it was, and one such line stopped every TUN
		// connection with an error about dns.servers[5].predefined.
		if net.ParseIP(v) == nil {
			bad = append(bad, k+" "+v)
			continue
		}
		out[k] = v
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		warnBadStaticHosts(strings.Join(bad, ", "))
	}
	return out
}

// warnBadStaticHosts names the lines left out, once for each set of them —
// the snapshot is taken several times for one connection.
var lastBadStaticHosts atomic.Value

func warnBadStaticHosts(lines string) {
	if prev, _ := lastBadStaticHosts.Load().(string); prev == lines {
		return
	}
	lastBadStaticHosts.Store(lines)
	vlog("warning", "static hosts left out — the address is not an IP address: %s", lines)
}

// Defaults: DNS leak protection ON, FakeIP OFF (FakeIPDisabled=true) — picked
// for safer out-of-the-box TUN behaviour (DNS through the tunnel, real DoH
// rather than FakeIP for app compatibility).
var appSettings = AppSettings{SourcesEnabled: true, DNSLeakProtection: true, KillSwitch: true, FakeIPDisabled: true, AutoHealthSec: 15, AutoFailThreshold: 2, AutoSwitch: true, AutoPingRefresh: true, AutoMaxLatencyMs: 300, DeepLinkEnabled: true, HideHints: true}

var settingsMu sync.RWMutex

func settingsFilePath() string {
	return dataPath("settings.json")
}

// settingsRev is the current revision of the settings file. A new default
// reaches only the files that do not mention the setting; a change that has to
// reach the files that do is made once by migrateSettings, for a file below
// the revision the change belongs to, and the file is stamped so it is never
// made again.
const settingsRev = 1

// migrateSettings brings settings written by an older version up to the
// current revision, and reports whether it changed anything.
//
// Revision 1 (2.7.0): setting descriptions hidden for everyone. Hidden is the
// default since 2.7.0, but a file that already said "shown" — every one written
// by a 2.7.0 test build, where the switch is stored either way — kept them
// shown. Once only: someone who turns them back on afterwards keeps them on.
func migrateSettings(s *AppSettings) bool {
	if s.SettingsRev >= settingsRev {
		return false
	}
	if s.SettingsRev < 1 {
		s.HideHints = true
	}
	s.SettingsRev = settingsRev
	return true
}

func loadSettings() {
	path := settingsFilePath()
	// A few tries before giving up on the file. The one reader that can meet
	// it half-replaced is a start that overlaps the previous copy still
	// shutting down and saving on its way out; a moment later it is whole.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			// First start: the defaults are the settings, and already the
			// current revision — there is nothing older to bring up to date.
			settingsMu.Lock()
			appSettings.SettingsRev = settingsRev
			settingsMu.Unlock()
			return
		}
		if err == nil {
			s, derr := decodeSettings(data)
			if derr == nil {
				migrated := migrateSettings(&s)
				settingsMu.Lock()
				appSettings = s
				settingsMu.Unlock()
				// The backup is the file as it was read; the migrated
				// settings are written over the file itself.
				keepSettingsBackup(data)
				if migrated {
					saveSettings()
				}
				return
			}
			lastErr = derr
		} else {
			lastErr = err
		}
		time.Sleep(150 * time.Millisecond)
	}

	// The file is there and cannot be read. Said out loud, because the failure
	// is otherwise invisible: every setting at its default, the dialog showing
	// those defaults as if they were the user's, and the next save writing them
	// over the file. The copy taken at the last start that read cleanly is the
	// way back; the unreadable file is set aside rather than overwritten, so
	// nothing is lost even when there is no copy.
	if bak, err := os.ReadFile(path + ".bak"); err == nil {
		if s, derr := decodeSettings(bak); derr == nil {
			setAsideBrokenSettings(path)
			migrateSettings(&s)
			settingsMu.Lock()
			appSettings = s
			settingsMu.Unlock()
			saveSettings()
			vlog("warning", "settings.json could not be read (%v) — restored from settings.json.bak, the copy taken at the last start that read it; the unreadable file is kept as settings.json.broken", lastErr)
			return
		}
	}
	setAsideBrokenSettings(path)
	vlog("error", "settings.json could not be read (%v) and there is no usable backup — running on defaults; the unreadable file is kept as settings.json.broken", lastErr)
}

// decodeSettings reads a settings file over a COPY of the current settings and
// hands it back only when the whole file decoded. encoding/json fills fields
// as it goes, so decoding straight into appSettings left a file that failed
// halfway applied halfway: some of the user's values, some defaults.
func decodeSettings(data []byte) (AppSettings, error) {
	data = trimBOM(data)
	if len(bytes.TrimSpace(data)) == 0 {
		return AppSettings{}, errEmptySettings
	}
	settingsMu.RLock()
	s := appSettings
	settingsMu.RUnlock()
	if err := json.Unmarshal(data, &s); err != nil {
		return AppSettings{}, err
	}
	return s, nil
}

var errEmptySettings = errors.New("the file is empty")

// keepSettingsBackup keeps settings.json.bak equal to the last file that was
// read cleanly at a start. Written only when it differs, so an ordinary start
// costs one read and no write.
func keepSettingsBackup(data []byte) {
	bak := settingsFilePath() + ".bak"
	if old, err := os.ReadFile(bak); err == nil && bytes.Equal(old, data) {
		return
	}
	if err := writeFileDurable(bak, data, 0o600); err != nil {
		vlog("warning", "settings.json.bak could not be written: %v", err)
	}
}

// setAsideBrokenSettings moves an unreadable settings.json out of the way so
// the next save cannot overwrite it. One copy is kept; a later failure
// replaces it.
func setAsideBrokenSettings(path string) {
	if _, err := os.Stat(path); err != nil {
		return
	}
	os.Remove(path + ".broken")     //nolint:errcheck
	os.Rename(path, path+".broken") //nolint:errcheck
}

// settingsSaveMu puts saves in a line. Each one takes a snapshot and writes
// it; two at once could finish in the other order and leave the OLDER
// snapshot on disk. In line, the last save writes the latest state.
var settingsSaveMu sync.Mutex

func saveSettings() {
	settingsSaveMu.Lock()
	defer settingsSaveMu.Unlock()
	settingsMu.RLock()
	data, _ := json.MarshalIndent(appSettings, "", "  ")
	settingsMu.RUnlock()
	os.MkdirAll(tabsDir(), 0o755) //nolint:errcheck
	// 0600: this file carries the web-interface token and the local proxy
	// passwords. The Chmod is for the copy an older version already wrote —
	// the rename keeps the new file's mode, but a failed save leaves the old.
	if err := writeFileDurable(settingsFilePath(), data, 0o600); err != nil {
		vlog("error", "settings.json could not be saved: %v", err)
	}
	os.Chmod(settingsFilePath(), 0o600) //nolint:errcheck
}

// currentCheckIPURL is the address the "check IP" button asks, and
// checkIPHost the hostname inside it.
//
// The hostname matters beyond the request: in the bypass routing modes that
// send most traffic direct, this one host is forced through the tunnel, or the
// button would cheerfully report the address the VPN is not using. Change the
// service and that rule has to follow it, which is why the host is derived from
// the URL rather than written down twice.
func currentCheckIPURL() string {
	settingsMu.RLock()
	u := strings.TrimSpace(appSettings.CheckIPURL)
	settingsMu.RUnlock()
	if u == "" {
		return checkIPURLDefault
	}
	return u
}

func checkIPHost() string {
	u := currentCheckIPURL()
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	if h, _, err := net.SplitHostPort(u); err == nil {
		u = h
	}
	if u == "" {
		return checkExitHost
	}
	return u
}

// Ranking modes for AUTO. Strings rather than an enum: they go through JSON to
// the settings file and back, and a number there would be unreadable and
// unstable the first time the order changed.
const (
	rankByDelay        = "delay"
	rankBySpeedLast    = "speed"
	rankByPingHistory  = "history"
	rankBySpeedHistory = "speed_history"
)

// autoRankMode returns the ranking mode, deriving it from the pre-2.6 pair when
// it has not been set.
//
// Both switches on becomes "speed", because that is what the program actually
// did: speed decided and history only separated equal speeds. Migrating it to
// "history" would silently change which server AUTO picks for everyone who had
// both on, which is precisely the kind of surprise an update must not deliver.
func autoRankMode() string {
	settingsMu.RLock()
	m := strings.TrimSpace(appSettings.AutoRankMode)
	bySpeed, stable := appSettings.AutoRankBySpeed, appSettings.AutoRankStable
	settingsMu.RUnlock()
	switch m {
	case rankByDelay, rankBySpeedLast, rankByPingHistory, rankBySpeedHistory:
		return m
	}
	switch {
	case bySpeed:
		return rankBySpeedLast
	case stable:
		return rankByPingHistory
	}
	return rankByDelay
}

// rankModeUsesSpeed reports whether the active ranking mode needs a speed
// measurement — the answer to "should this background sweep run the speed test
// as well as the ping".
func rankModeUsesSpeed() bool {
	m := autoRankMode()
	return m == rankBySpeedLast || m == rankBySpeedHistory
}

// syncAutoRankLegacy keeps the two old booleans agreeing with the mode, so a
// downgrade to a build that only knows them lands on the nearest equivalent
// rather than on the default.
func syncAutoRankLegacy(s *AppSettings) {
	switch strings.TrimSpace(s.AutoRankMode) {
	case rankBySpeedLast, rankBySpeedHistory:
		s.AutoRankBySpeed, s.AutoRankStable = true, false
	case rankByPingHistory:
		s.AutoRankBySpeed, s.AutoRankStable = false, true
	case rankByDelay:
		s.AutoRankBySpeed, s.AutoRankStable = false, false
	}
}

// The three answers to "what about IPv6", and what each one means in the
// engine's configuration:
//
//	ipv6Blocked  the tunnel claims ::/0 and refuses it, DNS answers A only.
//	             Nothing v6 leaves the machine. Refusing rather than dropping
//	             is deliberate: a browser races v4 and v6 (happy eyeballs) and
//	             falls back at once on a refusal, after a timeout on silence.
//	ipv6Tunnel   the tunnel carries ::/0, DNS prefers A but AAAA is allowed.
//	             Works as far as the server does — see the check after connect.
//	ipv6Direct   the tunnel takes IPv4 only and v6 goes out the physical
//	             interface, with the machine's real address on it. What the
//	             program did before this setting existed.
const (
	ipv6Blocked = "blocked"
	ipv6Tunnel  = "tunnel"
	ipv6Direct  = "direct"
)

// ipv6TunAddress is the tunnel's own v6 address. Unique-local (fc00::/7), so
// it cannot collide with anything routable, and a /126 for the same reason the
// v4 side uses a /30: the interface needs an address, not a network.
// tunAddress4 is the tunnel interface's own address, and tunSubnet4 the /30 it
// lives in. The peer address in that /30 — 172.19.0.2 — belongs to nobody: it
// exists because an interface needs a subnet, and no program answers on it.
//
// Which matters because Windows hands it to applications as the gateway. A
// program looking for its router (NAT-PMP on 5351, and anything else that asks
// the gateway a question) then sends packets that come back through the tunnel
// they were sent into, forever. See the reject rule in singboxRoutingRules.
const (
	tunAddress4    = "172.19.0.1/30"
	ipv6TunAddress = "fdfe:dcba:9876::1/126"
)

// tunSelfAddresses lists the addresses inside the tunnel's own prefixes that
// anything can actually be sent to: the interface itself and the peer address
// the platform derives from it (address + 1) and hands out as the gateway.
//
// Each on its own, never the prefix they sit in. The prefix would be shorter to
// write and wrong to use: on Linux sing-tun registers that same peer address
// with systemd-resolved as the resolver for "~.", so refusing the whole /30
// refuses every name lookup on the machine along with the loop.
func tunSelfAddresses() []string {
	out := make([]string, 0, 4)
	for _, prefix := range []string{tunAddress4, ipv6TunAddress} {
		ip, _, err := net.ParseCIDR(prefix)
		if err != nil {
			continue
		}
		bits := "/32"
		if ip.To4() == nil {
			bits = "/128"
		}
		out = append(out, ip.String()+bits)
		if peer := nextIP(ip); peer != nil {
			out = append(out, peer.String()+bits)
		}
	}
	return out
}

// nextIP returns the address one above ip, or nil if it wrapped.
func nextIP(ip net.IP) net.IP {
	out := make(net.IP, len(ip))
	copy(out, ip)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			return out
		}
	}
	return nil
}

// ipv6FakeRange is the FakeIP pool for AAAA answers, from the same unique-local
// space. Only used when v6 is in the tunnel: a fake address the tunnel does not
// route is an address nothing can connect to.
const ipv6FakeRange = "fc00::/18"

func ipv6Mode() string {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	switch appSettings.IPv6Mode {
	case ipv6Tunnel, ipv6Direct:
		return appSettings.IPv6Mode
	default:
		return ipv6Blocked
	}
}

// ipv6InTunnel reports whether the TUN interface should take an IPv6 address
// and the routes that come with it. True for both blocked and tunnel: blocking
// is done by holding the route and refusing, not by leaving it to the physical
// interface.
func ipv6InTunnel() bool { return ipv6Mode() != ipv6Direct }

// dnsAddressStrategy is what the DNS block answers with. ipv4_only is not used
// for the bootstrap server — the address of a v6-only VPN server has to be
// resolvable whatever this setting says, or the tunnel cannot be built at all.
func dnsAddressStrategy() string {
	if ipv6Mode() == ipv6Tunnel {
		return "prefer_ipv4"
	}
	return "ipv4_only"
}

// xrayDirectDomainStrategy is how xray resolves the destinations it sends out
// directly. In proxy mode this is the only half of the IPv6 choice that has
// anywhere to apply: there is no tunnel to hold a route, and traffic that
// ignores the system proxy was never ours to route. UseIPv4 asks for A records
// only; UseIP takes whatever the resolver gives.
func xrayDirectDomainStrategy() string {
	if ipv6Mode() == ipv6Blocked {
		return "UseIPv4"
	}
	return "UseIP"
}
