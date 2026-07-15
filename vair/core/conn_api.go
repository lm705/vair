package core

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
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
	broadcastAuto("paused", "", "", "manual")
	cm := state.conn
	cm.mu.Lock()
	if cm.state.Status != ConnIdle && cm.state.Status != ConnDisconnecting {
		cm.state.Status = ConnDisconnecting
	}
	s := cm.state
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: s})
	go stopConnection()
}

// ConnSnapshot returns the current connection state for the UI.
func ConnSnapshot() ConnState {
	return state.conn.snap()
}

// Shutdown tears down any active connection (clearing the Windows system proxy /
// killing the engine) and reaps stray engine processes. The shell MUST call this
// on real quit, or a proxy-mode session would leave the system proxy pointing at
// a dead port and break the user's internet.
func Shutdown() {
	stopConnection()
	KillOrphanedXray()
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
		tr = makeSharedTransport(cs.HTTPPort)
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

	// ip-api.com: free, no key, HTTP/JSON. fields filters the payload to what we
	// show. We hit it THROUGH the tunnel, so the reported IP is the exit IP.
	exitURL := "http://" + checkExitHost + "/json/?fields=status,message,country,countryCode,city,query,isp"
	req, _ := http.NewRequest("GET", exitURL, nil)
	resp, err := client.Do(req)
	if err != nil {
		msg := err.Error()
		if len(msg) > 80 {
			msg = msg[:80] + "…"
		}
		return ExitInfo{Error: "request failed: " + msg}
	}
	defer resp.Body.Close()
	var raw struct {
		Status      string `json:"status"`
		Message     string `json:"message"`
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
		City        string `json:"city"`
		Query       string `json:"query"` // the IP
		ISP         string `json:"isp"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&raw); err != nil {
		return ExitInfo{Error: "bad response from geo service"}
	}
	if raw.Status != "success" {
		msg := raw.Message
		if msg == "" {
			msg = "geo lookup failed"
		}
		return ExitInfo{Error: msg}
	}
	return ExitInfo{IP: raw.Query, Country: raw.Country, CountryCode: raw.CountryCode, City: raw.City, ISP: raw.ISP}
}
