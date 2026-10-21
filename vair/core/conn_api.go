package core

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Connect starts a connection to entry idx of the displayed table tab using the
// given mode ("proxy" — default — or "tun"). Returns false if idx is invalid.
// Mirrors the 1.10 handleConnect (arms auto-keepalive, marks the link user-owned).
func Connect(idx int, mode string) bool {
	// Align the active tab with the displayed table tab so the connection's
	// connTab is correct (the v3 stop-gap until the real tab bar lands).
	tab := TableTab()
	state.mu.Lock()
	state.activeTab = tab
	state.entries = state.tabEntries[tab]
	state.mu.Unlock()

	entry, ok := memEntry(tab, idx)
	if !ok {
		return false
	}
	autoWant.Store(true)
	autoManaged.Store(false)
	autoLiveRtt.Store(0)
	autoProbeNow.Store(true)
	if mode == "tun" {
		go startTUNConnection(entry)
	} else {
		go startProxyConnection(entry)
	}
	return true
}

// Disconnect tears down the current connection. Mirrors the 1.10 handleDisconnect
// (disarms auto, marks the panel paused, then stops in the background).
func Disconnect() {
	autoWant.Store(false)
	autoManaged.Store(false)
	autoLiveRtt.Store(0)
	noteSessionEndReason("disconnected by hand")
	broadcastAuto("paused", "", "", "manual")
	cm := state.conn
	cm.mu.Lock()
	if cm.state.Status != ConnIdle && cm.state.Status != ConnDisconnecting {
		cm.state.Status = ConnDisconnecting
	}
	s := cm.state
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: s})
	go func() {
		stopConnection()
		// The user said they are done, so nothing is restored on the next start
		// either.
		markSessionActive(false)
		// And the firewall block goes too — after the tunnel, not before it, so
		// there is no moment where the routes are back and the block is already
		// gone. A connection that merely died does NOT come through here; that
		// case is what the block is for.
		releasePersistentKillSwitch()
	}()
}

// ConnSnapshot returns the current connection state for the UI.
func ConnSnapshot() ConnState {
	return state.conn.snap()
}

// Shutdown tears down any active connection (clearing the system proxy /
// killing the engine) and reaps stray engine processes. The shell MUST call this
// on real quit, or a proxy-mode session would leave the system proxy pointing at
// a dead port and break the user's internet.
func Shutdown() {
	// The block does NOT come down here.
	//
	// "Keep blocking after the tunnel is gone" means what it says, and closing
	// the program is the plainest case of the tunnel being gone: releasing it
	// on the way out put the machine back on the open network with its real
	// address, which is the exact thing the switch was turned on to prevent.
	// Whether the next start reconnects changes nothing about that — an
	// unprotected minute is unprotected whether or not it ends.
	//
	// It is a decision with teeth, so it is only ever undone deliberately:
	// pressing disconnect, turning the setting off, clicking the red chip that
	// appears while a block is standing, or the PowerShell line the settings
	// screen prints in red. A start finds the block still there and says so
	// rather than quietly lifting it.
	releaseBlockOnExit()
	stopConnection()
	KillOrphanedXray()
	// Flush + close the optional log file. Without this the last few seconds of
	// the session die in the bufio buffer on every clean exit — exactly the
	// lines someone who turned file logging on wants to read afterwards.
	SetFileLogging(false)
}

// ExitInfo is the result of CheckExit — the public IP/geo as seen from the far
// end of the live tunnel (the conn-bar "check IP" chip).
type ExitInfo struct {
	IP          string `json:"ip"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	City        string `json:"city"`
	ISP         string `json:"isp"`
	Error       string `json:"error,omitempty"`
}

// CheckExit fetches the public exit IP/geo THROUGH the live tunnel (ported from
// the 1.10 handleCheckExit). Synchronous — the chip shows "checking…"; ~8s cap.
// In only_blocked mode the routing forces checkExitHost through the proxy, so
// the result reflects the VPN exit, not the direct IP.
func CheckExit() ExitInfo {
	cs := state.conn.snap()
	if cs.Status != ConnConnected {
		return ExitInfo{Error: "not connected"}
	}

	// Build a transport that rides the live tunnel — mirrors probeLiveTunnel.
	var tr *http.Transport
	if cs.Mode == ModeProxy {
		if cs.HTTPPort <= 0 {
			return ExitInfo{Error: "no proxy port"}
		}
		// Authenticate to the live proxy when HTTP-auth is on (else it 407s).
		hu, hp := proxyHttpCreds()
		tr = makeSharedTransport(cs.HTTPPort, hu, hp)
	} else {
		// TUN: a direct request is routed through the tunnel by the OS.
		tr = &http.Transport{
			Proxy:               nil,
			DisableKeepAlives:   true,
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: warmupTimeout,
			DialContext:         (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		}
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// The service is the user's choice (Settings → Testing), so the answer is
	// read by field NAME rather than by a struct that only fits one of them: the
	// half-dozen services that do this each spell the same five facts
	// differently, and a preset list whose entries need code is a preset list
	// that cannot take a custom URL.
	req, _ := http.NewRequest("GET", currentCheckIPURL(), nil)
	resp, err := client.Do(req)
	if err != nil {
		msg := err.Error()
		if len(msg) > 80 {
			msg = msg[:80] + "…"
		}
		return ExitInfo{Error: "request failed: " + msg}
	}
	defer resp.Body.Close()
	var raw map[string]interface{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&raw); err != nil {
		return ExitInfo{Error: "bad response from geo service"}
	}
	// ip-api says so in the body rather than in the status code.
	if st, _ := raw["status"].(string); st == "fail" {
		msg, _ := raw["message"].(string)
		if msg == "" {
			msg = "geo lookup failed"
		}
		return ExitInfo{Error: msg}
	}
	info := ExitInfo{
		IP:          pickString(raw, "query", "ip", "ip_addr", "YourFuckingIPAddress"),
		Country:     pickString(raw, "country", "country_name", "YourFuckingCountry"),
		CountryCode: pickString(raw, "countryCode", "country_code", "country_iso", "country"),
		City:        pickString(raw, "city", "YourFuckingCity"),
		ISP:         pickString(raw, "isp", "org", "asn_org", "YourFuckingISP"),
	}
	// ipwho.is and friends nest the network details one level down.
	if info.ISP == "" {
		if conn, ok := raw["connection"].(map[string]interface{}); ok {
			info.ISP = pickString(conn, "isp", "org")
		}
	}
	if info.IP == "" {
		return ExitInfo{Error: "the answer had no address in it"}
	}
	// A two-letter country is a code, not a name: several services return only
	// the code, and it belongs in the field the flag is drawn from.
	if info.Country != "" && len(info.Country) == 2 && info.CountryCode == "" {
		info.CountryCode, info.Country = info.Country, ""
	}
	if info.CountryCode != "" && len(info.CountryCode) != 2 {
		info.CountryCode = ""
	}
	return info
}

// pickString returns the first of the given keys that holds a non-empty string.
func pickString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
