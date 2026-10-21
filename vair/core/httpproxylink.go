package core

import (
	"net"
	"strconv"
	"strings"
)

// Telling a pasted http proxy from a pasted subscription link.
//
// Both are http:// URLs, and getting this wrong is expensive in one direction:
// treat a subscription as a proxy and one paste becomes hundreds of imaginary
// configurations. So the scanner ignored http:// entirely, and an http proxy
// could only be added through the form.
//
// They are, in fact, easy to tell apart, because they are used for different
// things and therefore written differently. A proxy is an ENDPOINT: credentials
// and a port, no path — nobody has ever been handed "the proxy is at
// http://proxy.example/list.txt". A subscription is a DOCUMENT: a path, often a
// query, rarely a port and almost never credentials in the URL.
//
// The test below asks for the endpoint shape and refuses everything else, which
// makes the failure safe in the direction that matters: an unusual proxy link is
// not recognised and can still be added through the form, while no subscription
// is ever mistaken for a proxy.

// looksLikeHTTPProxyLink reports whether an http:// or https:// URL is written
// the way a proxy is handed out rather than the way a subscription is.
func looksLikeHTTPProxyLink(raw string) bool {
	s := strings.TrimSpace(raw)
	rest := ""
	switch {
	case strings.HasPrefix(strings.ToLower(s), "https://"):
		rest = s[len("https://"):]
	case strings.HasPrefix(strings.ToLower(s), "http://"):
		rest = s[len("http://"):]
	default:
		return false
	}
	// The name after # is ours and can contain anything, including a slash.
	if i := strings.LastIndex(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	// A query is a document's, not an endpoint's — except for the two knobs we
	// ourselves write, which is what makes a link we produced round-trip.
	if i := strings.Index(rest, "?"); i >= 0 {
		if !onlyProxyParams(rest[i+1:]) {
			return false
		}
		rest = rest[:i]
	}
	// A path disqualifies it. A bare trailing slash does not: browsers add one.
	rest = strings.TrimSuffix(rest, "/")
	if strings.Contains(rest, "/") {
		return false
	}
	creds := false
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		creds = strings.TrimSpace(rest[:i]) != ""
		rest = rest[i+1:]
	}
	host, portStr, err := net.SplitHostPort(rest)
	if err != nil || strings.Trim(host, "[]") == "" {
		return false // no explicit port: a subscription's shape, not a proxy's
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	// A port alone is not enough — https://example.com:8443 is a perfectly
	// ordinary subscription host. Either credentials, or a port that is not one
	// the web is served on.
	return creds || (port != 80 && port != 443)
}

// onlyProxyParams reports whether a query carries nothing but the parameters a
// proxy link of ours writes.
func onlyProxyParams(q string) bool {
	for _, kv := range strings.Split(q, "&") {
		if kv == "" {
			continue
		}
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "security", "sni", "allowInsecure", "tls":
		default:
			return false
		}
	}
	return true
}
