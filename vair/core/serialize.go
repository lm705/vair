package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ConfigForm is the flat DTO the "add/edit config" UI edits. It's a superset of
// every protocol's fields; only the ones relevant to Form.Protocol are used when
// serialising. It's the bridge between the modal and the share-link URL: the form
// is turned into a standard `vless://` / `vmess://` / … URL via formToURL, then
// fed through the normal parseNode path so a manual config is indistinguishable
// from an imported one. configToForm is the inverse, for edit/view prefill.
type ConfigForm struct {
	Protocol string `json:"protocol"` // vless | vmess | trojan | ss | hysteria2 | tuic
	Name     string `json:"name"`
	Address  string `json:"address"`
	Port     int    `json:"port"`

	UUID     string `json:"uuid,omitempty"`     // vless/vmess/tuic
	Password string `json:"password,omitempty"` // trojan/ss/hysteria2/tuic

	Network     string `json:"network,omitempty"`     // tcp/ws/grpc/http/quic
	Path        string `json:"path,omitempty"`        // ws/http path
	HostHeader  string `json:"host_header,omitempty"` // ws/http Host header (query "host")
	ServiceName string `json:"service_name,omitempty"`

	Security      string `json:"security,omitempty"` // none/tls/reality
	SNI           string `json:"sni,omitempty"`
	ALPN          string `json:"alpn,omitempty"`
	Fingerprint   string `json:"fingerprint,omitempty"`
	AllowInsecure bool   `json:"allow_insecure,omitempty"`
	// PinnedCert is the SHA-256 of the server's certificate (pcs= in VLESS and
	// Trojan links): what xray offers in place of allowInsecure.
	PinnedCert string `json:"pinned_cert,omitempty"`

	Flow      string `json:"flow,omitempty"`       // vless reality/xtls
	PublicKey string `json:"public_key,omitempty"` // reality pbk
	ShortID   string `json:"short_id,omitempty"`   // reality sid

	AlterID    int    `json:"alter_id,omitempty"`    // vmess
	Encryption string `json:"encryption,omitempty"`  // vmess scy (auto/none/aes-128-gcm/…)
	HeaderType string `json:"header_type,omitempty"` // vmess tcp obfs (none/http)

	Method string `json:"method,omitempty"` // ss cipher

	ObfsType     string `json:"obfs_type,omitempty"`     // hysteria2 salamander
	ObfsPassword string `json:"obfs_password,omitempty"` // hysteria2

	CongestionControl string `json:"congestion_control,omitempty"` // tuic
	UDPRelayMode      string `json:"udp_relay_mode,omitempty"`     // tuic

	// Fields other clients offer that this form did not. They were carried
	// through untouched by Extra, which stops a config breaking when it is
	// edited but leaves no way to change them; these have boxes now.
	SpiderX  string `json:"spider_x,omitempty"`  // vless reality spx
	GRPCMode string `json:"grpc_mode,omitempty"` // gun | multi (also the xhttp mode)
	// mKCP's disguise and shared secret. Neither is tuning: get either wrong and
	// the far end sees noise.
	HeaderTypeKCP string `json:"header_type_kcp,omitempty"`
	Seed          string `json:"seed,omitempty"`
	PinSHA256     string `json:"pin_sha256,omitempty"` // hysteria2 certificate pin
	UpMbps        string `json:"up_mbps,omitempty"`    // hysteria2 bandwidth hint
	DownMbps      string `json:"down_mbps,omitempty"`  // hysteria2 bandwidth hint
	// PortHopping is hysteria2's "try these ports too" range ("443-450,8443"),
	// spelled mport by most producers and ports by the rest.
	PortHopping string `json:"port_hopping,omitempty"`
	// SSPlugin is the shadowsocks plugin and its options, exactly as the link
	// carries them: "obfs-local;obfs=tls;obfs-host=example.com".
	SSPlugin string `json:"ss_plugin,omitempty"`

	// WireGuard. The private key goes in the identity field the form already
	// has; the rest have no counterpart in any other protocol because no other
	// protocol configures an interface.
	PeerPublicKey string `json:"peer_public_key,omitempty"`
	PreSharedKey  string `json:"pre_shared_key,omitempty"`
	// Addresses are the interface's own, comma separated, in CIDR form. There is
	// no DHCP in WireGuard: without one the tunnel comes up and carries nothing.
	Addresses string `json:"addresses,omitempty"`
	Reserved  string `json:"reserved,omitempty"` // Cloudflare's three bytes: "1,2,3"
	MTU       int    `json:"mtu,omitempty"`

	// Extra is every query parameter of the original link, kept verbatim.
	//
	// A form can only have boxes for what it knows about, and a share link can
	// carry anything: an obfuscation plugin, a certificate pin, a REALITY
	// spiderX, a transport mode, a port-hopping range. Editing the NAME of such
	// a config used to drop all of it, silently, and the config then stopped
	// working for reasons nothing on screen explained.
	//
	// So the whole query is carried through the form and written back, and the
	// fields above win where they overlap: what the user edited is what they
	// meant, and what they never saw survives untouched.
	Extra map[string]string `json:"extra,omitempty"`

	// JSON is the outbound object itself, for Protocol "json": an xray or a
	// sing-box outbound, or a whole configuration with one server in it. None
	// of the fields above apply to it — see jsonconfig.go.
	JSON string `json:"json,omitempty"`
}

