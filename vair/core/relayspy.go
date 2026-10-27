package core

import (
	"bytes"
	"encoding/binary"
	"net"
	"strconv"
	"strings"
)

// What a relayed connection is for, read off the bytes as they pass.
//
// The relay stands between a client and the engine — a program and the local
// proxy in proxy mode, sing-box and xray in hybrid TUN — and the first thing
// the client sends is its request: an HTTP CONNECT, an absolute URL, or a SOCKS5
// greeting followed by the connect request. Reading that names the connection.
//
// It used to be read before anything was forwarded: one read of up to two
// seconds, and the request had to be in it. A SOCKS5 client sends the greeting,
// waits for the method reply and only then sends the request — sing-box does —
// so in TUN mode no connection was ever named: the panel listed nothing unless
// the engine's own log was on, and even then without byte counts, because the
// relay that counts could not match the engine's row. And a protocol in which
// the server speaks first waited those two seconds for nothing.
//
// Now everything is forwarded at once and the client's bytes are read as they
// go by: the greeting, the optional username/password exchange (the engine's
// SOCKS port asks for one when the proxy has a password), the request. When the
// request names an address rather than a name — hybrid TUN without FakeIP —
// the first bytes after it say the name the client meant: the server name of a
// TLS ClientHello, or an HTTP Host header. Nothing is changed and nothing is
// held up; a stream this does not understand is simply not named.

// spyMax bounds what is kept for reading: a ClientHello with a post-quantum key
// share is under two kilobytes.
const spyMax = 8 << 10

type destSpy struct {
	buf    []byte
	stage  int // 0 the request, 1 the first payload, 2 done
	from   int // where the payload starts in buf
	onDest func(dest string)
	onName func(name string)
}

func newDestSpy(onDest, onName func(string)) *destSpy {
	return &destSpy{onDest: onDest, onName: onName}
}

// feed takes the next bytes the client sent.
func (s *destSpy) feed(p []byte) {
	if s == nil || s.stage == 2 {
		return
	}
	if room := spyMax - len(s.buf); len(p) > room {
		p = p[:room]
	}
	s.buf = append(s.buf, p...)
	full := len(s.buf) >= spyMax
	if s.stage == 0 {
		dest, used, state := proxyRequest(s.buf)
		if state == spyNeedMore && !full {
			return
		}
		if state != spyFound {
			s.done()
			return
		}
		s.onDest(dest)
		host, _, _ := net.SplitHostPort(dest)
		if net.ParseIP(host) == nil || s.onName == nil {
			s.done() // a name already, which is what the panel shows
			return
		}
		s.stage, s.from = 1, used
	}
	name, state := payloadName(s.buf[s.from:])
	if state == spyNeedMore && !full {
		return
	}
	if state == spyFound {
		s.onName(name)
	}
	s.done()
}

func (s *destSpy) done() { s.stage, s.buf = 2, nil }

const (
	spyNeedMore = iota
	spyFound
	spyNot // will never parse: not a request this understands
)

// proxyRequest reads an HTTP proxy request or a SOCKS5 handshake and returns the
// destination and how many bytes the request took.
func proxyRequest(b []byte) (dest string, used int, state int) {
	if len(b) == 0 {
		return "", 0, spyNeedMore
	}
	if b[0] == 0x05 {
		return socks5Request(b)
	}
	end := bytes.Index(b, []byte("\r\n"))
	if end < 0 {
		if len(b) > 2048 {
			return "", 0, spyNot
		}
		return "", 0, spyNeedMore
	}
	f := strings.Fields(string(b[:end]))
	if len(f) < 2 {
		return "", 0, spyNot
	}
	if strings.EqualFold(f[0], "CONNECT") {
		if _, _, err := net.SplitHostPort(f[1]); err != nil {
			return "", 0, spyNot
		}
		// The payload begins after the request's headers.
		hdrEnd := bytes.Index(b, []byte("\r\n\r\n"))
		if hdrEnd < 0 {
			return "", 0, spyNeedMore
		}
		return f[1], hdrEnd + 4, spyFound
	}
	// GET http://host/path HTTP/1.1 — a name already, nothing after it needed.
	u := f[1]
	i := strings.Index(u, "://")
	if i < 0 {
		return "", 0, spyNot
	}
	u = u[i+3:]
	if j := strings.IndexAny(u, "/?"); j >= 0 {
		u = u[:j]
	}
	if u == "" {
		return "", 0, spyNot
	}
	if _, _, err := net.SplitHostPort(u); err != nil {
		u += ":80"
	}
	return u, len(b), spyFound
}

