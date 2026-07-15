package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ─────────────────────────── protocol kinds ─────────────────────

// NodeKind identifies the proxy protocol of a single node. The kind drives
// engine selection (xray for TCP-family, sing-box for UDP-family) and the
// per-protocol parser / outbound builder dispatch.
type NodeKind string

const (
	KindVLESS     NodeKind = "vless"
	KindVMess     NodeKind = "vmess"
	KindTrojan    NodeKind = "trojan"
	KindSS        NodeKind = "ss" // includes SS2022 (distinguished by method)
	KindHysteria2 NodeKind = "hysteria2"
	KindTUIC      NodeKind = "tuic"
)

// Node is the engine-agnostic descriptor of a parsed proxy URL. Exactly one
// of the per-protocol pointers is non-nil; that pointer holds the protocol
// specific parameters. Common fields (Host/Port/Name/Network/Security) are
// lifted up for the UI table and routing logic.
type Node struct {
	Kind     NodeKind
	Raw      string
	Name     string
	Host     string
	Port     int
	Network  string // UI display: tcp/ws/grpc/quic/...
	Security string // UI display: none/tls/reality/<ss method>

	Vless     *VlessParams
	Trojan    *TrojanParams
	Vmess     *VmessParams
	SS        *SSParams
	Hysteria2 *Hysteria2Params
	TUIC      *TuicParams
}

// nodeSchemes lists the URL schemes Vair recognises as proxy node URLs. The
// list is consulted by the fetch / parse path to decide whether a line is a
// candidate node. Order doesn't matter for prefix matching, but for the
// "earliest match in a line" scan it doesn't either — we pick whichever
// scheme appears earliest.
var nodeSchemes = []string{
	"vless://",
	"vmess://",
	"trojan://",
	"ss://",
	"hysteria2://",
	"hy2://",
	"tuic://",
}

// looseHostPort parses "host:port" tolerating trailing junk after the port
// (e.g. "1.2.3.4:443?" or "1.2.3.4:443@label🇬🇧") that real-world feeds glue on.
// The port is the leading digit run; the host is stripped of brackets/spaces.
func looseHostPort(s string) (host string, port int, ok bool) {
	s = strings.TrimSpace(s)
	h, portStr, err := net.SplitHostPort(s)
	if err != nil {
		i := strings.LastIndexByte(s, ':')
		if i < 0 {
			return "", 0, false
		}
		h, portStr = s[:i], s[i+1:]
	}
	j := 0
	for j < len(portStr) && portStr[j] >= '0' && portStr[j] <= '9' {
		j++
	}
	if j == 0 {
		return "", 0, false
	}
	p, _ := strconv.Atoi(portStr[:j])
	h = strings.TrimSpace(strings.Trim(h, "[]"))
	if h == "" || p <= 0 || p > 65535 {
		return "", 0, false
	}
	return h, p, true
}

// splitFragment splits a node URL into the part before the '#name' fragment and
// the decoded name. Strip the fragment BEFORE url.Parse so a malformed name
// (e.g. a bad percent-escape like "%2v" some feeds produce) can't fail the parse.
func splitFragment(raw string) (base, name string) {
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		base = raw[:i]
		if dec, err := url.QueryUnescape(raw[i+1:]); err == nil {
			name = dec
		} else {
			name = raw[i+1:]
		}
		return base, name
	}
	return raw, ""
}

// base64Prefix returns the leading run of base64 characters (standard + URL-safe
// alphabets and padding). Used to drop trailing junk glued after a base64 body
// (e.g. a vmess link with "[name]@channel" appended after the JSON blob).
func base64Prefix(s string) string {
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '=' {
			// Padding only appears at the end; consume it and stop, so junk glued
			// right after the padding (e.g. "==----必进") is dropped.
			for i < len(s) && s[i] == '=' {
				i++
			}
			break
		}
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '+' || c == '/' || c == '-' || c == '_' {
			i++
			continue
		}
		break
	}
	return s[:i]
}

// splitConfigURLs extracts every proxy URL from a line, even when several are
// concatenated without whitespace (e.g. "ss://...#namevmess://...ss://..."), by
// splitting at each scheme boundary. "://" never appears inside a base64 vmess
// payload (':' isn't a base64 char), so a vmess body is never sliced. Trailing
// whitespace-delimited junk is trimmed from each segment.
func splitConfigURLs(line string) []string {
	var starts []int
	for i := 0; i < len(line); {
		matched := false
		for _, s := range nodeSchemes {
			if strings.HasPrefix(line[i:], s) {
				starts = append(starts, i)
				// Skip past the scheme so a sub-scheme isn't matched inside it
				// ("ss://" is a substring of "vmess://").
				i += len(s)
				matched = true
				break
			}
		}
		if !matched {
			i++
		}
	}
	if len(starts) == 0 {
		return nil
	}
	out := make([]string, 0, len(starts))
	for k, st := range starts {
		end := len(line)
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		seg := line[st:end]
		// Split off the '#name' fragment first. The name may contain spaces (e.g.
		// "vless://…#By Ebra Sha 🧪"), but some feeds put a stray space BETWEEN the
		// config and its '#' ("…path=%2Fcmh #☁️ Anycast"). Only whitespace-separated
		// junk in the part BEFORE the fragment is dropped; the fragment is then
		// re-attached, so the name survives even with that stray space (previously
		// the fragment was cut off with the junk → the name fell back to the host).
		frag := ""
		if h := strings.IndexByte(seg, '#'); h >= 0 {
			frag = seg[h:] // "#name…" (keep, including the '#')
			seg = seg[:h]  // part before the fragment
		}
		if sp := strings.IndexAny(seg, " \t\r\n"); sp >= 0 {
			seg = seg[:sp]
		}
		if seg = strings.TrimSpace(seg); seg != "" {
			out = append(out, seg+frag)
		}
	}
	return out
}

// looksLikeNodeURL reports whether the given (already trimmed) line starts
// with any of the recognised proxy URL schemes.
func looksLikeNodeURL(line string) bool {
	for _, s := range nodeSchemes {
		if strings.HasPrefix(line, s) {
			return true
		}
	}
	return false
}

// schemeProtocol returns the protocol kind implied by a URL's scheme prefix
// (e.g. "trojan://…" → "trojan"), independent of whether the rest of the URL
// parses. Used to label configs that FAIL to parse: the scheme is still known,
// so a broken trojan:// link is tagged "trojan" instead of falling back to the
// UI's default ("vless"). Returns "" when no recognised scheme matches.
func schemeProtocol(raw string) string {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "vless://"):
		return string(KindVLESS)
	case strings.HasPrefix(raw, "vmess://"):
		return string(KindVMess)
	case strings.HasPrefix(raw, "trojan://"):
		return string(KindTrojan)
	case strings.HasPrefix(raw, "ss://"):
		return string(KindSS)
	case strings.HasPrefix(raw, "hysteria2://"), strings.HasPrefix(raw, "hy2://"):
		return string(KindHysteria2)
	case strings.HasPrefix(raw, "tuic://"):
		return string(KindTUIC)
	}
	return ""
}

// parseNode is the dispatcher: it picks the right per-protocol parser based
// on the URL scheme and returns a unified *Node. Stage 0 only implements
// VLESS; other schemes return a "not implemented" error. Later stages fill
// in the other parsers.
func parseNode(raw string) (*Node, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "vless://"):
		p, err := parseVless(raw)
		if err != nil {
			return nil, err
		}
		return &Node{
			Kind:     KindVLESS,
			Raw:      raw,
			Name:     p.Name,
			Host:     p.Host,
			Port:     p.Port,
			Network:  p.Network,
			Security: p.Security,
			Vless:    p,
		}, nil
	case strings.HasPrefix(raw, "vmess://"):
		p, err := parseVmess(raw)
		if err != nil {
			return nil, err
		}
		return &Node{
			Kind:     KindVMess,
			Raw:      raw,
			Name:     p.Name,
			Host:     p.Host,
			Port:     p.Port,
			Network:  p.Network,
			Security: p.Security,
			Vmess:    p,
		}, nil
	case strings.HasPrefix(raw, "trojan://"):
		p, err := parseTrojan(raw)
		if err != nil {
			return nil, err
		}
		return &Node{
			Kind:     KindTrojan,
			Raw:      raw,
			Name:     p.Name,
			Host:     p.Host,
			Port:     p.Port,
			Network:  p.Network,
			Security: p.Security,
			Trojan:   p,
		}, nil
	case strings.HasPrefix(raw, "ss://"):
		p, err := parseShadowsocks(raw)
		if err != nil {
			return nil, err
		}
		// UI display: keep Kind="ss" for both legacy & SS2022 (the same xray
		// outbound handles both); the SS2022 distinction lives on SSParams.
		// `Security` shows the cipher method — more informative than "none"
		// for a protocol that *is* its cipher choice.
		return &Node{
			Kind:     KindSS,
			Raw:      raw,
			Name:     p.Name,
			Host:     p.Host,
			Port:     p.Port,
			Network:  "tcp",
			Security: p.Method,
			SS:       p,
		}, nil
	case strings.HasPrefix(raw, "hysteria2://"), strings.HasPrefix(raw, "hy2://"):
		p, err := parseHysteria2(raw)
		if err != nil {
			return nil, err
		}
		return &Node{
			Kind:      KindHysteria2,
			Raw:       raw,
			Name:      p.Name,
			Host:      p.Host,
			Port:      p.Port,
			Network:   p.Network,
			Security:  p.Security,
			Hysteria2: p,
		}, nil
	case strings.HasPrefix(raw, "tuic://"):
		p, err := parseTuic(raw)
		if err != nil {
			return nil, err
		}
		return &Node{
			Kind:     KindTUIC,
			Raw:      raw,
			Name:     p.Name,
			Host:     p.Host,
			Port:     p.Port,
			Network:  p.Network,
			Security: p.Security,
			TUIC:     p,
		}, nil
	}
	return nil, fmt.Errorf("unsupported URL scheme")
}

// ─────────────────────────── VLESS params + parser ──────────────

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

func parseVless(raw string) (*VlessParams, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "vless://") {
		return nil, fmt.Errorf("not a vless URL")
	}
	base, name := splitFragment(raw)
	u, err := url.Parse(base)
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
	p.Name = name
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

// ─────────────────────────── Trojan params + parser ─────────────

