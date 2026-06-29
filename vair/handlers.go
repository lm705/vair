package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// ─────────────────────────── HTTP handlers ───────────────────────
func handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	// 1024-slot buffer absorbs bursts from bulk ping/speed runs (5 concurrent
	// runners × 4 live-callbacks/sec × few seconds of slack) without forcing
	// the broadcast path into its 50ms soft-block fallback.
	ch := make(chan SSEEvent, 1024)
	state.addClient(ch)
	defer state.removeClient(ch)
	send := func(ev SSEEvent) { data, _ := json.Marshal(ev); fmt.Fprintf(w, "data: %s\n\n", data); flusher.Flush() }
	// Tell the freshly-connected client to load the active tab's first window
	// (it fetches rows via /api/tab/window; the full list isn't pushed).
	send(SSEEvent{Type: "conn_update", Payload: state.conn.snap()})
	send(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	// Sync the client to the server's active tab, then have it load that tab's
	// first window (the full list isn't pushed — the client fetches it).
	send(SSEEvent{Type: "active_tab", Payload: state.activeTab})
	send(SSEEvent{Type: "loaded", Payload: nil, Tab: state.activeTab})
	// Send initial stats so the freshly-loaded UI shows lifetime totals
	// without waiting for the first ticker pulse (which only fires while
	// connected). We pass the live counter if a session is in progress.
	state.conn.mu.Lock()
	initialCounter := state.conn.counter
	state.conn.mu.Unlock()
	send(SSEEvent{Type: "stats_update", Payload: statsSnapshot(initialCounter)})
	send(SSEEvent{Type: "app_info", Payload: map[string]interface{}{
		"singbox_available": state.singboxBin != "",
		"is_admin":          checkAdmin(),
		"os":                "windows",
	}})
	// Deliver a deep-link that arrived before any UI was connected (the app was
	// launched by a vair:// link). First client to connect gets it, then it's
	// cleared so a reconnect doesn't re-import.
	pendingDeepLinkMu.Lock()
	if pendingDeepLink != "" {
		send(SSEEvent{Type: "deeplink", Payload: pendingDeepLink})
		pendingDeepLink = ""
	}
	pendingDeepLinkMu.Unlock()
	ctx := r.Context()
	for {
		select {
		case ev := <-ch:
			send(ev)
		case <-ctx.Done():
			return
		}
	}
}

// activeTabID reads the active tab id under the state lock.
func activeTabID() string {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.activeTab
}

// activeEntry returns entry idx of the active tab from the in-memory store.
// The returned *ConfigEntry is the live working copy.
func activeEntry(idx int) (*ConfigEntry, bool) {
	return memEntry(activeTabID(), idx)
}

func handleConnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	idx, err := strconv.Atoi(r.URL.Query().Get("idx"))
	if err != nil {
		http.Error(w, "bad idx", 400)
		return
	}
	mode := r.URL.Query().Get("mode")
	entry, ok := activeEntry(idx)
	if !ok {
		http.Error(w, "not found", 404)
		return
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
	// User explicitly connected → arm auto (keep alive / failover on death),
	// but mark the connection user-owned so the pool-honor switch won't move it.
	autoWant.Store(true)
	autoManaged.Store(false)
	autoLiveRtt.Store(0)     // stale until the next health probe measures this link
	autoProbeNow.Store(true) // probe ASAP so the panel shows THIS config's ping soon
	if mode == "tun" {
		go startTUNConnection(entry)
	} else {
		go startProxyConnection(entry)
	}
}

// handleConnectChain connects through a multi-hop chain. Query param `idx` is a
// comma-separated list of entry indices in hop order (first = entry, last =
// exit); `mode` selects proxy (default) or tun. Chains are xray-only;
// mixed/unsupported selections are rejected with 400 + a human-readable message
// the UI shows as a toast.
func handleConnectChain(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	mode := r.URL.Query().Get("mode")
	raw := r.URL.Query().Get("idx")
	parts := strings.Split(raw, ",")
	var entries []*ConfigEntry
	var nodes []*Node
	tab := activeTabID()
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		i, err := strconv.Atoi(p)
		if err != nil {
			http.Error(w, "bad idx", 400)
			return
		}
		e, ok := memEntry(tab, i)
		if !ok {
			http.Error(w, "bad idx", 400)
			return
		}
		n, perr := parseNode(e.Raw)
		if perr != nil {
			http.Error(w, "chain: unparseable config in selection", 400)
			return
		}
		entries = append(entries, e)
		nodes = append(nodes, n)
	}

	if len(entries) < 2 {
		http.Error(w, "a chain needs at least 2 configs", 400)
		return
	}
	// Validate engine compatibility up-front so the UI gets a clean rejection
	// before we tear down any existing connection.
	if reason := chainEngineReason(nodes); reason != "" {
		http.Error(w, reason, 400)
		return
	}

	w.WriteHeader(200)
	w.Write([]byte("ok"))
	// User explicitly connected → arm auto-keepalive but mark user-owned so the
	// supervisor's pool-honor switch won't move it. (Failover from a chain would
	// land on a single node — acceptable; the chain is a manual action.)
	autoWant.Store(true)
	autoManaged.Store(false)
	autoLiveRtt.Store(0)
	autoProbeNow.Store(true)
	connMode := ModeProxy
	if mode == "tun" {
		connMode = ModeTUN
	}
	go startChain(entries, connMode)
}

func handleDisconnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	// User deliberately disconnected → disarm auto (no reconnect/failover).
	autoWant.Store(false)
	autoManaged.Store(false)
	autoLiveRtt.Store(0)
	// Tell the Auto panel it's paused (suppressed until the user reconnects),
	// so its status block stops showing the now-disconnected config.
	broadcastAuto("paused", "", "", "manual")
	// Set visual state immediately, then start cleanup in background
	cm := state.conn
	cm.mu.Lock()
	if cm.state.Status != ConnIdle && cm.state.Status != ConnDisconnecting {
		cm.state.Status = ConnDisconnecting
	}
	s := cm.state
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: s})
	go stopConnection()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleConnState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(state.conn.snap())
}

// handleAutoSwitch implements the panel's "Switch now" button: arm intent and
// ask the supervisor to run an immediate failover/connect on its next tick
// (bypassing the health threshold and min-dwell). No-op (409) when auto is off.
func handleAutoSwitch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	settingsMu.RLock()
	on := appSettings.AutoConnect
	settingsMu.RUnlock()
	if !on {
		http.Error(w, "auto-connect is off", 409)
		return
	}
	autoWant.Store(true)
	autoForce.Store(true)
	autoKick()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// handleQR renders the raw URL of a config on the active tab as a QR-code PNG.
// Query: idx (entry index on the active tab). Works for any tab, including
// SOURCES — it only reads the config's raw URL, never edits it. Used by the
// "Show QR" context-menu item to let the user scan a config into a phone client.
func handleQR(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.URL.Query().Get("idx"))
	if err != nil {
		http.Error(w, "bad idx", 400)
		return
	}
	entry, ok := activeEntry(idx)
	if !ok {
		http.Error(w, "not found", 404)
		return
	}
	raw := entry.Raw
	if raw == "" {
		http.Error(w, "empty config", 400)
		return
	}
	// Medium ECC balances density vs scannability; 512px is crisp on screen and
	// still scans from a phone. Long VLESS/Reality URLs push QR to a high version
	// automatically — go-qrcode handles version selection.
	png, err := qrcode.Encode(raw, qrcode.Medium, 512)
	if err != nil {
		http.Error(w, "qr encode: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(png)
}

// handleQRText renders an arbitrary string (query param `data`) as a QR-code
// PNG. Used for source/subscription URLs — both the built-in SOURCES URLs and a
// user tab's source URLs — which aren't tied to a config index like handleQR.
func handleQRText(w http.ResponseWriter, r *http.Request) {
	data := r.URL.Query().Get("data")
	if strings.TrimSpace(data) == "" {
		http.Error(w, "empty data", 400)
		return
	}
	// Cap input: a QR tops out around ~2.9 KB of bytes, and a subscription URL is
	// far shorter. Reject anything implausibly long rather than fail in Encode.
	if len(data) > 2000 {
		http.Error(w, "data too long for QR", 400)
		return
	}
	png, err := qrcode.Encode(data, qrcode.Medium, 512)
	if err != nil {
		http.Error(w, "qr encode: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(png)
}

// handleSourcesInfo returns the built-in SOURCES source URLs. They are compiled
// in (sourceDefs), not user-editable like custom-tab URLs, so the Sources
// Settings modal shows them read-only (copy only).
func handleSourcesInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	urls := make([]string, 0, len(sourceDefs))
	for _, s := range sourceDefs {
		if s.URL != "" {
			urls = append(urls, s.URL)
		}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"urls": urls})
}

// handleAutoCandidates returns the ranked candidate pool the supervisor would
// choose from (same ordering as autoCandidates), for the panel's live list.
func handleAutoCandidates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	cs := state.conn.snap()
	connRaw := ""
	if cs.Status == ConnConnected {
		connRaw = cs.ConnRaw
	}
	type candDTO struct {
		Name    string  `json:"name"`
		Raw     string  `json:"raw"`
		Tab     string  `json:"tab"`
		Delay   int64   `json:"delay"`
		Status  Status  `json:"status"`
		Speed   float64 `json:"speed_mbps"`
		Current bool    `json:"current"`
	}
	out := []candDTO{}
	for _, c := range autoCandidates(autoPool(), "", nil) {
		es := c.entry.snap()
		out = append(out, candDTO{
			Name:    autoLabel(c.entry),
			Raw:     es.Raw,
			Tab:     c.tabID,
			Delay:   es.Delay,
			Status:  es.PingStatus,
			Speed:   es.SpeedMBps,
			Current: es.Raw == connRaw,
		})
	}
	json.NewEncoder(w).Encode(out)
}

