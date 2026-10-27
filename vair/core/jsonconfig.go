package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Raw JSON configurations: an outbound written for xray or for sing-box, taken
// as it is.
//
// A share link can only say what its format has a parameter for, and the form
// can only say what the link can. Everything else — xhttp's "extra", a mux
// setting, a TLS fingerprint the link format never named, whatever a provider
// hands out as a JSON file — used to have no way in. Now it does: the outbound
// object itself, stored whole and given to the engine it was written for.
//
// Everything past parsing treats it like any other config. The stored form is a
// link of Vair's own — vair-json://<the outbound, canonical, base64url>#name —
// so testing, AUTO, chains, the database, dedup, export and QR all work
// unchanged, and the same outbound pasted twice is the same link twice.
//
// What is checked, because a JSON object can say things a link cannot:
//   - it goes to a server. An outbound that sends traffic straight out
//     ("freedom", "direct") would connect, pass traffic and report the session
//     as up, while nothing went through a VPN at all. Only protocols that dial
//     a proxy server are accepted.
//   - it goes to ONE server, and it says which. The address is what the
//     kill-switch lets through, what routing keeps out of the tunnel and what
//     is resolved ahead of time under the block; a second server nobody
//     extracted would be a hole or a loop.
//   - it does not go through another outbound (dialerProxy, proxySettings,
//     detour). That would be a chain whose first hop is not this config, and
//     Vair builds chains itself.

// KindJSON is a raw outbound. Which engine runs it is in JSONParams.Engine.
const KindJSON NodeKind = "json"

// jsonScheme is the link form a JSON configuration is stored and shared in.
const jsonScheme = "vair-json://"

// JSONParams is a parsed raw outbound.
type JSONParams struct {
	Engine   string                 // "xray" or "singbox"
	Protocol string                 // the outbound's own protocol, lower case
	Outbound map[string]interface{} // as given, without its tag
	Host     string
	Port     int
	Network  string // for the table: tcp / ws / grpc / xhttp / udp …
	Security string // for the table: none / tls / reality / <ss method> …
}

// Protocols that dial a proxy server, per engine. Everything else is refused.
var (
	xrayJSONProtocols = map[string]bool{
		"vless": true, "vmess": true, "trojan": true, "shadowsocks": true,
		"socks": true, "http": true, "wireguard": true, "hysteria": true,
	}
	singboxJSONTypes = map[string]bool{
		"vless": true, "vmess": true, "trojan": true, "shadowsocks": true,
		"socks": true, "http": true, "hysteria": true, "hysteria2": true,
		"tuic": true, "anytls": true, "naive": true,
	}
	// Outbounds that are not a server at all. Inside a whole configuration
	// they are the routing's helpers and are skipped; given on their own they
	// are refused with the reason.
	notAServer = map[string]string{
		"freedom":   "sends traffic straight out, without a VPN server",
		"direct":    "sends traffic straight out, without a VPN server",
		"blackhole": "drops traffic",
		"block":     "drops traffic",
		"dns":       "answers DNS queries",
		"loopback":  "sends traffic back into the engine",
		"selector":  "chooses between other outbounds",
		"urltest":   "chooses between other outbounds",
	}
)

// parseJSONNode turns a vair-json:// link into a node.
func parseJSONNode(raw string) (*Node, error) {
	body, name := splitFragment(strings.TrimPrefix(strings.TrimSpace(raw), jsonScheme))
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(body, "="))
	if err != nil {
		return nil, fmt.Errorf("json config: the link is damaged (%v)", err)
	}
	ob, err := decodeJSONObject(data)
	if err != nil {
		return nil, err
	}
	p, err := inspectOutbound(ob)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = defaultJSONName(p)
	}
	return &Node{
		Kind:     KindJSON,
		Raw:      raw,
		Name:     name,
		Host:     p.Host,
		Port:     p.Port,
		Network:  p.Network,
		Security: p.Security,
		JSON:     p,
	}, nil
}