// TrojanParams holds parsed parameters of a `trojan://` URL.
//
// URL grammar (de-facto, no RFC): trojan://password@host:port?security=tls&sni=...&type=ws&path=...&host=...&alpn=...&fp=...&allowInsecure=0#name
//   - The password sits in the URL userinfo (no username component).
//   - `security` defaults to `tls` (Trojan is TLS-only by design — running it
//     over plain TCP defeats its purpose), unlike VLESS which defaults to `none`.
//   - `type` (network) defaults to `tcp`. WS/gRPC/h2 variants exist in xray
//     and reuse the same streamSettings as VLESS.
//   - REALITY is technically supported by xray for Trojan but extremely rare
//     in the wild; we still parse `pbk`/`sid`/`spx` for symmetry.
type TrojanParams struct {
	Raw           string
	Password      string
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
	PublicKey     string
	ShortID       string
	SpiderX       string
}

func parseTrojan(raw string) (*TrojanParams, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "trojan://") {
		return nil, fmt.Errorf("not a trojan URL")
	}

	// Hand-parse the URL instead of relying on net/url. Real-world trojan
	// passwords routinely contain characters that break url.Parse: '#'
	// (taken as fragment delimiter), '<', '[', "'", '?' etc. Example:
	//   trojan://8r<[9'l6hAO#8ZQi@host:port?...#@Name
	// url.Parse on that string mis-locates the fragment and the userinfo,
	// and the whole config gets rejected. We carve up the string by hand
	// using the known grammar:  PASSWORD @ HOST [: PORT] [? QUERY] [# NAME]
	body := raw[len("trojan://"):]

	// 1) Fragment (name). If a '?' exists, the real fragment starts at the
	//    first '#' AFTER the '?', so '#' embedded in the password is safe.
	//    Without a '?', fall back to the last '#' as the fragment delimiter.
	var fragment string
	if qIdx := strings.Index(body, "?"); qIdx >= 0 {
		if h := strings.Index(body[qIdx:], "#"); h >= 0 {
			fragment = body[qIdx+h+1:]
			body = body[:qIdx+h]
		}
	} else if h := strings.LastIndex(body, "#"); h >= 0 {
		fragment = body[h+1:]
		body = body[:h]
	}

	// 2) Query string.
	var rawQuery string
	if qIdx := strings.Index(body, "?"); qIdx >= 0 {
		rawQuery = body[qIdx+1:]
		body = body[:qIdx]
	}

	// 3) PASSWORD @ HOST[:PORT]. The host cannot contain '@', so the LAST
	//    '@' bounds the password — any '@' inside the password is fine.
	atIdx := strings.LastIndex(body, "@")
	if atIdx < 0 {
		return nil, fmt.Errorf("missing '@' in trojan URL")
	}
	password := body[:atIdx]
	hostport := body[atIdx+1:]
	if password == "" {
		return nil, fmt.Errorf("empty password")
	}
	// Decode percent-escapes if the URL was RFC-encoded; otherwise keep raw.
	if dec, err := url.PathUnescape(password); err == nil {
		password = dec
	}

	// Some real-world trojan URLs include a stray "/" or even a real path
	// after host:port (e.g. trojan://pw@host:2086/?type=ws...). The path
	// segment isn't meaningful for trojan transport — strip it before
	// splitting host:port, otherwise we'd hand net.SplitHostPort a port
	// like "2086/" and strconv.Atoi would fail with "invalid syntax".
	if slashIdx := strings.Index(hostport, "/"); slashIdx >= 0 {
		hostport = hostport[:slashIdx]
	}

	// 4) host:port (with IPv6-in-brackets support via net.SplitHostPort).
	host, portStr, hpErr := net.SplitHostPort(hostport)
	if hpErr != nil {
		host = hostport
		portStr = ""
	}
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}
	port := 443
	if portStr != "" {
		n, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, fmt.Errorf("bad port: %w", err)
		}
		port = n
	}

	// 5) Query parameters. ParseQuery tolerates un-encoded values.
	q, _ := url.ParseQuery(rawQuery)
	get := func(k string) string { return q.Get(k) }

	// 6) Visible name from the fragment (percent-decoded if applicable).
	name := fragment
	if dec, err := url.PathUnescape(name); err == nil {
		name = dec
	}

	p := &TrojanParams{
		Raw:      raw,
		Password: password,
		Host:     host,
		Port:     port,
		Name:     name,
	}
	if p.Name == "" {
		p.Name = fmt.Sprintf("%s:%d", p.Host, p.Port)
	}
	p.Network = get("type")
	if p.Network == "" {
		p.Network = "tcp"
	}
	// Trojan defaults to TLS (its whole reason for existing). Only override
	// if the URL explicitly says otherwise.
	p.Security = get("security")
	if p.Security == "" {
		p.Security = "tls"
	}
	p.Path, p.Host2, p.ServiceName = get("path"), get("host"), get("serviceName")
	p.SNI, p.ALPN, p.Fingerprint = get("sni"), get("alpn"), get("fp")
	p.AllowInsecure = get("allowInsecure") == "1"
	p.PublicKey, p.ShortID, p.SpiderX = get("pbk"), get("sid"), get("spx")
	return p, nil
}

// ─────────────────────────── VMess params + parser ──────────────
//
// VMess has two URL forms in the wild:
//
//   1. v2rayN base64-of-JSON: `vmess://<base64 of {"v":2,"ps":...,"add":...}>`
//      This is what 99% of subscription feeds emit. JSON keys are short
//      single-letter / lowercase strings. `port` and `aid` may be sent as
//      either int or string depending on the producer, so we type-switch.
//
//   2. Standard-URI variant: `vmess://uuid@host:port?type=...&host=...#name`
//      Less common but supported by some Android clients. Mirrors the VLESS
//      URI shape exactly. We fall through to this form when base64 decoding
//      fails OR the decoded blob doesn't look like JSON.
//
// VmessParams.AlterID defaults to 0 (AEAD mode); ancient VMess deployments
// used non-zero alterId for the legacy MD5-based auth which is deprecated.
// xray still accepts it via the `users[].alterId` field.

type VmessParams struct {
	Raw         string
	UUID        string
	Host        string
	Port        int
	Name        string
	Network     string
	Security    string // transport-layer security: none / tls
	Path        string
	Host2       string
	ServiceName string
	SNI         string
	ALPN        string
	Fingerprint string
	AlterID     int
	Scy         string // user encryption: auto / none / aes-128-gcm / chacha20-poly1305
	HeaderType  string // tcp obfs: "none" / "http"
}

// vmessLink mirrors the v2rayN JSON layout. `Port` and `Aid` are `any` so
// we can accept either int or string values without unmarshalling errors —
// v2rayN-export-as-clash sometimes writes "443", v2rayN itself writes 443.
type vmessLink struct {
	V    any    `json:"v"`
	Ps   string `json:"ps"`
	Add  string `json:"add"`
	Port any    `json:"port"`
	ID   string `json:"id"`
	Aid  any    `json:"aid"`
	Scy  string `json:"scy"`
	Net  string `json:"net"`
	Type string `json:"type"`
	Host string `json:"host"`
	Path string `json:"path"`
	// TLS is `any`: some feeds write a bool (`"tls": false`) or number instead of
	// the usual string ("tls" / "" / "none"). A string field would fail the whole
	// JSON unmarshal and the config would be wrongly rejected ("empty uuid").
	TLS  any    `json:"tls"`
	SNI  string `json:"sni"`
	ALPN string `json:"alpn"`
	FP   string `json:"fp"`
}

func parseVmess(raw string) (*VmessParams, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "vmess://") {
		return nil, fmt.Errorf("not a vmess URL")
	}
	// Strip scheme + optional fragment (some producers append #name after
	// the base64 blob; that fragment is decorative — the inner JSON's "ps"
	// is authoritative for v2rayN-style links).
	body := nodeBody(raw[len("vmess://"):])
	body = strings.TrimSpace(body)
	frag := ""
	if i := strings.LastIndex(raw, "#"); i >= 0 {
		// Decode the fragment if present (overrides ps when set).
		if dec, err := url.QueryUnescape(raw[i+1:]); err == nil {
			frag = dec
		}
	}

	// Try base64-of-JSON first (v2rayN form). Match fetchURL's tolerant
	// 4-variant decoder so URL-safe alphabets and missing padding both work.
	if p, err := tryParseVmessBase64(raw, body, frag); err == nil {
		return p, nil
	}
	// Fall back to standard-URI form.
	return tryParseVmessStandardURI(raw, frag)
}

func tryParseVmessBase64(raw, body, fragName string) (*VmessParams, error) {
	// Drop trailing junk glued after the base64 body (e.g. "[name]@channel"),
	// which would otherwise fail the decode and fall through to the URI parser.
	body = base64Prefix(strings.TrimSpace(body))
	var decoded []byte
	var err error
	// Try in this order: std → std-no-pad → URL → URL-no-pad. Same set as fetchURL.
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
	} {
		if decoded, err = dec(body); err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("vmess base64: %w", err)
	}
	var l vmessLink
	if err := json.Unmarshal(decoded, &l); err != nil {
		return nil, fmt.Errorf("vmess json: %w", err)
	}
	if l.ID == "" || l.Add == "" {
		return nil, fmt.Errorf("vmess: missing id/add")
	}
	p := &VmessParams{
		Raw:         raw,
		UUID:        l.ID,
		Host:        l.Add,
		Network:     strings.ToLower(strings.TrimSpace(l.Net)),
		Path:        l.Path,
		Host2:       l.Host,
		ServiceName: l.Path, // grpc producers cram serviceName into "path"
		SNI:         l.SNI,
		ALPN:        l.ALPN,
		Fingerprint: l.FP,
		Scy:         strings.ToLower(strings.TrimSpace(l.Scy)),
		HeaderType:  strings.ToLower(strings.TrimSpace(l.Type)),
	}
	if p.Network == "" {
		p.Network = "tcp"
	}
	if p.Scy == "" {
		p.Scy = "auto"
	}
	p.Port = anyToInt(l.Port, 443)
	p.AlterID = anyToInt(l.Aid, 0)
	// security: TLS on/off. Producers use "tls" / "" / "none" / "xtls" etc.
	tls := strings.ToLower(strings.TrimSpace(anyToStr(l.TLS)))
	if tls == "tls" || tls == "reality" || tls == "xtls" {
		p.Security = "tls"
	} else {
		p.Security = "none"
	}
	// Name: explicit fragment beats inline ps (the fragment is the editable
	// display label after a `disambiguateNames` rename, so respect it).
	if fragName != "" {
		p.Name = fragName
	} else if l.Ps != "" {
		p.Name = l.Ps
	} else {
		p.Name = fmt.Sprintf("%s:%d", p.Host, p.Port)
	}
	return p, nil
}

