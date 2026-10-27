package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
	noteUserConnAction()
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
	noteUserConnAction()
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

// PingLive is the round trip through the live connection, in milliseconds —
// a request across the tunnel, the same one the monitor makes — or -1 when
// nothing came back. The connection bar's ping used to run an ordinary PING of
// the configuration instead: a separate engine dialling the server, and only
// the first server of a chain, so a chain that carried nothing showed a time.
func PingLive() int64 {
	if state.conn.snap().Status != ConnConnected {
		return -1
	}
	alive, rtt := probeLiveTunnel(state.conn)
	if !alive || rtt <= 0 {
		return -1
	}
	autoLiveRtt.Store(rtt.Milliseconds())
	return rtt.Milliseconds()
}

// ConnSnapshot returns the current connection state for the UI.
func ConnSnapshot() ConnState {
	return state.conn.snap()
}

// Shutdown tears down any active connection (clearing the system proxy /
// killing the engine) and reaps stray engine processes. The shell MUST call this
// on real quit, or a proxy-mode session would leave the system proxy pointing at
// a dead port and break the user's internet.
//
// Once per process: the tray's Quit calls it and then quits the application,
// whose shutdown hook calls it again (see ShutdownUnattended).
func Shutdown() { shutdownOnce.Do(shutdown) }

var shutdownOnce sync.Once

// unattendedExit is set for an exit nobody is watching: nothing on the way out
// may wait on the user.
var unattendedExit atomic.Bool

// ShutdownUnattended is Shutdown for an exit the program did not start —
// registered as the application's shutdown hook, which is what runs when
// Windows ends the session (a restart, a shutdown, a sign-out). Before it, the
// end of a session was a crash as far as Vair was concerned: the engines were
// killed, the system proxy was left pointing at a port nothing listened on, and
// a proxy kill-switch block — persistent filters — outlived the restart, so the
// machine came back with no internet until Vair was started again.
//
// Unattended because nobody can answer a prompt while Windows is closing, and
// an elevation request waiting for one would hold the shutdown up. After the
// tray's Quit, which already ran Shutdown, it does nothing.
//
// And bounded. Windows waits for this to return before it goes on ending the
// session, and a teardown that waits on something — a PowerShell run, a lock
// held by the "engine stopped" handler that the session's end set off a moment
// earlier — held a restart for as long as it waited: in the test VM a restart
// with a TUN session up never finished. What must not outlive the program goes
// first in Shutdown (the block decision, the system proxy); whatever is still
// running after unattendedShutdownBudget is left to the next start, which
// sweeps the engines, the adapter and the firewall rule anyway.
func ShutdownUnattended() {
	unattendedExit.Store(true)
	sessionEnding.Store(true)
	done := make(chan struct{})
	go func() {
		Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(unattendedShutdownBudget):
		vlog("warning", "exit: the teardown is still running after %s — the rest is left to the next start", unattendedShutdownBudget)
	}
}

// unattendedShutdownBudget is how long an exit the program did not start may
// take. Windows gives a program about five seconds after it is told the
// session is ending before it puts up "this app is preventing shutdown".
const unattendedShutdownBudget = 4 * time.Second

// sessionEnding is set as soon as Windows says the session is ending — the
// shell reports WM_QUERYENDSESSION, which comes before the engines are killed
// under us — and by any exit the program did not start. From then on nothing
// on the way out starts another process: process creation is what hangs while
// Windows is shutting down.
var sessionEnding atomic.Bool

// SessionEnding is the shell telling core that Windows is ending the session.
func SessionEnding() { sessionEnding.Store(true) }

func shutdown() {
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
	// The system proxy next, before the engines are stopped. Both are single
	// calls, and when Windows is ending the session it gives a program a few
	// seconds before it stops waiting: what must not outlive the program goes
	// first. stopConnection would do it too — after killing the engines.
	unsetSystemProxy()
	stopConnection()
	KillOrphanedXray()
	// The test results and measurement history waiting for the next flush (it
	// runs every 0.4 s): written now rather than lost with the process.
	if store != nil {
		store.flushResults()
	}
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
	// Reason is what went wrong, as a key the interface words in the user's
	// language: "unreachable", "http", "unreadable", "no_address", "refused".
	// Error carries the detail for the tooltip.
	Reason string `json:"reason,omitempty"`
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
	req, err := http.NewRequest("GET", currentCheckIPURL(), nil)
	if err != nil {
		// A stored address from before the check on saving: a nil request here
		// was a panic the window showed as one.
		return ExitInfo{Error: "the check IP address is not a valid URL", Reason: "unreachable"}
	}
	resp, err := client.Do(req)
	if err != nil {
		msg := err.Error()
		if len(msg) > 80 {
			msg = msg[:80] + "…"
		}
		return ExitInfo{Error: "request failed: " + msg, Reason: "unreachable"}
	}
	defer resp.Body.Close()
	var raw map[string]interface{}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&raw)
	// An error status is said as one, with the service's own words when it
	// gives any: a rate limit — ipinfo.io hands one to a VPN exit address many
	// people share — and an outage call for different things, and "no address
	// in the answer" suggested neither.
	if resp.StatusCode >= 400 {
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if decodeErr == nil {
			if s := serviceMessage(raw); s != "" {
				msg += ": " + s
			}
		}
		return ExitInfo{Error: msg, Reason: "http"}
	}
	if decodeErr != nil {
		return ExitInfo{Error: "the answer is not JSON", Reason: "unreadable"}
	}
	// ip-api says so in the body rather than in the status code.
	if st, _ := raw["status"].(string); st == "fail" {
		msg, _ := raw["message"].(string)
		if msg == "" {
			msg = "geo lookup failed"
		}
		return ExitInfo{Error: msg, Reason: "refused"}
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
		return ExitInfo{Error: "the answer had no address in it", Reason: "no_address"}
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

// serviceMessage is the explanation an error answer carries, in the shapes the
// check-IP services use: "message", "error" as text, or "error" as an object
// with a title or a message. Cut short for a tooltip.
func serviceMessage(raw map[string]interface{}) string {
	s := pickString(raw, "message", "error", "reason")
	if s == "" {
		if e, ok := raw["error"].(map[string]interface{}); ok {
			s = pickString(e, "title", "message", "info")
		}
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
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