func decodeJSONObject(data []byte) (map[string]interface{}, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber() // numbers stay exactly as written
	var ob map[string]interface{}
	if err := d.Decode(&ob); err != nil {
		return nil, fmt.Errorf("json config: not a JSON object (%v)", err)
	}
	return ob, nil
}

// inspectOutbound decides whether an outbound is one Vair can run, and pulls
// out what the rest of the program needs to know about it.
func inspectOutbound(ob map[string]interface{}) (*JSONParams, error) {
	if ob == nil {
		return nil, fmt.Errorf("json config: empty")
	}
	p := &JSONParams{Outbound: withoutTag(ob)}
	var settings map[string]interface{}
	switch {
	case jsonStr(ob["protocol"]) != "":
		p.Engine = "xray"
		p.Protocol = strings.ToLower(jsonStr(ob["protocol"]))
		settings, _ = ob["settings"].(map[string]interface{})
	case jsonStr(ob["type"]) != "":
		p.Engine = "singbox"
		p.Protocol = strings.ToLower(jsonStr(ob["type"]))
	default:
		return nil, fmt.Errorf("json config: this is not an outbound — it names neither a \"protocol\" (xray) nor a \"type\" (sing-box)")
	}
	if why, ok := notAServer[p.Protocol]; ok {
		return nil, fmt.Errorf("json config: a %q outbound %s — only outbounds that go to a VPN server are accepted", p.Protocol, why)
	}
	allowed := xrayJSONProtocols
	if p.Engine == "singbox" {
		allowed = singboxJSONTypes
	}
	if !allowed[p.Protocol] {
		return nil, fmt.Errorf("json config: %s outbound %q is not one Vair can run", engineLabel(p.Engine), p.Protocol)
	}
	if via := chainedThrough(p.Engine, ob); via != "" {
		return nil, fmt.Errorf("json config: this outbound connects through another one (%s) — build chains with Vair's Chain instead", via)
	}

	var err error
	if p.Engine == "xray" {
		p.Host, p.Port, err = xrayJSONServer(p.Protocol, settings)
	} else {
		p.Host, p.Port = jsonStr(ob["server"]), jsonInt(ob["server_port"])
	}
	if err != nil {
		return nil, err
	}
	if p.Host == "" {
		return nil, fmt.Errorf("json config: the server address is missing")
	}
	if p.Port < 1 || p.Port > 65535 {
		return nil, fmt.Errorf("json config: the server port is missing or out of range")
	}
	p.Network, p.Security = jsonTransport(p, settings)
	return p, nil
}

func engineLabel(engine string) string {
	if engine == "singbox" {
		return "sing-box"
	}
	return "xray"
}

// chainedThrough names the field that sends this outbound through another,
// or "" when it dials its server itself.
func chainedThrough(engine string, ob map[string]interface{}) string {
	if engine == "singbox" {
		if jsonStr(ob["detour"]) != "" {
			return "detour"
		}
		return ""
	}
	if ps, ok := ob["proxySettings"].(map[string]interface{}); ok && jsonStr(ps["tag"]) != "" {
		return "proxySettings"
	}
	if ss, ok := ob["streamSettings"].(map[string]interface{}); ok {
		if so, ok := ss["sockopt"].(map[string]interface{}); ok && jsonStr(so["dialerProxy"]) != "" {
			return "dialerProxy"
		}
	}
	return ""
}

// xrayJSONServer finds the one server an xray outbound dials, in whichever of
// its shapes the outbound is written: the flat settings.address of newer xray,
// vnext for vless/vmess, servers for the rest, peers for WireGuard.
func xrayJSONServer(proto string, settings map[string]interface{}) (string, int, error) {
	if settings == nil {
		return "", 0, fmt.Errorf("json config: the outbound has no \"settings\"")
	}
	if a := jsonStr(settings["address"]); a != "" {
		return a, jsonInt(settings["port"]), nil
	}
	if proto == "wireguard" {
		peers, _ := settings["peers"].([]interface{})
		if len(peers) != 1 {
			return "", 0, fmt.Errorf("json config: a WireGuard outbound needs exactly one peer, this one has %d", len(peers))
		}
		peer, _ := peers[0].(map[string]interface{})
		h, ps, err := net.SplitHostPort(jsonStr(peer["endpoint"]))
		if err != nil {
			return "", 0, fmt.Errorf("json config: the peer's endpoint is not host:port")
		}
		port, _ := strconv.Atoi(ps)
		return h, port, nil
	}
	key := "servers"
	if proto == "vless" || proto == "vmess" {
		key = "vnext"
	}
	list, _ := settings[key].([]interface{})
	if len(list) != 1 {
		return "", 0, fmt.Errorf("json config: settings.%s lists %d servers — give exactly one, the one this config connects to", key, len(list))
	}
	s, _ := list[0].(map[string]interface{})
	return jsonStr(s["address"]), jsonInt(s["port"]), nil
}