func tryParseVmessStandardURI(raw, fragName string) (*VmessParams, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("vmess url.Parse: %w", err)
	}
	p := &VmessParams{Raw: raw}
	p.UUID = u.User.Username()
	if p.UUID == "" {
		return nil, fmt.Errorf("vmess: empty uuid")
	}
	p.Host = u.Hostname()
	if p.Host == "" {
		return nil, fmt.Errorf("vmess: empty host")
	}
	if port := u.Port(); port == "" {
		p.Port = 443
	} else if p.Port, err = strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("vmess bad port: %w", err)
	}
	q := u.Query()
	get := func(k string) string { return q.Get(k) }
	p.Network = strings.ToLower(get("type"))
	if p.Network == "" {
		p.Network = "tcp"
	}
	// VMess URI sometimes uses `security` (xray-style) and sometimes
	// `encryption` (clash-style) — accept either.
	tls := strings.ToLower(get("security"))
	if tls == "" {
		tls = strings.ToLower(get("encryption"))
	}
	if tls == "tls" || tls == "reality" || tls == "xtls" {
		p.Security = "tls"
	} else {
		p.Security = "none"
	}
	p.Path, p.Host2, p.ServiceName = get("path"), get("host"), get("serviceName")
	p.SNI, p.ALPN, p.Fingerprint = get("sni"), get("alpn"), get("fp")
	p.HeaderType = strings.ToLower(get("headerType"))
	p.Scy = strings.ToLower(get("scy"))
	if p.Scy == "" {
		p.Scy = "auto"
	}
	if aid := get("aid"); aid != "" {
		if v, err := strconv.Atoi(aid); err == nil {
			p.AlterID = v
		}
	}
	if fragName != "" {
		p.Name = fragName
	} else if u.Fragment != "" {
		p.Name = u.Fragment
	} else {
		p.Name = fmt.Sprintf("%s:%d", p.Host, p.Port)
	}
	return p, nil
}

// anyToInt extracts an int from a JSON value that could be a float64 (the
// default for JSON numbers in Go) or a string (when the producer over-quoted).
func anyToInt(v any, dflt int) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return dflt
		}
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return dflt
}

// anyToStr coerces a JSON value that should be a string but may arrive as a bool
// or number (some vmess feeds write `"tls": false`). For booleans true → "tls".
func anyToStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "tls"
		}
		return ""
	case float64:
		if x != 0 {
			return "tls"
		}
		return ""
	}
	return ""
}

// ─────────────────────────── Shadowsocks params + parser ───────
//
// Shadowsocks has *four* common URL shapes — historical baggage from a
// decade of clients that each invented their own variant:
//
//   1. SIP002 (the spec):       ss://BASE64URL(method:password)@host:port[/?plugin=...]#name
//   2. SS2022 plain:            ss://method:password@host:port[/?plugin=...]#name
//   3. Legacy whole-base64:     ss://BASE64(method:password@host:port)#name
//      (Old form, predates SIP002 — userinfo+hostport are encoded together.)
//   4. Plugin SIP002:           same as (1) but with a populated ?plugin=
//      query whose value is "plugin-name;opt=val;...".
//
// SS2022 is distinguished from legacy SS purely by the cipher *method*
// prefix: "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm",
// "2022-blake3-chacha20-poly1305". xray-core uses the same outbound stanza
// for both, so internally there's no separate Kind — SSParams.IsSS2022 is
// just a UI flag.
//
// Plugin transports (obfs-local, v2ray-plugin, shadow-tls, …) need an
// external plugin binary that xray-core does not ship and Vair does not
// bundle. We still parse the plugin field so dedup matches the canonical
// SIP002 spelling, but we set UnsupportedPlugin so the UI can warn and
// the connect path can fail-fast rather than spawning xray with a stripped
// config that would silently try plain TCP and get banned by DPI.

type SSParams struct {
	Raw               string
	Method            string
	Password          string
	Host              string
	Port              int
	Name              string
	IsSS2022          bool
	PluginName        string // raw plugin name part of "plugin=name;opts"
	PluginOpts        string // raw opts part (everything after the first ";")
	UnsupportedPlugin bool
}

// parseShadowsocks accepts all four common ss:// URL forms (see SSParams).
//
// Strategy:
//  1. Strip prefix + URL fragment (#name) + query (?plugin=...).
//  2. If body contains '@', it's SIP002 / SS2022-plain. Split at the LAST
//     '@' (password may contain '@'), validate hostport via net.SplitHostPort,
//     then try userinfo as plain "method:password" first; fall back to a
//     base64-decode if the candidate method doesn't look like a method name.
//  3. Otherwise treat the whole body as legacy BASE64(method:password@host:port).
//  4. If a plugin= query is present, flag UnsupportedPlugin.
func parseShadowsocks(raw string) (*SSParams, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "ss://") {
		return nil, fmt.Errorf("not a ss URL")
	}
	body := raw[len("ss://"):]

	// Fragment (#name) — extracted before the query split because some
	// producers put the name after the query, others before. The LAST '#'
	// is authoritative since the body never legitimately contains a '#'.
	name := ""
	if i := strings.LastIndex(body, "#"); i >= 0 {
		if dec, err := url.QueryUnescape(body[i+1:]); err == nil {
			name = dec
		} else {
			name = body[i+1:]
		}
		body = body[:i]
	}

	// Query (?plugin=...&...). SIP002 puts the query *before* the fragment,
	// so this runs second.
	queryStr := ""
	if i := strings.Index(body, "?"); i >= 0 {
		queryStr = body[i+1:]
		body = body[:i]
	}
	// SIP002 sometimes inserts a trailing '/' between hostport and '?'.
	body = strings.TrimSuffix(body, "/")

	p := &SSParams{Raw: raw, Name: name}

	if strings.Contains(body, "@") {
		// SIP002 or SS2022-plain. Try the LAST '@' first (the password may contain
		// '@'); if the part after it isn't a valid host:port, fall back to the
		// FIRST '@' (covers base64 userinfo + a junk "@label" glued after the port).
		idx := strings.LastIndex(body, "@")
		userinfo := body[:idx]
		host, port, ok := looseHostPort(body[idx+1:])
		if !ok {
			if fi := strings.IndexByte(body, '@'); fi >= 0 && fi != idx {
				if h, pt, ok2 := looseHostPort(body[fi+1:]); ok2 {
					userinfo, host, port, ok = body[:fi], h, pt, true
				}
			}
		}
		if !ok {
			return nil, fmt.Errorf("ss: bad host:port")
		}
		p.Host = host
		p.Port = port

		// Userinfo may be percent-encoded by some feeds — notably the ':'
		// between method and password as %3A (e.g. "aes-128-gcm%3Apass").
		// Decode first so the plain "method:password" split below sees the
		// real ':'. Keep the raw form as a fallback in case the producer
		// percent-escaped base64 padding instead.
		userDecoded := userinfo
		if dec, err := url.QueryUnescape(userinfo); err == nil {
			userDecoded = dec
		}
		// Plain first (covers SS2022, SIP002-plain, and any feed that left the
		// method:password readable — possibly %-encoded). Try the decoded form,
		// then the raw, before falling back to base64.
		if m, pw, ok := splitMethodPassword(userDecoded); ok {
			p.Method, p.Password = m, pw
		} else if m, pw, ok := splitMethodPassword(userinfo); ok {
			p.Method, p.Password = m, pw
		} else {
			// Base64(method:password). The spec mandates BASE64URL but real-world
			// feeds sometimes %-escape '+', '/', '=', so try the percent-decoded
			// candidate.
			decoded, err := ssDecodeBase64(userDecoded)
			if err != nil {
				return nil, fmt.Errorf("ss: userinfo neither plain nor base64: %w", err)
			}
			m, pw, ok := splitMethodPassword(decoded)
			if !ok {
				return nil, fmt.Errorf("ss: decoded userinfo missing ':' or bad method")
			}
			p.Method, p.Password = m, pw
		}
	} else {
		// Legacy whole-base64: BASE64(method:password@host:port).
		decoded, err := ssDecodeBase64(body)
		if err != nil {
			return nil, fmt.Errorf("ss: whole-base64 decode failed: %w", err)
		}
		// Password may contain '@', so split on the LAST '@'.
		at := strings.LastIndex(decoded, "@")
		if at < 0 {
			return nil, fmt.Errorf("ss: legacy form missing '@'")
		}
		userinfo := decoded[:at]
		host, port, hpOK := looseHostPort(decoded[at+1:])
		if !hpOK {
			return nil, fmt.Errorf("ss: legacy bad host:port")
		}
		m, pw, ok := splitMethodPassword(userinfo)
		if !ok {
			return nil, fmt.Errorf("ss: legacy userinfo missing ':' or bad method")
		}
		p.Method, p.Password = m, pw
		p.Host = host
		p.Port = port
	}

	if p.Host == "" {
		return nil, fmt.Errorf("ss: empty host")
	}
	if p.Method == "" {
		return nil, fmt.Errorf("ss: empty method")
	}
	p.IsSS2022 = strings.HasPrefix(p.Method, "2022-blake3-")

	// Plugin handling — parse, record, and flag unsupported. We don't reject
	// the URL outright: the row should appear in the table so the user knows
	// the feed contains plugin nodes (and we keep dedup keys stable).
	//
	// SIP002 says ';' inside the plugin value MUST be percent-encoded, but
	// many real-world feeds omit the encoding ("plugin=obfs-local;obfs=tls"
	// instead of "plugin=obfs-local%3Bobfs%3Dtls"). url.ParseQuery returns
	// an error on raw ';' in Go 1.17+, so extract the plugin value manually
	// — scan from "plugin=" up to the next '&' (real query separator).
	if i := strings.Index(queryStr, "plugin="); i >= 0 {
		rest := queryStr[i+len("plugin="):]
		end := strings.Index(rest, "&")
		var rawPlugin string
		if end < 0 {
			rawPlugin = rest
		} else {
			rawPlugin = rest[:end]
		}
		if dec, err := url.QueryUnescape(rawPlugin); err == nil {
			rawPlugin = dec
		}
		if rawPlugin != "" {
			parts := strings.SplitN(rawPlugin, ";", 2)
			p.PluginName = parts[0]
			if len(parts) > 1 {
				p.PluginOpts = parts[1]
			}
			p.UnsupportedPlugin = true
		}
	}

	if p.Name == "" {
		p.Name = fmt.Sprintf("%s:%d", p.Host, p.Port)
	}
	return p, nil
}