// handleAutoConnectCand connects to a specific candidate chosen by the user in
// the AUTO panel's STATUS table (left-click). It finds the candidate in the
// current pool by its raw URL and connects to that exact entry — across tabs,
// so a click works even for a config on a non-active tab. Mirrors handleConnect:
// arms auto-want (keep-alive / failover-on-death) but marks the connection
// user-owned (autoManaged=false) so the supervisor won't switch away from the
// user's pick on its own.
func handleAutoConnectCand(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	raw := r.URL.Query().Get("raw")
	if raw == "" {
		http.Error(w, "bad raw", 400)
		return
	}
	mode := r.URL.Query().Get("mode")
	var found *autoCand
	for _, c := range autoCandidates(autoPool(), "", nil) {
		if c.entry.snap().Raw == raw {
			cc := c
			found = &cc
			break
		}
	}
	if found == nil {
		http.Error(w, "candidate not found", 404)
		return
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
	autoWant.Store(true)
	autoManaged.Store(false)
	autoLiveRtt.Store(0)
	autoProbeNow.Store(true)
	// The *OnTab connect functions require the caller to hold cm.actionMu (they
	// don't lock themselves — only the public startProxyConnection wrapper does).
	// Take it here so this user-driven connect serialises against the supervisor
	// and other connect/disconnect flows, exactly like handleConnect.
	go func() {
		cm := state.conn
		cm.actionMu.Lock()
		defer cm.actionMu.Unlock()
		if mode == "tun" {
			startTUNConnectionOnTab(found.entry, found.tabID)
		} else {
			startProxyConnectionOnTab(found.entry, found.tabID)
		}
	}()
}

func handlePingOne(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	idx, err := strconv.Atoi(r.URL.Query().Get("idx"))
	if err != nil {
		http.Error(w, "bad idx", 400)
		return
	}
	tabID := activeTabID()
	entry, ok := activeEntry(idx)
	if !ok {
		http.Error(w, "not found", 404)
		return
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
	// Register a cancel channel so RELOAD on this tab can stop this manual ping.
	cancelCh := make(chan struct{})
	registerManualTest(tabID, cancelCh)
	go func() {
		defer unregisterManualTest(tabID, cancelCh)
		entry.mu.Lock()
		entry.PingStatus = StatusTestingPing
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})

		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "⚠ ping test panic: %v\n", r)
				}
			}()
			runPingForEntry(entry, cancelCh)
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			fmt.Fprintf(os.Stderr, "⚠ ping test timeout for #%d\n", entry.Index)
		}

		entry.mu.Lock()
		// Cancelled by RELOAD → leave pending (runPingForEntry already set it);
		// only the genuine 20s-watchdog case force-fails a stuck "testing" pill.
		if entry.PingStatus == StatusTestingPing && !isTestCancelled(cancelCh) {
			entry.PingStatus = StatusFailed
			entry.PingErr = "timeout"
			entry.Delay = -1
		}
		entry.mu.Unlock()
		mirrorPingResult(tabID, entry) // persist so a tab switch-back reads it
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	}()
}

// handlePingConnected re-pings the currently connected config regardless of
// which tab the UI is showing. The browser path (pingOne) needs an index in
// the active tab; when the connected config lives on another tab the chip
// calls this instead. We locate the entry by its raw URL (its connection
// tab first, then any tab) and ping it, broadcasting under that entry's tab.
func handlePingConnected(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	cs := state.conn.snap()
	if cs.Status != ConnConnected || cs.ConnRaw == "" {
		w.WriteHeader(200)
		w.Write([]byte("not connected"))
		return
	}
	entry, tabID, _ := memEntryByRaw(cs.ConnTab, cs.ConnRaw)
	if entry == nil {
		w.WriteHeader(200)
		w.Write([]byte("entry not found"))
		return
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
	go func() {
		entry.mu.Lock()
		entry.PingStatus = StatusTestingPing
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "⚠ ping connected panic: %v\n", r)
				}
			}()
			runPingForEntry(entry, nil)
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
		entry.mu.Lock()
		if entry.PingStatus == StatusTestingPing {
			entry.PingStatus = StatusFailed
			entry.PingErr = "timeout"
			entry.Delay = -1
		}
		entry.mu.Unlock()
		mirrorPingResult(tabID, entry) // persist so a tab switch-back reads it
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	}()
}

// handleCheckExit reports the public exit IP (and its geo) as seen from the
// other end of the LIVE tunnel — proving traffic really egresses through the
// proxy, and, for a chain, that the EXIT hop is what reaches the internet (its
// country/IP is what shows). Goes through the same live path probeLiveTunnel
// uses: proxy mode via the local HTTP port, TUN mode direct (system routes it
// through the tunnel). Queries ip-api.com (no key, returns JSON over the
// tunnel). Synchronous (the UI shows a spinner); ~8s hard cap.
// checkExitHost is the geo/IP service used by the "check IP" button. It's forced
// through the proxy in only_blocked mode so the button reflects the VPN exit.
const checkExitHost = "ip-api.com"

func handleCheckExit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	cs := state.conn.snap()
	if cs.Status != ConnConnected {
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "not connected"})
		return
	}

	// Build a transport that rides the live tunnel — mirrors probeLiveTunnel.
	var tr *http.Transport
	if cs.Mode == ModeProxy {
		if cs.HTTPPort <= 0 {
			json.NewEncoder(w).Encode(map[string]interface{}{"error": "no proxy port"})
			return
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
	// show. We hit it THROUGH the tunnel, so the reported IP is the exit IP. In
	// only_blocked mode the routing forces checkExitHost through the proxy (see
	// the routing builders) so this still reflects the VPN exit, not the direct IP.
	exitURL := "http://" + checkExitHost + "/json/?fields=status,message,country,countryCode,city,query,isp"
	req, _ := http.NewRequest("GET", exitURL, nil)
	resp, err := client.Do(req)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "request failed: " + shortErr(err.Error())})
		return
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
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "bad response from geo service"})
		return
	}
	if raw.Status != "success" {
		msg := raw.Message
		if msg == "" {
			msg = "geo lookup failed"
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"error": msg})
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ip":           raw.Query,
		"country":      raw.Country,
		"country_code": raw.CountryCode,
		"city":         raw.City,
		"isp":          raw.ISP,
	})
}

// handleRenameEntry renames a single config on the ACTIVE (non-main) tab.
// Query: idx (entry index on the active tab), name (new display name).
//
// The display name lives in the URL fragment (#name), so renaming rewrites
// entry.Raw via setNodeName. Because several things are keyed by the full raw
// URL — Favorites, the "last connected" badge, the live connection match — we
// migrate those keys from the old raw to the new one so a rename doesn't
// silently drop a favorite star or the "last" tag. Sources (main) is excluded:
// its configs are re-fetched from the source and would lose the name on reload.
func handleRenameEntry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	idx, err := strconv.Atoi(r.URL.Query().Get("idx"))
	if err != nil {
		http.Error(w, "bad idx", 400)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "empty name", 400)
		return
	}
	// Cap the length so a pasted essay can't bloat the table / URL fragment.
	if len(name) > 120 {
		name = name[:120]
	}

	tabID := activeTabID()
	if tabID == "main" {
		http.Error(w, "rename is not available on the Sources tab", 400)
		return
	}
	entry, ok := activeEntry(idx)
	if !ok {
		http.Error(w, "not found", 404)
		return
	}
	oldRaw := entry.Raw
	newRaw := setNodeName(oldRaw, name)
	entry.Raw = newRaw
	entry.Name = name
	if store != nil {
		store.updateName(tabID, idx, name, newRaw)
	}

	// Migrate raw-keyed references so the rename is transparent. (Favorites are
	// keyed by body, which a rename doesn't change, so they need no migration.)
	if oldRaw != newRaw {
		settingsMu.Lock()
		if appSettings.LastConnectedRaw == oldRaw {
			appSettings.LastConnectedRaw = newRaw
		}
		settingsMu.Unlock()
		// If this config is the live connection, keep ConnState's raw in sync so
		// the conn-bar highlight / disconnect button keep matching the row.
		cm := state.conn
		cm.mu.Lock()
		if cm.state.ConnRaw == oldRaw {
			cm.state.ConnRaw = newRaw
		}
		for i, cr := range cm.state.ChainRaws {
			if cr == oldRaw {
				cm.state.ChainRaws[i] = newRaw
			}
		}
		cm.mu.Unlock()
	}

	state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	saveTabs()
	saveSettings()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleSpeedOne(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	idx, err := strconv.Atoi(r.URL.Query().Get("idx"))
	if err != nil {
		http.Error(w, "bad idx", 400)
		return
	}
	tabID := activeTabID()
	entry, ok := activeEntry(idx)
	if !ok {
		http.Error(w, "not found", 404)
		return
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
	// Register a cancel channel so RELOAD on this tab can stop this manual speed
	// test promptly (it aborts the in-flight ping/download via the threaded cancel).
	cancelCh := make(chan struct{})
	registerManualTest(tabID, cancelCh)
	go func() {
		defer unregisterManualTest(tabID, cancelCh)
		entry.mu.Lock()
		entry.SpeedStatus = StatusTestingSpeed
		entry.SpeedMBps = 0
		entry.SpeedLive = 0
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})

		// Hard timeout: if runSpeedForEntry doesn't return in 45s, force-fail.
		// This catches any possible deadlock/hang in xray, transport, or HTTP client.
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "⚠ speed test panic: %v\n", r)
				}
			}()
			runSpeedForEntry(entry, tabID, cancelCh)
		}()
		select {
		case <-done:
			// Normal completion
		case <-time.After(25 * time.Second):
			fmt.Fprintf(os.Stderr, "⚠ speed test timeout for #%d\n", entry.Index)
		}

		// Guarantee status is never stuck on "testing" — unless cancelled by
		// RELOAD, in which case runSpeedForEntry already left it pending.
		cancelled := isTestCancelled(cancelCh)
		entry.mu.Lock()
		if entry.SpeedStatus == StatusTestingSpeed && !cancelled {
			entry.SpeedStatus = StatusFailed
			entry.SpeedErr = "timeout"
			entry.SpeedLive = 0
		}
		// Same protection for PingStatus: runSpeedForEntry sets PingStatus
		// inside the withXray callback. If xray crashes before that branch
		// runs and PingStatus was already TestingPing (e.g. a prior ping was
		// in flight when speed was clicked), force-fail it too.
		if entry.PingStatus == StatusTestingPing && !cancelled {
			entry.PingStatus = StatusFailed
			entry.Delay = -1
			if entry.PingErr == "" {
				entry.PingErr = "timeout"
			}
		}
		entry.mu.Unlock()
		mirrorSpeedResult(tabID, entry) // persist so a tab switch-back reads it
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	}()
}

