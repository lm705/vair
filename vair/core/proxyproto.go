package core

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// A plain SOCKS5 or HTTP proxy as a configuration.
//
// Not a tunnel and not pretending to be one: no encryption of its own, no
// obfuscation, nothing that resists inspection. What it is good for is the case
// the rest of this program cannot cover — a proxy somebody hands you as a host,
// a port and a password, on a corporate network or from a provider that sells
// nothing else. Both engines have spoken these two protocols all along; only the
// share link, the form and the type pill were missing.
//
// The link format is the one every client in this family uses:
//
//	socks://user:pass@host:port#name
//	socks5://host:port#name
//	http://user:pass@host:port#name
//	socks://BASE64(user:pass)@host:port#name   (a spelling some panels emit)
//
// The base64 form is why this cannot lean on url.Parse alone: the userinfo is
// not percent-encoded, and a password with a "/" or an "@" in it — which is
// most generated ones — makes the whole URL invalid.

// ProxyParams is a SOCKS/HTTP proxy node.
type ProxyParams struct {
	Raw      string
	Kind     NodeKind // KindSOCKS or KindHTTP
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	// TLS wraps the connection to the PROXY, which the http scheme calls https
	// and socks calls nothing at all. Rare, but it is the difference between
	// credentials on the wire and credentials not on the wire.
	TLS bool
	SNI string
	// AllowInsecure skips the certificate check on that wrapper.
	AllowInsecure bool
}

// parseProxyLink reads socks://, socks5://, http:// and https:// links.
func parseProxyLink(raw string) (*ProxyParams, error) {
	raw = strings.TrimSpace(raw)
	p := &ProxyParams{Raw: raw}

	scheme := ""
	for _, s := range []string{"socks5://", "socks://", "https://", "http://"} {
		if strings.HasPrefix(raw, s) {
			scheme = s
			break
		}
	}
	if scheme == "" {
		return nil, fmt.Errorf("not a proxy link")
	}
	body := raw[len(scheme):]
	switch scheme {
	case "https://":
		p.Kind, p.TLS = KindHTTP, true
	case "http://":
		p.Kind = KindHTTP
	default:
		p.Kind = KindSOCKS
	}

	// #name last, so a "#" inside a password cannot be mistaken for it… which is
	// why the fragment is taken from the END.
	if i := strings.LastIndex(body, "#"); i >= 0 {
		name, err := url.QueryUnescape(body[i+1:])
		if err != nil {
			name = body[i+1:]
		}
		p.Name = name
		body = body[:i]
	}
	var query string
	if i := strings.Index(body, "?"); i >= 0 {
		query = body[i+1:]
		body = body[:i]
	}
	// The LAST "@": a password may contain one, a hostname may not.
	if i := strings.LastIndex(body, "@"); i >= 0 {
		p.User, p.Password = splitProxyCreds(body[:i])
		body = body[i+1:]
	}
	body = strings.TrimSuffix(body, "/")
	host, portStr, err := net.SplitHostPort(body)
	if err != nil {
		return nil, fmt.Errorf("expected host:port, got %q", body)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("bad port %q", portStr)
	}
	p.Host, p.Port = strings.Trim(host, "[]"), port
	if p.Host == "" {
		return nil, fmt.Errorf("no host")
	}

	if q, err := url.ParseQuery(query); err == nil {
		p.SNI = q.Get("sni")
		if v := q.Get("security"); v == "tls" {
			p.TLS = true
		}
		if v := q.Get("tls"); v == "1" || v == "true" {
			p.TLS = true
		}
		if v := q.Get("allowInsecure"); v == "1" || v == "true" {
			p.AllowInsecure = true
		}
	}
	return p, nil
}