// splitMethodPassword splits "method:password" into (method, password, true).
// Method names use only [A-Za-z0-9_-] so they never contain ':' — splitting
// on the first ':' is unambiguous. Returns false if the input lacks a ':',
// or if the candidate method contains characters outside the method-name
// alphabet (a strong signal that the input is actually base64-encoded
// userinfo: '+', '/', '=' are illegal in method names but mandatory in
// std-base64, and '.' / ' ' don't appear in either).
func splitMethodPassword(s string) (method, password string, ok bool) {
	i := strings.Index(s, ":")
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	m := s[:i]
	for _, r := range m {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return "", "", false
		}
	}
	return m, s[i+1:], true
}

// ssDecodeBase64 tolerantly decodes a Shadowsocks base64 string. Different
// generators emit different alphabet/padding combos: std+pad, std-no-pad,
// url-safe+pad, url-safe-no-pad. Try all four; return the first success.
func ssDecodeBase64(s string) (string, error) {
	var (
		out []byte
		err error
	)
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
	} {
		if out, err = dec(s); err == nil {
			return string(out), nil
		}
	}
	return "", err
}

// ─────────────────────────── Hysteria2 params + parser ──────────
//
// Hysteria2 is a QUIC/HTTP-3 based protocol handled by sing-box (xray-core
// has no usable Hysteria2 outbound). De-facto URL grammar:
//
//   hysteria2://auth@host:port?obfs=salamander&obfs-password=...&sni=...
//              &insecure=1&pinSHA256=...&alpn=h3#name
//
// `hy2://` is an accepted alias for the same scheme. The userinfo is the
// auth string (sing-box calls it "password"); it may itself contain a ':'
// so we rejoin user:pass exactly like Trojan. Hysteria2 always runs over
// TLS — there is no plaintext mode — so Security is hard-coded to "tls"
// and Network to "quic" for the UI.

type Hysteria2Params struct {
	Raw          string
	Password     string // auth string (sing-box "password")
	Host         string
	Port         int
	Name         string
	SNI          string
	ALPN         string // comma-separated; defaults to h3 at outbound build time
	Insecure     bool
	ObfsType     string // e.g. "salamander"; empty = no obfuscation
	ObfsPassword string
	PinSHA256    string // certificate pin; parsed for completeness/display
	Network      string // UI display: always "quic"
	Security     string // UI display: always "tls"
}

func parseHysteria2(raw string) (*Hysteria2Params, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "hysteria2://") && !strings.HasPrefix(raw, "hy2://") {
		return nil, fmt.Errorf("not a hysteria2 URL")
	}
	// Canonicalise the hy2:// alias so url.Parse sees one scheme. The scheme
	// string is irrelevant past parsing — we dispatch on Node.Kind.
	if strings.HasPrefix(raw, "hy2://") {
		raw = "hysteria2://" + strings.TrimPrefix(raw, "hy2://")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("url.Parse: %w", err)
	}
	p := &Hysteria2Params{Raw: raw, Network: "quic", Security: "tls"}
	// Auth lives in userinfo. Like Trojan, accept both "auth@host" and
	// "user:pass@host" — rejoin so a literal ':' in the auth survives.
	if u.User != nil {
		if pw, has := u.User.Password(); has && pw != "" {
			p.Password = u.User.Username() + ":" + pw
		} else {
			p.Password = u.User.Username()
		}
	}
	if p.Password == "" {
		return nil, fmt.Errorf("empty auth/password")
	}
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
	p.SNI = get("sni")
	p.ALPN = get("alpn")
	// insecure / allowInsecure, "1" or "true" — all seen in the wild.
	p.Insecure = get("insecure") == "1" || get("insecure") == "true" ||
		get("allowInsecure") == "1" || get("allowInsecure") == "true"
	p.ObfsType = get("obfs")
	p.ObfsPassword = firstNonEmpty(get("obfs-password"), get("obfsParam"))
	p.PinSHA256 = get("pinSHA256")
	return p, nil
}

// ─────────────────────────── TUIC params + parser ───────────────
//
// TUIC v5 is a QUIC-based protocol handled by sing-box (xray-core has no
// TUIC outbound). De-facto URL grammar:
//
//   tuic://uuid:password@host:port?congestion_control=bbr
//          &udp_relay_mode=native&alpn=h3&sni=...&allow_insecure=1#name
//
// Unlike Trojan/Hysteria2 the userinfo is a real `uuid:password` pair, so
// the std-lib splits it for us: u.User.Username() → uuid, Password() →
// password. TUIC always runs over TLS, so Security is hard-coded "tls" and
// Network "quic" for the UI.

type TuicParams struct {
	Raw               string
	UUID              string
	Password          string
	Host              string
	Port              int
	Name              string
	SNI               string
	ALPN              string // comma-separated; sing-box REQUIRES non-empty — defaulted to h3 at build time
	Insecure          bool
	CongestionControl string // e.g. "bbr"; empty = sing-box default (cubic)
	UDPRelayMode      string // "native" | "quic"; empty = sing-box default
	Network           string // UI display: always "quic"
	Security          string // UI display: always "tls"
}

func parseTuic(raw string) (*TuicParams, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "tuic://") {
		return nil, fmt.Errorf("not a tuic URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("url.Parse: %w", err)
	}
	p := &TuicParams{Raw: raw, Network: "quic", Security: "tls"}
	if u.User != nil {
		p.UUID = u.User.Username()
		if pw, has := u.User.Password(); has {
			p.Password = pw
		}
	}
	if p.UUID == "" {
		return nil, fmt.Errorf("empty uuid")
	}
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
	p.SNI = get("sni")
	p.ALPN = get("alpn")
	p.Insecure = get("allow_insecure") == "1" || get("allow_insecure") == "true" ||
		get("insecure") == "1" || get("insecure") == "true" ||
		get("allowInsecure") == "1" || get("allowInsecure") == "true"
	p.CongestionControl = get("congestion_control")
	p.UDPRelayMode = get("udp_relay_mode")
	return p, nil
}

// ─────────────────────────── DNS pre-resolution ─────────────────

// preResolveVPNHost replaces p.Host (a domain) with a resolved IPv4
// address using Go's default resolver. The original hostname is moved
// to p.SNI so TLS still uses the right server name in its handshake
// — without this, certificate validation against an IP would fail.
//
// Why we do this even when DNS leak protection is off:
//   - Eliminates one DNS lookup that would happen inside xray, which
//     is harder to control or surface errors from.
//   - Makes the kill-switch / strict_route case actually startable —
//     when strict_route is on, sing-box's WFP filter would block
//     xray's UDP/53 probe to the OS resolver.
//   - No behaviour change for IP-only servers — pre-resolve is a
//     no-op.
//   - No behaviour change for TLS — SNI keeps the original domain.
//
// Static hosts take precedence over DNS. They're checked first so the
// user can pin a VPN server IP even when DNS resolution is broken.
//
// preResolveHost dispatches by Node.Kind to mutate the right discriminated
// union's Host/SNI fields. Each protocol's params carry their own copies,
// so the dispatcher writes through whichever pointer the parser populated.
func preResolveHost(n *Node) error {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case KindVLESS:
		if n.Vless == nil {
			return nil
		}
		if err := resolveHostIntoSNI(&n.Vless.Host, &n.Vless.SNI); err != nil {
			return err
		}
		n.Host = n.Vless.Host
	case KindTrojan:
		if n.Trojan == nil {
			return nil
		}
		if err := resolveHostIntoSNI(&n.Trojan.Host, &n.Trojan.SNI); err != nil {
			return err
		}
		n.Host = n.Trojan.Host
	case KindVMess:
		if n.Vmess == nil {
			return nil
		}
		if err := resolveHostIntoSNI(&n.Vmess.Host, &n.Vmess.SNI); err != nil {
			return err
		}
		n.Host = n.Vmess.Host
	case KindSS:
		if n.SS == nil {
			return nil
		}
		// Shadowsocks doesn't use TLS so there's no SNI field to preserve —
		// we just resolve the host in place and pass nil for the SNI sink.
		if err := resolveHostIntoSNI(&n.SS.Host, nil); err != nil {
			return err
		}
		n.Host = n.SS.Host
	case KindHysteria2:
		if n.Hysteria2 == nil {
			return nil
		}
		// Hysteria2 is QUIC-over-TLS: carry the original hostname into SNI
		// (sing-box writes it to tls.server_name) so the cert still
		// validates after the host is swapped for a numeric IP.
		if err := resolveHostIntoSNI(&n.Hysteria2.Host, &n.Hysteria2.SNI); err != nil {
			return err
		}
		n.Host = n.Hysteria2.Host
	case KindTUIC:
		if n.TUIC == nil {
			return nil
		}
		// TUIC is QUIC-over-TLS — same SNI carry-over as Hysteria2.
		if err := resolveHostIntoSNI(&n.TUIC.Host, &n.TUIC.SNI); err != nil {
			return err
		}
		n.Host = n.TUIC.Host
	}
	return nil
}

// resolveHostIntoSNI is the protocol-agnostic core of preResolveHost: takes
// pointers to a host and an SNI field, replaces the host in-place with an
// IPv4 literal, and preserves the original hostname in SNI (only if SNI was
// previously empty) so TLS still validates against the right cert.
func resolveHostIntoSNI(host, sni *string) error {
	if host == nil || *host == "" {
		return nil
	}
	if net.ParseIP(*host) != nil {
		// Already an IP literal — nothing to resolve, SNI stays as-is.
		return nil
	}
	orig := *host
	key := strings.ToLower(orig)

	// Static hosts: user override comes first.
	if hosts := staticHostsSnapshot(); hosts != nil {
		if ip, ok := hosts[key]; ok && net.ParseIP(ip) != nil {
			if sni != nil && *sni == "" {
				*sni = orig
			}
			*host = ip
			return nil
		}
	}

	// DNS resolve via Go's default resolver. This uses the OS resolver
	// (and thus the OS DNS settings) and runs before sing-box brings
	// up TUN, so it's always reachable.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", orig)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", orig, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("no IPs returned for %s", orig)
	}
	if sni != nil && *sni == "" {
		*sni = orig
	}
	*host = ips[0].String()
	return nil
}

// ─────────────────────────── URL body / name helpers ────────────

// nodeBody returns the part of a node URL before the `#` fragment — i.e.
// the connection details (uuid@host:port?params, base64 blob for vmess,
// method:password@host:port for ss, …) without the human-readable name.
// Two configs with identical bodies but different names are functionally
// the same server/configuration, which is what dedup should compare on.
//
// Scheme-agnostic: works for vless/vmess/trojan/ss/hysteria2/tuic alike,
// because the `#fragment` convention is preserved by every scheme Vair
// supports.
func nodeBody(raw string) string {
	// Use the LAST `#` since query strings can technically contain a `#`
	// when malformed, and we want everything up to the fragment marker.
	if i := strings.LastIndex(raw, "#"); i >= 0 {
		return raw[:i]
	}
	return raw
}

