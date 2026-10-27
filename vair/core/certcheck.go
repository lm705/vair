package core

import (
	"strings"
)

// How a configuration has the server's certificate checked.
//
// Three answers exist in the links in circulation: the usual check against the
// system's trusted authorities, a pin (pcs= — the SHA-256 of the one
// certificate the server must present, self-signed or not), and no check at all
// (allowInsecure=1). xray 26 dropped the third: a configuration carrying it
// stops xray before it starts, with a line about its own config parser, so
// every such configuration failed to connect, test or take part in AUTO.
// sing-box still has it. So a configuration that asks for no check runs on
// sing-box (engineForNode), and on xray the field is never written.

// skipsCertCheck reports whether a node asks for its server's certificate not
// to be checked and gives nothing to check it against instead.
func skipsCertCheck(n *Node) bool {
	if n == nil {
		return false
	}
	switch n.Kind {
	case KindVLESS:
		p := n.Vless
		return p != nil && p.Security == "tls" && p.AllowInsecure && p.PinnedCertSHA256 == "" && p.VerifyCertName == ""
	case KindTrojan:
		p := n.Trojan
		return p != nil && p.Security == "tls" && p.AllowInsecure && p.PinnedCertSHA256 == "" && p.VerifyCertName == ""
	case KindHTTP, KindSOCKS:
		p := n.Proxy
		return p != nil && p.TLS && p.AllowInsecure
	}
	return false
}

// certPinOf is the node's pcs= value, or "".
func certPinOf(n *Node) string {
	if n == nil {
		return ""
	}
	switch n.Kind {
	case KindVLESS:
		if n.Vless != nil {
			return n.Vless.PinnedCertSHA256
		}
	case KindTrojan:
		if n.Trojan != nil {
			return n.Trojan.PinnedCertSHA256
		}
	}
	return ""
}

// validCertPins accepts what xray takes for pinnedPeerCertSha256: one or more
// SHA-256 hashes in hexadecimal, comma-separated, with or without colons
// between the bytes. Base64 — which some panels write — is refused by xray,
// so it is refused here, where the reason can be said.
func validCertPins(s string) bool {
	parts := strings.Split(s, ",")
	for _, p := range parts {
		h := strings.ReplaceAll(strings.TrimSpace(p), ":", "")
		if len(h) != 64 {
			return false
		}
		for _, c := range h {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return len(parts) > 0
}

// certCheckLabel is how a node's certificate is checked, for the session
// panel: "" for a node with no TLS of its own to check.
func certCheckLabel(n *Node) string {
	if n == nil {
		return ""
	}
	pin, name, insecure, tls := "", "", false, false
	switch n.Kind {
	case KindVLESS:
		if p := n.Vless; p != nil {
			if p.Security == "reality" {
				return "REALITY key"
			}
			tls, insecure, pin, name = p.Security == "tls", p.AllowInsecure, p.PinnedCertSHA256, p.VerifyCertName
		}
	case KindTrojan:
		if p := n.Trojan; p != nil {
			if p.Security == "reality" {
				return "REALITY key"
			}
			tls, insecure, pin, name = p.Security == "tls", p.AllowInsecure, p.PinnedCertSHA256, p.VerifyCertName
		}
	case KindVMess:
		if p := n.Vmess; p != nil {
			tls = p.Security == "tls"
		}
	case KindHTTP, KindSOCKS:
		if p := n.Proxy; p != nil {
			tls, insecure = p.TLS, p.AllowInsecure
		}
	case KindHysteria2:
		// Its pinSHA256 is read from the link but not handed to the engine
		// (sing-box pins a public key, not a certificate), so it is not
		// claimed here.
		if p := n.Hysteria2; p != nil {
			tls, insecure = true, p.Insecure
		}
	case KindTUIC:
		if p := n.TUIC; p != nil {
			tls, insecure = true, p.Insecure
		}
	}
	switch {
	case !tls:
		return ""
	case pin != "":
		return "pinned (SHA-256)"
	case name != "":
		return "checked against the name in the link"
	case insecure:
		return "not checked (allowInsecure)"
	}
	return "checked"
}

// noteCertCheckOnXray says, once per connection, when a configuration asked
// for no certificate check and is getting one anyway: sing-box cannot carry
// its transport, and xray no longer skips the check. A server with a valid
// certificate connects as before; a self-signed one fails the check.
func noteCertCheckOnXray(n *Node) {
	if skipsCertCheck(n) && engineForNode(n) == "xray" {
		vlog("warning", "%s asks for no certificate check (allowInsecure), which xray no longer allows, and its transport runs only on xray — the server's certificate is checked; a self-signed server needs its certificate's SHA-256 in the link (pcs=)", n.Name)
	}
}

// engineErrorText is the error line for an engine that stopped before it was
// ready. The engine's own words, except where they are about a setting the
// user can act on and say so only in the engine's terms.
func engineErrorText(engine, msg string) string {
	if strings.Contains(msg, `"allowInsecure" has been removed`) {
		return allowInsecureRemoved
	}
	return engine + ": " + msg
}

// allowInsecureRemoved is what a raw JSON configuration carrying allowInsecure
// gets from xray, in plain words. A fixed phrase: the window translates it.
const allowInsecureRemoved = "xray no longer allows turning off the certificate check (allowInsecure) — pin the certificate instead: pinnedPeerCertSha256 in the configuration"