// splitProxyCreds reads the userinfo, which arrives in two spellings: plain
// "user:pass", or base64 of the same. Percent-decoded first, because a client
// that DID escape its userinfo is also common.
func splitProxyCreds(userinfo string) (user, pass string) {
	if userinfo == "" {
		return "", ""
	}
	if dec, err := url.QueryUnescape(userinfo); err == nil {
		userinfo = dec
	}
	if !strings.Contains(userinfo, ":") {
		// No colon: either a lone username, or base64 hiding one.
		if dec, err := base64.StdEncoding.DecodeString(padB64(userinfo)); err == nil && strings.Contains(string(dec), ":") {
			userinfo = string(dec)
		} else if dec, err := base64.RawURLEncoding.DecodeString(userinfo); err == nil && strings.Contains(string(dec), ":") {
			userinfo = string(dec)
		}
	}
	u, p, _ := strings.Cut(userinfo, ":")
	return u, p
}

// padB64 restores the padding standard base64 needs and share links often drop.
func padB64(s string) string {
	if n := len(s) % 4; n != 0 {
		s += strings.Repeat("=", 4-n)
	}
	return s
}

// serializeProxy is the inverse: form → link.
func serializeProxy(f ConfigForm) (string, error) {
	scheme := "socks"
	if f.Protocol == "http" {
		scheme = "http"
	}
	var b strings.Builder
	b.WriteString(scheme)
	b.WriteString("://")
	if f.UUID != "" || f.Password != "" {
		// UUID carries the username here: the form has one identity field per
		// protocol and this one calls it a user name.
		b.WriteString(url.QueryEscape(f.UUID))
		if f.Password != "" {
			b.WriteString(":")
			b.WriteString(url.QueryEscape(f.Password))
		}
		b.WriteString("@")
	}
	b.WriteString(hostPort(f.Address, f.Port))
	q := url.Values{}
	if f.Security == "tls" {
		q.Set("security", "tls")
	}
	setNonEmpty(q, "sni", f.SNI)
	if f.AllowInsecure {
		q.Set("allowInsecure", "1")
	}
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

// xrayOutboundProxy builds the xray outbound. Both protocols take the same
// shape: one server, one optional account.
func xrayOutboundProxy(p *ProxyParams) map[string]interface{} {
	server := map[string]interface{}{"address": p.Host, "port": p.Port}
	if p.User != "" || p.Password != "" {
		server["users"] = []interface{}{map[string]interface{}{"user": p.User, "pass": p.Password}}
	}
	proto := "socks"
	if p.Kind == KindHTTP {
		proto = "http"
	}
	out := map[string]interface{}{
		"protocol": proto,
		"settings": map[string]interface{}{"servers": []interface{}{server}},
		"tag":      "proxy",
	}
	if p.TLS {
		// No allowInsecure: xray 26 refuses it, and a proxy that asks for it
		// runs on sing-box (engineForNode). The handshake has the browser shape
		// from Settings, as every other TLS configuration does.
		tls := map[string]interface{}{"fingerprint": currentTLSFingerprint()}
		if sni := p.SNI; sni != "" {
			tls["serverName"] = sni
		} else {
			tls["serverName"] = p.Host
		}
		out["streamSettings"] = map[string]interface{}{
			"network":     "tcp",
			"security":    "tls",
			"tlsSettings": tls,
		}
	}
	return out
}

// singboxOutboundProxy is the same for sing-box, which the TUN path needs.
func singboxOutboundProxy(p *ProxyParams, tag string) map[string]interface{} {
	typ := "socks"
	if p.Kind == KindHTTP {
		typ = "http"
	}
	out := map[string]interface{}{
		"type":        typ,
		"tag":         tag,
		"server":      p.Host,
		"server_port": p.Port,
	}
	if p.User != "" {
		out["username"] = p.User
	}
	if p.Password != "" {
		out["password"] = p.Password
	}
	if p.TLS {
		tls := map[string]interface{}{"enabled": true, "insecure": p.AllowInsecure,
			"utls": map[string]interface{}{"enabled": true, "fingerprint": currentTLSFingerprint()}}
		if p.SNI != "" {
			tls["server_name"] = p.SNI
		} else {
			tls["server_name"] = p.Host
		}
		out["tls"] = tls
	}
	return out
}
