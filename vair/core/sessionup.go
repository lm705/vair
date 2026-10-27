package core

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// The program's own downloads, and when they happen.
//
// Vair fetches a few things for itself: the SOURCES list, the routing lists
// (rule sets and the user's blocklist URLs) and the update manifest. Each of
// these used to go out when the program started or a connection was being
// built — before there was a tunnel, from the machine's real address, with the
// names resolved by the provider's resolver, and with the kill-switch letting
// this process through on purpose so a reconnect can work. A capture of a cold
// start showed the GitHub and mirror requests every time, the update check
// once more for every window and web page that opened the interface.
//
// Now they wait for a tunnel when they can: whatever has a copy on disk uses
// that copy, and the refresh goes out once a session has been checked to carry
// traffic — through it. Only what cannot be done without (an empty SOURCES list
// on a first start, a rule set that has never been downloaded) still goes out
// directly, and a user's own click (RELOAD, "Check for updates") is the user's.

// ownHTTPClient is the client for those downloads. With a proxy session up it
// goes through the session's source-fetch port, which is pinned to the proxy
// outbound and so rides the tunnel in every routing mode; a TUN session
// carries this process anyway. No keep-alive: these are one-off requests, and
// a connection kept open from before a session goes on using the physical
// adapter once the tunnel is up — the capture showed one closing there, mid
// session, which the kill-switch allows because it is this program's.
func ownHTTPClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{DisableKeepAlives: true}
	if pu := ownProxyURL(state.conn.snap()); pu != nil {
		tr.Proxy = http.ProxyURL(pu)
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// ownTrafficTunnelled reports whether a request of this program's would ride a
// tunnel right now.
func ownTrafficTunnelled() bool {
	return state.conn.snap().Status == ConnConnected
}

var (
	deferredMu         sync.Mutex
	deferredRuleSets   = map[string]ruRuleSetDef{}
	deferredBlocklists = map[string]bool{}
	// sourcesDeferred is set when the SOURCES list was not refreshed at start
	// because it already had configurations; the first session refreshes it.
	sourcesDeferred atomic.Bool

	autoUpdateOnce sync.Once
	autoUpdate     atomic.Value // UpdateInfo, once the automatic check has run
)

func deferRuleSet(rs ruRuleSetDef) {
	deferredMu.Lock()
	deferredRuleSets[rs.file] = rs
	deferredMu.Unlock()
}

func deferBlocklist(u string) {
	deferredMu.Lock()
	deferredBlocklists[u] = true
	deferredMu.Unlock()
}

// afterSessionUp runs what was put off until there is a tunnel. Called once a
// session has been checked to carry traffic; in the background, so nothing
// here holds the connection up. Rule sets first (the next connection's
// configuration), then SOURCES, then — once per run of the program — the
// update check.
var afterSessionUp = func() {
	go func() {
		deferredMu.Lock()
		var defs []ruRuleSetDef
		for _, rs := range deferredRuleSets {
			defs = append(defs, rs)
		}
		var urls []string
		for u := range deferredBlocklists {
			urls = append(urls, u)
		}
		deferredRuleSets = map[string]ruRuleSetDef{}
		deferredBlocklists = map[string]bool{}
		deferredMu.Unlock()
		if len(defs) > 0 {
			refreshRuleSets(defs)
		}
		if len(urls) > 0 {
			refreshCustomBlocklists(urls)
		}
		if sourcesDeferred.CompareAndSwap(true, false) {
			refreshSourcesDeferred()
		}
		autoUpdateOnce.Do(func() {
			info := CheckForUpdate()
			autoUpdate.Store(info)
			if info.Notify {
				state.broadcast(SSEEvent{Type: "update_available", Payload: info})
			}
		})
	}()
}

// PendingUpdate is what the automatic update check found, for a window opened
// after it ran; the zero value before it has.
func PendingUpdate() UpdateInfo {
	if v, ok := autoUpdate.Load().(UpdateInfo); ok {
		return v
	}
	return UpdateInfo{Current: appVersion}
}