// parseFilterIndices reads an optional JSON body of the form
// {"indices":[0,3,5,...]} and returns it as an ordered slice. Order matters:
// the bulk test runners iterate in this order and acquire the concurrency
// semaphore in sequence, so the user-visible test order matches whatever the
// client sent (which is the on-screen sortedList order).
// Returns nil if no body or the body is empty/invalid — caller treats nil as
// "test everything" for backwards compatibility.
func parseFilterIndices(r *http.Request) []int {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil || len(body) == 0 {
		return nil
	}
	var req struct {
		Indices []int `json:"indices"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	return req.Indices
}

func handlePingAll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	indices := parseFilterIndices(r)
	go runPingAll(indices)
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleSpeedAll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	indices := parseFilterIndices(r)
	go runSpeedAll(indices)
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleTestsCancel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	tabID := r.URL.Query().Get("tab")
	testMu.Lock()
	onThisTab := tabID == "" || testingTab == "" || testingTab == tabID
	testMu.Unlock()
	if !onThisTab {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
		return
	}
	if atomic.LoadInt32(&state.pingRunning) == 1 {
		cancelPingAll()
	}
	if atomic.LoadInt32(&state.speedRunning) == 1 {
		cancelSpeedAll()
	}
	// Wait for tests to actually finish before returning
	for i := 0; i < 70; i++ {
		if atomic.LoadInt32(&state.pingRunning) == 0 && atomic.LoadInt32(&state.speedRunning) == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleReload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	state.mu.RLock()
	tabID := state.activeTab
	var sourceURLs []string
	var sourceFiles []TabFile
	var ghReady bool
	for _, t := range state.tabs {
		if t.ID == tabID {
			sourceURLs = t.SourceURLs
			sourceFiles = t.SourceFiles
			ghReady = t.gitHubReady()
			break
		}
	}
	state.mu.RUnlock()

	// Stop an in-flight ping/speed run IMMEDIATELY — but ONLY if it's testing
	// THIS tab. Tests are global (one at a time across all tabs), so reloading a
	// different tab must not abort a test running elsewhere; it just refreshes
	// its own configs. cancelTestsOnTab checks testingTab before cancelling.
	// When it does cancel, two effects: the bulk loop stops taking new configs
	// AND the in-flight ping/download aborts within one read (via the cancel
	// channel threaded into measurePing/measureSpeedOne); it also makes the
	// runner skip reconcileBulkResults, so old results never re-assert onto the
	// freshly-reset list (the historical "old test bled into the new list" bug).
	cancelTestsOnTab(tabID)
	// Briefly mark the tab cancelled so any bulk loop bound to THIS tab bails
	// before we re-broadcast the fresh list. It's per-tab, so safe to set
	// unconditionally. We clear it on a short independent timer — the reload
	// itself fires IMMEDIATELY below (no longer gated behind a 300ms sleep, which
	// made RELOAD feel laggy on idle tabs).
	state.mu.Lock()
	state.cancelledTabs[tabID] = true
	state.mu.Unlock()
	go func() {
		time.Sleep(300 * time.Millisecond)
		state.mu.Lock()
		delete(state.cancelledTabs, tabID)
		state.mu.Unlock()
	}()

	if tabID == "main" {
		go fetchAndInit()
	} else if len(sourceURLs) > 0 || len(sourceFiles) > 0 || ghReady {
		go func() {
			state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: tabID})
			fetchTabURLs(tabID, sourceURLs, sourceFiles)
		}()
	} else {
		// No URL: reset all test results for this tab (memory + store).
		go func() {
			resetTabResultsMem(tabID)
			loadedSignal(tabID)
		}()
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// ─────────────────────────── tab handlers ───────────────────────

func handleTabCreate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	n := nextTabNumber()
	// Use timestamp in ID to prevent reuse of cancelled tab IDs.
	// Display name uses the sequential number for readability.
	tab := Tab{
		ID:       fmt.Sprintf("tab-%d-%d", n, time.Now().UnixMilli()),
		Name:     fmt.Sprintf("Tab %d", n),
		IsMain:   false,
		Closable: true,
	}
	state.mu.Lock()
	state.tabs = append(state.tabs, tab)
	delete(state.cancelledTabs, tab.ID) // clear stale cancellation from deleted tab with same ID
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()
	json.NewEncoder(w).Encode(tab)
}

func handleTabDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	if id == "main" {
		http.Error(w, "cannot delete main tab", 400)
		return
	}
	state.mu.Lock()
	state.cancelledTabs[id] = true // signal running tests to stop
	for i, t := range state.tabs {
		if t.ID == id {
			state.tabs = append(state.tabs[:i], state.tabs[i+1:]...)
			break
		}
	}
	delete(state.tabEntries, id) // drop the closed tab's in-memory configs
	if state.activeTab == id {
		state.activeTab = "main"
		state.entries = state.tabEntries["main"]
	}
	state.mu.Unlock()
	memInvalidate(id)
	// The tab's already gone from memory — tell the UI now so a big tab disappears
	// instantly, and drop its DB rows in the background (a single DELETE on 200k
	// rows can take a second+). Orphaned rows from a crash mid-delete are swept on
	// the next startup (sweepOrphanTabRows).
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	state.broadcast(SSEEvent{Type: "active_tab", Payload: state.activeTab})
	loadedSignal("main")
	saveTabs()
	if store != nil {
		go store.deleteTabRows(id)
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// loadedSignal tells connected clients that a tab's config set changed; they
// re-fetch the visible window via /api/tab/window. The full list is no longer
// pushed over SSE (that serialization + the client-side JSON.parse of the whole
// list was the freeze on large tabs). Tagged with the tab so the client applies
// it only when that tab is active.
func loadedSignal(tabID string) {
	state.broadcast(SSEEvent{Type: "loaded", Payload: nil, Tab: tabID})
}

// windowQueryFromReq parses the shared window query params (sort/filter/proto/
// offset/limit) and resolves the tab's dedup mode + the favorites list. Used by
// both handleTabWindow and handleTabIndices.
func windowQueryFromReq(r *http.Request) (string, windowQuery) {
	q := r.URL.Query()
	id := q.Get("id")
	if id == "" {
		id = state.activeTab
	}
	state.mu.RLock()
	dedupHide := false
	for _, t := range state.tabs {
		if t.ID == id {
			dedupHide = t.DedupMode == "hide"
			break
		}
	}
	state.mu.RUnlock()
	settingsMu.RLock()
	favs := append([]string(nil), appSettings.Favorites...)
	settingsMu.RUnlock()
	var proto []string
	if p := strings.TrimSpace(q.Get("proto")); p != "" {
		proto = strings.Split(p, ",")
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 200
	}
	return id, windowQuery{
		sort: q.Get("sort"), filter: q.Get("filter"), proto: proto, dedupHide: dedupHide,
		favorites: favs, offset: offset, limit: limit,
	}
}

// handleTabWindow serves a window of a tab's configs from the IN-MEMORY store
// with sort / filter / dedup / favorites + header counters. The
// windowed client holds only the visible rows. Query: id, sort(idx|ping|speed),
// filter, proto, offset, limit. meta=1 also returns total + the header stats.
func handleTabWindow(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	id, wq := windowQueryFromReq(r)
	meta := r.URL.Query().Get("meta") == "1"
	rows, total, st := memWindow(id, wq, wq.favorites, meta)
	resp := map[string]interface{}{"rows": rows, "offset": wq.offset}
	if meta {
		resp["total"] = total
		resp["tab_total"] = memTabCount(id) // unfiltered count → "matching / total"
		resp["stats"] = map[string]interface{}{"total": st.total, "ok": st.ok, "err": st.fail, "min_ping": st.minPing, "max_speed": st.maxSpeed}
	}
	json.NewEncoder(w).Encode(resp)
}

// handleTabIndices returns every matching entry index in screen order. The
// windowed client uses it for "ping/speed all" so a bulk test covers the whole
// filtered set without the client holding the full list.
func handleTabIndices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	id, wq := windowQueryFromReq(r)
	idxs := memIndices(id, wq, wq.favorites)
	if idxs == nil {
		idxs = []int{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"idx": idxs})
}

// handleTabRaws returns raw config URLs in screen order so the windowed client
// can copy rows it never loaded. With all=1 it returns every matching row's
// index + raw (Ctrl+A / "select all" → copy the whole filtered set). Otherwise
// it reads a JSON array of indices from the body and returns their raws in the
// same order (shift-range copy).
func handleTabRaws(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Query().Get("all") == "1" {
		id, wq := windowQueryFromReq(r)
		idx, raw := memRawsOrdered(id, wq, wq.favorites)
		if idx == nil {
			idx = []int{}
		}
		if raw == nil {
			raw = []string{}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"idx": idx, "raw": raw})
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		id = state.activeTab
	}
	var idxs []int
	json.NewDecoder(r.Body).Decode(&idxs)
	raw := memRawsForIndices(id, idxs)
	if raw == nil {
		raw = []string{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"raw": raw})
}

// handleTabDeleteFailed removes every config whose ping OR speed test failed.
// Done server-side (over the whole tab) so it works regardless of which rows the
// windowed client currently holds. No-op on main (its entries are re-fetched).
func handleTabDeleteFailed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	if id == "" || id == "main" {
		w.WriteHeader(200)
		w.Write([]byte(`{"remaining":0}`))
		return
	}
	var kept []*ConfigEntry
	for _, e := range loadTabEntries(id) {
		if e.PingStatus != StatusFailed && e.SpeedStatus != StatusFailed {
			kept = append(kept, e)
		}
	}
	for i, e := range kept {
		e.Index = i
	}
	storeReplace(id, kept) // re-indexed; SQLite is the source of truth
	loadedSignal(id)
	saveTabs()
	w.WriteHeader(200)
	fmt.Fprintf(w, `{"remaining":%d}`, len(kept))
}

func handleTabSwitch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	state.mu.Lock()
	found := false
	for _, t := range state.tabs {
		if t.ID == id {
			found = true
			break
		}
	}
	if !found {
		state.mu.Unlock()
		http.Error(w, "tab not found", 404)
		return
	}
	state.activeTab = id
	inFlight := state.fetching[id]
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "active_tab", Payload: id})
	// The client loads the tab's first window on active_tab. If a reload is still
	// running, show the spinner instead; loadedSignal on completion re-triggers.
	if inFlight {
		state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: id})
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// pendingDeepLink holds a vair:// payload received before any UI client was
// connected (the app was launched by the link). handleSSE delivers it to the
// first client that connects, then clears it.
var (
	pendingDeepLink   string
	pendingDeepLinkMu sync.Mutex
)

// parseDeepLink extracts the import payload from a vair:// URL. Supported forms:
//
//	vair://import/<url-encoded subscription|config>
//	vair://import?url=<url-encoded …>
//
// Returns "" when raw isn't a vair:// link. Uses PathUnescape (not QueryUnescape)
// so a '+' inside a base64 config/subscription survives.
func parseDeepLink(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "vair://") {
		return ""
	}
	body := raw[len("vair://"):]
	var payload string
	if i := strings.IndexByte(body, '?'); i >= 0 {
		if v, err := url.ParseQuery(body[i+1:]); err == nil {
			payload = v.Get("url")
		}
	}
	if payload == "" {
		if i := strings.IndexByte(body, '/'); i >= 0 {
			payload = body[i+1:]
		}
	}
	if dec, err := url.PathUnescape(payload); err == nil && dec != "" {
		payload = dec
	}
	return strings.TrimSpace(payload)
}

// handleDeepLink receives a vair:// URL (from a second instance forwarding the
// link, or local startup), extracts the payload, raises the window, and pushes a
// "deeplink" SSE event so the connected UI imports it through the same routing as
// a paste/QR (subscription → tab source, configs → paste).
func handleDeepLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	payload := parseDeepLink(string(body))
	if payload == "" {
		http.Error(w, "not a vair:// import link", 400)
		return
	}
	focusMainWindow()
	state.broadcast(SSEEvent{Type: "deeplink", Payload: payload})
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// forwardDeepLink tries to hand a vair:// URL to an already-running instance over
// its local HTTP server. Returns true when delivered (so the second process can
// exit instead of starting a duplicate).
func forwardDeepLink(rawURL string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/api/deeplink", webPort),
		"text/plain", strings.NewReader(rawURL))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// isSubscriptionURL reports whether s is a single bare http(s) URL (no spaces,
// no embedded config) — i.e. a subscription link to fetch rather than parse.
func isSubscriptionURL(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func handleTabPaste(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	if id == "" {
		id = state.activeTab
	}
	if id == "main" {
		http.Error(w, "cannot paste into main tab", 400)
		return
	}
	// Mark the tab as fetching + show the spinner: parsing a big paste (hundreds
	// of thousands of configs) takes a moment, and a switch back to the tab during
	// it should keep the spinner (same as a reload). Cleared via loadedSignal +
	// the defer below.
	state.mu.Lock()
	state.fetching[id] = true
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: id})
	defer func() {
		state.mu.Lock()
		delete(state.fetching, id)
		state.mu.Unlock()
	}()
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024)) // ~250k configs; old 10 MB cap truncated big pastes at ~40k
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	// A bare subscription URL (from a scanned QR or a pasted link) is NOT parsed
	// here — the UI routes those to /api/tab/add-url so they're added as a
	// persistent tab source. This endpoint only ingests actual config lines.
	newEntries := parseConfigLines(string(body))
	existing := loadTabEntries(id)
	baseIdx := len(existing)
	for i, e := range newEntries {
		e.Index = baseIdx + i
	}
	existing = append(existing, newEntries...)

	// Apply server-side dedup when the tab is in "delete" mode. Without this,
	// dupes pasted into a delete-mode tab silently accumulated because "delete"
	// was only ever applied during fetchTabURLs / explicit mode transition.
	// Re-index so positions are contiguous after dedup.
	state.mu.RLock()
	var dedupMode string
	for _, t := range state.tabs {
		if t.ID == id {
			dedupMode = t.DedupMode
			break
		}
	}
	state.mu.RUnlock()
	if dedupMode == "delete" {
		existing = dedupByBody(existing)
		for i, e := range existing {
			e.Index = i
		}
	}

	storeReplace(id, existing) // SQLite is the source of truth now
	loadedSignal(id)
	saveTabs()
	w.WriteHeader(200)
	fmt.Fprintf(w, `{"added":%d}`, len(newEntries))
}

// handleTabAddURL appends a subscription URL to a user tab's source list and
// re-fetches it. Used when a scanned QR (or a pasted link) is a subscription:
// instead of a one-shot import it becomes a persistent, auto-refreshing source.
// If the tab already has source URLs, the new one is added alongside them
// (deduplicated against an exact match). The re-fetch reloads ALL of the tab's
// sources, so the combined config list reflects every URL.
func handleTabAddURL(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	id := r.URL.Query().Get("id")
	if id == "" {
		id = state.activeTab
	}
	if id == "main" {
		http.Error(w, "cannot add a source to the main tab", 400)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	u := strings.TrimSpace(string(body))
	if !isSubscriptionURL(u) {
		http.Error(w, "not a subscription URL", 400)
		return
	}
	var urls []string
	var files []TabFile
	found, added := false, false
	state.mu.Lock()
	for i := range state.tabs {
		if state.tabs[i].ID != id {
			continue
		}
		found = true
		dup := false
		for _, ex := range state.tabs[i].SourceURLs {
			if ex == u {
				dup = true
				break
			}
		}
		if !dup {
			state.tabs[i].SourceURLs = append(state.tabs[i].SourceURLs, u)
			added = true
		}
		urls = append([]string(nil), state.tabs[i].SourceURLs...)
		files = append([]TabFile(nil), state.tabs[i].SourceFiles...)
		break
	}
	state.mu.Unlock()
	if !found {
		http.Error(w, "tab not found", 404)
		return
	}
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()
	if state.activeTab == id {
		state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: id})
	}
	// Re-fetch with the full (possibly augmented) source set.
	go fetchTabURLs(id, urls, files)
	fmt.Fprintf(w, `{"added":%t,"urls":%d}`, added, len(urls))
}

func handleTabRename(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	name := r.URL.Query().Get("name")
	if id == "main" || name == "" {
		http.Error(w, "bad request", 400)
		return
	}
	state.mu.Lock()
	for i, t := range state.tabs {
		if t.ID == id {
			state.tabs[i].Name = name
			break
		}
	}
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// strSlicesEqual reports whether two string slices have identical contents in
// the same order (nil and empty are treated as equal).
func strSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func handleTabSetURL(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	type tabSettingsReq struct {
		URLs            []string  `json:"urls"`
		DisabledURLs    []string  `json:"disabled_urls"`
		Files           []TabFile `json:"files"`
		RefreshMin      int       `json:"refresh_min"`
		ExcludeFilter   []string  `json:"exclude_filter"`
		ExcludeDisabled bool      `json:"exclude_disabled"`
		RefreshDisabled bool      `json:"refresh_disabled"`
		// New 3-state field. Legacy "dedup":true is accepted below for
		// older clients during the migration window.
		DedupMode string `json:"dedup_mode"`
		Dedup     bool   `json:"dedup"`
		// "" / "ping" / "speed" — test to run after a scheduled auto-refresh.
		AutoRefreshTest string `json:"auto_refresh_test"`
		// Per-tab GitHub private-repo import via PAT.
		GitHubEnabled bool   `json:"github_enabled"`
		GitHubOwner   string `json:"github_owner"`
		GitHubRepo    string `json:"github_repo"`
		GitHubFile    string `json:"github_file"`
		GitHubPAT     string `json:"github_pat"`
	}
	var req tabSettingsReq
	if r.Body != nil {
		// Body contains URLs, file metadata, and config arrays. Content
		// is read directly from disk, never sent over this endpoint.
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
		json.Unmarshal(body, &req)
	}
	// Normalize the mode. Empty or unrecognized → "" (off). Old `dedup: true`
	// payloads from any in-flight client get treated as "hide".
	newMode := req.DedupMode
	switch newMode {
	case "off":
		newMode = ""
	case "hide", "delete", "":
		// recognized
	default:
		newMode = ""
	}
	if newMode == "" && req.Dedup {
		newMode = "hide"
	}

	// Clean URLs
	var cleanURLs []string
	for _, u := range req.URLs {
		u = strings.TrimSpace(u)
		if u != "" {
			cleanURLs = append(cleanURLs, u)
		}
	}
	// Disabled URLs: keep only those still present in the URL list (drop stale
	// entries for sources the user removed) so SourceDisabled never lingers.
	var cleanDisabled []string
	for _, u := range req.DisabledURLs {
		u = strings.TrimSpace(u)
		if u != "" && strInSlice(u, cleanURLs) && !strInSlice(u, cleanDisabled) {
			cleanDisabled = append(cleanDisabled, u)
		}
	}
	// Clean files: every file now needs a Path. Drag-drop is gone, so the
	// only way files enter the system is the native picker which always
	// provides a path. We stat each path here to refresh size/mtime for
	// display purposes; content is read lazily by fetchTabURLs.
	var cleanFiles []TabFile
	for _, f := range req.Files {
		f.Name = strings.TrimSpace(f.Name)
		f.Path = strings.TrimSpace(f.Path)
		if f.Path == "" {
			continue
		}
		if f.Name == "" {
			f.Name = filepath.Base(f.Path)
		}
		if info, err := os.Stat(f.Path); err == nil {
			f.Size = info.Size()
			f.Mtime = info.ModTime().Unix()
		}
		cleanFiles = append(cleanFiles, f)
	}

	// GitHub import fields: trim, strip a leading "/" off the file path. ghReady
	// means "fully configured" (used to decide whether this tab has any source to
	// fetch); the enable flag and raw fields are persisted regardless so a
	// half-filled form survives a save.
	ghEnabled := req.GitHubEnabled
	ghOwner := strings.TrimSpace(req.GitHubOwner)
	ghRepo := strings.TrimSpace(req.GitHubRepo)
	ghFile := strings.TrimLeft(strings.TrimSpace(req.GitHubFile), "/")
	ghPAT := strings.TrimSpace(req.GitHubPAT)
	ghReady := ghEnabled && ghOwner != "" && ghRepo != "" && ghFile != "" && ghPAT != ""
	// hasSource: does the tab have anything to fetch (URLs, files, or a ready
	// GitHub import)? Drives the re-fetch / clear decisions below so a
	// GitHub-only tab behaves like a URL-only one.
	hasSource := len(cleanURLs) > 0 || len(cleanFiles) > 0 || ghReady

	var sourcesChanged bool
	var excludeChanged bool
	var oldMode string
	state.mu.Lock()
	for i, t := range state.tabs {
		if t.ID == id {
			// The exclude filter is applied at FETCH time (matching configs are
			// dropped from the list), so a change to it — including toggling it
			// off — needs a rebuild to re-show / re-hide configs.
			excludeChanged = t.ExcludeDisabled != req.ExcludeDisabled ||
				!strSlicesEqual(t.ExcludeFilter, req.ExcludeFilter)
			if !t.IsMain {
				oldURLs := strings.Join(t.SourceURLs, "|")
				newURLs := strings.Join(cleanURLs, "|")
				oldDisabled := strings.Join(t.SourceDisabled, "|")
				newDisabled := strings.Join(cleanDisabled, "|")
				oldFilesKey := tabFilesKey(t.SourceFiles)
				newFilesKey := tabFilesKey(cleanFiles)
				oldGH := githubKey(t.GitHubEnabled, t.GitHubOwner, t.GitHubRepo, t.GitHubFile, t.GitHubPAT)
				newGH := githubKey(ghEnabled, ghOwner, ghRepo, ghFile, ghPAT)
				// Toggling a source on/off changes what gets fetched, so treat it
				// like a source change → triggers the rebuild below.
				sourcesChanged = (oldURLs != newURLs) || (oldDisabled != newDisabled) ||
					(oldFilesKey != newFilesKey) || (oldGH != newGH)
				oldMode = t.DedupMode
				state.tabs[i].SourceURLs = cleanURLs
				state.tabs[i].SourceDisabled = cleanDisabled
				state.tabs[i].SourceFiles = cleanFiles
				state.tabs[i].DedupMode = newMode
				state.tabs[i].GitHubEnabled = ghEnabled
				state.tabs[i].GitHubOwner = ghOwner
				state.tabs[i].GitHubRepo = ghRepo
				state.tabs[i].GitHubFile = ghFile
				state.tabs[i].GitHubPAT = ghPAT
			}
			state.tabs[i].RefreshMin = req.RefreshMin
			state.tabs[i].ExcludeFilter = req.ExcludeFilter
			state.tabs[i].ExcludeDisabled = req.ExcludeDisabled
			state.tabs[i].RefreshDisabled = req.RefreshDisabled
			// Normalize the after-auto-refresh test mode; applies to main + user tabs.
			switch req.AutoRefreshTest {
			case "ping", "speed":
				state.tabs[i].AutoRefreshTest = req.AutoRefreshTest
			default:
				state.tabs[i].AutoRefreshTest = ""
			}
			break
		}
	}
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()

	// If the exclude filter changed (content or the on/off toggle), rebuild the
	// list so dropped configs reappear / new matches are removed. Excluded
	// configs aren't kept server-side, so the only way to bring them back is to
	// re-fetch the tab's sources. (Sourceless paste-only tabs have nothing to
	// re-fetch and the filter never dropped their configs, so they fall through.)
	if excludeChanged {
		if id == "main" {
			go fetchAndInit()
			w.WriteHeader(200)
			w.Write([]byte("ok"))
			return
		}
		if hasSource {
			// The upcoming fetch replaces the store; the spinner covers the gap.
			if state.activeTab == id {
				state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: id})
			}
			go fetchTabURLs(id, cleanURLs, cleanFiles)
			w.WriteHeader(200)
			w.Write([]byte("ok"))
			return
		}
	}

	// "delete" mode requested AND mode has actually transitioned into delete
	// AND sources didn't change → apply server-side dedup to the current
	// entries in place, so duplicates disappear on the switch instead of only
	// after the next reload. Applies to the main tab too. (When sources
	// changed we re-fetch below, and that path dedups via fetchTabURLs.)
	if !sourcesChanged && newMode == "delete" && oldMode != "delete" {
		applyDeleteDedupInPlace(id)
		w.WriteHeader(200)
		w.Write([]byte("ok"))
		return
	}

	// Otherwise: dedup is purely a view-filter concern — toggling it must
	// not touch server entries, otherwise paste-only tabs (no URLs, no
	// files) lose every config the moment dedup is flipped. The client
	// already picked up the new dedup_mode via the tabs_update broadcast
	// above and re-rendered.
	if !sourcesChanged || id == "main" {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
		return
	}
	if hasSource {
		// The upcoming fetch replaces the store; the spinner covers the gap.
		if state.activeTab == id {
			state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: id})
		}
		go fetchTabURLs(id, cleanURLs, cleanFiles)
	} else {
		// All sources removed — clear the subscription info too (this branch
		// doesn't go through fetchTabURLs, where Subs is otherwise reconciled).
		state.mu.Lock()
		for i := range state.tabs {
			if state.tabs[i].ID == id {
				state.tabs[i].Subs = nil
				break
			}
		}
		state.mu.Unlock()
		storeReplace(id, nil) // all sources removed, no re-fetch → clear the store now
		state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
		loadedSignal(id)
		saveTabs()
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// githubKey produces a deterministic string for change-detection on a tab's
// GitHub import config. A disabled import collapses to "" so toggling it off
// (or editing fields while disabled) is treated as "no GitHub source"; enabling
// it or changing any field while enabled changes the key and triggers a
// re-fetch.
func githubKey(enabled bool, owner, repo, file, pat string) string {
	if !enabled {
		return ""
	}
	return owner + "\x00" + repo + "\x00" + file + "\x00" + pat
}

// tabFilesKey produces a deterministic string for change-detection on a list
// of files. Two file lists are "the same set of sources" if their (name,path)
// pairs match in order. Size/mtime are intentionally NOT part of the key —
// content changes on disk are picked up by RELOAD, not by save.
func tabFilesKey(files []TabFile) string {
	var sb strings.Builder
	for _, f := range files {
		sb.WriteString(f.Name)
		sb.WriteByte('|')
		sb.WriteString(f.Path)
		if f.Disabled {
			sb.WriteString("|off") // toggling on/off changes what gets fetched
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// fetchTabURLs fetches configs from multiple URLs and replaces tab entries.
// Files (already loaded content) are appended after URL contents in order.
//
// For each file with a Path, we re-read the file from disk before parsing
// so RELOAD picks up edits the user made outside of Vair. The freshly read
// content (and updated mtime) is written back into state.tabs so it gets
// persisted and the next reload starts from the same baseline.
func fetchTabURLs(tabID string, urls []string, files []TabFile) {
	// Mark this tab as fetching so switching to it shows a spinner (not a stale/
	// empty list), and clear it however we return.
	state.mu.Lock()
	state.fetching[tabID] = true
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		delete(state.fetching, tabID)
		state.mu.Unlock()
	}()

	// Snapshot which URL sources the user switched off — skipped on fetch.
	state.mu.RLock()
	var disabled []string
	for _, t := range state.tabs {
		if t.ID == tabID {
			disabled = t.SourceDisabled
			break
		}
	}
	state.mu.RUnlock()

	var allEntries []*ConfigEntry
	// One record per ENABLED URL source: its metadata (with URL + config count) on
	// success, or {URL, Error} on failure — so the settings modal can show which
	// link each came from and surface a source that didn't load. Rebuilt every
	// fetch, so removing/disabling a source drops its record (reconciliation).
	var subs []subMeta
	for _, u := range urls {
		if strInSlice(u, disabled) {
			continue
		}
		lines, meta, err := fetchURLMeta(u)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ tab %s fetch %s: %v\n", tabID, u, err)
			subs = append(subs, subMeta{URL: u, Error: err.Error()})
			continue
		}
		if len(lines) == 0 {
			// Reachable but carried no configs (wrong link, HTML error page, …) —
			// surface it like an unreachable source rather than silently nothing.
			subs = append(subs, subMeta{URL: u, Error: "no configs found"})
			continue
		}
		rec := subMeta{URL: u}
		if meta != nil {
			rec = *meta // already carries URL + Count
		} else {
			rec.Count = len(lines) // loaded OK but no metadata
		}
		subs = append(subs, rec)
		entries := parseConfigLines(strings.Join(lines, "\n"))
		allEntries = append(allEntries, entries...)
	}
	// Reconcile subscription info NOW (before any early return) so a tab emptied of
	// sources, an all-failed fetch, or a disabled source updates the display.
	state.mu.Lock()
	for i := range state.tabs {
		if state.tabs[i].ID == tabID {
			state.tabs[i].Subs = subs
			break
		}
	}
	state.mu.Unlock()
	// Read each file fresh from its on-disk path. Content lives in memory
	// only for the moment it takes parseConfigLines to walk through it —
	// after that it's eligible for GC. We refresh size/mtime on the way so
	// the UI can display current file metadata. A missing or unreadable
	// file just contributes zero entries this cycle (same behaviour URLs
	// have on fetch failure); the user can RELOAD again later.
	updatedFiles := make([]TabFile, len(files))
	copy(updatedFiles, files)
	for i := range updatedFiles {
		f := &updatedFiles[i]
		if f.Path == "" {
			fmt.Fprintf(os.Stderr, "⚠ tab %s: file %q has no path, skipping\n", tabID, f.Name)
			continue
		}
		if f.Disabled {
			continue // user switched this file source off
		}
		// Refresh stat info for the UI/persistence (size, mtime).
		if info, statErr := os.Stat(f.Path); statErr == nil {
			f.Size = info.Size()
			f.Mtime = info.ModTime().Unix()
		}
		// Stream the file line by line. parseConfigFile uses bufio.Scanner
		// internally so peak memory is one line, not the whole file —
		// essential for the multi-hundred-MB subscription dumps some
		// users have. Each entry's Raw owns its own bytes, so the file
		// contents become eligible for GC the moment scanning finishes.
		entries, err := parseConfigFile(f.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ tab %s read %s: %v\n", tabID, f.Path, err)
			continue
		}
		allEntries = append(allEntries, entries...)
	}

	// GitHub private-repo import (per-tab, via PAT). Appended after URL + file
	// sources. Config is read from the live tab so every fetch path (manual
	// reload, auto-refresh, settings save) picks it up without extra plumbing.
	state.mu.RLock()
	var ghOwner, ghRepo, ghFile, ghPAT string
	var ghReady bool
	for _, t := range state.tabs {
		if t.ID == tabID {
			ghReady = t.gitHubReady()
			ghOwner, ghRepo, ghFile, ghPAT = t.GitHubOwner, t.GitHubRepo, t.GitHubFile, t.GitHubPAT
			break
		}
	}
	state.mu.RUnlock()
	if ghReady {
		ghLines, err := fetchGitHubPATContent(ghOwner, ghRepo, ghFile, ghPAT)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ tab %s GitHub import: %v\n", tabID, err)
			vlog("warning", "tab: GitHub import failed (%s/%s): %v", ghOwner, ghRepo, err)
		} else {
			entries := parseConfigLines(strings.Join(ghLines, "\n"))
			allEntries = append(allEntries, entries...)
			vlog("info", "tab: imported %d config(s) from GitHub %s/%s", len(ghLines), ghOwner, ghRepo)
		}
	}

	// If nothing fetched, keep existing entries — but still push the reconciled
	// subscription info (errors / cleared sources) to the UI.
	if len(allEntries) == 0 {
		fmt.Fprintf(os.Stderr, "⚠ tab %s: no configs fetched, keeping existing\n", tabID)
		state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
		// Always re-signal (tagged) so the tab's spinner clears even when the
		// fetch finished while the tab was inactive and is now switched back to.
		loadedSignal(tabID)
		return
	}

	// Apply per-tab exclude filter and read dedup mode
	state.mu.RLock()
	var excludeFilter []string
	var dedupMode string
	for _, t := range state.tabs {
		if t.ID == tabID {
			if !t.ExcludeDisabled {
				excludeFilter = t.ExcludeFilter
			}
			dedupMode = t.DedupMode
			break
		}
	}
	state.mu.RUnlock()

	if len(excludeFilter) > 0 {
		var filtered []*ConfigEntry
		for _, e := range allEntries {
			if !shouldSkip(e.Name, e.Protocol, e.Host, e.Network, e.Security, excludeFilter) {
				filtered = append(filtered, e)
			}
		}
		allEntries = filtered
	}

	// "delete" mode removes body-duplicates on the server side. "hide" is a
	// client-side view filter handled by matches() in the JS — server keeps
	// every entry in that case so toggling back to "off" / "hide" → "off"
	// restores everything instantly. "" / unset means no dedup at all.
	if dedupMode == "delete" {
		allEntries = dedupByBody(allEntries)
	}

	// Always: rename duplicate display names so each entry is uniquely
	// identifiable in the table — applied to every tab globally.
	disambiguateNames(allEntries)

	// Re-index
	for i, e := range allEntries {
		e.Index = i
	}
	state.mu.Lock()
	// Persist any updated file content/mtime back to the tab so it survives
	// restart and the next RELOAD doesn't have to detect changes from scratch.
	for i := range state.tabs {
		if state.tabs[i].ID == tabID {
			state.tabs[i].SourceFiles = updatedFiles
			// Subs were reconciled right after the URL loop above.
			break
		}
	}
	state.mu.Unlock()
	addedN, removedN, addedIdx := reloadDelta(tabID, allEntries) // before storeReplace overwrites
	storeReplace(tabID, allEntries)                              // persist to the store (outside the lock — DB write can be slow)
	// Always signal tagged with the tab. The client applies it only when this tab
	// is active (events are tab-filtered), so a fetch that finishes after the user
	// switched away — then back — still lands.
	loadedSignal(tabID)
	broadcastReloadDelta(tabID, addedN, removedN, addedIdx)
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()
	// Re-ping this tab if it's in the auto-connect pool (entries were rebuilt
	// with no ping data). No-ops when auto-connect is off.
	go autoPingAfterRefresh(tabID)
	// Multi-hundred-MB subscription files can leave Go's heap several GB
	// large after parsing — the previous entries are unreferenced but the
	// runtime would normally hold that memory for future allocations. Hint
	// at returning it to the OS so Task Manager actually reflects the
	// drop. Doing this here (rather than on a timer) means we release
	// right after the big spike, when there's nothing else to allocate.
	debug.FreeOSMemory()
}

func handleTabDeleteEntries(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	if id == "main" {
		http.Error(w, "cannot delete from main tab", 400)
		return
	}
	// 16 MB covers "select all → delete" on a huge tab (a JSON array of a few
	// hundred thousand ints).
	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	var indices []int
	if err := json.Unmarshal(body, &indices); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), 400)
		return
	}
	// Deleting a big selection writes thousands of rows — show the spinner while it
	// runs (the DB delete stays synchronous so the removal is persisted before we
	// confirm; loadedSignal below clears the spinner). Skipped for small deletes,
	// which are instant.
	if len(indices) > 2000 {
		state.mu.Lock()
		state.fetching[id] = true
		state.mu.Unlock()
		state.broadcast(SSEEvent{Type: "loading", Payload: map[string]string{"op": "delete"}, Tab: id})
		defer func() {
			state.mu.Lock()
			delete(state.fetching, id)
			state.mu.Unlock()
		}()
	}
	toRemove := make(map[int]bool, len(indices))
	for _, idx := range indices {
		toRemove[idx] = true
	}
	// Drop the rows from the in-memory store IN PLACE. Survivors keep their idx —
	// we deliberately do NOT re-index: every consumer looks entries up by idx
	// (not array position), so gaps are harmless, and this lets us delete only the
	// removed rows from SQLite instead of rewriting the whole tab. For a few rows
	// out of hundreds of thousands that's near-instant vs a multi-second replace.
	state.mu.Lock()
	src := state.tabEntries[id]
	kept := make([]*ConfigEntry, 0, len(src))
	for _, e := range src {
		if !toRemove[e.Index] {
			kept = append(kept, e)
		}
	}
	state.tabEntries[id] = kept
	if state.activeTab == id {
		state.entries = kept
	}
	state.mu.Unlock()
	memInvalidate(id)
	if store != nil {
		store.deleteEntriesByIdx(id, indices) // incremental DB delete (only the removed rows)
	}
	loadedSignal(id)
	saveTabs()
	w.WriteHeader(200)
	fmt.Fprintf(w, `{"remaining":%d}`, len(kept))
}

func handleTabReorder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	var ids []string
	if err := json.Unmarshal(body, &ids); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), 400)
		return
	}
	state.mu.Lock()
	tabMap := make(map[string]Tab)
	for _, t := range state.tabs {
		tabMap[t.ID] = t
	}
	newTabs := make([]Tab, 0, len(ids))
	for _, id := range ids {
		if t, ok := tabMap[id]; ok {
			newTabs = append(newTabs, t)
			delete(tabMap, id)
		}
	}
	// Append any remaining tabs not in the reorder list
	for _, t := range state.tabs {
		if _, ok := tabMap[t.ID]; ok {
			newTabs = append(newTabs, t)
		}
	}
	state.tabs = newTabs
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == "GET" {
		w.Header().Set("Content-Type", "application/json")
		settingsMu.RLock()
		json.NewEncoder(w).Encode(appSettings)
		settingsMu.RUnlock()
		return
	}
	// POST: update settings
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	var newSettings AppSettings
	if err := json.Unmarshal(body, &newSettings); err != nil {
		http.Error(w, "bad json: "+err.Error(), 400)
		return
	}
	settingsMu.Lock()
	// Preserve server-authoritative counters. The Settings UI loads them
	// at open time and POSTs them back unchanged, but the 30s ticker can
	// update them while the dialog is open — using the client's snapshot
	// would roll them back. Reset goes through a dedicated endpoint.
	newSettings.StatsTotalUp = appSettings.StatsTotalUp
	newSettings.StatsTotalDown = appSettings.StatsTotalDown
	// Only the dedicated AUTO master toggle (POST /api/settings?auto=1) may change
	// the AUTO state. A generic settings save (autostart, theme, tray, …) carries
	// the client's whole settings object; if that copy has drifted from the server
	// (e.g. after an import, or an unreloaded tab) it must NOT flip AUTO on/off as
	// a side effect — that was the "enabling Launch-at-startup turns AUTO on" bug.
	// So for non-AUTO saves we keep the server's current AutoConnect value and run
	// none of the connect/disconnect side effects.
	autoCtl := r.URL.Query().Get("auto") == "1"
	if !autoCtl {
		newSettings.AutoConnect = appSettings.AutoConnect
	}
	// Detect the auto-connect master toggle flipping on. When the user enables
	// it (even while idle and never previously connected), arm autoWant so the
	// supervisor connects from idle on its next tick. We only act on the
	// false→true transition.
	autoEnabledNow := autoCtl && newSettings.AutoConnect && !appSettings.AutoConnect
	autoDisabledNow := autoCtl && !newSettings.AutoConnect && appSettings.AutoConnect
	// Detect rank-by-speed flipping on while auto is already enabled — that needs
	// a fresh speed test of the pool so the new ranking has data to work with.
	speedRankEnabledNow := autoCtl && newSettings.AutoConnect && newSettings.AutoRankBySpeed &&
		!(appSettings.AutoConnect && appSettings.AutoRankBySpeed) && !autoEnabledNow
	// Reconcile the Windows logon Run-key only when the toggle actually changed.
	autostartChanged := newSettings.AutostartEnabled != appSettings.AutostartEnabled
	deepLinkChanged := newSettings.DeepLinkEnabled != appSettings.DeepLinkEnabled
	appSettings = newSettings
	settingsMu.Unlock()
	if autostartChanged {
		if err := applyAutostart(newSettings.AutostartEnabled); err != nil {
			vlog("warning", "autostart: could not update Run key: %v", err)
		}
	}
	if deepLinkChanged {
		if err := registerDeepLink(newSettings.DeepLinkEnabled); err != nil {
			vlog("warning", "deeplink: could not update vair:// scheme: %v", err)
		}
	}
	if speedRankEnabledNow {
		vlog("info", "auto: rank-by-speed enabled — running speed test on candidate pool")
		go autoTestTabs(autoPool(), true)
	}
	if autoEnabledNow {
		autoWant.Store(true)
		vlog("info", "auto: enabled — will connect to the fastest working config")
		// Clear any stale "paused" the panel is still showing from an earlier
		// manual disconnect: enabling means the supervisor is taking over, so
		// reflect that immediately rather than leaving "Paused" up until the
		// first auto-connect lands.
		if ecs := state.conn.snap(); ecs.Status == ConnConnected {
			autoProbeNow.Store(true) // measure this link's ping for the panel soon
			broadcastAuto("health", ecs.EntryName, ecs.ConnRaw, "")
		} else {
			broadcastAuto("idle", "", "", "")
		}
		// Refresh ping (and speed, if ranking by speed) data for the candidate
		// pool so the supervisor can rank configs on its next tick.
		settingsMu.RLock()
		withSpeed := appSettings.AutoRankBySpeed
		settingsMu.RUnlock()
		go autoTestTabs(autoPool(), withSpeed)
	}
	if autoDisabledNow {
		// Master toggle turned off → disarm auto and disconnect the live
		// connection immediately (user chose "disconnect immediately").
		autoWant.Store(false)
		cm := state.conn
		cm.mu.Lock()
		orig := cm.state.Status
		if orig != ConnIdle && orig != ConnDisconnecting {
			cm.state.Status = ConnDisconnecting
		}
		s := cm.state
		cm.mu.Unlock()
		if orig != ConnIdle && orig != ConnDisconnecting {
			state.broadcast(SSEEvent{Type: "conn_update", Payload: s})
			vlog("info", "auto: disabled — disconnecting active connection")
			go stopConnection()
		}
	}
	// Nudge the supervisor so an enable/disable (or interval/threshold change)
	// takes effect immediately instead of up to one 2s tick later.
	autoKick()
	saveSettings()
	state.broadcast(SSEEvent{Type: "stats_update", Payload: statsSnapshot(currentSessionCounter())})
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// currentSessionCounter returns the live traffic counter if a session is
// active, or nil otherwise. Used by handlers that need a fresh stats
// snapshot without poking at connManager internals from the outside.
func currentSessionCounter() *trafficCounter {
	state.conn.mu.Lock()
	defer state.conn.mu.Unlock()
	return state.conn.counter
}

// handleStatsReset clears the lifetime traffic totals. The live session
// counter (if any) is left alone — the user is asking to reset
// "lifetime", and the current session will fold in normally on
// disconnect, starting accumulation fresh from zero.
func handleStatsReset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	settingsMu.Lock()
	appSettings.StatsTotalUp = 0
	appSettings.StatsTotalDown = 0
	settingsMu.Unlock()
	// Forget what was already folded in for the live session so its
	// future deltas don't immediately re-inflate the just-cleared
	// total. The visible session bytes (counter.Up/Down) stay intact.
	if c := currentSessionCounter(); c != nil {
		c.LastPersistedUp.Store(c.Up.Load())
		c.LastPersistedDown.Store(c.Down.Load())
	}
	saveSettings()
	state.broadcast(SSEEvent{Type: "stats_update", Payload: statsSnapshot(currentSessionCounter())})
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleRestartAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(200)
	w.Write([]byte("restarting"))
	go func() {
		time.Sleep(200 * time.Millisecond)
		stopConnection()
		killOrphanedXray()
		restartAsAdmin()
	}()
}

// handleStorageOpen reveals %LOCALAPPDATA%\vair in Explorer so the user
// can see/back up tabs.json, settings.json, and the extracted xray/sing-box
// binaries. Failures are reported as a 500 with the error text so the UI
// can flash a notification.
func handleStorageOpen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if err := openStorageLocation(tabsDir()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// handleLogs returns the current log buffer as JSON — used by the Logs panel
// to fill itself on open. Live updates afterwards arrive via the SSE "log".
func handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs.snapshot())
}

// handleLogsClear empties the log buffer (the panel's "Clear" button).
func handleLogsClear(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	logs.clear()
	go debug.FreeOSMemory() // hand the buffer's heap back to the OS
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// handleUpdateCheck reports the current version and whether a newer build is
// published. Only the public fields of updateInfo are serialized (the download
// URL + checksum stay server-side; apply re-fetches the manifest itself).
func handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(checkForUpdate())
}

// handleUpdateApply kicks off the download + verify + replace flow in the
// background; progress streams over SSE as update_status events. Responds
// immediately so the UI can start listening.
func handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	go runUpdate()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// handleUpdateDismiss records "don't show again" for ?version=, so the startup
// banner stays hidden until a strictly newer version ships.
func handleUpdateDismiss(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	dismissUpdateVersion(r.URL.Query().Get("version"))
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// settingsExport is the on-disk file format produced by /api/export and
// consumed by /api/import. Schema version is checked on import; bump it
// whenever a field changes shape so old exports either keep working or
// fail with a clear message instead of silently corrupting state.
type settingsExport struct {
	Version     int            `json:"version"`
	ExportedAt  string         `json:"exported_at"`
	AppName     string         `json:"app"`
	AppSettings AppSettings    `json:"app_settings"`
	Tabs        []persistedTab `json:"tabs"`
}

const settingsExportVersion = 1

// buildSettingsExport snapshots tabs + their current config entries + the
// app settings into a single document. We send config entries (not just
// source URLs) because a tab might be a hand-pasted collection with no
// source URL — round-tripping it through export/import has to preserve
// that data.
func buildSettingsExport() settingsExport {
	state.mu.RLock()
	var tabs []persistedTab
	for _, t := range state.tabs {
		pt := persistedTab{
			ID: t.ID, Name: t.Name,
			SourceURLs: t.SourceURLs, SourceDisabled: t.SourceDisabled, SourceFiles: t.SourceFiles,
			RefreshMin: t.RefreshMin, ExcludeFilter: t.ExcludeFilter,
			ExcludeDisabled: t.ExcludeDisabled, RefreshDisabled: t.RefreshDisabled,
			DedupMode:       t.DedupMode,
			AutoRefreshTest: t.AutoRefreshTest, Subs: t.Subs,
			GitHubEnabled: t.GitHubEnabled, GitHubOwner: t.GitHubOwner,
			GitHubRepo: t.GitHubRepo, GitHubFile: t.GitHubFile, GitHubPAT: t.GitHubPAT,
		}
		if !t.IsMain {
			// Snapshot the raw config strings for pasted tabs (from memory; we
			// already hold state.mu.RLock) so the import on another machine sees the
			// same configs without needing the original source URL to be reachable.
			for _, e := range state.tabEntries[t.ID] {
				if e.Raw != "" {
					pt.Configs = append(pt.Configs, e.Raw)
				}
			}
		}
		tabs = append(tabs, pt)
	}
	state.mu.RUnlock()
	settingsMu.RLock()
	settingsCopy := appSettings
	settingsMu.RUnlock()
	return settingsExport{
		Version:     settingsExportVersion,
		ExportedAt:  time.Now().UTC().Format(time.RFC3339),
		AppName:     "Vair",
		AppSettings: settingsCopy,
		Tabs:        tabs,
	}
}

func handleSettingsExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	data, err := json.MarshalIndent(buildSettingsExport(), "", "  ")
	if err != nil {
		http.Error(w, "marshal: "+err.Error(), 500)
		return
	}
	filename := fmt.Sprintf("vair_settings_%s.json", time.Now().Format("20060102_150405"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(200)
	w.Write(data)
}

// handleSettingsImport accepts a settingsExport JSON payload and applies it
// in place of the current state. The on-disk tabs.json / settings.json get
// rewritten, the in-memory state is rebuilt, and clients are told to
// reload via the SSE channel so the UI re-renders with the new tabs.
func handleSettingsImport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	// tabs=0 → import only the app settings; leave the user's existing tabs
	// untouched. Default (no param, or anything except "0") is full import.
	includeTabs := r.URL.Query().Get("tabs") != "0"

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	var imp settingsExport
	if err := json.Unmarshal(body, &imp); err != nil {
		http.Error(w, "parse JSON: "+err.Error(), 400)
		return
	}
	if imp.Version == 0 || imp.Version > settingsExportVersion {
		http.Error(w, fmt.Sprintf("unsupported export version %d (expected %d)", imp.Version, settingsExportVersion), 400)
		return
	}
	if includeTabs && len(imp.Tabs) == 0 {
		http.Error(w, "no tabs in export", 400)
		return
	}

	// Take ownership of the new state. Stop any running tests so they
	// don't reference about-to-be-replaced *ConfigEntry pointers.
	if atomic.LoadInt32(&state.pingRunning) == 1 {
		cancelPingAll()
	}
	if atomic.LoadInt32(&state.speedRunning) == 1 {
		cancelSpeedAll()
	}
	stopConnection()

	// Replace app settings (atomic on disk + in memory).
	settingsMu.Lock()
	appSettings = imp.AppSettings
	settingsMu.Unlock()
	saveSettings()

	// App-settings-only import: skip the tab rebuild entirely. The SSE push
	// at the end still refreshes any UI that watches app_settings.
	if !includeTabs {
		state.broadcast(SSEEvent{Type: "app_info", Payload: map[string]interface{}{
			"singbox_available": state.singboxBin != "",
			"is_admin":          checkAdmin(),
			"os":                "windows",
		}})
		w.WriteHeader(200)
		fmt.Fprintf(w, `{"tabs":0,"app_settings_only":true}`)
		return
	}

	// Rebuild tabs in memory from the imported document. Mirrors loadTabs
	// (which reads the same persistedTab shape) but on injected data
	// instead of tabs.json.
	state.mu.Lock()
	state.tabs = []Tab{{ID: "main", Name: "Sources", IsMain: true, Closable: false}}
	state.tabEntries = make(map[string][]*ConfigEntry) // wipe in-memory configs (rebuilt below)
	state.entries = nil
	imported := map[string][]*ConfigEntry{} // tabID → parsed configs, written to the store after unlock
	for _, pt := range imp.Tabs {
		if pt.ID == "main" {
			for i, t := range state.tabs {
				if t.ID == "main" {
					state.tabs[i].ExcludeFilter = pt.ExcludeFilter
					state.tabs[i].RefreshMin = pt.RefreshMin
					state.tabs[i].ExcludeDisabled = pt.ExcludeDisabled
					state.tabs[i].RefreshDisabled = pt.RefreshDisabled
					break
				}
			}
			continue
		}
		mode := pt.DedupMode
		if mode == "" && pt.Dedup {
			mode = "hide"
		}
		urls := pt.SourceURLs
		if len(urls) == 0 && pt.SourceURL != "" {
			urls = []string{pt.SourceURL}
		}
		tab := Tab{
			ID: pt.ID, Name: pt.Name, IsMain: false, Closable: true,
			SourceURLs: urls, SourceDisabled: pt.SourceDisabled, SourceFiles: pt.SourceFiles,
			RefreshMin: pt.RefreshMin, ExcludeFilter: pt.ExcludeFilter,
			ExcludeDisabled: pt.ExcludeDisabled, RefreshDisabled: pt.RefreshDisabled,
			DedupMode:       mode,
			AutoRefreshTest: pt.AutoRefreshTest, Subs: pt.subsOf(),
			GitHubEnabled: pt.GitHubEnabled, GitHubOwner: pt.GitHubOwner,
			GitHubRepo: pt.GitHubRepo, GitHubFile: pt.GitHubFile, GitHubPAT: pt.GitHubPAT,
		}
		state.tabs = append(state.tabs, tab)
		imported[tab.ID] = parseConfigLines(strings.Join(pt.Configs, "\n"))
	}
	// Make sure the active tab still exists; if the imported set dropped it,
	// fall back to "main".
	activeOK := false
	for _, t := range state.tabs {
		if t.ID == state.activeTab {
			activeOK = true
			break
		}
	}
	if !activeOK {
		state.activeTab = "main"
	}
	state.mu.Unlock()
	// Import rebuilds everything: wipe the store, then write each tab's configs.
	if store != nil {
		store.deleteAll()
		for tid, ents := range imported {
			storeReplace(tid, ents)
		}
	}
	saveTabs()

	// Push the new tab list + active tab; the client re-fetches the active tab's
	// window (the full list isn't pushed over SSE anymore).
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	state.broadcast(SSEEvent{Type: "active_tab", Payload: state.activeTab})
	loadedSignal(state.activeTab)

	w.WriteHeader(200)
	fmt.Fprintf(w, `{"tabs":%d}`, len(imp.Tabs))
}
