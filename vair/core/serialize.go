package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	f.Address = strings.TrimSpace(f.Address)
	if f.Address == "" {
		return "", fmt.Errorf("address is required")
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
	if f.AllowInsecure {
		q.Set("allowInsecure", "1")
	}
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
	if f.AllowInsecure {
		q.Set("allowInsecure", "1")
	}
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
	if f.AllowInsecure {
		q.Set("insecure", "1")
	}
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
	u := url.URL{Scheme: "tuic", User: url.UserPassword(f.UUID, f.Password), Host: hostPort(f.Address, f.Port), RawQuery: q.Encode(), Fragment: f.Name}
	return u.String(), nil
}

// configToForm maps a parsed Node back into the flat form (edit/view prefill).
func configToForm(n *Node) ConfigForm {
	f := ConfigForm{Name: n.Name, Address: n.Host, Port: n.Port}
	switch {
	case n.Vless != nil:
		p := n.Vless
		f.Protocol, f.UUID = "vless", p.UUID
		f.Network, f.Security = p.Network, p.Security
		f.Path, f.HostHeader, f.ServiceName = p.Path, p.Host2, p.ServiceName
		f.SNI, f.ALPN, f.Fingerprint, f.AllowInsecure = p.SNI, p.ALPN, p.Fingerprint, p.AllowInsecure
		f.Flow, f.PublicKey, f.ShortID = p.Flow, p.PublicKey, p.ShortID
	case n.Trojan != nil:
		p := n.Trojan
		f.Protocol, f.Password = "trojan", p.Password
		f.Network, f.Security = p.Network, p.Security
		f.Path, f.HostHeader, f.ServiceName = p.Path, p.Host2, p.ServiceName
		f.SNI, f.ALPN, f.Fingerprint, f.AllowInsecure = p.SNI, p.ALPN, p.Fingerprint, p.AllowInsecure
		f.PublicKey, f.ShortID = p.PublicKey, p.ShortID // trojan has no flow field
	case n.Vmess != nil:
		p := n.Vmess
		f.Protocol, f.UUID = "vmess", p.UUID
		f.Network, f.Security = p.Network, p.Security
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
	}
	return f
}
