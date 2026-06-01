package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

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
//
//	One server, "dns-local" of type "local". sing-box defers to the
//	OS resolver. DNS leaks are possible because port 53 packets the
//	OS resolver sends out are not forced into TUN (strict_route is
//	false in this mode). This is what 1.4.0 shipped with — kept for
//	compatibility.
//
// protected mode (leakProtect=true): three real DNS servers plus an
// optional FakeIP server used via rule (never as `final`).
//
//   - dns-bootstrap (plain UDP, no detour) — sing-box's internal
//     fallback for routing-time resolutions. No `detour: direct`
//     here: in sing-box 1.13+ that errors out as
//     "detour to an empty direct outbound makes no sense". Without
//     a detour, sing-box dials the IP through OS routing, with
//     its own internal carve-out from the strict_route WFP filter
//     that blocks UDP/53 for other processes.
//   - dns-direct (plain UDP, no detour) — for bypass traffic
//     (geosite-ru, custom direct domains). Same shape as bootstrap.
//   - dns-remote (DoH over proxy) — for tunnelled traffic when
//     FakeIP is disabled, AND always as the `final` server so any
//     non-A/AAAA query (PTR, MX, SRV…) gets a real answer through
//     the tunnel. FakeIP cannot be `final` — sing-box 1.13+ rejects
//     that with "default server cannot be fakeip".
//   - dns-fakeip (FakeIP, optional) — used only via a rule that
//     matches `query_type` A/AAAA. Returns 198.18.0.0/15 pseudo-
//     addresses immediately; real resolution happens once the
//     connection is dialled through the proxy.
//
// Rule ordering (matters):
//  1. Static hosts (predefined map) — highest priority.
//  2. RU bypass + user-direct-domains → dns-direct.
//  3. If FakeIP enabled: A/AAAA → dns-fakeip.
//  4. Everything else falls through to `final = dns-remote`.
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
		// Track the warm-up budget: the priming request's TLS/Reality handshake
		// goes through this transport, so a tight handshake timeout would defeat
		// a raised warm-up setting for "slow to connect" servers.
		MaxIdleConnsPerHost: 4, TLSHandshakeTimeout: currentWarmupTimeout(),
		DialContext: (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
	}
}

// autoProbeURL is the health-probe endpoint for the auto-supervisor: a tiny
// 204 with no body, on a foreign host that is NOT in any direct-routing list
// (RU geosite / DirectDomains). The "foreign + not-direct" property is critical
// for TUN mode — a probe to a direct-routed host could bypass the tunnel and
// falsely report "healthy". HTTPS is used deliberately: the plain-HTTP
// connectivitycheck host routes/throttles unpredictably (observed 300–800ms),
// while www.gstatic.com over TLS tracks the real path latency (~100–200ms) and
// reuses the kept-alive connection so the handshake cost is paid only once.
const autoProbeURL = "https://www.gstatic.com/generate_204"

// probeLiveTunnel makes one small request through the *live* connection to
// decide whether the current config still works. Unlike handlePingConnected
// (which spins a separate test engine via withEngine), this exercises the real
// running tunnel:
//   - proxy mode: through the live HTTP proxy port (127.0.0.1:HTTPPort);
//   - TUN mode:   a direct request, which the system routes through the TUN.
//
// Returns (alive, rtt): alive is true on a clean response (status < 500); rtt
// is the round-trip of the probe request (for the latency-budget check).
// Conservative: when there's no transport we can build (e.g. proxy mode with no
// HTTP port) it returns (true, 0) so a switch is never triggered on a probe we
// couldn't even attempt. rtt is 0 (treated as under budget) for every
// non-measured path.
func probeLiveTunnel(cm *connManager) (bool, time.Duration) {
	cs := cm.snap()
	if cs.Status != ConnConnected {
		return true, 0
	}
	var tr *http.Transport
	if cs.Mode == ModeProxy {
		if cs.HTTPPort <= 0 {
			return true, 0
		}
		tr = makeSharedTransport(cs.HTTPPort) // keep-alive ON
	} else {
		// TUN: all system traffic routes through the tunnel, so a direct
		// request measures the live connection. Keep-alive ON so the warm-up
		// request below amortises the TLS handshake before we measure.
		tr = &http.Transport{
			Proxy:               nil,
			DisableKeepAlives:   false,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        2,
			TLSHandshakeTimeout: warmupTimeout,
			DialContext:         (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		}
	}
	defer tr.CloseIdleConnections()
	// 4s per attempt: long enough for a slow-but-alive hop, short enough that a
	// dead config (warm-up + retry) is caught well inside a 15s health interval.
	client := &http.Client{
		Transport:     tr,
		Timeout:       4 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// One small helper: GET the probe URL, drain+close, return (statusOK, rtt, err).
	do := func() (bool, time.Duration, error) {
		req, err := http.NewRequest("GET", autoProbeURL, nil)
		if err != nil {
			return false, 0, err
		}
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return false, 0, err
		}
		rtt := time.Since(start)
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return resp.StatusCode < 500, rtt, nil
	}
	// Warm-up: establishes the (TLS) connection so the measured request reuses it
	// via keep-alive — the measured rtt then reflects the network round-trip, not
	// the handshake, matching what the manual ping reports. If the warm-up fails,
	// retry once: two cold failures in a row mean the link genuinely can't pass
	// traffic, so we don't waste a third attempt on the measurement.
	if _, _, err := do(); err != nil {
		if _, _, err2 := do(); err2 != nil {
			return false, 0
		}
	}
	// Measured request over the now-warm connection, with one retry so a single
	// transient blip (TCP reset, momentary loss) isn't counted as a failure.
	ok, rtt, err := do()
	if err != nil {
		if ok2, rtt2, err2 := do(); err2 == nil {
			return ok2, rtt2
		}
		return false, 0
	}
	return ok, rtt
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
