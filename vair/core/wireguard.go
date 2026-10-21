package core

import (
	"encoding/base64"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// WireGuard support.
//
// WireGuard is not a proxy protocol like the others here — there is no server
// that speaks a wrapper around your TCP stream, there is a network interface
// with an address, and packets are encrypted to a peer's public key. What makes
// it worth carrying anyway is Cloudflare WARP, which is free, needs no account,
// and hands out an exit address that sites treat as a consumer connection
// rather than as a data centre. A large part of any public list lives on VPS
// ranges that Google, banks and streaming services meet with a captcha or a
// refusal; the same traffic through WARP arrives from an address they accept.
//
// It is handled by sing-box, whose "endpoint" is the right shape for a thing
// that owns an interface rather than dialling out. That is also why it needs a
// path of its own through the config builders: every other protocol here
// produces an outbound, and WireGuard produces a top-level endpoints entry with
// the same tag.

// WireGuardParams is a parsed WireGuard peer.
type WireGuardParams struct {
	Raw        string
	Name       string
	Host       string
	Port       int
	PrivateKey string // ours, base64
	PublicKey  string // the peer's, base64
	PreShared  string // optional symmetric key, base64
	// Addresses are the interface's own addresses, in CIDR form. WireGuard has
	// no DHCP: the address is part of the configuration and the tunnel does not
	// come up without it.
	Addresses []string
	// Reserved is Cloudflare's three-byte client identifier. WARP will accept a
	// handshake without it and then silently drop everything, which looks
	// exactly like a dead server.
	Reserved []int
	MTU      int
}

// wireguardSchemes are the prefixes recognised as a WireGuard config.
var wireguardSchemes = []string{"wireguard://", "wg://"}

// parseWireGuard reads the share-link form the other clients emit:
//
//	wireguard://<private-key>@host:port?publickey=…&address=…&reserved=…&mtu=…#name
//
// The private key sits where the password would be in every other scheme,
// which is unfortunate — it is the one field here that must never be shared —
// but it is what the format is, and refusing to read it would only mean the
// user cannot import a config every other client accepts.
func parseWireGuard(raw string) (*WireGuardParams, error) {
	raw = strings.TrimSpace(raw)
	rest := ""
	for _, s := range wireguardSchemes {
		if strings.HasPrefix(raw, s) {
			rest = raw[len(s):]
			break
		}
	}
	if rest == "" {
		return nil, fmt.Errorf("not a wireguard link")
	}

	// Split the private key off by hand instead of letting url.Parse find the
	// userinfo. A base64 key contains "/", and half the tools that emit these
	// links do not percent-encode it — url.Parse then reads everything from the
	// slash as a path, loses the key and the host, and the config looks
	// malformed when it is merely written the common way. "@" is not in the
	// base64 alphabet, so the first one is always the separator.
	key := ""
	if i := strings.Index(rest, "@"); i >= 0 {
		key, rest = rest[:i], rest[i+1:]
	}

	u, err := url.Parse("wireguard://" + rest)
	if err != nil {
		return nil, fmt.Errorf("wireguard: %w", err)
	}
	p := &WireGuardParams{Raw: raw, Name: u.Fragment}
	if key != "" {
		if unescaped, err := url.QueryUnescape(key); err == nil {
			key = unescaped
		}
		p.PrivateKey = wgKey(key)
	}
	p.Host = u.Hostname()
	if v := u.Port(); v != "" {
		p.Port, _ = strconv.Atoi(v)
	}
	if p.Port == 0 {
		p.Port = 51820 // the protocol's own default
	}

	q := u.Query()
	get := func(names ...string) string {
		for _, n := range names {
			if v := strings.TrimSpace(q.Get(n)); v != "" {
				return v
			}
		}
		return ""
	}
	// The same field is spelled three ways across the clients that emit these.
	p.PublicKey = wgKey(get("publickey", "public_key", "peer_public_key", "pubkey"))
	p.PreShared = wgKey(get("presharedkey", "pre_shared_key", "psk"))
	for _, a := range strings.Split(get("address", "addresses", "ip"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			p.Addresses = append(p.Addresses, withCIDRMask(a))
		}
	}
	if v := get("mtu"); v != "" {
		p.MTU, _ = strconv.Atoi(v)
	}
	for _, r := range strings.Split(get("reserved"), ",") {
		if r = strings.TrimSpace(r); r != "" {
			if n, err := strconv.Atoi(r); err == nil {
				p.Reserved = append(p.Reserved, n)
			}
		}
	}

	fillWARPDefaults(p)
	if err := p.validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// warpPeerKey is Cloudflare's WARP public key. Every WARP account in the world
// peers with it — verified against a live registration, not copied from a
// forum.
const warpPeerKey = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="

// warpInterfaceV4 is the address WARP hands every client. Also verified live:
// the v4 side is the same for everyone, only the v6 address is per-account.
const warpInterfaceV4 = "172.16.0.2/32"

// warpNets are the endpoint ranges Cloudflare answers WARP on. The generators
// that produce short links pick an address out of these.
var warpNets = []string{
	"162.159.192.0/24", "162.159.193.0/24", "162.159.195.0/24",
	"188.114.96.0/22",
	"2606:4700:d0::/48", "2606:4700:d1::/48",
}

// fillWARPDefaults completes a WARP link that was written short.
//
// Links like "wireguard://<key>@162.159.195.230:1843" are everywhere — the
// generators leave out the peer key and the interface address because for WARP
// they are the same for everyone, and every client that understands WARP fills
// them in. Refusing such a link is technically correct and practically useless.
//
// Deliberately narrow: only when the endpoint is one of Cloudflare's own, and
// only for fields the link did not carry. Anything else keeps failing with the
// message that says which field is missing, because for a private peer there is
// nothing sensible to guess.
func fillWARPDefaults(p *WireGuardParams) {
	if !looksLikeWARPEndpoint(p.Host) {
		return
	}
	if p.PublicKey == "" {
		p.PublicKey = warpPeerKey
		vlog("info", "WireGuard: link has no peer key — using Cloudflare WARP's")
	}
	if len(p.Addresses) == 0 {
		p.Addresses = []string{warpInterfaceV4}
		vlog("info", "WireGuard: link has no interface address — using WARP's %s", warpInterfaceV4)
	}
}

// looksLikeWARPEndpoint reports whether this is Cloudflare's WARP.
func looksLikeWARPEndpoint(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return false
	}
	if strings.HasSuffix(h, "cloudflareclient.com") {
		return true
	}
	addr, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	for _, n := range warpNets {
		if pfx, err := netip.ParsePrefix(n); err == nil && pfx.Contains(addr) {
			return true
		}
	}
	return false
}

// validate refuses what cannot possibly connect.
//
// Worth doing here rather than letting the engine fail: a WireGuard tunnel with
// a missing address or a malformed key does not report an error, it simply
// never carries a packet — which the user reads as "this config is dead" and
// we would report as "connected".
func (p *WireGuardParams) validate() error {
	if p.Host == "" {
		return fmt.Errorf("wireguard: no server address")
	}
	if !validWGKey(p.PrivateKey) {
		return fmt.Errorf("wireguard: private key is missing or not 32 bytes of base64")
	}
	if !validWGKey(p.PublicKey) {
		return fmt.Errorf("wireguard: peer public key is missing or not 32 bytes of base64")
	}
	if p.PreShared != "" && !validWGKey(p.PreShared) {
		return fmt.Errorf("wireguard: pre-shared key is not 32 bytes of base64")
	}
	if len(p.Addresses) == 0 {
		return fmt.Errorf("wireguard: no interface address — the tunnel would come up and carry nothing")
	}
	return nil
}

// wgKey repairs a base64 key that travelled through a query string.
//
// WireGuard keys are standard base64 and contain "+"; in a query string "+"
// means a space, so a link whose emitter did not percent-encode arrives with
// spaces where the key had plus signs. Both spellings are out there — the same
// key, one of them silently unusable — and refusing the un-encoded form would
// mean rejecting configurations that every other client imports. Only touched
// when the value does not already decode, so a key containing a genuine space
// (there is no such key) is not what this breaks.
func wgKey(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || validWGKey(v) {
		return v
	}
	if repaired := strings.ReplaceAll(v, " ", "+"); validWGKey(repaired) {
		return repaired
	}
	return v
}

// validWGKey checks the one thing that can be checked without trying: every
// WireGuard key is 32 bytes, base64-encoded.
func validWGKey(s string) bool {
	if s == "" {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		// Some emitters use the URL alphabet.
		b, err = base64.URLEncoding.DecodeString(s)
	}
	return err == nil && len(b) == 32
}

// withCIDRMask adds the mask a bare address is missing. "172.16.0.2" means one
// address, not a network, and sing-box wants that said explicitly.
func withCIDRMask(addr string) string {
	if strings.Contains(addr, "/") {
		return addr
	}
	if strings.Contains(addr, ":") {
		return addr + "/128"
	}
	return addr + "/32"
}

// singboxEndpointWireGuard renders the endpoint sing-box expects.
//
// An endpoint rather than an outbound because that is what sing-box calls a
// thing that owns an interface; the tag is the same "proxy" every other egress
// here uses, so the routing rules do not need to know the difference.
func singboxEndpointWireGuard(p *WireGuardParams) map[string]interface{} {
	if p == nil {
		return nil
	}
	peer := map[string]interface{}{
		"address":     p.Host,
		"port":        p.Port,
		"public_key":  p.PublicKey,
		"allowed_ips": []string{"0.0.0.0/0", "::/0"},
	}
	if p.PreShared != "" {
		peer["pre_shared_key"] = p.PreShared
	}
	if len(p.Reserved) == 3 {
		peer["reserved"] = p.Reserved
	}
	ep := map[string]interface{}{
		"type":        "wireguard",
		"tag":         "proxy",
		"address":     p.Addresses,
		"private_key": p.PrivateKey,
		"peers":       []interface{}{peer},
		// Bind the tunnel's own sockets explicitly.
		//
		// Without this, sing-box applies the auto-detected default interface to
		// them, and on Windows that fails the moment the default adapter has IPv6
		// switched off — a common enough setting, and the one on the machine this
		// was found on:
		//
		//	unable to update bind: listen udp6 [::]: An invalid argument was supplied
		//
		// after which the handshake never leaves and the tunnel is up and silent.
		// Naming both families keeps IPv6 peers reachable; measured working on
		// Windows (where it is the difference between a tunnel and nothing) and on
		// Linux (where it changes nothing).
		"inet4_bind_address": "0.0.0.0",
		"inet6_bind_address": "::",
	}
	if p.MTU > 0 {
		ep["mtu"] = p.MTU
	}
	return ep
}

// serializeWireGuardForm turns the form back into a wireguard:// link, in the
// spelling parseWireGuard reads.
//
// The private key is the userinfo, as in every producer's links, and it is NOT
// percent-encoded there: base64 contains "/" and "+", and a client that escapes
// them writes a link half the other clients cannot read. parseWireGuard splits
// at the first "@" for the same reason.
func serializeWireGuardForm(f ConfigForm) (string, error) {
	if strings.TrimSpace(f.UUID) == "" {
		return "", fmt.Errorf("the private key is required")
	}
	if strings.TrimSpace(f.PeerPublicKey) == "" {
		return "", fmt.Errorf("the peer's public key is required")
	}
	q := url.Values{}
	q.Set("publickey", strings.TrimSpace(f.PeerPublicKey))
	setNonEmpty(q, "presharedkey", strings.TrimSpace(f.PreSharedKey))
	setNonEmpty(q, "address", strings.TrimSpace(f.Addresses))
	setNonEmpty(q, "reserved", strings.TrimSpace(f.Reserved))
	if f.MTU > 0 {
		q.Set("mtu", strconv.Itoa(f.MTU))
	}
	applyExtra(q, f.Extra)
	var b strings.Builder
	b.WriteString("wireguard://")
	b.WriteString(strings.TrimSpace(f.UUID))
	b.WriteString("@")
	b.WriteString(hostPort(f.Address, f.Port))
	if enc := q.Encode(); enc != "" {
		b.WriteString("?")
		b.WriteString(enc)
	}
	if f.Name != "" {
		b.WriteString("#")
		b.WriteString(url.QueryEscape(f.Name))
	}
	return b.String(), nil
}