// setNodeName replaces the URL fragment of a node URL with the given
// (decoded) name, percent-encoding it as needed. Used by disambiguateNames
// so that copying a row puts the disambiguated name on the clipboard
// (otherwise "anycast - 3" reverts to plain "anycast" on paste).
//
// Scheme-agnostic, same caveats as nodeBody. Note: for vmess base64-JSON
// URLs the appended `#name` is honoured by the display layer (which reads
// node.Name), but the inner JSON "ps" field is not rewritten — that's fine
// for Vair's use because we never read the inner "ps" after parsing.
func setNodeName(raw, name string) string {
	encoded := url.PathEscape(name)
	if i := strings.LastIndex(raw, "#"); i >= 0 {
		return raw[:i+1] + encoded
	}
	return raw + "#" + encoded
}

// ─────────────────────────── xray config builders ───────────────
//
// The full xray config is assembled from independent pieces:
//   xrayShell           — log + inbounds (HTTP always, SOCKS if persistent) + sniffing.
//   xrayStreamSettings  — transport (ws/grpc/h2/...) + security (tls/reality).
//                         Reused by every TCP-family outbound (VLESS/VMess/Trojan).
//   xrayOutboundXxx     — protocol-specific outbound stanza.
//   xrayRoutingForProxy — routing rules for persistent (proxy/TUN) sessions,
//                         honours Russian-sites and direct-domains settings.
//   buildXrayConfigForNode — dispatcher that glues all of the above by Node.Kind.
//
// In `httpPort, socksPort`: socksPort > 0 selects "persistent" mode (both
// inbounds + routing rules). socksPort <= 0 is "test" mode (HTTP-only,
// no routing — measurePing/measureSpeed connect directly through HTTP).

// xrayShell returns the per-session config base (log + inbounds + direct +
// block + freedom). The caller appends the proxy outbound and (optionally)
// the routing section. Sniffing is enabled only in persistent mode so that
// the test path stays as minimal as possible.
func xrayShell(httpPort, socksPort int, socksUser, socksPass string) map[string]interface{} {
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
		// SOCKS auth: a non-empty username requires password auth on the local
		// SOCKS listener so other local apps can't use the proxy or probe the VPN
		// server; an empty username means no auth. The internal TUN handoff always
		// passes credentials, so this only affects the user-facing proxy listener.
		socksSettings := map[string]interface{}{"udp": true}
		if socksUser != "" {
			socksSettings["auth"] = "password"
			socksSettings["accounts"] = []interface{}{
				map[string]interface{}{"user": socksUser, "pass": socksPass},
			}
		} else {
			socksSettings["auth"] = "noauth"
		}
		inbounds = append(inbounds, map[string]interface{}{
			"tag": "socks", "listen": "127.0.0.1", "port": socksPort,
			"protocol": "socks",
			"settings": socksSettings,
			"sniffing": sniffing,
		})
	}
	return map[string]interface{}{
		"log":      xrayLogConfig(),
		"inbounds": inbounds,
		// Outbounds are filled in by the dispatcher: [proxy, direct, block].
		"outbounds": []interface{}{
			// placeholder for proxy — dispatcher overwrites index 0
			nil,
			map[string]interface{}{"tag": "direct", "protocol": "freedom",
				"settings": map[string]interface{}{"domainStrategy": "UseIPv4"}},
			map[string]interface{}{"tag": "block", "protocol": "blackhole", "settings": map[string]interface{}{}},
		},
	}
}

// xrayStreamSettings builds the shared streamSettings stanza used by every
// xray TCP-family outbound. All transport-level (network, ws path, grpc
// service name, h2 host, …) and security-level (tls SNI/alpn/fp, reality
// pbk/sid/spx) knobs go here. Parameters are explicit rather than a single
// params-struct so this remains scheme-agnostic — Trojan and VMess will
// pass values from their own param structs.
func xrayStreamSettings(network, security, path, host2, serviceName, sni, alpn, fingerprint string, allowInsecure bool, publicKey, shortID, spiderX string) map[string]interface{} {
	if network == "" {
		network = "tcp"
	}
	if security == "" {
		security = "none"
	}
	stream := map[string]interface{}{"network": network, "security": security}
	switch network {
	case "ws":
		ws := map[string]interface{}{"path": path}
		if host2 != "" {
			ws["headers"] = map[string]interface{}{"Host": host2}
		}
		stream["wsSettings"] = ws
	case "grpc":
		stream["grpcSettings"] = map[string]interface{}{"serviceName": serviceName, "multiMode": false}
	case "h2", "http":
		h2 := map[string]interface{}{"path": path}
		if host2 != "" {
			h2["host"] = []string{host2}
		}
		stream["httpSettings"] = h2
	case "httpupgrade":
		hu := map[string]interface{}{"path": path}
		if host2 != "" {
			hu["host"] = host2
		}
		stream["httpupgradeSettings"] = hu
	case "splithttp", "xhttp":
		// xhttp is the newer name for splithttp in xray-core 1.8+
		sh := map[string]interface{}{"path": path}
		if host2 != "" {
			sh["host"] = host2
		}
		stream["xhttpSettings"] = sh
	}
	// Fall back to the configured uTLS fingerprint (default "chrome") when the
	// config carries no fp=. Without a fingerprint xray uses Go's TLS stack,
	// which DPI can fingerprint; chrome ClientHello camouflage is what
	// v2rayN/Hiddify default to. A config's explicit fp= still wins.
	if fingerprint == "" {
		fingerprint = currentTLSFingerprint()
	}
	switch security {
	case "tls":
		tls := map[string]interface{}{"serverName": sni, "allowInsecure": allowInsecure, "fingerprint": fingerprint}
		if alpn != "" {
			tls["alpn"] = strings.Split(alpn, ",")
		}
		stream["tlsSettings"] = tls
	case "reality":
		stream["realitySettings"] = map[string]interface{}{
			"serverName": sni, "fingerprint": fingerprint,
			"publicKey": publicKey, "shortId": shortID, "spiderX": spiderX,
		}
	}
	return stream
}

// xrayRoutingForProxy returns the routing block used in persistent (proxy /
// hybrid TUN) sessions. Snapshots the settings under lock so we don't race
// against settings mutations mid-build.
func xrayRoutingForProxy() map[string]interface{} {
	mode := routingMode()
	settingsMu.RLock()
	var directDomains []string
	if !appSettings.DirectDomainsDisabled {
		directDomains = make([]string, len(appSettings.DirectDomains))
		copy(directDomains, appSettings.DirectDomains)
	}
	settingsMu.RUnlock()

	rules := []interface{}{
		map[string]interface{}{"type": "field", "ip": []string{"geoip:private"}, "outboundTag": "direct"},
	}
	// addDomains appends a domain rule ("domain:" = suffix match) to the given tag.
	addDomains := func(domains []string, tag string) {
		var suffixes []string
		for _, d := range domains {
			if d = strings.TrimSpace(d); d != "" {
				suffixes = append(suffixes, "domain:"+d)
			}
		}
		if len(suffixes) > 0 {
			rules = append(rules, map[string]interface{}{"type": "field", "domain": suffixes, "outboundTag": tag})
		}
	}
	// Custom "without VPN" domains → direct (all modes; no-op where default is direct).
	addDomains(directDomains, "direct")

	switch mode {
	case "only_blocked":
		// Default direct; only blocked-in-RU resources go through the VPN.
		refreshBlockedRuleSets()
		refreshCustomBlocklist(blocklistURL())
		proxyDomains := effectiveProxyDomains()
		proxyDomains = append(proxyDomains, customBlocklistDomains()...)
		// Force the "check IP" service through the VPN so the button reflects the
		// tunnel exit (it isn't a blocked resource, so it'd go direct otherwise).
		proxyDomains = append(proxyDomains, checkExitHost)
		addDomains(proxyDomains, "proxy")
		rules = append(rules,
			map[string]interface{}{"type": "field", "domain": []string{"ext:geosite-ru-blocked.dat:ru-blocked"}, "outboundTag": "proxy"},
			map[string]interface{}{"type": "field", "ip": []string{"ext:geoip-ru-blocked.dat:ru-blocked"}, "outboundTag": "proxy"},
			map[string]interface{}{"type": "field", "network": "tcp,udp", "outboundTag": "direct"},
		)
	case "bypass_ru":
		// Everything through the VPN except Russian sites.
		rules = append(rules,
			map[string]interface{}{"type": "field", "domain": []string{"geosite:category-ru"}, "outboundTag": "direct"},
			map[string]interface{}{"type": "field", "ip": []string{"geoip:ru"}, "outboundTag": "direct"},
			map[string]interface{}{"type": "field", "network": "tcp,udp", "outboundTag": "proxy"},
		)
	default: // proxy_all
		rules = append(rules,
			map[string]interface{}{"type": "field", "network": "tcp,udp", "outboundTag": "proxy"},
		)
	}
	return map[string]interface{}{
		"domainStrategy": "IPIfNonMatch",
		"rules":          rules,
	}
}

// xrayRoutingAllProxy sends everything to the proxy outbound (private IPs direct
// as a safety net). Used for the hybrid-TUN xray child, where sing-box has
// already decided proxy-vs-direct and the child must not re-split.
func xrayRoutingAllProxy() map[string]interface{} {
	return map[string]interface{}{
		"domainStrategy": "AsIs",
		"rules": []interface{}{
			map[string]interface{}{"type": "field", "ip": []string{"geoip:private"}, "outboundTag": "direct"},
			map[string]interface{}{"type": "field", "network": "tcp,udp", "outboundTag": "proxy"},
		},
	}
}

// xrayOutboundVless builds the xray outbound stanza for a VLESS node.
func xrayOutboundVless(p *VlessParams) map[string]interface{} {
	user := map[string]interface{}{"id": p.UUID, "encryption": "none"}
	if p.Flow != "" {
		user["flow"] = p.Flow
	}
	settings := map[string]interface{}{
		"vnext": []interface{}{map[string]interface{}{
			"address": p.Host, "port": p.Port, "users": []interface{}{user},
		}},
	}
	stream := xrayStreamSettings(
		p.Network, p.Security,
		p.Path, p.Host2, p.ServiceName,
		p.SNI, p.ALPN, p.Fingerprint, p.AllowInsecure,
		p.PublicKey, p.ShortID, p.SpiderX,
	)
	return map[string]interface{}{
		"tag":            "proxy",
		"protocol":       "vless",
		"settings":       settings,
		"streamSettings": stream,
	}
}