// jsonTransport is what the table shows in its TRANSPORT and SECURITY columns.
func jsonTransport(p *JSONParams, settings map[string]interface{}) (network, security string) {
	ob := p.Outbound
	if p.Engine == "xray" {
		switch p.Protocol {
		case "wireguard":
			return "udp", "wireguard"
		case "hysteria":
			return "udp", "tls"
		}
		network, security = "tcp", "none"
		if ss, ok := ob["streamSettings"].(map[string]interface{}); ok {
			if n := strings.ToLower(jsonStr(ss["network"])); n != "" && n != "raw" {
				network = n
			}
			if s := strings.ToLower(jsonStr(ss["security"])); s != "" {
				security = s
			}
		}
		if p.Protocol == "shadowsocks" {
			if m := xrayJSONMethod(settings); m != "" {
				security = m
			}
		}
		return network, security
	}
	network, security = "tcp", "none"
	switch p.Protocol {
	case "hysteria", "hysteria2", "tuic":
		network = "udp"
	}
	if tr, ok := ob["transport"].(map[string]interface{}); ok {
		if t := strings.ToLower(jsonStr(tr["type"])); t != "" {
			network = t
		}
	}
	if tls, ok := ob["tls"].(map[string]interface{}); ok && jsonBool(tls["enabled"]) {
		security = "tls"
		if r, ok := tls["reality"].(map[string]interface{}); ok && jsonBool(r["enabled"]) {
			security = "reality"
		}
	}
	if p.Protocol == "shadowsocks" {
		if m := strings.ToLower(jsonStr(ob["method"])); m != "" {
			security = m
		}
	}
	return network, security
}

func xrayJSONMethod(settings map[string]interface{}) string {
	if settings == nil {
		return ""
	}
	if m := jsonStr(settings["method"]); m != "" {
		return strings.ToLower(m)
	}
	if list, ok := settings["servers"].([]interface{}); ok && len(list) == 1 {
		if s, ok := list[0].(map[string]interface{}); ok {
			return strings.ToLower(jsonStr(s["method"]))
		}
	}
	return ""
}

func defaultJSONName(p *JSONParams) string {
	return strings.ToUpper(p.Protocol) + " " + net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
}

// encodeJSONLink stores an outbound as a vair-json:// link. The body is the
// outbound without its tag, with keys sorted — the same outbound written twice
// is the same link, and dedup sees one config.
func encodeJSONLink(ob map[string]interface{}, name string) string {
	b, _ := json.Marshal(withoutTag(ob))
	raw := jsonScheme + base64.RawURLEncoding.EncodeToString(b)
	if name = strings.TrimSpace(name); name != "" {
		raw += "#" + url.QueryEscape(name)
	}
	return raw
}