// linkQuery returns the query parameters of a share link, flattened. Written by
// hand rather than with url.Parse because half of these links are not valid URLs
// — an ss:// body is base64, a password can carry a "/" — and all that is wanted
// is the part between "?" and "#".
func linkQuery(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "#"); i >= 0 {
		raw = raw[:i]
	}
	i := strings.Index(raw, "?")
	if i < 0 {
		return nil
	}
	vals, err := url.ParseQuery(raw[i+1:])
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(vals))
	for k, v := range vals {
		if len(v) > 0 && strings.TrimSpace(v[0]) != "" {
			out[k] = v[0]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyExtra writes back the parameters the form has no box for, without
// touching anything it does.
func applyExtra(q url.Values, extra map[string]string) {
	for k, v := range extra {
		if q.Get(k) == "" {
			q.Set(k, v)
		}
	}
}

// hostPort renders "host:port", bracketing IPv6 literals.
func hostPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return host + ":" + strconv.Itoa(port)
}

// setNonEmpty adds k=v to the query only when v isn't empty.
func setNonEmpty(q url.Values, k, v string) {
	if v != "" {
		q.Set(k, v)
	}
}

// formToURL serialises a ConfigForm into the protocol's standard share-link URL.
// It's the inverse of parseNode; a round-trip (parse→form→url→parse) is covered
// by TestConfigFormRoundTrip.
func formToURL(f ConfigForm) (string, error) {
	f.Name = strings.TrimSpace(f.Name)
	if f.Protocol == "json" {
		return jsonFormToURL(f)
	}
	f.Address = strings.TrimSpace(f.Address)
	if f.Address == "" {
		return "", fmt.Errorf("address is required")
	}
	if err := checkAddress(f.Address); err != nil {
		return "", err
	}
	if f.Port < 1 || f.Port > 65535 {
		return "", fmt.Errorf("port must be 1–65535")
	}
	switch f.Protocol {
	case "vless":
		return serializeVless(f)
	case "trojan":
		return serializeTrojan(f)
	case "vmess":
		return serializeVmess(f)
	case "ss", "shadowsocks":
		return serializeSS(f)
	case "hysteria2", "hy2":
		return serializeHysteria2(f)
	case "tuic":
		return serializeTuic(f)
	case "socks", "http":
		return serializeProxy(f)
	case "wireguard", "wg":
		return serializeWireGuardForm(f)
	default:
		return "", fmt.Errorf("unknown protocol %q", f.Protocol)
	}
}

func serializeVless(f ConfigForm) (string, error) {
	if f.UUID == "" {
		return "", fmt.Errorf("UUID is required")
	}
	q := url.Values{}
	setNonEmpty(q, "type", f.Network)
	setNonEmpty(q, "security", f.Security)
	setNonEmpty(q, "path", f.Path)
	setNonEmpty(q, "host", f.HostHeader)
	setNonEmpty(q, "serviceName", f.ServiceName)
	setNonEmpty(q, "sni", f.SNI)
	setNonEmpty(q, "alpn", f.ALPN)
	setNonEmpty(q, "fp", f.Fingerprint)
	setNonEmpty(q, "flow", f.Flow)
	setNonEmpty(q, "pbk", f.PublicKey)
	setNonEmpty(q, "sid", f.ShortID)
	setNonEmpty(q, "spx", f.SpiderX)
	setNonEmpty(q, "mode", f.GRPCMode)
	setNonEmpty(q, "seed", f.Seed)
	setNonEmpty(q, "headerType", f.HeaderTypeKCP)
	if f.AllowInsecure {
		q.Set("allowInsecure", "1")
	}
	if err := setCertPin(q, f.PinnedCert); err != nil {
		return "", err
	}
	applyExtra(q, f.Extra)
	u := url.URL{Scheme: "vless", User: url.User(f.UUID), Host: hostPort(f.Address, f.Port), RawQuery: q.Encode(), Fragment: f.Name}
	return u.String(), nil
}

func serializeTrojan(f ConfigForm) (string, error) {
	if f.Password == "" {
		return "", fmt.Errorf("password is required")
	}
	q := url.Values{}
	setNonEmpty(q, "security", f.Security)
	setNonEmpty(q, "type", f.Network)
	setNonEmpty(q, "path", f.Path)
	setNonEmpty(q, "host", f.HostHeader)
	setNonEmpty(q, "serviceName", f.ServiceName)
	setNonEmpty(q, "sni", f.SNI)
	setNonEmpty(q, "alpn", f.ALPN)
	setNonEmpty(q, "fp", f.Fingerprint)
	setNonEmpty(q, "pbk", f.PublicKey)
	setNonEmpty(q, "sid", f.ShortID)
	setNonEmpty(q, "mode", f.GRPCMode)
	setNonEmpty(q, "seed", f.Seed)
	setNonEmpty(q, "headerType", f.HeaderTypeKCP)
	if f.AllowInsecure {
		q.Set("allowInsecure", "1")
	}
	if err := setCertPin(q, f.PinnedCert); err != nil {
		return "", err
	}
	applyExtra(q, f.Extra)
	// The trojan parser hand-carves the URL (password taken literally up to the
	// LAST '@', fragment taken raw), so we assemble it by hand to match.
	var b strings.Builder
	b.WriteString("trojan://")
	b.WriteString(f.Password)
	b.WriteString("@")
	b.WriteString(hostPort(f.Address, f.Port))
	if enc := q.Encode(); enc != "" {
		b.WriteString("?")
		b.WriteString(enc)
	}
	if f.Name != "" {
		b.WriteString("#")
		b.WriteString(f.Name)
	}
	return b.String(), nil
}

func serializeVmess(f ConfigForm) (string, error) {
	if f.UUID == "" {
		return "", fmt.Errorf("UUID is required")
	}
	tls := ""
	if f.Security == "tls" || f.Security == "reality" {
		tls = "tls"
	}
	l := vmessLink{
		V: "2", Ps: f.Name, Add: f.Address, Port: f.Port, ID: f.UUID,
		Aid: f.AlterID, Scy: f.Encryption, Net: f.Network, Type: f.HeaderType,
		Host: f.HostHeader, Path: f.Path, TLS: tls, SNI: f.SNI, ALPN: f.ALPN, FP: f.Fingerprint,
	}
	if l.Net == "grpc" && l.Path == "" {
		l.Path = f.ServiceName
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(raw), nil
}

func serializeSS(f ConfigForm) (string, error) {
	if f.Method == "" {
		return "", fmt.Errorf("method is required")
	}
	if f.Password == "" {
		return "", fmt.Errorf("password is required")
	}
	// SIP002: base64url(method:password) in the userinfo.
	userinfo := base64.RawURLEncoding.EncodeToString([]byte(f.Method + ":" + f.Password))
	var b strings.Builder
	b.WriteString("ss://")
	b.WriteString(userinfo)
	b.WriteString("@")
	b.WriteString(hostPort(f.Address, f.Port))
	// The plugin (obfs-local, v2ray-plugin, …) lives in the query, and this is
	// the one serializer with no query of its own — so dropping it produced a
	// link that parsed cleanly and could not connect.
	q := url.Values{}
	setNonEmpty(q, "plugin", f.SSPlugin)
	applyExtra(q, f.Extra)
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

func serializeHysteria2(f ConfigForm) (string, error) {
	if f.Password == "" {
		return "", fmt.Errorf("password is required")
	}
	q := url.Values{}
	setNonEmpty(q, "sni", f.SNI)
	setNonEmpty(q, "alpn", f.ALPN)
	setNonEmpty(q, "obfs", f.ObfsType)
	setNonEmpty(q, "obfs-password", f.ObfsPassword)
	setNonEmpty(q, "pinSHA256", f.PinSHA256)
	setNonEmpty(q, "up", f.UpMbps)
	setNonEmpty(q, "down", f.DownMbps)
	setNonEmpty(q, "mport", f.PortHopping)
	if f.AllowInsecure {
		q.Set("insecure", "1")
	}
	applyExtra(q, f.Extra)
	u := url.URL{Scheme: "hysteria2", User: url.User(f.Password), Host: hostPort(f.Address, f.Port), RawQuery: q.Encode(), Fragment: f.Name}
	return u.String(), nil
}

func serializeTuic(f ConfigForm) (string, error) {
	if f.UUID == "" {
		return "", fmt.Errorf("UUID is required")
	}
	q := url.Values{}
	setNonEmpty(q, "sni", f.SNI)
	setNonEmpty(q, "alpn", f.ALPN)
	setNonEmpty(q, "congestion_control", f.CongestionControl)
	setNonEmpty(q, "udp_relay_mode", f.UDPRelayMode)
	if f.AllowInsecure {
		q.Set("allow_insecure", "1")
	}
	applyExtra(q, f.Extra)
	u := url.URL{Scheme: "tuic", User: url.UserPassword(f.UUID, f.Password), Host: hostPort(f.Address, f.Port), RawQuery: q.Encode(), Fragment: f.Name}
	return u.String(), nil
}

// configToForm maps a parsed Node back into the flat form (edit/view prefill).
func configToForm(n *Node) ConfigForm {
	if n.Kind == KindJSON && n.JSON != nil {
		return ConfigForm{Protocol: "json", Name: n.Name, JSON: prettyJSON(n.JSON.Outbound)}
	}
	f := ConfigForm{Name: n.Name, Address: n.Host, Port: n.Port}
	// Parameters the parsers do not keep but the form now edits, read straight
	// off the link. Anything still not named here stays in Extra and survives
	// untouched — the two together are why an edit can no longer lose anything.
	q := linkQuery(n.Raw)
	f.GRPCMode = q["mode"]
	f.Seed = q["seed"]
	// vmess spells its TCP obfuscation "type" and keeps it in the JSON body; the
	// query "headerType" is the mKCP disguise and belongs to a different box.
	f.HeaderTypeKCP = q["headerType"]
	f.SpiderX = q["spx"]
	f.PinSHA256 = q["pinSHA256"]
	f.UpMbps, f.DownMbps = q["up"], q["down"]
	if f.PortHopping = q["mport"]; f.PortHopping == "" {
		f.PortHopping = q["ports"]
	}
	f.SSPlugin = q["plugin"]
	switch {
	case n.Vless != nil:
		p := n.Vless
		f.Protocol, f.UUID = "vless", p.UUID
		f.Network, f.Security = shownNetwork(p.Network), p.Security
		f.Path, f.HostHeader, f.ServiceName = p.Path, p.Host2, p.ServiceName
		f.SNI, f.ALPN, f.Fingerprint, f.AllowInsecure = p.SNI, p.ALPN, p.Fingerprint, p.AllowInsecure
		f.PinnedCert = p.PinnedCertSHA256
		f.Flow, f.PublicKey, f.ShortID = p.Flow, p.PublicKey, p.ShortID
	case n.Trojan != nil:
		p := n.Trojan
		f.Protocol, f.Password = "trojan", p.Password
		f.Network, f.Security = shownNetwork(p.Network), p.Security
		f.Path, f.HostHeader, f.ServiceName = p.Path, p.Host2, p.ServiceName
		f.SNI, f.ALPN, f.Fingerprint, f.AllowInsecure = p.SNI, p.ALPN, p.Fingerprint, p.AllowInsecure
		f.PinnedCert = p.PinnedCertSHA256
		f.PublicKey, f.ShortID = p.PublicKey, p.ShortID // trojan has no flow field
	case n.Vmess != nil:
		p := n.Vmess
		f.Protocol, f.UUID = "vmess", p.UUID
		f.Network, f.Security = shownNetwork(p.Network), p.Security
		// VmessParams.ServiceName is just a copy of Path (grpc producers cram
		// serviceName into "path"), so we don't map it back — Path carries it.
		f.Path, f.HostHeader = p.Path, p.Host2
		f.SNI, f.ALPN, f.Fingerprint = p.SNI, p.ALPN, p.Fingerprint
		f.AlterID, f.Encryption, f.HeaderType = p.AlterID, p.Scy, p.HeaderType
	case n.SS != nil:
		p := n.SS
		f.Protocol, f.Method, f.Password = "ss", p.Method, p.Password
	case n.Hysteria2 != nil:
		p := n.Hysteria2
		f.Protocol, f.Password = "hysteria2", p.Password
		f.SNI, f.ALPN, f.AllowInsecure = p.SNI, p.ALPN, p.Insecure
		f.ObfsType, f.ObfsPassword = p.ObfsType, p.ObfsPassword
	case n.TUIC != nil:
		p := n.TUIC
		f.Protocol, f.UUID, f.Password = "tuic", p.UUID, p.Password
		f.SNI, f.ALPN, f.AllowInsecure = p.SNI, p.ALPN, p.Insecure
		f.CongestionControl, f.UDPRelayMode = p.CongestionControl, p.UDPRelayMode
	case n.WireGuard != nil:
		p := n.WireGuard
		f.Protocol = "wireguard"
		f.UUID = p.PrivateKey
		f.PeerPublicKey, f.PreSharedKey = p.PublicKey, p.PreShared
		f.Addresses = strings.Join(p.Addresses, ",")
		f.MTU = p.MTU
		if len(p.Reserved) > 0 {
			parts := make([]string, 0, len(p.Reserved))
			for _, r := range p.Reserved {
				parts = append(parts, strconv.Itoa(r))
			}
			f.Reserved = strings.Join(parts, ",")
		}
	case n.Proxy != nil:
		p := n.Proxy
		f.Protocol = string(p.Kind)
		// The user name goes in the identity field the form already has; a plain
		// proxy has no third credential to confuse it with.
		f.UUID, f.Password = p.User, p.Password
		f.SNI, f.AllowInsecure = p.SNI, p.AllowInsecure
		if p.TLS {
			f.Security = "tls"
		}
	}
	f.Extra = unmodelledParams(n.Raw, f)
	return f
}

// unmodelledParams is the part of a link's query that no field of the form
// stands for.
//
// Worked out by asking the serializer rather than from a list per protocol: what
// it writes for this form is what the form models, and everything else in the
// original is what would be lost. A list would be right the day it was written
// and wrong the first time a field was added.
func unmodelledParams(raw string, f ConfigForm) map[string]string {
	orig := linkQuery(raw)
	if len(orig) == 0 {
		return nil
	}
	f.Extra = nil
	out, err := formToURL(f)
	if err != nil {
		// The form cannot be serialised (a required field is missing in the
		// original). Keeping everything beats keeping nothing.
		return orig
	}
	modelled := linkQuery(out)
	rest := make(map[string]string, len(orig))
	for k, v := range orig {
		if _, ours := modelled[k]; !ours {
			rest[k] = v
		}
	}
	if len(rest) == 0 {
		return nil
	}
	return rest
}

// checkAddress refuses an address that cannot be one, before it becomes a
// configuration that fails later with a worse message.
//
// Deliberately not a strict hostname grammar: single-label names are legitimate
// on a local network, and a name with an underscore in it is illegal and widely
// used anyway. What is caught here is what someone typing into a form actually
// gets wrong — a whole URL pasted into the address box, a space, or half an IP
// address, which reads as a hostname to every parser and resolves for nobody.
func checkAddress(addr string) error {
	a := strings.TrimSpace(addr)
	if strings.ContainsAny(a, " \t/\\?#@") {
		return fmt.Errorf("the address should be a host name or an IP, without a scheme or a path")
	}
	if net.ParseIP(strings.Trim(a, "[]")) != nil {
		return nil
	}
	labels := strings.Split(a, ".")
	numeric := true
	for _, l := range labels {
		if l == "" {
			return fmt.Errorf("the address has an empty part: %q", addr)
		}
		for _, r := range l {
			if r < '!' || r > '~' {
				return fmt.Errorf("the address has a character that cannot be in a host name")
			}
			if r < '0' || r > '9' {
				numeric = false
			}
		}
	}
	// All-numeric labels mean somebody meant an IP address and mistyped it:
	// "1.2.3" is a syntactically fine host name that will never resolve.
	if numeric {
		return fmt.Errorf("%q looks like an incomplete IP address", addr)
	}
	return nil
}

// setCertPin writes a certificate pin into a link, refusing one xray could not
// read: the form says so on saving rather than the engine on connecting.
func setCertPin(q url.Values, pin string) error {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return nil
	}
	if !validCertPins(pin) {
		return fmt.Errorf("certificate SHA-256: expected 64 hexadecimal characters")
	}
	q.Set("pcs", pin)
	return nil
}
