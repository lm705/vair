package core

// Canonical de-duplication.
//
// The old dedup key was the raw URL minus its "#name" fragment. That misses the
// very common case where two feeds export the SAME server with the parameters
// written differently: reordered query strings (`?type=ws&security=tls` vs
// `?security=tls&type=ws`), transport aliases (xray accepts `type=raw` and
// `type=tcp` for the same RAW/TCP transport), omitted-vs-explicit defaults
// (`security` absent means `none`), upper/lower-case hosts and UUIDs, vmess
// links whose base64 JSON has its keys in another order, and so on. Textually
// different, functionally identical — and the old key kept them all.
//
// dedupKey builds the key from the PARSED node instead: the parsers already fill
// protocol defaults, so field-by-field extraction is inherently order- and
// encoding-independent. On top of that we normalise known aliases below.
//
// Design rule — asymmetric risk: merging two configs that are NOT the same hides
// a config the user may need (bad), while failing to merge two that are leaves a
// duplicate (harmless). So we only normalise equivalences that are certain, and
// keep every field that can change how the connection is made (flow, fp, pbk,
// sid, alpn, allowInsecure…) as part of the identity. Anything that fails to
// parse falls back to the old exact-body key.

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// networkAliases maps transport spellings that mean the same thing.
// Xray renamed the TCP transport to "raw" (both spellings are accepted and
// identical); "h2" is the older name of the HTTP/2 transport; sing-box/xray
// call the same split-HTTP transport "xhttp" or "splithttp"; "gun" is an old
// alias of gRPC; mKCP appears as both "kcp" and "mkcp".
var networkAliases = map[string]string{
	"":            "tcp",
	"raw":         "tcp",
	"tcp":         "tcp",
	"none":        "tcp",
	"h2":          "http",
	"http":        "http",
	"ws":          "ws",
	"websocket":   "ws",
	"grpc":        "grpc",
	"gun":         "grpc",
	"kcp":         "kcp",
	"mkcp":        "kcp",
	"quic":        "quic",
	"xhttp":       "xhttp",
	"splithttp":   "xhttp",
	"httpupgrade": "httpupgrade",
}

// securityAliases normalises the TLS-layer spelling. NOTE: "reality" is kept
// DISTINCT from "tls" — they are different handshakes, not aliases.
var securityAliases = map[string]string{
	"":     "none",
	"none": "none",
	"tls":  "tls",
	"xtls": "xtls",
}

func normNetwork(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if v, ok := networkAliases[s]; ok {
		return v
	}
	return s
}

func normSecurity(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if v, ok := securityAliases[s]; ok {
		return v
	}
	return s // reality, and anything unknown, kept verbatim
}

// normHost lowercases the host, drops a trailing root dot and normalises IP
// literals (IPv6 bracket forms / zero-compression) to their canonical text.
func normHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ".")
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	if ip := net.ParseIP(h); ip != nil {
		return ip.String()
	}
	return h
}

// normPath normalises a transport path. Producers write "" and "/" for the same
// default path on the HTTP-family transports, so they are folded together
// there; other transports keep the value verbatim (grpc serviceName is a name,
// not a path).
func normPath(network, p string) string {
	p = strings.TrimSpace(p)
	switch network {
	case "ws", "http", "xhttp", "httpupgrade":
		if p == "" {
			return "/"
		}
	}
	return p
}