// xrayOutboundTrojan builds the xray outbound stanza for a Trojan node.
// xray's trojan outbound uses `servers: [{address, port, password}]` (note:
// "servers", not "vnext" — the schema differs from VLESS/VMess).
func xrayOutboundTrojan(p *TrojanParams) map[string]interface{} {
	settings := map[string]interface{}{
		"servers": []interface{}{
			map[string]interface{}{
				"address":  p.Host,
				"port":     p.Port,
				"password": p.Password,
			},
		},
	}
	stream := xrayStreamSettings(
		p.Network, p.Security,
		p.Path, p.Host2, p.ServiceName,
		p.SNI, p.ALPN, p.Fingerprint, p.AllowInsecure,
		p.PublicKey, p.ShortID, p.SpiderX,
	)
	return map[string]interface{}{
		"tag":            "proxy",
		"protocol":       "trojan",
		"settings":       settings,
		"streamSettings": stream,
	}
}

// xrayOutboundVmess builds the xray outbound stanza for a VMess node.
// xray's vmess outbound uses `vnext: [{address, port, users: [{id, alterId,
// security}]}]` (similar to VLESS, with `alterId` + `security` per-user
// instead of VLESS's `encryption` + `flow`).
//
// VMess-specific transport: when network=tcp and the server uses HTTP
// header obfuscation (legacy), `streamSettings.tcpSettings.header.type`
// must be set to "http" — encode that here, not in xrayStreamSettings
// (which is meant to stay protocol-agnostic).
func xrayOutboundVmess(p *VmessParams) map[string]interface{} {
	user := map[string]interface{}{
		"id":       p.UUID,
		"alterId":  p.AlterID,
		"security": p.Scy,
	}
	settings := map[string]interface{}{
		"vnext": []interface{}{map[string]interface{}{
			"address": p.Host, "port": p.Port, "users": []interface{}{user},
		}},
	}
	stream := xrayStreamSettings(
		p.Network, p.Security,
		p.Path, p.Host2, p.ServiceName,
		p.SNI, p.ALPN, p.Fingerprint, false,
		"", "", "",
	)
	// Legacy TCP HTTP-obfs: rare in practice (predates WS/gRPC) but cheap
	// to support and harmless when HeaderType is empty/"none".
	if p.Network == "tcp" && p.HeaderType == "http" {
		stream["tcpSettings"] = map[string]interface{}{
			"header": map[string]interface{}{
				"type": "http",
				"request": map[string]interface{}{
					"path": []string{firstNonEmpty(p.Path, "/")},
				},
			},
		}
	}
	return map[string]interface{}{
		"tag":            "proxy",
		"protocol":       "vmess",
		"settings":       settings,
		"streamSettings": stream,
	}
}

