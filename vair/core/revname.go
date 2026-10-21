package core

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

// Putting a name to an address.
//
// The connections panel shows the destination the client ASKED for, and that is
// usually a hostname because that is what a client sends: "CONNECT
// example.com:443". But a program that resolves names itself and then connects
// to the result asks for an address instead, and the row reads "104.18.7.1:443"
// — which is a true answer to the wrong question, because nobody recognises
// their own game or their own bank in it. That is the difference between a panel
// that explains a problem and one that merely proves something happened.
//
// So an address gets a name where one can be had. Two sources, cheapest first:
// the names we have already seen asked for on this machine, and failing that a
// reverse lookup. Neither is authoritative — a shared CDN address answers with
// whatever its owner put in DNS, and plenty of addresses answer with nothing —
// so the name is shown BESIDE the address, never instead of it.

// revCache holds both directions of what we know, with an expiry: an address
// reassigned to somebody else must not keep an old name for the rest of a
// session.
var revCache = struct {
	sync.Mutex
	m map[string]revEntry
}{m: map[string]revEntry{}}

type revEntry struct {
	name string
	when time.Time
	// pending marks a lookup in flight, so a table redrawn every second does not
	// start the same one sixty times.
	pending bool
}

const (
	revTTL = 30 * time.Minute
	// revMax bounds the cache; a machine talking to more distinct addresses than
	// this in half an hour is not one where a few forgotten names matter.
	revMax = 4096
)

// noteResolvedHost records a name the machine has been seen asking for, so an
// address that later turns up bare can be recognised. Called with whatever the
// proxy path already learned — it costs nothing, because the name was there.
func noteResolvedHost(host string) {
	host = strings.TrimSpace(host)
	if host == "" || net.ParseIP(host) != nil {
		return
	}
	// The lookup is what fills the reverse direction; this only remembers that
	// somebody cared about this name, which the resolver below uses to prefer a
	// name the user would recognise over a machine-generated PTR.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			return
		}
		revCache.Lock()
		defer revCache.Unlock()
		for _, a := range addrs {
			if e, ok := revCache.m[a]; ok && e.name != "" && !e.pending {
				continue // an asked-for name already; do not overwrite it
			}
			revCache.m[a] = revEntry{name: host, when: time.Now()}
		}
	}()
}

// reverseName returns a name for an address, or "" when there is none yet.
//
// Never blocks: an unknown address starts a lookup in the background and the
// next redraw of the table shows the answer. A table that stalled for a second
// per row would be worse than one that fills in.
func reverseName(ip string) string {
	if ip == "" || net.ParseIP(ip) == nil {
		return ""
	}
	revCache.Lock()
	e, ok := revCache.m[ip]
	fresh := ok && time.Since(e.when) < revTTL
	if fresh && !e.pending {
		revCache.Unlock()
		return e.name
	}
	if ok && e.pending {
		revCache.Unlock()
		return ""
	}
	if len(revCache.m) >= revMax {
		revCache.m = map[string]revEntry{}
	}
	revCache.m[ip] = revEntry{pending: true, when: time.Now()}
	revCache.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		names, err := net.DefaultResolver.LookupAddr(ctx, ip)
		name := ""
		if err == nil && len(names) > 0 {
			name = strings.TrimSuffix(names[0], ".")
		}
		revCache.Lock()
		revCache.m[ip] = revEntry{name: name, when: time.Now()}
		revCache.Unlock()
	}()
	return ""
}