// jsonConfigsInText turns a pasted or downloaded JSON document into
// vair-json:// links. ok is false when the text is not JSON at all, and the
// caller treats it as ordinary text.
//
// Accepted: one outbound (xray or sing-box); a whole configuration, from which
// every outbound that goes to a server is taken; and an array of either — the
// shape of the JSON subscriptions some providers and v2rayNG hand out. In a
// whole configuration the routing helpers (direct, block, dns …) are skipped;
// everything else becomes a row, and one Vair cannot run shows why it cannot.
func jsonConfigsInText(text string) (links []string, ok bool) {
	t := strings.TrimSpace(strings.TrimPrefix(text, string(rune(0xFEFF))))
	if t == "" || (t[0] != '{' && t[0] != '[') {
		return nil, false
	}
	d := json.NewDecoder(strings.NewReader(t))
	d.UseNumber()
	var v interface{}
	if err := d.Decode(&v); err != nil {
		return nil, false
	}
	var docs []map[string]interface{}
	switch x := v.(type) {
	case map[string]interface{}:
		docs = append(docs, x)
	case []interface{}:
		for _, e := range x {
			if m, ok := e.(map[string]interface{}); ok {
				docs = append(docs, m)
			}
		}
	}
	for _, doc := range docs {
		links = append(links, jsonDocLinks(doc)...)
	}
	return links, true
}

func jsonDocLinks(doc map[string]interface{}) []string {
	remarks := strings.TrimSpace(jsonStr(doc["remarks"]))
	outs, whole := doc["outbounds"].([]interface{})
	// An object that names its own protocol is an outbound, even one with an
	// "outbounds" list of its own — sing-box's selector has one.
	if !whole || jsonStr(doc["protocol"]) != "" || jsonStr(doc["type"]) != "" {
		return []string{encodeJSONLink(doc, firstNonEmpty(remarks, jsonStr(doc["tag"])))}
	}
	var servers []map[string]interface{}
	for _, o := range outs {
		m, ok := o.(map[string]interface{})
		if !ok {
			continue
		}
		kind := strings.ToLower(firstNonEmpty(jsonStr(m["protocol"]), jsonStr(m["type"])))
		if _, helper := notAServer[kind]; helper || kind == "" {
			continue
		}
		servers = append(servers, m)
	}
	var links []string
	for _, m := range servers {
		name := remarks
		if tag := jsonStr(m["tag"]); len(servers) > 1 || name == "" {
			name = strings.TrimSpace(remarks + " " + tag)
		}
		links = append(links, encodeJSONLink(m, name))
	}
	return links
}

// preResolveJSON is preResolveHost for a raw outbound: the server's name is
// resolved here, by this program, and written into the outbound as an address,
// so the engine never has to look it up — under a standing block its lookups
// would not get out. With TLS, the name moves into the server name the
// certificate is checked against, unless one is already set.
func preResolveJSON(p *JSONParams) error {
	host := p.Host
	sni := jsonSNI(p)
	before := sni
	if err := resolveHostIntoSNI(&host, &sni); err != nil {
		return err
	}
	if host == p.Host {
		return nil
	}
	setJSONServer(p, host)
	if sni != before && jsonUsesTLS(p) {
		setJSONSNI(p, sni)
	}
	p.Host = host
	return nil
}

func jsonUsesTLS(p *JSONParams) bool {
	if p.Engine == "xray" {
		ss, _ := p.Outbound["streamSettings"].(map[string]interface{})
		return strings.EqualFold(jsonStr(ss["security"]), "tls")
	}
	tls, _ := p.Outbound["tls"].(map[string]interface{})
	return jsonBool(tls["enabled"]) && !jsonBool(jsonMap(tls, "reality")["enabled"])
}

func jsonSNI(p *JSONParams) string {
	if p.Engine == "xray" {
		ss, _ := p.Outbound["streamSettings"].(map[string]interface{})
		return jsonStr(jsonMap(ss, "tlsSettings")["serverName"])
	}
	return jsonStr(jsonMap(p.Outbound, "tls")["server_name"])
}

func setJSONSNI(p *JSONParams, sni string) {
	if p.Engine == "xray" {
		ss := ensureMap(p.Outbound, "streamSettings")
		ensureMap(ss, "tlsSettings")["serverName"] = sni
		return
	}
	ensureMap(p.Outbound, "tls")["server_name"] = sni
}

