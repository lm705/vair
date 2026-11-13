package core

import (
	"net"
	"strings"
)

// Configurations xray will not start.
//
// From 26.9 xray refuses a VLESS outbound with neither TLS/REALITY nor VLESS
// encryption, and a Trojan outbound without TLS, when the server's address is
// public ("vless without TLS or other encryption is prohibited unless the
// server address is a private IP or domain"). Such links are common in public
// lists. sing-box still runs them, so they go to sing-box (engineForNode), as
// the configurations that switch the certificate check off already do. Where
// sing-box cannot carry the transport (xhttp, mKCP) nothing can run the link,
// and nodeUnsupportedReason says so instead of xray's parser.
//
// Vair never writes VLESS encryption (the user is always "encryption": "none"),
// so for a VLESS link it is the stream security alone that decides.

// xrayRefusesPlaintext reports whether xray refuses the node for carrying its
// traffic unencrypted to a public address.
func xrayRefusesPlaintext(n *Node) bool {
	if n == nil {
		return false
	}
	switch n.Kind {
	case KindVLESS:
		p := n.Vless
		return p != nil && !streamEncrypted(p.Security) && publicAddress(p.Host)
	case KindTrojan:
		p := n.Trojan
		return p != nil && !streamEncrypted(p.Security) && publicAddress(p.Host)
	}
	return false
}

// streamEncrypted: the stream securities xray counts as encryption.
func streamEncrypted(security string) bool {
	switch strings.ToLower(strings.TrimSpace(security)) {
	case "tls", "reality":
		return true
	}
	return false
}

// publicAddress mirrors what xray lets through: a private, loopback or
// link-local IP is not public. Every name counts as public except localhost —
// xray also allows names on its private-domain list, but a link that only
// looks public here costs nothing: sing-box runs it either way.
func publicAddress(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]")), ".")
	if h == "" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
	}
	return h != "localhost" && !strings.HasSuffix(h, ".localhost")
}

// plaintextUnsupported is the reason a plaintext link that sing-box cannot
// carry is refused. A fixed phrase: the window translates it.
const plaintextUnsupported = "xray no longer connects to a public address without TLS, and sing-box does not run this transport"