// normALPN normalises an ALPN list: lower-cased, de-spaced and SORTED, so the
// same protocol set written in a different order is one key.
func normALPN(s string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func boolKey(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// dedupKey returns the canonical identity of a node URL: two configs that
// connect to the same endpoint in the same way produce the same key regardless
// of how their URL was written. Unparseable input falls back to the exact body
// (old behaviour), so nothing is ever merged by accident.
func dedupKey(raw string) string {
	raw = strings.TrimSpace(raw)
	n, err := parseNode(raw)
	if err != nil || n == nil {
		return "?\x00" + nodeBody(raw)
	}
	// f joins the fixed field list with a separator that cannot appear in the
	// values, so distinct field layouts can never collide.
	f := func(parts ...string) string { return strings.Join(parts, "\x00") }
	host := normHost(n.Host)
	port := fmt.Sprint(n.Port)

	switch {
	case n.Vless != nil:
		p := n.Vless
		net_ := normNetwork(p.Network)
		return f("vless", host, port, strings.ToLower(strings.TrimSpace(p.UUID)),
			net_, normSecurity(p.Security),
			normPath(net_, p.Path), strings.ToLower(strings.TrimSpace(p.Host2)),
			strings.TrimSpace(p.ServiceName),
			strings.ToLower(strings.TrimSpace(p.SNI)), normALPN(p.ALPN),
			strings.ToLower(strings.TrimSpace(p.Fingerprint)),
			strings.TrimSpace(p.Flow), strings.TrimSpace(p.PublicKey),
			strings.ToLower(strings.TrimSpace(p.ShortID)), strings.TrimSpace(p.SpiderX),
			boolKey(p.AllowInsecure))

	case n.Trojan != nil:
		p := n.Trojan
		net_ := normNetwork(p.Network)
		return f("trojan", host, port, p.Password, // password is case-sensitive
			net_, normSecurity(p.Security),
			normPath(net_, p.Path), strings.ToLower(strings.TrimSpace(p.Host2)),
			strings.TrimSpace(p.ServiceName),
			strings.ToLower(strings.TrimSpace(p.SNI)), normALPN(p.ALPN),
			strings.ToLower(strings.TrimSpace(p.Fingerprint)),
			strings.TrimSpace(p.PublicKey), strings.ToLower(strings.TrimSpace(p.ShortID)),
			strings.TrimSpace(p.SpiderX), boolKey(p.AllowInsecure))

	case n.Vmess != nil:
		p := n.Vmess
		net_ := normNetwork(p.Network)
		scy := strings.ToLower(strings.TrimSpace(p.Scy))
		if scy == "" {
			scy = "auto" // v2rayN's default when the field is absent
		}
		hdr := strings.ToLower(strings.TrimSpace(p.HeaderType))
		if hdr == "" {
			hdr = "none"
		}
		return f("vmess", host, port, strings.ToLower(strings.TrimSpace(p.UUID)),
			fmt.Sprint(p.AlterID), scy, net_, normSecurity(p.Security), hdr,
			normPath(net_, p.Path), strings.ToLower(strings.TrimSpace(p.Host2)),
			strings.TrimSpace(p.ServiceName),
			strings.ToLower(strings.TrimSpace(p.SNI)), normALPN(p.ALPN),
			strings.ToLower(strings.TrimSpace(p.Fingerprint)))

	case n.SS != nil:
		p := n.SS
		// The method name is case-insensitive; the password is not. The plugin
		// string is part of how the connection is made, so it stays in the key.
		return f("ss", host, port,
			strings.ToLower(strings.TrimSpace(p.Method)), p.Password,
			strings.TrimSpace(p.PluginName), strings.TrimSpace(p.PluginOpts))

	case n.Hysteria2 != nil:
		p := n.Hysteria2
		return f("hysteria2", host, port, p.Password,
			strings.ToLower(strings.TrimSpace(p.SNI)), normALPN(p.ALPN),
			strings.ToLower(strings.TrimSpace(p.ObfsType)), p.ObfsPassword,
			strings.TrimSpace(p.PinSHA256), boolKey(p.Insecure))

	case n.TUIC != nil:
		p := n.TUIC
		return f("tuic", host, port,
			strings.ToLower(strings.TrimSpace(p.UUID)), p.Password,
			strings.ToLower(strings.TrimSpace(p.SNI)), normALPN(p.ALPN),
			strings.ToLower(strings.TrimSpace(p.CongestionControl)),
			strings.ToLower(strings.TrimSpace(p.UDPRelayMode)), boolKey(p.Insecure))
	}
	// Parsed, but no protocol payload we know how to canonicalise.
	return "?\x00" + nodeBody(raw)
}
