package core

import (
	"fmt"
	"net/url"
	"sync"
)

// The service port of a proxy session: an HTTP inbound of the engine, on
// 127.0.0.1, that always goes through the tunnel whatever the routing mode
// says. Vair's own requests use it — rule sets, lists, SOURCES, the update
// check, the reverse lookups of the connections panel — and nothing else
// should.
//
// It used to accept anyone. It shows up in any list of listening ports, and a
// program on this machine could send its traffic through the VPN by it while
// SOCKS and HTTP authentication were on — which exist to stop exactly that. It
// now asks for a login made for each session and kept in memory: only this
// program knows it, and nobody has to type it anywhere.

var srcFetch struct {
	sync.Mutex
	user, pass string
}

// newSrcFetchCreds makes the login for the service port of the session being
// built.
func newSrcFetchCreds() (user, pass string) {
	user, pass = randomHex(16), randomHex(32)
	srcFetch.Lock()
	srcFetch.user, srcFetch.pass = user, pass
	srcFetch.Unlock()
	return user, pass
}

func srcFetchCreds() (user, pass string) {
	srcFetch.Lock()
	defer srcFetch.Unlock()
	return srcFetch.user, srcFetch.pass
}

// ownProxyURL is where Vair's own requests go in a proxy session: the service
// port with this session's login, or — on an engine without one — the user's
// HTTP port with the user's login. nil when no proxy session is up.
func ownProxyURL(cs ConnState) *url.URL {
	if cs.Status != ConnConnected || cs.Mode != ModeProxy {
		return nil
	}
	port, user, pass := cs.SrcFetchPort, "", ""
	if port > 0 {
		user, pass = srcFetchCreds()
	} else {
		port = cs.HTTPPort
		user, pass = proxyHttpCreds()
	}
	if port <= 0 {
		return nil
	}
	u := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
	if user != "" {
		u.User = url.UserPassword(user, pass)
	}
	return u
}