// xrayOutboundShadowsocks builds the xray outbound stanza for a Shadowsocks
// node (covers both legacy SS ciphers and SS2022 — xray-core's
// `protocol: "shadowsocks"` handles them interchangeably as of 1.8+).
//
// Shape: `servers: [{address, port, method, password, uot: true}]`. No
// `streamSettings.security`/transport — SS is raw TCP, the cipher *is* the
// security layer. uot=true (UDP-over-TCP) lets UDP-dependent traffic (QUIC,
// DNS) tunnel through even on TCP-only paths; harmless when not needed.
//
// Plugin-bearing nodes (UnsupportedPlugin=true) reach this point only if
// the connect path forgot to fail-fast. We still emit a config (so users
// who add an external plugin runner manually can experiment), but obfs/
// v2ray-plugin/shadow-tls won't activate without their respective binaries.
func xrayOutboundShadowsocks(p *SSParams) map[string]interface{} {
	settings := map[string]interface{}{
		"servers": []interface{}{
			map[string]interface{}{
				"address":  p.Host,
				"port":     p.Port,
				"method":   p.Method,
				"password": p.Password,
				"uot":      true,
			},
		},
	}
	return map[string]interface{}{
		"tag":      "proxy",
		"protocol": "shadowsocks",
		"settings": settings,
		// Explicit naked-TCP streamSettings — xray defaults are fine, but
		// being explicit avoids surprises when global stream defaults change.
		"streamSettings": map[string]interface{}{"network": "tcp", "security": "none"},
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// splitALPN turns a comma-separated ALPN list ("h3,h2") into a trimmed,
// empty-filtered slice. Returns nil for an empty/blank input so callers can
// apply a protocol-appropriate default.
func splitALPN(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// buildXrayConfigForNode is the protocol-dispatching entry point. Returns
// nil if n.Kind is not an xray-handled protocol — callers should check
// engineFor(n.Kind) first to route UDP-family protocols to sing-box.
func buildXrayConfigForNode(n *Node, httpPort, socksPort int, socksUser, socksPass string) map[string]interface{} {
	if n == nil {
		return nil
	}
	ob := xrayOutboundForNode(n)
	if ob == nil {
		// Not an xray protocol; caller bug.
		return nil
	}
	cfg := xrayShell(httpPort, socksPort, socksUser, socksPass)
	outbounds := cfg["outbounds"].([]interface{})
	outbounds[0] = ob
	cfg["outbounds"] = outbounds
	if socksPort > 0 {
		cfg["routing"] = xrayRoutingForProxy()
	}
	applyXrayFragment(cfg)
	return cfg
}

// applyXrayFragment rewires the config so the entry outbound dials its server
// through a local "fragment" freedom outbound that splits the TLS ClientHello —
// the DPI-evasion knob (Settings → toggle). No-op when the setting is off.
//
// The entry outbound is always outbounds[0]: the single proxy in
// buildXrayConfigForNode, or hop0 (the hop your machine dials directly) in a
// chain. Inner chain hops are already inside the tunnel, so only the entry —
// the one your local DPI can see — needs fragmenting. The "tlshello" packet
// selector means it only touches TLS handshakes; plain-TCP nodes are unaffected.
func applyXrayFragment(cfg map[string]interface{}) {
	if cfg == nil || !tlsFragmentEnabled() {
		return
	}
	outs, ok := cfg["outbounds"].([]interface{})
	if !ok || len(outs) == 0 {
		return
	}
	entry, ok := outs[0].(map[string]interface{})
	if !ok || entry == nil {
		return
	}
	ss, _ := entry["streamSettings"].(map[string]interface{})
	if ss == nil {
		ss = map[string]interface{}{}
		entry["streamSettings"] = ss
	}
	sockopt, _ := ss["sockopt"].(map[string]interface{})
	if sockopt == nil {
		sockopt = map[string]interface{}{}
		ss["sockopt"] = sockopt
	}
	// Don't clobber an existing dialerProxy (shouldn't happen for the entry, but
	// be defensive — a pre-set dialer would otherwise be lost).
	if _, exists := sockopt["dialerProxy"]; exists {
		return
	}
	sockopt["dialerProxy"] = "fragment"
	length, interval := currentTLSFragmentParams()
	fragOut := map[string]interface{}{
		"tag":      "fragment",
		"protocol": "freedom",
		"settings": map[string]interface{}{
			"domainStrategy": "AsIs",
			"fragment": map[string]interface{}{
				"packets":  "tlshello",
				"length":   length,
				"interval": interval,
			},
		},
		"streamSettings": map[string]interface{}{
			"sockopt": map[string]interface{}{"tcpNoDelay": true},
		},
	}
	cfg["outbounds"] = append(outs, fragOut)
}

// xrayOutboundForNode returns the bare proxy outbound stanza for an
// xray-handled node (VLESS/VMess/Trojan/SS), tagged "proxy". Returns nil for
// non-xray protocols. Factored out of buildXrayConfigForNode so the chain
// builder can assemble several of these into one config.
func xrayOutboundForNode(n *Node) map[string]interface{} {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case KindVLESS:
		return xrayOutboundVless(n.Vless)
	case KindTrojan:
		return xrayOutboundTrojan(n.Trojan)
	case KindVMess:
		return xrayOutboundVmess(n.Vmess)
	case KindSS:
		return xrayOutboundShadowsocks(n.SS)
	}
	return nil
}

// buildXrayChainConfig assembles a multi-hop ("chain") xray config. Order is
// user-meaningful: nodes[0] is the ENTRY hop (first server your machine dials),
// nodes[len-1] is the EXIT hop (the server that egresses to the internet — its
// IP is what sites see). Traffic path: you → nodes[0] → … → nodes[len-1] → net.
//
// xray wiring (this is the part that's easy to get backwards):
//   - The routing rule sends traffic to the outbound tagged "proxy" — and that
//     outbound is the one that egresses, i.e. the EXIT. So the EXIT (nodes[last])
//     gets tag "proxy".
//   - An outbound reaches ITS OWN server through streamSettings.sockopt.
//     dialerProxy. So each hop i (i>=1) dials through hop i-1: the connection to
//     hop i's server is tunnelled via the previous hop. The ENTRY (nodes[0]) has
//     no dialerProxy — your machine dials it directly.
//
// Concretely for [Czech, NL]: Czech=entry (tag hop0, no dialer), NL=exit
// (tag "proxy", dialerProxy=hop0). Routing → "proxy" (NL). NL's link to the NL
// server is dialed through Czech, and Czech is dialed directly. Net path:
// you → Czech → NL → internet; exit IP = NL.
//
// All nodes MUST be xray-family (see chainEngineReason). A nil/empty list or
// any non-xray node returns nil.
func buildXrayChainConfig(nodes []*Node, httpPort, socksPort int, socksUser, socksPass string) map[string]interface{} {
	if len(nodes) == 0 {
		return nil
	}
	if len(nodes) == 1 {
		return buildXrayConfigForNode(nodes[0], httpPort, socksPort, socksUser, socksPass)
	}
	cfg := xrayShell(httpPort, socksPort, socksUser, socksPass)
	shell := cfg["outbounds"].([]interface{})
	// shell layout: [proxy-placeholder(nil), direct, block]. Keep direct+block.
	direct, block := shell[1], shell[2]

	last := len(nodes) - 1
	// Tag each hop: the exit (routing target) is "proxy"; the others "hop{i}".
	tagFor := func(i int) string {
		if i == last {
			return "proxy"
		}
		return fmt.Sprintf("hop%d", i)
	}

	hops := make([]interface{}, 0, len(nodes))
	for i, n := range nodes {
		ob := xrayOutboundForNode(n)
		if ob == nil {
			return nil // non-xray node slipped through — caller must pre-validate
		}
		ob["tag"] = tagFor(i)
		// Every hop except the entry dials through the PREVIOUS hop.
		if i > 0 {
			ss, _ := ob["streamSettings"].(map[string]interface{})
			if ss == nil {
				ss = map[string]interface{}{}
				ob["streamSettings"] = ss
			}
			sockopt, _ := ss["sockopt"].(map[string]interface{})
			if sockopt == nil {
				sockopt = map[string]interface{}{}
				ss["sockopt"] = sockopt
			}
			sockopt["dialerProxy"] = tagFor(i - 1)
		}
		hops = append(hops, ob)
	}
	// Final outbound list: [hop0(entry), …, proxy(exit), direct, block].
	outs := make([]interface{}, 0, len(hops)+2)
	outs = append(outs, hops...)
	outs = append(outs, direct, block)
	cfg["outbounds"] = outs
	if socksPort > 0 {
		cfg["routing"] = xrayRoutingForProxy()
	}
	applyXrayFragment(cfg)
	return cfg
}

// chainEngineReason validates that a set of nodes can form a single-engine
// chain. Returns "" when the chain is buildable, or a human-readable reason
// why not (mixed engines, or a non-xray node). Chains are currently xray-only:
// xray's dialerProxy links TCP-family outbounds (VLESS/VMess/Trojan/SS) in one
// process. Hysteria2/TUIC run under sing-box and can't be mixed into an xray
// dialer chain, so they're rejected with a clear message.
func chainEngineReason(nodes []*Node) string {
	if len(nodes) < 2 {
		return "a chain needs at least 2 configs"
	}
	for _, n := range nodes {
		if n == nil {
			return "unparseable config in selection"
		}
		if engineFor(n.Kind) != "xray" {
			return fmt.Sprintf("%s can't be chained (chains support VLESS/VMess/Trojan/Shadowsocks only)", n.Kind)
		}
	}
	return ""
}

// nodeUnsupportedReason returns a non-empty human-readable reason when the
// node cannot be used as-is. Empty return means the node is usable. This is
// the single check point all runners (ping/speed/connect) call right after
// parseNode so the row fails fast with a clear pill-text instead of waiting
// for xray to spawn and exit on a config it can't make sense of.
//
// Currently flags:
//   - SS nodes that use an external plugin (obfs-local, v2ray-plugin,
//     shadow-tls). xray-core does not run plugin binaries, and Vair doesn't
//     bundle them. A future stage could shell out to a plugin runner if we
//     ever ship one.
func nodeUnsupportedReason(n *Node) string {
	if n == nil {
		return ""
	}
	if n.Kind == KindSS && n.SS != nil && n.SS.UnsupportedPlugin {
		// Plugins sing-box can run natively (v2ray-plugin / obfs) are no
		// longer a hard failure — engineForNode routes them through sing-box.
		if ssPluginSupportedBySingbox(n.SS.PluginName) {
			return ""
		}
		if n.SS.PluginName != "" {
			return "ss plugin not supported: " + n.SS.PluginName
		}
		return "ss plugin not supported"
	}
	return ""
}

// engineFor reports which backend handles a given protocol. xray covers the
// TCP-family (VLESS/VMess/Trojan/SS), sing-box owns the UDP-family
// (Hysteria2/TUIC) because xray-core's support there is patchy.
func engineFor(k NodeKind) string {
	switch k {
	case KindHysteria2, KindTUIC:
		return "singbox"
	default:
		return "xray"
	}
}

// ─────────────────────────── sing-box config ────────────────────
//
// sing-box assembly mirrors the xray side: a small set of declarative
// builders here, stitched together by config dispatchers. The hybrid TUN
// path (xray-backed protocols tunnelled through sing-box) and the future
// pure-sing-box path (Hysteria2/TUIC) share the routing-rule builder so a
// settings change behaves identically no matter which engine is active.

// singboxRoutingRules builds the shared sing-box route.rules array. It is
// used both by the hybrid TUN config (xray-backed protocols, where sing-box
// is only the TUN front-end) and — from Stage 6 on — by the pure-sing-box
// TUN/proxy config for Hysteria2/TUIC. The rules cover, in order: traffic
// sniffing, DNS hijack, an optional xray-process carve-out, LAN bypass,
// user direct-apps, RU geosite/geoip bypass, and user direct-domains. The
// caller is responsible for appending `final` separately.
//
// includeXrayCarveout adds a `process_name:[xray.exe] → direct` rule so the
// xray child (which itself dials the real VPN server) is never routed back
// into the tunnel. Pure-sing-box TUN passes false: there is no xray child,
// and a stray process_name rule would just be dead weight.
func singboxRoutingRules(includeXrayCarveout bool) []interface{} {
	allowLAN := allowLANTraffic()

	mode := routingMode()
	settingsMu.RLock()
	var directDomains []string
	if !appSettings.DirectDomainsDisabled {
		directDomains = make([]string, len(appSettings.DirectDomains))
		copy(directDomains, appSettings.DirectDomains)
	}
	var directApps []string
	if !appSettings.DirectAppsDisabled {
		directApps = make([]string, len(appSettings.DirectApps))
		copy(directApps, appSettings.DirectApps)
	}
	settingsMu.RUnlock()

	rules := []interface{}{
		map[string]interface{}{"action": "sniff"},
		map[string]interface{}{"protocol": "dns", "action": "hijack-dns"},
	}
	// Exclude xray process from TUN to prevent a routing loop (hybrid only).
	if includeXrayCarveout {
		rules = append(rules, map[string]interface{}{
			"process_name": []string{"xray.exe", "xray"},
			"outbound":     "direct",
		})
	}
	// LAN handling: when allowed (default), private IPs bypass the tunnel so
	// printers / NAS / router admin pages still work. With kill-switch +
	// BlockLAN, even those go through proxy — usually breaks them, but it's
	// a deliberate user choice.
	if allowLAN {
		rules = append(rules,
			map[string]interface{}{"ip_is_private": true, "outbound": "direct"},
		)
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
	// addSuffix appends a domain_suffix rule to the given outbound.
	addSuffix := func(domains []string, outbound string) {
		var suffixes []string
		for _, d := range domains {
			if d = strings.TrimSpace(d); d != "" {
				suffixes = append(suffixes, d)
			}
		}
		if len(suffixes) > 0 {
			rules = append(rules, map[string]interface{}{"domain_suffix": suffixes, "outbound": outbound})
		}
	}
	// Custom "without VPN" domains → direct (all modes).
	addSuffix(directDomains, "direct")

	switch mode {
	case "only_blocked":
		// Manual + custom-URL "through VPN" domains → proxy, then blocked-in-RU
		// sets → proxy. Everything else falls through to route.final = "direct"
		// (set by applySingboxRouteMode).
		proxyDomains := effectiveProxyDomains()
		proxyDomains = append(proxyDomains, customBlocklistDomains()...)
		// Force the "check IP" service through the VPN so the button reflects the
		// tunnel exit (it isn't a blocked resource, so it'd go direct otherwise).
		proxyDomains = append(proxyDomains, checkExitHost)
		addSuffix(proxyDomains, "proxy")
		rules = append(rules,
			map[string]interface{}{"rule_set": "geosite-ru-blocked", "outbound": "proxy"},
			map[string]interface{}{"rule_set": "geoip-ru-blocked", "outbound": "proxy"},
		)
	case "bypass_ru":
		rules = append(rules,
			map[string]interface{}{"rule_set": "geosite-ru", "outbound": "direct"},
			map[string]interface{}{"rule_set": "geoip-ru", "outbound": "direct"},
		)
	}
	return rules
}

// singboxRuRuleSet returns the route.rule_set definitions for the RU-bypass
// geosite/geoip rule sets. Only meaningful when RuSitesDirect is on; callers
// gate on that and attach the result to route["rule_set"].
//
// The sets are referenced as LOCAL files (under binDir) rather than remote URLs:
// the remote form downloaded from raw.githubusercontent.com at start, which is
// blocked in Russia and made sing-box abort with a FATAL. refreshRuRuleSets
// still tries to pull the freshest copy from upstream (best-effort, throttled);
// the embedded baseline is the fallback, so a local file is always present.
func singboxRuRuleSet() []interface{} {
	refreshRuRuleSets()
	return []interface{}{
		map[string]interface{}{
			"type":   "local",
			"tag":    "geosite-ru",
			"format": "binary",
			"path":   ruRuleSetLocalPath("geosite-ru.srs"),
		},
		map[string]interface{}{
			"type":   "local",
			"tag":    "geoip-ru",
			"format": "binary",
			"path":   ruRuleSetLocalPath("geoip-ru.srs"),
		},
	}
}

// singboxBlockedRuleSet returns the route.rule_set definitions for the RU-blocked
// sets (only_blocked mode), as local srs files with the same best-effort upstream
// refresh + embedded fallback as singboxRuRuleSet. Also kicks the custom blocklist
// refresh so its domains are fresh when singboxRoutingRules reads them.
func singboxBlockedRuleSet() []interface{} {
	refreshBlockedRuleSets()
	refreshCustomBlocklist(blocklistURL())
	return []interface{}{
		map[string]interface{}{
			"type":   "local",
			"tag":    "geosite-ru-blocked",
			"format": "binary",
			"path":   ruRuleSetLocalPath("geosite-ru-blocked.srs"),
		},
		map[string]interface{}{
			"type":   "local",
			"tag":    "geoip-ru-blocked",
			"format": "binary",
			"path":   ruRuleSetLocalPath("geoip-ru-blocked.srs"),
		},
	}
}

// applySingboxRouteMode sets route.final and route.rule_set according to the
// active routing mode. only_blocked defaults to direct and attaches the
// ru-blocked rule-sets (matched → proxy in singboxRoutingRules); the other modes
// default to proxy, with the RU-bypass rule-sets attached for bypass_ru.
func applySingboxRouteMode(route map[string]interface{}) {
	switch routingMode() {
	case "only_blocked":
		route["final"] = "direct"
		route["rule_set"] = singboxBlockedRuleSet()
	case "bypass_ru":
		route["final"] = "proxy"
		route["rule_set"] = singboxRuRuleSet()
	default: // proxy_all
		route["final"] = "proxy"
		delete(route, "rule_set")
	}
}

// customBlocklistDomains reads the user's fetched custom blocklist (plain domain
// list; one suffix per line; #/! comments and a leading "0.0.0.0"/"*." ignored).
// Capped to keep the generated config sane. Empty when none configured/fetched.
func customBlocklistDomains() []string {
	data, err := os.ReadFile(customBlocklistPath())
	if err != nil || len(data) == 0 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		fields := strings.Fields(line) // handles "0.0.0.0 domain" hosts format
		d := strings.TrimPrefix(fields[len(fields)-1], "*.")
		if d != "" {
			out = append(out, d)
		}
		if len(out) >= 200000 {
			break
		}
	}
	return out
}

// singboxOutboundHysteria2 builds the sing-box "hysteria2" outbound. The
// obfs block is only emitted when an obfs type is present — sing-box
// rejects an obfs object with an empty "type". ALPN defaults to ["h3"]:
// Hysteria2 rides HTTP/3, and an empty alpn lets some servers negotiate
// the wrong protocol and silently fail the handshake. tls.server_name
// prefers the explicit SNI and falls back to the (pre-resolution) host.
func singboxOutboundHysteria2(p *Hysteria2Params) map[string]interface{} {
	if p == nil {
		return nil
	}
	alpn := splitALPN(p.ALPN)
	if len(alpn) == 0 {
		alpn = []string{"h3"}
	}
	tls := map[string]interface{}{
		"enabled":  true,
		"insecure": p.Insecure,
		"alpn":     alpn,
	}
	if sni := firstNonEmpty(p.SNI, p.Host); sni != "" {
		tls["server_name"] = sni
	}
	out := map[string]interface{}{
		"type":        "hysteria2",
		"tag":         "proxy",
		"server":      p.Host,
		"server_port": p.Port,
		"password":    p.Password,
		"tls":         tls,
	}
	if p.ObfsType != "" {
		out["obfs"] = map[string]interface{}{
			"type":     p.ObfsType,
			"password": p.ObfsPassword,
		}
	}
	return out
}

// singboxOutboundTUIC builds the sing-box "tuic" outbound. sing-box REQUIRES
// a non-empty tls.alpn for TUIC (it rejects the config otherwise), so an
// absent URL alpn= defaults to ["h3"]. congestion_control / udp_relay_mode
// are only emitted when present — passing empty strings would override
// sing-box's sensible defaults (cubic / native) with invalid values.
// tls.server_name prefers the explicit SNI, falling back to the
// (pre-resolution) host so the cert still validates after host→IP swap.
func singboxOutboundTUIC(p *TuicParams) map[string]interface{} {
	if p == nil {
		return nil
	}
	alpn := splitALPN(p.ALPN)
	if len(alpn) == 0 {
		alpn = []string{"h3"}
	}
	tls := map[string]interface{}{
		"enabled":  true,
		"insecure": p.Insecure,
		"alpn":     alpn,
	}
	if sni := firstNonEmpty(p.SNI, p.Host); sni != "" {
		tls["server_name"] = sni
	}
	out := map[string]interface{}{
		"type":        "tuic",
		"tag":         "proxy",
		"server":      p.Host,
		"server_port": p.Port,
		"uuid":        p.UUID,
		"password":    p.Password,
		"tls":         tls,
	}
	if p.CongestionControl != "" {
		out["congestion_control"] = p.CongestionControl
	}
	if p.UDPRelayMode != "" {
		out["udp_relay_mode"] = p.UDPRelayMode
	}
	return out
}

// ssPluginSupportedBySingbox reports whether sing-box can run this SS plugin
// natively (no external binary). sing-box ships obfs-local (simple-obfs) and
// v2ray-plugin. Anything else (shadow-tls, kcptun, …) still needs a runner we
// don't bundle, so it stays flagged unsupported.
func ssPluginSupportedBySingbox(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "v2ray-plugin", "obfs-local", "simple-obfs":
		return true
	}
	return false
}

// singboxPluginName maps a SIP003 plugin name to the spelling sing-box
// expects. The simple-obfs plugin is "obfs-local" in sing-box.
func singboxPluginName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "simple-obfs" {
		return "obfs-local"
	}
	return n
}