// socks5Request walks a SOCKS5 client's side of the handshake: the greeting,
// the username/password exchange if there is one, and a CONNECT request.
func socks5Request(b []byte) (string, int, int) {
	need := func(n int) bool { return len(b) >= n }
	if !need(2) {
		return "", 0, spyNeedMore
	}
	off := 2 + int(b[1])
	if !need(off + 1) {
		return "", 0, spyNeedMore
	}
	if b[off] == 0x01 { // RFC 1929: VER ULEN UNAME PLEN PASSWD
		if !need(off + 2) {
			return "", 0, spyNeedMore
		}
		ulen := int(b[off+1])
		if !need(off + 3 + ulen) {
			return "", 0, spyNeedMore
		}
		plen := int(b[off+2+ulen])
		off += 3 + ulen + plen
		if !need(off + 1) {
			return "", 0, spyNeedMore
		}
	}
	if !need(off + 4) {
		return "", 0, spyNeedMore
	}
	if b[off] != 0x05 || b[off+1] != 0x01 { // CONNECT only: no row for UDP
		return "", 0, spyNot
	}
	p := off + 4
	var host string
	switch b[off+3] {
	case 0x01:
		if !need(p + 6) {
			return "", 0, spyNeedMore
		}
		host = net.IP(b[p : p+4]).String()
		p += 4
	case 0x03:
		if !need(p + 1) {
			return "", 0, spyNeedMore
		}
		l := int(b[p])
		if !need(p + 1 + l + 2) {
			return "", 0, spyNeedMore
		}
		host = string(b[p+1 : p+1+l])
		p += 1 + l
	case 0x04:
		if !need(p + 18) {
			return "", 0, spyNeedMore
		}
		host = net.IP(b[p : p+16]).String()
		p += 16
	default:
		return "", 0, spyNot
	}
	port := int(binary.BigEndian.Uint16(b[p:]))
	return net.JoinHostPort(host, strconv.Itoa(port)), p + 2, spyFound
}

// payloadName reads the name the client meant out of its first bytes: the
// server name of a TLS ClientHello, or the Host of an HTTP request.
func payloadName(b []byte) (string, int) {
	if len(b) == 0 {
		return "", spyNeedMore
	}
	if b[0] == 0x16 {
		return tlsServerName(b)
	}
	end := bytes.Index(b, []byte("\r\n\r\n"))
	if end < 0 {
		if len(b) > 4096 {
			return "", spyNot
		}
		return "", spyNeedMore
	}
	for _, line := range strings.Split(string(b[:end]), "\r\n")[1:] {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "host") {
			h := strings.TrimSpace(v)
			if hh, _, err := net.SplitHostPort(h); err == nil {
				h = hh
			}
			if validHostName(h) {
				return h, spyFound
			}
			return "", spyNot
		}
	}
	return "", spyNot
}

// tlsServerName finds server_name in a TLS ClientHello.
func tlsServerName(b []byte) (string, int) {
	if len(b) < 5 {
		return "", spyNeedMore
	}
	recLen := int(binary.BigEndian.Uint16(b[3:5]))
	if len(b) < 5+recLen {
		return "", spyNeedMore
	}
	h := b[5 : 5+recLen]
	if len(h) < 4 || h[0] != 0x01 {
		return "", spyNot
	}
	p := 4 + 2 + 32 // handshake header, version, random
	if len(h) < p+1 {
		return "", spyNot
	}
	p += 1 + int(h[p]) // session id
	if len(h) < p+2 {
		return "", spyNot
	}
	p += 2 + int(binary.BigEndian.Uint16(h[p:])) // cipher suites
	if len(h) < p+1 {
		return "", spyNot
	}
	p += 1 + int(h[p]) // compression methods
	if len(h) < p+2 {
		return "", spyNot
	}
	extEnd := p + 2 + int(binary.BigEndian.Uint16(h[p:]))
	p += 2
	if extEnd > len(h) {
		extEnd = len(h)
	}
	for p+4 <= extEnd {
		typ := binary.BigEndian.Uint16(h[p:])
		l := int(binary.BigEndian.Uint16(h[p+2:]))
		p += 4
		if p+l > extEnd {
			break
		}
		if typ == 0 && l >= 5 { // server_name: list length, type 0, name length, name
			nl := int(binary.BigEndian.Uint16(h[p+3:]))
			if h[p+2] == 0 && 5+nl <= l {
				name := string(h[p+5 : p+5+nl])
				if validHostName(name) {
					return name, spyFound
				}
			}
			return "", spyNot
		}
		p += l
	}
	return "", spyNot
}

// validHostName accepts what can be shown as a host name and nothing else.
func validHostName(s string) bool {
	if s == "" || len(s) > 253 || net.ParseIP(s) != nil {
		return false
	}
	for _, c := range s {
		if !(c == '.' || c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}
