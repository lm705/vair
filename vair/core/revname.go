package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Putting a name to an address.
//
// The "Not through Vair" table of the connections panel lists the machine's own
// connections that go around the proxy, and those are bare addresses —
// "104.18.7.1:443" — which nobody recognises as their game or their bank. So an
// address gets a name where one can be had, shown BESIDE the address, never
// instead of it: a shared CDN address answers with whatever its owner put in
// DNS, and plenty answer with nothing.
//
// The name comes from a reverse lookup, and the lookup is asked THROUGH the
// tunnel, as DNS over HTTPS through the session's own proxy port, or not at
// all. It used to go to the system resolver — the provider's, as a rule — and
// alongside it every hostname a proxied connection asked for was looked up
// there too, to fill this cache: in proxy mode every site the user visited
// through the VPN was also sent to the provider in a plain DNS query, with the
// panel's recording on by default. Without a proxy session up there is no
// lookup, and the table shows addresses.

// revCache holds the answers, with an expiry: an address reassigned to somebody
// else must not keep an old name for the rest of a session.
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

// revDoHURL is the DNS-over-HTTPS endpoint the reverse lookups go to, by
// address so that no name has to be resolved first. A var only for tests.
var revDoHURL = "https://1.1.1.1/dns-query"

// revLookup asks for the PTR name of ip through the live proxy session, or
// reports that it cannot. A var only for tests.
var revLookup = reverseThroughTunnel

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
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		name, asked := revLookup(ctx, ip)
		revCache.Lock()
		defer revCache.Unlock()
		if !asked {
			// No session to ask through: nothing is remembered, so the address
			// gets its name once there is one.
			delete(revCache.m, ip)
			return
		}
		revCache.m[ip] = revEntry{name: name, when: time.Now()}
	}()
	return ""
}

// reverseThroughTunnel looks the PTR record up with DNS over HTTPS, through the
// pinned source-fetch port of a live proxy session (which always rides the
// tunnel, whatever the routing mode) — never directly. asked is false when there
// is no such session, in which case nothing was sent anywhere.
func reverseThroughTunnel(ctx context.Context, ip string) (name string, asked bool) {
	cs := state.conn.snap()
	if cs.Status != ConnConnected || cs.Mode != ModeProxy {
		return "", false
	}
	proxyURL := ownProxyURL(cs)
	if proxyURL == nil {
		return "", false
	}
	arpa, err := reverseArpa(ip)
	if err != nil {
		return "", true
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, revDoHURL+"?name="+url.QueryEscape(arpa)+"&type=PTR", nil)
	if err != nil {
		return "", true
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := client.Do(req)
	if err != nil {
		return "", true
	}
	defer resp.Body.Close()
	var ans struct {
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&ans) != nil {
		return "", true
	}
	for _, a := range ans.Answer {
		if a.Type == 12 && a.Data != "" {
			return strings.TrimSuffix(a.Data, "."), true
		}
	}
	return "", true
}

// reverseArpa is the reverse-lookup name of an address.
func reverseArpa(s string) (string, error) {
	ip := net.ParseIP(s)
	if ip == nil {
		return "", fmt.Errorf("not an address: %q", s)
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa", v4[3], v4[2], v4[1], v4[0]), nil
	}
	const hex = "0123456789abcdef"
	var b strings.Builder
	for i := len(ip) - 1; i >= 0; i-- {
		b.WriteByte(hex[ip[i]&0x0f])
		b.WriteByte('.')
		b.WriteByte(hex[ip[i]>>4])
		b.WriteByte('.')
	}
	b.WriteString("ip6.arpa")
	return b.String(), nil
}