// singboxOutboundShadowsocks builds the sing-box "shadowsocks" outbound. Used
// only for SS nodes that carry a sing-box-supported plugin — plain SS still
// runs through xray. plugin_opts is passed through verbatim (the SIP003
// "opt=val;opt2=val2" string sing-box also expects).
func singboxOutboundShadowsocks(p *SSParams) map[string]interface{} {
	if p == nil {
		return nil
	}
	out := map[string]interface{}{
		"type":        "shadowsocks",
		"tag":         "proxy",
		"server":      p.Host,
		"server_port": p.Port,
		"method":      p.Method,
		"password":    p.Password,
	}
	if p.PluginName != "" {
		out["plugin"] = singboxPluginName(p.PluginName)
		if p.PluginOpts != "" {
			out["plugin_opts"] = p.PluginOpts
		}
	}
	return out
}

// singboxOutboundForNode dispatches to the per-protocol sing-box outbound
// builder. Returns nil for any kind sing-box doesn't own — callers gate on
// engineForNode.
func singboxOutboundForNode(n *Node) map[string]interface{} {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case KindHysteria2:
		return singboxOutboundHysteria2(n.Hysteria2)
	case KindTUIC:
		return singboxOutboundTUIC(n.TUIC)
	case KindSS:
		return singboxOutboundShadowsocks(n.SS)
	}
	return nil
}

// engineForNode is the node-aware companion to engineFor. Besides the
// protocol-family split it routes Shadowsocks nodes carrying a
// sing-box-supported plugin (v2ray-plugin / obfs) through sing-box, since
// xray-core cannot run those plugins. Plain SS (no plugin) stays on xray.
func engineForNode(n *Node) string {
	if n == nil {
		return "xray"
	}
	if n.Kind == KindSS && n.SS != nil && n.SS.PluginName != "" &&
		ssPluginSupportedBySingbox(n.SS.PluginName) {
		return "singbox"
	}
	return engineFor(n.Kind)
}

// buildSingboxTestConfig builds a minimal sing-box config for ping/speed
// probing: one HTTP inbound on httpPort, one proxy outbound for the node,
// plus a direct outbound, no routing. This is the sing-box analogue of the
// xray "test" config (buildXrayConfigForNode with socksPort = -1). Returns
// nil for protocols sing-box doesn't own — callers gate on engineFor.
func buildSingboxTestConfig(n *Node, httpPort int) map[string]interface{} {
	if n == nil {
		return nil
	}
	out := singboxOutboundForNode(n)
	if out == nil {
		return nil
	}
	return map[string]interface{}{
		"log": map[string]interface{}{"level": singboxLogLevel(), "timestamp": true},
		"inbounds": []interface{}{
			map[string]interface{}{
				"type":        "http",
				"tag":         "http-in",
				"listen":      "127.0.0.1",
				"listen_port": httpPort,
			},
		},
		"outbounds": []interface{}{
			out,
			map[string]interface{}{"type": "direct", "tag": "direct"},
		},
	}
}

// buildSingboxProxyConfig builds the sing-box config for a persistent local
// proxy session (system-proxy mode) of a UDP-family node. HTTP + SOCKS
// inbounds on 127.0.0.1 (apps / system proxy point here, via the byte
// counter), the protocol outbound plus a direct outbound, and the shared
// routing rules. singboxRoutingRules(false): no xray carve-out — there is
// no xray child in the pure-sing-box path. A minimal legacy DNS block (the
// same one hybrid TUN uses with leak-protection off) plus a matching
// default_domain_resolver keeps sing-box 1.13 from rejecting the config for
// a missing resolver. Returns nil if sing-box can't build an outbound for n.
func buildSingboxProxyConfig(n *Node, httpPort, socksPort int) map[string]interface{} {
	if n == nil {
		return nil
	}
	out := singboxOutboundForNode(n)
	if out == nil {
		return nil
	}

	route := map[string]interface{}{
		"auto_detect_interface":   true,
		"default_domain_resolver": "dns-local",
		"rules":                   singboxRoutingRules(false),
	}
	applySingboxRouteMode(route)

	return map[string]interface{}{
		"log": map[string]interface{}{"level": singboxLogLevel(), "timestamp": true},
		"dns": buildDNSBlock(false, false),
		"inbounds": []interface{}{
			map[string]interface{}{
				"type": "http", "tag": "http-in",
				"listen": "127.0.0.1", "listen_port": httpPort,
			},
			map[string]interface{}{
				"type": "socks", "tag": "socks-in",
				"listen": "127.0.0.1", "listen_port": socksPort,
			},
		},
		"outbounds": []interface{}{
			out,
			map[string]interface{}{"type": "direct", "tag": "direct"},
		},
		"route": route,
	}
}

// buildSingboxTUNConfig builds the pure-sing-box TUN config for a UDP-family
// node (Hysteria2/TUIC). It is the single-process analogue of
// buildHybridTUNConfig: instead of a socks outbound dialling an xray child,
// the protocol outbound dials the VPN server directly. No xray carve-out in
// the routing rules (there is no xray process — sing-box's own outbound
// bypasses TUN via auto_detect_interface). DNS, the TUN inbound, leak
// protection and RU/direct bypass behave exactly as in hybrid TUN so the
// kill-switch / split-tunnel behaviour is identical regardless of protocol.
//
// Trade-off: there is no local SOCKS hop here, so the byte counter cannot be
// inserted — session/lifetime traffic stats are unavailable for Hysteria2/
// TUIC TUN sessions (surfaced in the UI). Returns nil if sing-box can't
// build an outbound for n.
func buildSingboxTUNConfig(n *Node, ifaceName string) map[string]interface{} {
	if n == nil {
		return nil
	}
	out := singboxOutboundForNode(n)
	if out == nil {
		return nil
	}
	leakProtect := dnsLeakProtectionEnabled()
	useFakeIP := fakeIPEnabled()

	tun := map[string]interface{}{
		"type":           "tun",
		"tag":            "tun-in",
		"interface_name": ifaceName,
		"address":        []string{"172.19.0.1/30"},
		"mtu":            currentMTU(),
		"auto_route":     true,
		"strict_route":   leakProtect,
		"stack":          "gvisor",
	}

	defaultResolver := "dns-local"
	if leakProtect {
		defaultResolver = "dns-bootstrap"
	}
	route := map[string]interface{}{
		"auto_detect_interface":   true,
		"default_domain_resolver": defaultResolver,
		"find_process":            true,
		"rules":                   singboxRoutingRules(false),
	}
	applySingboxRouteMode(route)

	return map[string]interface{}{
		"log":      map[string]interface{}{"level": singboxLogLevel(), "timestamp": true},
		"dns":      buildDNSBlock(leakProtect, useFakeIP),
		"inbounds": []interface{}{tun},
		"outbounds": []interface{}{
			out,
			map[string]interface{}{"type": "direct", "tag": "direct"},
		},
		"route": route,
	}
}