// setJSONServer writes the server address back where inspectOutbound found it.
func setJSONServer(p *JSONParams, host string) {
	if p.Engine == "singbox" {
		p.Outbound["server"] = host
		return
	}
	settings, _ := p.Outbound["settings"].(map[string]interface{})
	if settings == nil {
		return
	}
	if jsonStr(settings["address"]) != "" {
		settings["address"] = host
		return
	}
	if p.Protocol == "wireguard" {
		if peers, _ := settings["peers"].([]interface{}); len(peers) == 1 {
			if peer, ok := peers[0].(map[string]interface{}); ok {
				peer["endpoint"] = net.JoinHostPort(host, strconv.Itoa(p.Port))
			}
		}
		return
	}
	key := "servers"
	if p.Protocol == "vless" || p.Protocol == "vmess" {
		key = "vnext"
	}
	if list, _ := settings[key].([]interface{}); len(list) == 1 {
		if s, ok := list[0].(map[string]interface{}); ok {
			s["address"] = host
		}
	}
}

// jsonOutboundFor is the outbound handed to an engine: a copy, since the
// builders write into it (the tag, the fragment dialer, a chain's detour), and
// the node's own must stay as parsed.
func jsonOutboundFor(p *JSONParams, engine string) map[string]interface{} {
	if p == nil || p.Engine != engine {
		return nil
	}
	ob := cloneJSONValue(p.Outbound).(map[string]interface{})
	ob["tag"] = "proxy"
	return ob
}

// jsonFormToURL is formToURL for a JSON config: the text in the form must hold
// exactly one outbound that goes to a server. A whole configuration with
// several is for the paste, which adds one row for each.
func jsonFormToURL(f ConfigForm) (string, error) {
	if strings.TrimSpace(f.JSON) == "" {
		return "", fmt.Errorf("paste an xray or sing-box outbound, or a whole configuration")
	}
	links, ok := jsonConfigsInText(f.JSON)
	if !ok {
		return "", fmt.Errorf("this is not valid JSON")
	}
	switch len(links) {
	case 0:
		return "", fmt.Errorf("no outbound here goes to a server")
	case 1:
	default:
		return "", fmt.Errorf("this holds %d servers — the form adds one; paste the whole text with “Paste configs” to add all of them", len(links))
	}
	n, err := parseNode(links[0])
	if err != nil {
		return "", err
	}
	name := f.Name
	if name == "" {
		name = n.Name
	}
	return encodeJSONLink(n.JSON.Outbound, name), nil
}

// prettyJSON is the outbound as the form shows it.
func prettyJSON(ob map[string]interface{}) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(ob)
	return strings.TrimSpace(b.String())
}

// jsonSummary is one line on what a JSON config was recognised as.
func jsonSummary(n *Node) string {
	if n == nil || n.JSON == nil {
		return ""
	}
	p := n.JSON
	return fmt.Sprintf("%s · %s · %s · %s / %s", engineLabel(p.Engine), p.Protocol,
		net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), p.Network, p.Security)
}

// ── small helpers over decoded JSON ─────────────────────────────────────────

func withoutTag(ob map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(ob))
	for k, v := range ob {
		if k != "tag" {
			out[k] = v
		}
	}
	return out
}

func cloneJSONValue(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		m := make(map[string]interface{}, len(x))
		for k, e := range x {
			m[k] = cloneJSONValue(e)
		}
		return m
	case []interface{}:
		s := make([]interface{}, len(x))
		for i, e := range x {
			s[i] = cloneJSONValue(e)
		}
		return s
	}
	return v
}

func jsonMap(m map[string]interface{}, key string) map[string]interface{} {
	if m == nil {
		return nil
	}
	out, _ := m[key].(map[string]interface{})
	return out
}

func ensureMap(m map[string]interface{}, key string) map[string]interface{} {
	if out, ok := m[key].(map[string]interface{}); ok {
		return out
	}
	out := map[string]interface{}{}
	m[key] = out
	return out
}

func jsonStr(v interface{}) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	}
	return ""
}

func jsonInt(v interface{}) int {
	switch x := v.(type) {
	case json.Number:
		n, _ := strconv.Atoi(x.String())
		return n
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	}
	return 0
}

func jsonBool(v interface{}) bool {
	b, _ := v.(bool)
	return b
}
