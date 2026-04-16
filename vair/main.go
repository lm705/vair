package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ─────────────────────────── constants ───────────────────────────

// ─────────────────────────── config sources ─────────────────────
type SourceDef struct {
	URL   string
	Group string
}

var sourceDefs = []SourceDef{
	{"https://raw.githubusercontent.com/lm705/vair/refs/heads/main/vless_alive.txt", ""},
}

const (
	githubOwner  = ""
	githubRepo   = ""
	githubFile   = ""
	githubPAT    = ""
	githubAPIURL = "https://api.github.com/repos/" + githubOwner + "/" + githubRepo + "/contents/" + githubFile
)

const (
	pingTestURL   = "https://www.gstatic.com/generate_204"
	pingTimeout   = 1500 * time.Millisecond
	warmupTimeout = 2 * time.Second
	pingRounds    = 3

	speedTestURL   = "https://speed.cloudflare.com/__down?bytes=10000000"
	speedDuration  = 4 * time.Second
	speedUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

	startupTimeout = 4 * time.Second
	dialTimeout    = 5 * time.Second

	// xrayStartupTimeout: time to wait for xray HTTP port to open during ping/speed test.
	// Increased for Windows Defender scan delay on first run of extracted binary.
	xrayStartupTimeout = 8 * time.Second

	// xrayConnTimeout: time to wait for xray to start in persistent proxy connection mode.
	xrayConnTimeout = 12 * time.Second

	// tunStartupTimeout: time to wait for sing-box TUN adapter to come up.
	tunStartupTimeout = 3 * time.Second

	pingConcurrency      = 10
	pingSpeedConcurrency = 5

	// Default ports for persistent proxy connection
	connHTTPPort  = 10819
	connSOCKSPort = 10818

	webPort = 19876
)

var skipTokens = []string{}

// ─────────────────────────── types ───────────────────────────────

type VlessParams struct {
	Raw           string
	UUID          string
	Host          string
	Port          int
	Name          string
	Network       string
	Security      string
	Path          string
	Host2         string
	ServiceName   string
	SNI           string
	ALPN          string
	Fingerprint   string
	AllowInsecure bool
	Flow          string
	PublicKey     string
	ShortID       string
	SpiderX       string
}

type Status string

const (
	StatusPending      Status = "pending"
	StatusTestingPing  Status = "testing_ping"
	StatusTestingSpeed Status = "testing_speed"
	StatusOK           Status = "ok"
	StatusFailed       Status = "failed"
	StatusSkipped      Status = "skipped"
)

type ConfigEntry struct {
	mu          sync.Mutex
	Index       int     `json:"index"`
	Raw         string  `json:"raw"`
	Name        string  `json:"name"`
	Host        string  `json:"host"`
	Port        int     `json:"port"`
	Network     string  `json:"network"`
	Security    string  `json:"security"`
	PingStatus  Status  `json:"ping_status"`
	Delay       int64   `json:"delay"`
	PingErr     string  `json:"ping_err,omitempty"`
	SpeedStatus Status  `json:"speed_status"`
	SpeedMBps   float64 `json:"speed_mbps"`
	SpeedErr    string  `json:"speed_err,omitempty"`
	SpeedLive   float64 `json:"speed_live"`
}

func (e *ConfigEntry) snap() ConfigEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	return *e
}

// ─────────────────────────── connection types ────────────────────

type ConnMode string
type ConnStatus string

const (
	ModeProxy ConnMode = "proxy"
	ModeTUN   ConnMode = "tun"
)
const (
	ConnIdle          ConnStatus = "idle"
	ConnConnecting    ConnStatus = "connecting"
	ConnConnected     ConnStatus = "connected"
	ConnDisconnecting ConnStatus = "disconnecting"
	ConnError         ConnStatus = "error"
)

type ConnState struct {
	Status     ConnStatus `json:"status"`
	Mode       ConnMode   `json:"mode"`
	EntryIndex int        `json:"entry_index"`
	EntryName  string     `json:"entry_name"`
	ConnTab    string     `json:"conn_tab"`
	HTTPPort   int        `json:"http_port,omitempty"`
	SOCKSPort  int        `json:"socks_port,omitempty"`
	TUNIface   string     `json:"tun_iface,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	ErrMsg     string     `json:"error,omitempty"`
	UptimeSec  int64      `json:"uptime_sec"`
}

type connManager struct {
	mu         sync.Mutex
	state      ConnState
	cmd        *exec.Cmd // main process (xray for proxy, sing-box for TUN)
	xrayCmd    *exec.Cmd // secondary xray process (hybrid TUN mode only)
	cancel     context.CancelFunc
	xrayCancel context.CancelFunc
	tmpCfg     string
	xrayTmpCfg string
}

func newConnManager() *connManager {
	return &connManager{state: ConnState{Status: ConnIdle, EntryIndex: -1}}
}

func (cm *connManager) snap() ConnState {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	s := cm.state
	if s.Status == ConnConnected && !s.StartedAt.IsZero() {
		s.UptimeSec = int64(time.Since(s.StartedAt).Seconds())
	}
	return s
}

// ─────────────────────────── tabs ───────────────────────────────

type Tab struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsMain    bool   `json:"is_main"`
	Closable  bool   `json:"closable"`
	SourceURL string `json:"source_url,omitempty"`
}

// persistedTab is the JSON structure saved to disk
type persistedTab struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	SourceURL string   `json:"source_url,omitempty"`
	Configs   []string `json:"configs,omitempty"`
}
type persistedData struct {
	Tabs []persistedTab `json:"tabs"`
}

// ─────────────────────────── SSE / state ─────────────────────────

type SSEEvent struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
	Tab     string      `json:"tab,omitempty"`
}

type AppState struct {
	mu            sync.RWMutex
	entries       []*ConfigEntry // active tab entries
	tabs          []Tab
	tabEntries    map[string][]*ConfigEntry // tab_id -> entries
	cancelledTabs map[string]bool           // tabs pending cancellation
	activeTab     string
	xrayBin       string
	singboxBin    string
	clients       map[chan SSEEvent]struct{}
	clientMu      sync.Mutex
	pingRunning   int32
	speedRunning  int32
	conn          *connManager
}

var state = &AppState{
	clients:       make(map[chan SSEEvent]struct{}),
	conn:          newConnManager(),
	tabEntries:    make(map[string][]*ConfigEntry),
	cancelledTabs: make(map[string]bool),
	activeTab:     "main",
	tabs:          []Tab{{ID: "main", Name: "Sources", IsMain: true, Closable: false}},
}

// ─────────────────────────── tab persistence ────────────────────

func tabsDir() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "vair")
	}
	if d := os.Getenv("APPDATA"); d != "" {
		return filepath.Join(d, "vair")
	}
	return "."
}

func tabsFilePath() string {
	return filepath.Join(tabsDir(), "tabs.json")
}

func saveTabs() {
	state.mu.RLock()
	var pd persistedData
	for _, t := range state.tabs {
		if t.IsMain {
			continue
		}
		pt := persistedTab{ID: t.ID, Name: t.Name, SourceURL: t.SourceURL}
		if entries, ok := state.tabEntries[t.ID]; ok {
			for _, e := range entries {
				pt.Configs = append(pt.Configs, e.Raw)
			}
		}
		pd.Tabs = append(pd.Tabs, pt)
	}
	state.mu.RUnlock()
	dir := tabsDir()
	os.MkdirAll(dir, 0755)
	data, err := json.MarshalIndent(pd, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ saveTabs: %v\n", err)
		return
	}
	if err := os.WriteFile(tabsFilePath(), data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ saveTabs write: %v\n", err)
	}
}

func loadTabs() {
	data, err := os.ReadFile(tabsFilePath())
	if err != nil {
		return // no saved data
	}
	var pd persistedData
	if err := json.Unmarshal(data, &pd); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ loadTabs: %v\n", err)
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, pt := range pd.Tabs {
		tab := Tab{ID: pt.ID, Name: pt.Name, IsMain: false, Closable: true, SourceURL: pt.SourceURL}
		state.tabs = append(state.tabs, tab)
		entries := parseConfigLines(strings.Join(pt.Configs, "\n"))
		state.tabEntries[tab.ID] = entries
	}
}

func nextTabNumber() int {
	state.mu.RLock()
	defer state.mu.RUnlock()
	used := make(map[int]bool)
	for _, t := range state.tabs {
		if strings.HasPrefix(t.ID, "custom-") {
			if n, err := strconv.Atoi(strings.TrimPrefix(t.ID, "custom-")); err == nil {
				used[n] = true
			}
		}
	}
	for i := 1; ; i++ {
		if !used[i] {
			return i
		}
	}
}

func (s *AppState) broadcast(ev SSEEvent) {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- ev:
		default:
		}
	}
}
func (s *AppState) addClient(ch chan SSEEvent) {
	s.clientMu.Lock()
	s.clients[ch] = struct{}{}
	s.clientMu.Unlock()
}
func (s *AppState) removeClient(ch chan SSEEvent) {
	s.clientMu.Lock()
	delete(s.clients, ch)
	s.clientMu.Unlock()
}

// ─────────────────────────── admin check ─────────────────────────

func checkAdmin() bool {
	f, err := os.Open(`\\.\PHYSICALDRIVE0`)
	if err == nil {
		f.Close()
		return true
	}
	return false
}

// ─────────────────────────── VLESS parsing ───────────────────────

func parseVless(raw string) (*VlessParams, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "vless://") {
		return nil, fmt.Errorf("not a vless URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("url.Parse: %w", err)
	}
	p := &VlessParams{Raw: raw}
	p.UUID = u.User.Username()
	p.Host = u.Hostname()
	if p.Host == "" {
		return nil, fmt.Errorf("empty host")
	}
	if port := u.Port(); port == "" {
		p.Port = 443
	} else if p.Port, err = strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("bad port: %w", err)
	}
	p.Name = u.Fragment
	if p.Name == "" {
		p.Name = fmt.Sprintf("%s:%d", p.Host, p.Port)
	}
	q := u.Query()
	get := func(k string) string { return q.Get(k) }
	p.Network = get("type")
	if p.Network == "" {
		p.Network = "tcp"
	}
	p.Security = get("security")
	if p.Security == "" {
		p.Security = "none"
	}
	p.Path, p.Host2, p.ServiceName = get("path"), get("host"), get("serviceName")
	p.SNI, p.ALPN, p.Fingerprint = get("sni"), get("alpn"), get("fp")
	p.AllowInsecure = get("allowInsecure") == "1"
	p.Flow = get("flow")
	p.PublicKey, p.ShortID, p.SpiderX = get("pbk"), get("sid"), get("spx")
	return p, nil
}

func shouldSkip(name string) bool {
	low := strings.ToLower(name)
	for _, tok := range skipTokens {
		if strings.Contains(low, tok) {
			return true
		}
	}
	return false
}

// ─────────────────────────── xray config (test + System Proxy) ───

func buildXrayConfig(p *VlessParams, httpPort, socksPort int) map[string]interface{} {
	user := map[string]interface{}{"id": p.UUID, "encryption": "none"}
	if p.Flow != "" {
		user["flow"] = p.Flow
	}
	outSettings := map[string]interface{}{
		"vnext": []interface{}{map[string]interface{}{
			"address": p.Host, "port": p.Port, "users": []interface{}{user},
		}},
	}
	stream := map[string]interface{}{"network": p.Network, "security": p.Security}
	switch p.Network {
	case "ws":
		ws := map[string]interface{}{"path": p.Path}
		if p.Host2 != "" {
			ws["headers"] = map[string]interface{}{"Host": p.Host2}
		}
		stream["wsSettings"] = ws
	case "grpc":
		stream["grpcSettings"] = map[string]interface{}{"serviceName": p.ServiceName, "multiMode": false}
	case "h2", "http":
		h2 := map[string]interface{}{"path": p.Path}
		if p.Host2 != "" {
			h2["host"] = []string{p.Host2}
		}
		stream["httpSettings"] = h2
	case "httpupgrade":
		hu := map[string]interface{}{"path": p.Path}
		if p.Host2 != "" {
			hu["host"] = p.Host2
		}
		stream["httpupgradeSettings"] = hu
	case "splithttp", "xhttp":
		// xhttp is the newer name for splithttp in xray-core 1.8+
		sh := map[string]interface{}{"path": p.Path}
		if p.Host2 != "" {
			sh["host"] = p.Host2
		}
		stream["xhttpSettings"] = sh
	}
	switch p.Security {
	case "tls":
		tls := map[string]interface{}{"serverName": p.SNI, "allowInsecure": p.AllowInsecure}
		if p.Fingerprint != "" {
			tls["fingerprint"] = p.Fingerprint
		}
		if p.ALPN != "" {
			tls["alpn"] = strings.Split(p.ALPN, ",")
		}
		stream["tlsSettings"] = tls
	case "reality":
		stream["realitySettings"] = map[string]interface{}{
			"serverName": p.SNI, "fingerprint": p.Fingerprint,
			"publicKey": p.PublicKey, "shortId": p.ShortID, "spiderX": p.SpiderX,
		}
	}
	persistent := socksPort > 0
	sniffing := map[string]interface{}{
		"enabled":      persistent,
		"destOverride": []string{"http", "tls", "quic"},
	}
	inbounds := []interface{}{
		map[string]interface{}{
			"tag": "http", "listen": "127.0.0.1", "port": httpPort,
			"protocol": "http",
			"settings": map[string]interface{}{"auth": "noauth"},
			"sniffing": sniffing,
		},
	}
	if persistent {
		inbounds = append(inbounds, map[string]interface{}{
			"tag": "socks", "listen": "127.0.0.1", "port": socksPort,
			"protocol": "socks",
			"settings": map[string]interface{}{"auth": "noauth", "udp": true},
			"sniffing": sniffing,
		})
	}
	cfg := map[string]interface{}{
		"log":      map[string]interface{}{"loglevel": "warning"},
		"inbounds": inbounds,
		"outbounds": []interface{}{
			map[string]interface{}{
				"tag": "proxy", "protocol": "vless",
				"settings": outSettings, "streamSettings": stream,
			},
			map[string]interface{}{"tag": "direct", "protocol": "freedom",
				"settings": map[string]interface{}{"domainStrategy": "UseIPv4"}},
			map[string]interface{}{"tag": "block", "protocol": "blackhole", "settings": map[string]interface{}{}},
		},
	}
	if persistent {
		cfg["routing"] = map[string]interface{}{
			"domainStrategy": "IPIfNonMatch",
			"rules": []interface{}{
				map[string]interface{}{"type": "field", "ip": []string{"geoip:private"}, "outboundTag": "direct"},
				map[string]interface{}{"type": "field", "network": "tcp,udp", "outboundTag": "proxy"},
			},
		}
	}
	return cfg
}

func buildHybridTUNConfig(ifaceName string, xrayHTTPPort, xraySocksPort int) map[string]interface{} {
	// Hybrid TUN: sing-box routes traffic, xray handles VLESS protocol.
	//
	// DNS: "local" = OS system resolver. With strict_route=false, the OS resolver's
	// UDP packets reach the router naturally through the physical NIC (not TUN).
	// This works on both Ethernet and WiFi without knowing the router IP.
	//
	// strict_route=false: auto_route still captures most traffic through TUN,
	// but system-level DNS and some edge cases can escape through the physical NIC.
	// This avoids the chicken-and-egg DNS problem on WiFi.
	dns := map[string]interface{}{
		"servers": []interface{}{
			map[string]interface{}{"tag": "dns-local", "type": "local"},
		},
		"final":             "dns-local",
		"independent_cache": true,
	}

	tun := map[string]interface{}{
		"type":           "tun",
		"tag":            "tun-in",
		"interface_name": ifaceName,
		"address":        []string{"172.19.0.1/30"},
		"mtu":            9000,
		"auto_route":     true,
		"strict_route":   false,
		"stack":          "gvisor",
	}

	proxyOut := map[string]interface{}{
		"type":        "socks",
		"tag":         "proxy",
		"server":      "127.0.0.1",
		"server_port": xraySocksPort,
	}

	route := map[string]interface{}{
		"auto_detect_interface":   true,
		"default_domain_resolver": "dns-local",
		"find_process":            true,
		"rules": []interface{}{
			map[string]interface{}{"action": "sniff"},
			map[string]interface{}{"protocol": "dns", "action": "hijack-dns"},
			// Exclude xray process from TUN to prevent routing loop.
			// xray connects to the VPN server directly — those connections
			// must go through the physical NIC, not back through TUN.
			map[string]interface{}{
				"process_name": []string{"xray.exe", "xray"},
				"outbound":     "direct",
			},
			map[string]interface{}{"ip_is_private": true, "outbound": "direct"},
		},
		"final": "proxy",
	}

	return map[string]interface{}{
		"log":      map[string]interface{}{"level": "warn", "timestamp": true},
		"dns":      dns,
		"inbounds": []interface{}{tun},
		"outbounds": []interface{}{
			proxyOut,
			map[string]interface{}{"type": "direct", "tag": "direct"},
		},
		"route": route,
	}
}

// ─────────────────────────── helpers ─────────────────────────────

func findFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func waitForPort(port int, deadline time.Time) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return true
		}
		time.Sleep(80 * time.Millisecond)
	}
	return false
}

func makeSharedTransport(httpPort int) *http.Transport {
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", httpPort))
	return &http.Transport{
		Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: false, ForceAttemptHTTP2: false,
		MaxIdleConnsPerHost: 4, TLSHandshakeTimeout: warmupTimeout,
		DialContext: (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
	}
}

func shortErr(s string) string {
	if idx := strings.LastIndex(s, ": "); idx >= 0 && idx < len(s)-2 {
		if tail := s[idx+2:]; len(tail) < 90 {
			return tail
		}
	}
	if len(s) > 90 {
		return s[:90]
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func writeTempJSON(data interface{}, prefix string) (string, error) {
	tmp, err := os.CreateTemp("", prefix+"-*.json")
	if err != nil {
		return "", err
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err = enc.Encode(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	tmp.Close()
	return tmp.Name(), nil
}

// ─────────────────────────── xray lifecycle (testing) ────────────

func withXray(p *VlessParams, ttl time.Duration, fn func(httpPort int, tr *http.Transport) error) error {
	httpPort, err := findFreePort()
	if err != nil {
		return fmt.Errorf("no free port")
	}
	tmpPath, err := writeTempJSON(buildXrayConfig(p, httpPort, -1), "xray-test")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	defer os.Remove(tmpPath)
	ctx, cancel := context.WithTimeout(context.Background(), ttl)
	defer cancel()
	cmd := exec.CommandContext(ctx, state.xrayBin, "run", "-c", tmpPath)
	cmd.Stdout = io.Discard
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	hideProcess(cmd)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("xray start: %w", err)
	}
	trackPID(cmd.Process.Pid)
	defer func() {
		cmd.Process.Kill() //nolint:errcheck
		untrackPID(cmd.Process.Pid)
	}()

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	portResult := make(chan bool, 1)
	go func() {
		portResult <- waitForPort(httpPort, time.Now().Add(xrayStartupTimeout))
	}()
	select {
	case exitErr := <-exitCh:
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg == "" {
			if exitErr != nil {
				errMsg = exitErr.Error()
			} else {
				errMsg = "exited unexpectedly"
			}
		}
		if len(errMsg) > 160 {
			errMsg = "..." + errMsg[len(errMsg)-160:]
		}
		return fmt.Errorf("xray: %s", errMsg)
	case ready := <-portResult:
		if !ready {
			return fmt.Errorf("xray: port not ready after %s", xrayStartupTimeout)
		}
	}
	return fn(httpPort, makeSharedTransport(httpPort))
}

// ─────────────────────────── system proxy ────────────────────────

func setSystemProxy(port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	bypass := "localhost;127.*;10.*;172.16.*;192.168.*;*.local;<local>"
	rp := `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	for _, c := range [][]string{
		{"reg", "add", rp, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "1", "/f"},
		{"reg", "add", rp, "/v", "ProxyServer", "/t", "REG_SZ", "/d", addr, "/f"},
		{"reg", "add", rp, "/v", "ProxyOverride", "/t", "REG_SZ", "/d", bypass, "/f"},
	} {
		if err := runHidden(c[0], c[1:]...).Run(); err != nil {
			return fmt.Errorf("reg: %w", err)
		}
	}
	runHidden("rundll32.exe", "inetcpl.cpl,ClearMyTracksByProcess", "8").Run() //nolint:errcheck
	return nil
}

func unsetSystemProxy() {
	rp := `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	runHidden("reg", "add", rp, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "0", "/f").Run() //nolint:errcheck
}

// ─────────────────────────── connection manager ──────────────────

func startProxyConnection(entry *ConfigEntry) {
	cm := state.conn
	stopConnectionLocked(cm)

	state.mu.RLock()
	connTab := state.activeTab
	state.mu.RUnlock()

	p, err := parseVless(entry.Raw)
	if err != nil {
		setConnError(cm, entry, err.Error(), connTab)
		return
	}

	cm.mu.Lock()
	cm.state = ConnState{Status: ConnConnecting, Mode: ModeProxy, EntryIndex: entry.Index, EntryName: p.Name, ConnTab: connTab}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})

	httpPort, socksPort := connHTTPPort, connSOCKSPort
	if !portFree(httpPort) {
		if pf, e := findFreePort(); e == nil {
			httpPort = pf
		}
	}
	if !portFree(socksPort) {
		if pf, e := findFreePort(); e == nil {
			socksPort = pf
		}
	}

	tmpPath, err := writeTempJSON(buildXrayConfig(p, httpPort, socksPort), "xray-conn")
	if err != nil {
		setConnError(cm, entry, err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, state.xrayBin, "run", "-c", tmpPath)
	cmd.Stdout = io.Discard
	hideProcess(cmd)

	// Capture stderr so we can report the real error if xray crashes.
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf

	if err = cmd.Start(); err != nil {
		cancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "xray start: "+err.Error())
		return
	}

	// Watch for immediate crash in background.
	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()

	// Wait for xray HTTP port OR an immediate crash, whichever comes first.
	portResult := make(chan bool, 1)
	go func() {
		portResult <- waitForPort(httpPort, time.Now().Add(xrayConnTimeout))
	}()

	select {
	case exitErr := <-exitCh:
		// xray exited before the port opened — report stderr
		cancel()
		os.Remove(tmpPath)
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg == "" {
			if exitErr != nil {
				errMsg = exitErr.Error()
			} else {
				errMsg = "xray exited unexpectedly"
			}
		}
		if len(errMsg) > 200 {
			errMsg = "..." + errMsg[len(errMsg)-200:]
		}
		setConnError(cm, entry, "xray: "+errMsg)
		return
	case ready := <-portResult:
		if !ready {
			cancel()
			os.Remove(tmpPath)
			setConnError(cm, entry, "xray: port not ready after timeout")
			return
		}
	}

	if err = setSystemProxy(httpPort); err != nil {
		fmt.Fprintf(os.Stderr, "⚠  setSystemProxy: %v\n", err)
	}

	cm.mu.Lock()
	cm.cmd = cmd
	cm.cancel = cancel
	cm.tmpCfg = tmpPath
	cm.state = ConnState{
		Status: ConnConnected, Mode: ModeProxy, ConnTab: connTab,
		EntryIndex: entry.Index, EntryName: p.Name,
		HTTPPort: httpPort, SOCKSPort: socksPort,
		StartedAt: time.Now(),
	}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	startUptimeTicker(cm)
}

func startTUNConnection(entry *ConfigEntry) {
	state.mu.RLock()
	connTab := state.activeTab
	state.mu.RUnlock()

	if !checkAdmin() {
		setConnError(state.conn, entry, "TUN mode requires administrator/root. Run the program as admin.")
		return
	}
	if state.singboxBin == "" {
		setConnError(state.conn, entry, "sing-box not found. Pass path as 2nd arg: vair xray.exe sing-box.exe")
		return
	}

	cm := state.conn
	stopConnectionLocked(cm)

	// Best-effort cleanup of any stale adapters from previous sessions.
	// With unique names below this is no longer strictly required,
	// but prevents accumulation of ghost adapters in Device Manager.
	removeTUNAdapter()

	p, err := parseVless(entry.Raw)
	if err != nil {
		setConnError(cm, entry, err.Error())
		return
	}

	cm.mu.Lock()
	cm.state = ConnState{Status: ConnConnecting, Mode: ModeTUN, EntryIndex: entry.Index, EntryName: p.Name, ConnTab: connTab}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})

	// Unique interface name per session avoids "file already exists".
	// Even if Windows hasn't fully cleaned the previous adapter kernel-side,
	// a new name means sing-box never conflicts with the old one.
	tunIfaceName := fmt.Sprintf("xc-tun-%d", time.Now().Unix()%10000)

	// 1. Start xray as local HTTP+SOCKS proxy
	xrayHTTPPort, e1 := findFreePort()
	if e1 != nil {
		setConnError(cm, entry, "no free port for xray")
		return
	}
	xraySocksPort, e2 := findFreePort()
	if e2 != nil {
		setConnError(cm, entry, "no free port for xray socks")
		return
	}
	xrayCfg := buildXrayConfig(p, xrayHTTPPort, xraySocksPort)
	// For hybrid TUN: xray resolves the VPN server hostname via the OS resolver.
	// sing-box's route rule `ip_is_private → direct` catches the resulting DNS
	// packets (OS resolver hits the router at 192.168.x.1 which is private IP)
	// and routes them out through the physical interface (direct outbound),
	// not TUN. So we don't need to override xray's DNS — the default works.
	// Simplify routing: everything goes to proxy.
	// sing-box handles private IP routing, xray doesn't need geoip.
	xrayCfg["routing"] = map[string]interface{}{
		"domainStrategy": "AsIs",
		"rules": []interface{}{
			map[string]interface{}{"type": "field", "network": "tcp,udp", "outboundTag": "proxy"},
		},
	}
	xrayTmpPath, err := writeTempJSON(xrayCfg, "xray-hybrid")
	if err != nil {
		setConnError(cm, entry, "xray config write: "+err.Error())
		return
	}
	// Save debug copy
	if debugPath := filepath.Join(tabsDir(), "last-xray-hybrid.json"); true {
		os.MkdirAll(tabsDir(), 0755)
		data, _ := json.MarshalIndent(xrayCfg, "", "  ")
		os.WriteFile(debugPath, data, 0644)
	}
	xrayCtx, xrayCancel := context.WithCancel(context.Background())
	xrayCmd := exec.CommandContext(xrayCtx, state.xrayBin, "run", "-c", xrayTmpPath)
	xrayCmd.Stdout = io.Discard
	// Capture xray stderr for debugging
	var xrayStderrBuf strings.Builder
	xrayCmd.Stderr = &xrayStderrBuf
	hideProcess(xrayCmd)
	if err = xrayCmd.Start(); err != nil {
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "xray hybrid start: "+err.Error())
		return
	}
	// Wait for xray port
	if !waitForPort(xrayHTTPPort, time.Now().Add(xrayStartupTimeout)) {
		xrayCmd.Process.Kill() //nolint:errcheck
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "xray hybrid: port not ready")
		return
	}
	fmt.Printf("ℹ  hybrid TUN: xray proxy on :%d/%d for %s\n", xrayHTTPPort, xraySocksPort, p.Network)
	// 2. Build sing-box TUN config routing through xray proxy
	cfg := buildHybridTUNConfig(tunIfaceName, xrayHTTPPort, xraySocksPort)
	tmpPath, err := writeTempJSON(cfg, "singbox-tun")
	if err != nil {
		xrayCmd.Process.Kill() //nolint:errcheck
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "singbox config write: "+err.Error())
		return
	}
	fmt.Printf("ℹ  sing-box hybrid config: %s\n", tmpPath)
	// Save debug copy
	if debugPath := filepath.Join(tabsDir(), "last-singbox-hybrid.json"); true {
		os.MkdirAll(tabsDir(), 0755)
		data, _ := json.MarshalIndent(cfg, "", "  ")
		os.WriteFile(debugPath, data, 0644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, state.singboxBin, "run", "-c", tmpPath)
	stderrPipe, _ := cmd.StderrPipe()
	cmd.Stdout = io.Discard
	hideProcess(cmd)

	if err = cmd.Start(); err != nil {
		cancel()
		os.Remove(tmpPath)
		xrayCmd.Process.Kill() //nolint:errcheck
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "sing-box start: "+err.Error())
		return
	}

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	var stderrLines []string
	var stderrMu sync.Mutex
	// Write all sing-box stderr to a log file for debugging
	logPath := filepath.Join(tabsDir(), "last-singbox.log")
	os.MkdirAll(tabsDir(), 0755)
	logFile, _ := os.Create(logPath)
	go func() {
		if stderrPipe == nil {
			return
		}
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			stderrMu.Lock()
			stderrLines = append(stderrLines, line)
			stderrMu.Unlock()
			if logFile != nil {
				fmt.Fprintln(logFile, line)
			}
		}
		if logFile != nil {
			logFile.Close()
		}
	}()

	select {
	case <-exitCh:
		cancel()
		os.Remove(tmpPath)
		xrayCmd.Process.Kill() //nolint:errcheck
		xrayCancel()
		os.Remove(xrayTmpPath)
		stderrMu.Lock()
		lines := stderrLines
		stderrMu.Unlock()
		errMsg := "sing-box crashed at startup"
		for i := len(lines) - 1; i >= 0 && i >= len(lines)-3; i-- {
			if lines[i] != "" {
				errMsg = lines[i]
				break
			}
		}
		if len(errMsg) > 180 {
			errMsg = "..." + errMsg[len(errMsg)-180:]
		}
		setConnError(cm, entry, errMsg)
		return
	case <-time.After(tunStartupTimeout):
	}

	cm.mu.Lock()
	cm.cmd = cmd
	cm.cancel = cancel
	cm.tmpCfg = tmpPath
	cm.xrayCmd = xrayCmd
	cm.xrayCancel = xrayCancel
	cm.xrayTmpCfg = xrayTmpPath
	cm.state = ConnState{
		Status: ConnConnected, Mode: ModeTUN, ConnTab: connTab,
		EntryIndex: entry.Index, EntryName: p.Name,
		TUNIface: tunIfaceName, StartedAt: time.Now(),
	}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	startUptimeTicker(cm)
}

func stopConnection() {
	stopConnectionLocked(state.conn)
	state.broadcast(SSEEvent{Type: "conn_update", Payload: state.conn.snap()})
}

// removeTUNAdapter removes the WinTUN/TUN adapter by name so it doesn't linger
// in the system and doesn't cause "file already exists" on reconnect.
func removeTUNAdapter() {
	runHidden("powershell", "-NonInteractive", "-Command",
		`Get-NetAdapter -Name 'xc-tun-*' -ErrorAction SilentlyContinue | Remove-NetAdapter -Confirm:$false`,
	).Run() //nolint:errcheck
}

func stopConnectionLocked(cm *connManager) {
	cm.mu.Lock()
	if cm.state.Status == ConnIdle {
		cm.mu.Unlock()
		return
	}
	wasTUN := cm.state.Mode == ModeTUN
	wasProxy := cm.state.Mode == ModeProxy
	prevHTTPPort := cm.state.HTTPPort
	cm.state.Status = ConnDisconnecting

	// Extract process references under lock, then release lock for killing
	mainCmd := cm.cmd
	mainCancel := cm.cancel
	mainTmpCfg := cm.tmpCfg
	xrayCmd := cm.xrayCmd
	xrayCancel := cm.xrayCancel
	xrayTmpCfg := cm.xrayTmpCfg

	// Clear references immediately so a concurrent call sees them as nil
	cm.cmd = nil
	cm.cancel = nil
	cm.tmpCfg = ""
	cm.xrayCmd = nil
	cm.xrayCancel = nil
	cm.xrayTmpCfg = ""
	cm.mu.Unlock()

	// === Kill processes WITHOUT holding the lock ===

	// Cancel contexts first (sends signals)
	if mainCancel != nil {
		mainCancel()
	}
	if xrayCancel != nil {
		xrayCancel()
	}

	// Kill sing-box and xray in parallel. Go's Process.Kill() on Windows is
	// soft — it sends WM_CLOSE-like signal and process may try to drain pending
	// operations before exit (DNS queries with long timeouts, etc).
	// taskkill /F forces immediate termination.
	killProc := func(cmd *exec.Cmd, done chan<- struct{}) {
		defer close(done)
		if cmd == nil || cmd.Process == nil {
			return
		}
		pid := cmd.Process.Pid
		// Force-kill immediately in parallel with Go's kill (whoever wins).
		go runHidden("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run() //nolint:errcheck
		cmd.Process.Kill()                                                    //nolint:errcheck
		waitDone := make(chan struct{})
		go func() { cmd.Wait(); close(waitDone) }() //nolint:errcheck
		select {
		case <-waitDone:
		case <-time.After(1500 * time.Millisecond):
			// Still running — one more taskkill attempt
			runHidden("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run() //nolint:errcheck
		}
	}

	mainDone := make(chan struct{})
	xrayDone := make(chan struct{})
	go killProc(mainCmd, mainDone)
	go killProc(xrayCmd, xrayDone)
	<-mainDone
	<-xrayDone

	// Cleanup temp files
	if mainTmpCfg != "" {
		os.Remove(mainTmpCfg)
	}
	if xrayTmpCfg != "" {
		os.Remove(xrayTmpCfg)
	}

	if wasProxy {
		unsetSystemProxy()
		if prevHTTPPort > 0 {
			deadline := time.Now().Add(400 * time.Millisecond)
			for time.Now().Before(deadline) {
				if portFree(prevHTTPPort) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
	}
	if wasTUN {
		removeTUNAdapter()
	}

	// Re-acquire lock to set final state
	cm.mu.Lock()
	cm.state = ConnState{Status: ConnIdle, EntryIndex: -1}
	cm.mu.Unlock()
}

func setConnError(cm *connManager, entry *ConfigEntry, msg string, tab ...string) {
	name := entry.Name
	if name == "" {
		name = fmt.Sprintf("#%d", entry.Index)
	}
	connTab := ""
	if len(tab) > 0 {
		connTab = tab[0]
	}
	cm.mu.Lock()
	cm.state = ConnState{Status: ConnError, EntryIndex: entry.Index, EntryName: name, ErrMsg: msg, ConnTab: connTab}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
}

func startUptimeTicker(cm *connManager) {
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for range t.C {
			cm.mu.Lock()
			ok := cm.state.Status == ConnConnected
			cm.mu.Unlock()
			if !ok {
				return
			}
			state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
		}
	}()
}

// ─────────────────────────── ping / speed ────────────────────────

func measurePing(tr *http.Transport) (int64, error) {
	wc := &http.Client{Transport: tr, Timeout: warmupTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	w, err := wc.Get(pingTestURL)
	if err != nil {
		return -1, fmt.Errorf("warmup: %w", err)
	}
	io.Copy(io.Discard, w.Body)
	w.Body.Close()
	mc := &http.Client{Transport: tr, Timeout: pingTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var best int64 = -1
	var lastErr error
	for i := 0; i < pingRounds; i++ {
		start := time.Now()
		resp, e := mc.Get(pingTestURL)
		if e != nil {
			lastErr = e
			if best > 0 {
				break
			}
			continue
		}
		ms := time.Since(start).Milliseconds()
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if best < 0 || ms < best {
			best = ms
		}
	}
	if best < 0 {
		if lastErr != nil {
			return -1, lastErr
		}
		return -1, fmt.Errorf("all measurements failed")
	}
	return best, nil
}

func measureSpeed(tr *http.Transport, onProgress func(float64)) (float64, error) {
	sc := &http.Client{Transport: tr, Timeout: speedDuration + 5*time.Second}
	req, err := http.NewRequest("GET", speedTestURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", speedUserAgent)
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := sc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 64*1024)
	var total int64
	start := time.Now()
	deadline := start.Add(speedDuration)
	lastReport := start
	for time.Now().Before(deadline) {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			total += int64(n)
		}
		now := time.Now()
		if onProgress != nil && now.Sub(lastReport) >= 400*time.Millisecond && total > 0 {
			onProgress(float64(total) / now.Sub(start).Seconds() / 1024 / 1024)
			lastReport = now
		}
		if rerr != nil {
			break
		}
	}
	if total == 0 {
		return 0, fmt.Errorf("no data received")
	}
	return float64(total) / time.Since(start).Seconds() / 1024 / 1024, nil
}

// ─────────────────────────── entry runners ───────────────────────

func runPingForEntry(entry *ConfigEntry) {
	p, err := parseVless(entry.Raw)
	if err != nil {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = err.Error()
		entry.mu.Unlock()
		return
	}
	ttl := startupTimeout + warmupTimeout + pingTimeout*time.Duration(pingRounds) + 3*time.Second
	if err = withXray(p, ttl, func(_ int, tr *http.Transport) error {
		delay, e := measurePing(tr)
		entry.mu.Lock()
		defer entry.mu.Unlock()
		if e != nil || delay < 0 {
			entry.PingStatus = StatusFailed
			entry.Delay = -1
			entry.PingErr = cleanPingErr(e)
		} else {
			entry.PingStatus = StatusOK
			entry.Delay = delay
			entry.PingErr = ""
		}
		return nil
	}); err != nil {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = shortErr(err.Error())
		entry.mu.Unlock()
	}
}

// runSpeedForEntry always does ping→speed in a single xray session.
// Per-row ⬇ speed button should re-measure ping even if already tested,
// because conditions may have changed and speed result is meaningless without fresh ping.
func runSpeedForEntry(entry *ConfigEntry, tabID string) {
	p, err := parseVless(entry.Raw)
	if err != nil {
		entry.mu.Lock()
		entry.SpeedStatus = StatusFailed
		entry.SpeedErr = err.Error()
		entry.mu.Unlock()
		return
	}
	ttl := startupTimeout + warmupTimeout + pingTimeout*time.Duration(pingRounds) + speedDuration + 10*time.Second
	if err = withXray(p, ttl, func(_ int, tr *http.Transport) error {
		// Always re-ping — gives fresh delay AND warms the tunnel for speed test
		delay, pingErr := measurePing(tr)
		entry.mu.Lock()
		if pingErr != nil || delay < 0 {
			entry.PingStatus = StatusFailed
			entry.Delay = -1
			entry.PingErr = cleanPingErr(pingErr)
			entry.SpeedStatus = StatusSkipped
			entry.SpeedErr = "ping failed"
			entry.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
			return nil
		}
		entry.PingStatus = StatusOK
		entry.Delay = delay
		entry.PingErr = ""
		entry.SpeedStatus = StatusTestingSpeed
		entry.SpeedLive = 0
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
		mbps, e := measureSpeed(tr, func(live float64) {
			entry.mu.Lock()
			entry.SpeedLive = live
			entry.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
		})
		entry.mu.Lock()
		defer entry.mu.Unlock()
		entry.SpeedLive = 0
		if e != nil {
			entry.SpeedStatus = StatusFailed
			entry.SpeedMBps = 0
			entry.SpeedErr = shortErr(e.Error())
		} else {
			entry.SpeedStatus = StatusOK
			entry.SpeedMBps = mbps
			entry.SpeedErr = ""
		}
		return nil
	}); err != nil {
		entry.mu.Lock()
		entry.SpeedStatus = StatusFailed
		entry.SpeedMBps = 0
		entry.SpeedLive = 0
		entry.SpeedErr = shortErr(err.Error())
		entry.mu.Unlock()
	}
}

func runPingAndSpeedForEntry(entry *ConfigEntry, tabID string) {
	p, err := parseVless(entry.Raw)
	if err != nil {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = err.Error()
		entry.SpeedStatus = StatusSkipped
		entry.SpeedErr = "parse error"
		entry.mu.Unlock()
		return
	}
	ttl := startupTimeout + warmupTimeout + pingTimeout*time.Duration(pingRounds) + speedDuration + 10*time.Second
	if err = withXray(p, ttl, func(_ int, tr *http.Transport) error {
		delay, pingErr := measurePing(tr)
		entry.mu.Lock()
		if pingErr != nil || delay < 0 {
			entry.PingStatus = StatusFailed
			entry.Delay = -1
			entry.PingErr = cleanPingErr(pingErr)
			entry.SpeedStatus = StatusSkipped
			entry.SpeedErr = "ping failed"
			entry.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
			return nil
		}
		entry.PingStatus = StatusOK
		entry.Delay = delay
		entry.PingErr = ""
		entry.SpeedStatus = StatusTestingSpeed
		entry.SpeedLive = 0
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
		mbps, sErr := measureSpeed(tr, func(live float64) {
			entry.mu.Lock()
			entry.SpeedLive = live
			entry.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
		})
		entry.mu.Lock()
		defer entry.mu.Unlock()
		entry.SpeedLive = 0
		if sErr != nil {
			entry.SpeedStatus = StatusFailed
			entry.SpeedMBps = 0
			entry.SpeedErr = shortErr(sErr.Error())
		} else {
			entry.SpeedStatus = StatusOK
			entry.SpeedMBps = mbps
			entry.SpeedErr = ""
		}
		return nil
	}); err != nil {
		entry.mu.Lock()
		entry.PingStatus = StatusFailed
		entry.Delay = -1
		entry.PingErr = shortErr(err.Error())
		entry.SpeedStatus = StatusSkipped
		entry.SpeedErr = "xray failed"
		entry.mu.Unlock()
	}
}

func cleanPingErr(e error) string {
	if e == nil {
		return "timeout"
	}
	s := e.Error()
	if strings.Contains(s, "context deadline") || strings.Contains(s, "timeout") {
		return "timeout"
	}
	return shortErr(s)
}

// ─────────────────────────── bulk runners ────────────────────────

func runPingAll() {
	if !atomic.CompareAndSwapInt32(&state.pingRunning, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&state.pingRunning, 0)
	state.mu.RLock()
	entries := make([]*ConfigEntry, len(state.entries))
	copy(entries, state.entries)
	tabID := state.activeTab
	state.mu.RUnlock()
	state.broadcast(SSEEvent{Type: "bulk_ping_start", Payload: len(entries), Tab: tabID})
	sem := make(chan struct{}, pingConcurrency)
	var wg sync.WaitGroup
	var done int64
	for _, e := range entries {
		wg.Add(1)
		go func(ent *ConfigEntry) {
			defer wg.Done()
			// Skip if tab was deleted during test
			state.mu.RLock()
			cancelled := state.cancelledTabs[tabID]
			state.mu.RUnlock()
			if cancelled {
				atomic.AddInt64(&done, 1)
				return
			}
			sem <- struct{}{}
			defer func() { <-sem }()
			ent.mu.Lock()
			ent.PingStatus = StatusTestingPing
			ent.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			runPingForEntry(ent)
			n := atomic.AddInt64(&done, 1)
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			state.broadcast(SSEEvent{Type: "bulk_ping_progress", Payload: map[string]interface{}{"done": n, "total": int64(len(entries))}, Tab: tabID})
		}(e)
	}
	wg.Wait()
	state.broadcast(SSEEvent{Type: "bulk_ping_done", Payload: nil, Tab: tabID})
}

func runSpeedAll() {
	if !atomic.CompareAndSwapInt32(&state.speedRunning, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&state.speedRunning, 0)
	state.mu.RLock()
	entries := make([]*ConfigEntry, len(state.entries))
	copy(entries, state.entries)
	tabID := state.activeTab
	state.mu.RUnlock()
	state.broadcast(SSEEvent{Type: "bulk_speed_start", Payload: len(entries), Tab: tabID})
	sem := make(chan struct{}, pingSpeedConcurrency)
	var wg sync.WaitGroup
	var done int64
	for _, e := range entries {
		wg.Add(1)
		go func(ent *ConfigEntry) {
			defer wg.Done()
			state.mu.RLock()
			cancelled := state.cancelledTabs[tabID]
			state.mu.RUnlock()
			if cancelled {
				atomic.AddInt64(&done, 1)
				return
			}
			sem <- struct{}{}
			defer func() { <-sem }()
			ent.mu.Lock()
			ent.PingStatus = StatusTestingPing
			ent.SpeedStatus = StatusPending
			ent.SpeedMBps = 0 // reset so sort doesn't use stale value
			ent.SpeedLive = 0
			ent.Delay = -1 // reset ping too
			ent.mu.Unlock()
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			runPingAndSpeedForEntry(ent, tabID)
			n := atomic.AddInt64(&done, 1)
			state.broadcast(SSEEvent{Type: "entry_update", Payload: ent.snap(), Tab: tabID})
			state.broadcast(SSEEvent{Type: "bulk_speed_progress", Payload: map[string]interface{}{"done": n, "total": int64(len(entries))}, Tab: tabID})
		}(e)
	}
	wg.Wait()
	state.broadcast(SSEEvent{Type: "bulk_speed_done", Payload: nil, Tab: tabID})
}

// ─────────────────────────── fetch ───────────────────────────────

func fetchAndInit() {
	if state.activeTab == "main" {
		state.broadcast(SSEEvent{Type: "loading", Payload: nil})
	}
	var raws []string

	// 1. Fetch from public sources
	for _, src := range sourceDefs {
		lines, err := fetchURL(src.URL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠  fetch %s: %v\n", src.URL, err)
			continue
		}
		raws = append(raws, lines...)
		fmt.Printf("ℹ  fetched %d configs from %s\n", len(lines), src.URL)
	}

	// 2. Fetch from private GitHub repo via PAT
	ghLines, err := fetchGitHubPAT()
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠  GitHub PAT fetch: %v\n", err)
	} else {
		raws = append(raws, ghLines...)
		fmt.Printf("ℹ  fetched %d configs from GitHub PAT\n", len(ghLines))
	}

	// Deduplicate by raw URL
	seen := make(map[string]bool, len(raws))
	var deduped []string
	for _, r := range raws {
		if !seen[r] {
			seen[r] = true
			deduped = append(deduped, r)
		}
	}

	entries := make([]*ConfigEntry, 0, len(deduped))
	for _, raw := range deduped {
		e := &ConfigEntry{Raw: raw, PingStatus: StatusPending, Delay: -1, SpeedStatus: StatusPending}
		p, parseErr := parseVless(raw)
		if parseErr != nil {
			e.Name = raw[:minInt(40, len(raw))]
			e.PingStatus = StatusFailed
			e.PingErr = parseErr.Error()
			e.SpeedStatus = StatusFailed
			e.SpeedErr = parseErr.Error()
		} else if shouldSkip(p.Name) {
			continue
		} else {
			e.Name = p.Name
			e.Host = p.Host
			e.Port = p.Port
			e.Network = p.Network
			e.Security = p.Security
		}
		entries = append(entries, e)
	}
	for i, e := range entries {
		e.Index = i
	}
	state.mu.Lock()
	state.tabEntries["main"] = entries
	// Only update active entries if main tab is active
	if state.activeTab == "main" {
		state.entries = entries
	}
	state.mu.Unlock()
	// Only broadcast loaded data if main tab is active
	if state.activeTab == "main" {
		snaps := make([]ConfigEntry, len(entries))
		for i, e := range entries {
			snaps[i] = e.snap()
		}
		state.broadcast(SSEEvent{Type: "loaded", Payload: snaps})
	}
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
}

func fetchURL(u string) ([]string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, err
	}
	text := string(body)

	// Try to detect base64 subscription:
	// If the text doesn't contain "://" in the first few hundred chars, try base64 decode
	sample := text
	if len(sample) > 500 {
		sample = sample[:500]
	}
	if !strings.Contains(sample, "://") {
		// Looks like base64 — try to decode
		cleaned := strings.ReplaceAll(strings.TrimSpace(text), "\n", "")
		cleaned = strings.ReplaceAll(cleaned, "\r", "")
		if decoded, err := base64.StdEncoding.DecodeString(cleaned); err == nil {
			text = string(decoded)
		} else if decoded, err := base64.RawStdEncoding.DecodeString(cleaned); err == nil {
			text = string(decoded)
		} else if decoded, err := base64.URLEncoding.DecodeString(cleaned); err == nil {
			text = string(decoded)
		} else if decoded, err := base64.RawURLEncoding.DecodeString(cleaned); err == nil {
			text = string(decoded)
		}
		// If all decoding attempts fail, fall through and try to parse as-is
	}

	var lines []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "vless://") {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

func fetchGitHubPAT() ([]string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", githubAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+githubPAT)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API: %s", resp.Status)
	}
	var ghResp struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ghResp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(ghResp.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	var lines []string
	for _, l := range strings.Split(string(decoded), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "vless://") {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

func parseConfigLines(text string) []*ConfigEntry {
	var entries []*ConfigEntry
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "vless://") {
			continue
		}
		e := &ConfigEntry{Raw: line, PingStatus: StatusPending, Delay: -1, SpeedStatus: StatusPending}
		p, parseErr := parseVless(line)
		if parseErr != nil {
			e.Name = line[:minInt(40, len(line))]
			e.PingStatus = StatusFailed
			e.PingErr = parseErr.Error()
			e.SpeedStatus = StatusFailed
			e.SpeedErr = parseErr.Error()
		} else {
			e.Name = p.Name
			e.Host = p.Host
			e.Port = p.Port
			e.Network = p.Network
			e.Security = p.Security
		}
		entries = append(entries, e)
	}
	for i, e := range entries {
		e.Index = i
	}
	return entries
}

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
	ch := make(chan SSEEvent, 128)
	state.addClient(ch)
	defer state.removeClient(ch)
	send := func(ev SSEEvent) { data, _ := json.Marshal(ev); fmt.Fprintf(w, "data: %s\n\n", data); flusher.Flush() }
	state.mu.RLock()
	if len(state.entries) > 0 {
		snaps := make([]ConfigEntry, len(state.entries))
		for i, e := range state.entries {
			snaps[i] = e.snap()
		}
		state.mu.RUnlock()
		send(SSEEvent{Type: "loaded", Payload: snaps})
	} else {
		state.mu.RUnlock()
	}
	send(SSEEvent{Type: "conn_update", Payload: state.conn.snap()})
	send(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	send(SSEEvent{Type: "app_info", Payload: map[string]interface{}{
		"singbox_available": state.singboxBin != "",
		"is_admin":          checkAdmin(),
		"os":                "windows",
	}})
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

func handleConnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	idx, err := strconv.Atoi(r.URL.Query().Get("idx"))
	if err != nil {
		http.Error(w, "bad idx", 400)
		return
	}
	mode := r.URL.Query().Get("mode")
	state.mu.RLock()
	if idx < 0 || idx >= len(state.entries) {
		state.mu.RUnlock()
		http.Error(w, "not found", 404)
		return
	}
	entry := state.entries[idx]
	state.mu.RUnlock()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
	if mode == "tun" {
		go startTUNConnection(entry)
	} else {
		go startProxyConnection(entry)
	}
}

func handleDisconnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
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

func handlePingOne(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	idx, err := strconv.Atoi(r.URL.Query().Get("idx"))
	if err != nil {
		http.Error(w, "bad idx", 400)
		return
	}
	state.mu.RLock()
	if idx < 0 || idx >= len(state.entries) {
		state.mu.RUnlock()
		http.Error(w, "not found", 404)
		return
	}
	entry := state.entries[idx]
	tabID := state.activeTab
	state.mu.RUnlock()
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
					fmt.Fprintf(os.Stderr, "⚠ ping test panic: %v\n", r)
				}
			}()
			runPingForEntry(entry)
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			fmt.Fprintf(os.Stderr, "⚠ ping test timeout for #%d\n", entry.Index)
		}

		entry.mu.Lock()
		if entry.PingStatus == StatusTestingPing {
			entry.PingStatus = StatusFailed
			entry.PingErr = "timeout"
			entry.Delay = -1
		}
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	}()
}

func handleSpeedOne(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	idx, err := strconv.Atoi(r.URL.Query().Get("idx"))
	if err != nil {
		http.Error(w, "bad idx", 400)
		return
	}
	state.mu.RLock()
	if idx < 0 || idx >= len(state.entries) {
		state.mu.RUnlock()
		http.Error(w, "not found", 404)
		return
	}
	entry := state.entries[idx]
	tabID := state.activeTab
	state.mu.RUnlock()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
	go func() {
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
			runSpeedForEntry(entry, tabID)
		}()
		select {
		case <-done:
			// Normal completion
		case <-time.After(25 * time.Second):
			fmt.Fprintf(os.Stderr, "⚠ speed test timeout for #%d\n", entry.Index)
		}

		// Guarantee status is never stuck on "testing"
		entry.mu.Lock()
		if entry.SpeedStatus == StatusTestingSpeed {
			entry.SpeedStatus = StatusFailed
			entry.SpeedErr = "timeout"
			entry.SpeedLive = 0
		}
		entry.mu.Unlock()
		state.broadcast(SSEEvent{Type: "entry_update", Payload: entry.snap(), Tab: tabID})
	}()
}

func handlePingAll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if atomic.LoadInt32(&state.pingRunning) == 1 {
		http.Error(w, "already running", 409)
		return
	}
	go runPingAll()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleSpeedAll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if atomic.LoadInt32(&state.speedRunning) == 1 {
		http.Error(w, "already running", 409)
		return
	}
	go runSpeedAll()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleReload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	state.mu.RLock()
	tabID := state.activeTab
	var sourceURL string
	for _, t := range state.tabs {
		if t.ID == tabID {
			sourceURL = t.SourceURL
			break
		}
	}
	state.mu.RUnlock()

	if tabID == "main" {
		go fetchAndInit()
	} else if sourceURL != "" {
		// Re-fetch from the tab's source URL
		go func() {
			state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: tabID})
			lines, err := fetchURL(sourceURL)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠ reload tab %s: %v\n", tabID, err)
				// Restore current entries
				state.mu.RLock()
				cur := state.tabEntries[tabID]
				snaps := make([]ConfigEntry, len(cur))
				for i, e := range cur {
					snaps[i] = e.snap()
				}
				state.mu.RUnlock()
				state.broadcast(SSEEvent{Type: "loaded", Payload: snaps, Tab: tabID})
				return
			}
			newEntries := parseConfigLines(strings.Join(lines, "\n"))
			state.mu.Lock()
			state.tabEntries[tabID] = newEntries
			if state.activeTab == tabID {
				state.entries = newEntries
			}
			state.mu.Unlock()
			snaps := make([]ConfigEntry, len(newEntries))
			for i, e := range newEntries {
				snaps[i] = e.snap()
			}
			state.broadcast(SSEEvent{Type: "loaded", Payload: snaps, Tab: tabID})
			saveTabs()
		}()
	} else {
		// No URL: reset all test results for this tab
		go func() {
			state.mu.Lock()
			entries := state.tabEntries[tabID]
			for _, e := range entries {
				e.mu.Lock()
				e.PingStatus = StatusPending
				e.Delay = -1
				e.PingErr = ""
				e.SpeedStatus = StatusPending
				e.SpeedMBps = 0
				e.SpeedLive = 0
				e.SpeedErr = ""
				e.mu.Unlock()
			}
			state.mu.Unlock()
			if state.activeTab == tabID {
				snaps := make([]ConfigEntry, len(entries))
				for i, e := range entries {
					snaps[i] = e.snap()
				}
				state.broadcast(SSEEvent{Type: "loaded", Payload: snaps, Tab: tabID})
			}
		}()
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// ─────────────────────────── tab handlers ───────────────────────

func handleTabCreate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	n := nextTabNumber()
	tab := Tab{
		ID:       fmt.Sprintf("custom-%d", n),
		Name:     fmt.Sprintf("Tab %d", n),
		IsMain:   false,
		Closable: true,
	}
	state.mu.Lock()
	state.tabs = append(state.tabs, tab)
	state.tabEntries[tab.ID] = nil
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
			delete(state.tabEntries, id)
			break
		}
	}
	if state.activeTab == id {
		state.activeTab = "main"
		state.entries = state.tabEntries["main"]
	}
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	state.mu.RLock()
	snaps := make([]ConfigEntry, len(state.entries))
	for i, e := range state.entries {
		snaps[i] = e.snap()
	}
	state.mu.RUnlock()
	state.broadcast(SSEEvent{Type: "active_tab", Payload: state.activeTab})
	state.broadcast(SSEEvent{Type: "loaded", Payload: snaps})
	saveTabs()
	w.WriteHeader(200)
	w.Write([]byte("ok"))
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
	entries := state.tabEntries[id]
	state.entries = entries
	state.mu.Unlock()
	snaps := make([]ConfigEntry, len(entries))
	for i, e := range entries {
		snaps[i] = e.snap()
	}
	state.broadcast(SSEEvent{Type: "active_tab", Payload: id})
	state.broadcast(SSEEvent{Type: "loaded", Payload: snaps})
	w.WriteHeader(200)
	w.Write([]byte("ok"))
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
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	newEntries := parseConfigLines(string(body))
	state.mu.Lock()
	existing := state.tabEntries[id]
	baseIdx := len(existing)
	for i, e := range newEntries {
		e.Index = baseIdx + i
	}
	existing = append(existing, newEntries...)
	state.tabEntries[id] = existing
	if state.activeTab == id {
		state.entries = existing
	}
	state.mu.Unlock()
	if state.activeTab == id {
		snaps := make([]ConfigEntry, len(existing))
		for i, e := range existing {
			snaps[i] = e.snap()
		}
		state.broadcast(SSEEvent{Type: "loaded", Payload: snaps})
	}
	saveTabs()
	w.WriteHeader(200)
	fmt.Fprintf(w, `{"added":%d}`, len(newEntries))
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

func handleTabSetURL(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	sourceURL := r.URL.Query().Get("url")
	if id == "main" {
		http.Error(w, "cannot set URL on main tab", 400)
		return
	}
	// Check if URL actually changed
	var oldURL string
	state.mu.Lock()
	for i, t := range state.tabs {
		if t.ID == id {
			oldURL = t.SourceURL
			state.tabs[i].SourceURL = sourceURL
			break
		}
	}
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "tabs_update", Payload: state.tabs})
	saveTabs()
	// Only fetch if URL changed AND is not empty
	if sourceURL != "" && sourceURL != oldURL {
		go func() {
			lines, err := fetchURL(sourceURL)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠ tab %s fetch URL: %v\n", id, err)
				return
			}
			newEntries := parseConfigLines(strings.Join(lines, "\n"))
			state.mu.Lock()
			// Replace all entries — URL is the source of truth
			state.tabEntries[id] = newEntries
			if state.activeTab == id {
				state.entries = newEntries
			}
			state.mu.Unlock()
			if state.activeTab == id {
				snaps := make([]ConfigEntry, len(newEntries))
				for i, e := range newEntries {
					snaps[i] = e.snap()
				}
				state.broadcast(SSEEvent{Type: "loaded", Payload: snaps})
			}
			saveTabs()
		}()
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

func handleTabDeleteEntries(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	id := r.URL.Query().Get("id")
	if id == "main" {
		http.Error(w, "cannot delete from main tab", 400)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1*1024*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}
	var indices []int
	if err := json.Unmarshal(body, &indices); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), 400)
		return
	}
	toRemove := make(map[int]bool, len(indices))
	for _, idx := range indices {
		toRemove[idx] = true
	}
	state.mu.Lock()
	old := state.tabEntries[id]
	var kept []*ConfigEntry
	for _, e := range old {
		if !toRemove[e.Index] {
			kept = append(kept, e)
		}
	}
	// Re-index
	for i, e := range kept {
		e.Index = i
	}
	state.tabEntries[id] = kept
	if state.activeTab == id {
		state.entries = kept
	}
	state.mu.Unlock()
	if state.activeTab == id {
		snaps := make([]ConfigEntry, len(kept))
		for i, e := range kept {
			snaps[i] = e.snap()
		}
		state.broadcast(SSEEvent{Type: "loaded", Payload: snaps})
	}
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

// ─────────────────────────── embedded UI ─────────────────────────

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Vair</title>
<style>
@import url('https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500;700&display=swap');
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
:root{
  --bg:#0c0c0c;--bg2:#111;--bg3:#181818;--bg4:#1e1e1e;
  --border:#222;--border2:#2a2a2a;
  --text:#c8c8c8;--dim:#4a4a4a;--dim2:#303030;
  --accent:#e8c547;--green:#4ade80;--red:#f87171;
  --blue:#60a5fa;--orange:#fb923c;--purple:#c084fc;--teal:#2dd4bf;
  --font:'JetBrains Mono',Consolas,monospace;
}
html,body{height:100%;background:var(--bg);color:var(--text);font-family:var(--font);font-size:13px;line-height:1.4;user-select:none;-webkit-user-select:none}
body{display:flex;flex-direction:column;height:100vh;overflow:hidden}
input,textarea{user-select:text;-webkit-user-select:text}

/* ── connection bar ── */
#conn-bar{
  flex-shrink:0;border-top:2px solid var(--border);
  padding:0 16px;display:flex;align-items:center;gap:10px;flex-wrap:nowrap;
  background:var(--bg2);transition:background .3s,border-color .3s;
  min-height:38px;overflow:visible;
}
#conn-bar.cp{background:#071a09;border-top-color:#1a3a20}
#conn-bar.conn-tun{background:#060f1f;border-top-color:#1a2a40}
#conn-bar.cc{background:#12120a;border-top-color:#2a2a12}
#conn-bar.ce{background:#1a0707;border-top-color:#3a1212}

.cdot{width:8px;height:8px;min-width:8px;max-width:8px;border-radius:50%;flex-shrink:0;align-self:center;background:var(--dim2);transition:background .3s}
.cdot.cp{background:var(--green);animation:pulse 2s infinite}
.cdot.conn-tun{background:var(--blue);animation:pulse 2s infinite}
.cdot.cc{background:var(--accent);animation:blink .7s infinite}
.cdot.ce{background:var(--red)}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.4}}

#clabel{font-size:11px;font-weight:700;letter-spacing:.07em;color:var(--dim);white-space:nowrap;min-width:88px;flex-shrink:0}
#clabel.cp{color:var(--green)}
#clabel.conn-tun{color:var(--blue)}
#clabel.cc{color:var(--accent)}
#clabel.ce{color:var(--red)}
#cdetail{font-size:11px;color:var(--dim);flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0}
#cdetail span{color:var(--text)}
#cports{display:none;gap:6px;align-items:center;flex-shrink:0}
.pchip{
  background:var(--bg3);border:1px solid var(--border2);border-radius:3px;
  padding:2px 7px;font-size:10px;cursor:pointer;transition:border-color .15s;white-space:nowrap;
}
.pchip:hover{border-color:var(--accent)}.pchip b{color:var(--accent)}


/* mode pills */
.mode-wrap{display:flex;gap:4px;align-items:center;flex-shrink:0;white-space:nowrap}
.mpill{
  all:unset;cursor:pointer;font-family:var(--font);font-size:10px;font-weight:700;
  padding:3px 10px;border-radius:3px;text-transform:uppercase;letter-spacing:.07em;
  border:1px solid var(--border2);color:var(--dim);transition:all .15s;white-space:nowrap;
}
.mpill:hover{color:var(--text);border-color:var(--dim)}
.mpill.sel-proxy{color:var(--green);border-color:var(--green);background:rgba(74,222,128,.08)}
.mpill.sel-tun  {color:var(--blue); border-color:var(--blue); background:rgba(96,165,250,.08)}
.mpill.off{opacity:.28;pointer-events:none;cursor:default}
.mode-wrap .mtip{
  font-size:10px;color:var(--dim);border:1px solid var(--border2);border-radius:3px;
  padding:2px 7px;background:var(--bg3);white-space:nowrap;
}

/* header */
header{
  flex-shrink:0;background:var(--bg2);
  padding:9px 16px;display:flex;align-items:center;gap:14px;flex-wrap:wrap;
}
.stats{display:flex;gap:14px;align-items:center}
.stat{display:flex;flex-direction:column;align-items:flex-end;gap:1px}
.sv{font-size:16px;font-weight:700;line-height:1}
.sl{font-size:9px;color:var(--dim);text-transform:uppercase;letter-spacing:.1em}
.sv.ok{color:var(--green)}.sv.err{color:var(--red)}.sv.ms{color:var(--accent)}.sv.sp{color:var(--teal)}
.spacer{flex:1}
.ctrls{display:flex;gap:5px;align-items:center;flex-wrap:wrap}
.btn{
  all:unset;cursor:pointer;font-family:var(--font);font-size:11px;font-weight:700;
  padding:5px 11px;border-radius:3px;letter-spacing:.07em;text-transform:uppercase;
  display:inline-flex;align-items:center;gap:5px;transition:opacity .15s;white-space:nowrap;
  border:1px solid transparent;
}
.btn:hover{opacity:.8}.btn:active{opacity:.55}
.btn.ghost  {background:transparent;         color:var(--text);  border-color:var(--border2)}
.btn.red    {background:rgba(248,113,113,.12);color:var(--red);  border-color:rgba(248,113,113,.35)}
.btn.sm     {padding:3px 8px;font-size:10px;letter-spacing:.05em}
.btn.sm-disc{padding:3px 9px;font-size:10px;background:rgba(248,113,113,.1);color:var(--red);border-color:rgba(248,113,113,.3)}
.btn:disabled{opacity:.22;pointer-events:none}

.prog-area{flex-shrink:0}
.pbar-row{height:2px;background:var(--dim2);position:relative}
.pbar-fill{height:100%;transition:width .22s ease;width:0;position:absolute;inset:0}
.pbar-ping{background:var(--accent)}

/* tabs */
.tab-bar{display:flex;gap:2px;align-items:center;flex-shrink:0;overflow-x:auto;max-width:55%}
.tab-btn{
  all:unset;cursor:pointer;font-family:var(--font);font-size:10px;font-weight:700;
  padding:3px 4px 3px 7px;border-radius:3px;color:var(--dim);border:1px solid var(--border2);
  transition:all .15s;white-space:nowrap;text-transform:uppercase;letter-spacing:.05em;
  display:inline-flex;align-items:center;gap:3px;
}
.tab-btn.no-close{padding:3px 7px}
.tab-btn:hover{color:var(--text);border-color:var(--dim)}
.tab-btn.active{color:var(--accent);border-color:var(--accent)}
.tab-btn .tab-x{
  font-size:8px;color:var(--dim);cursor:pointer;opacity:.4;
  transition:color .12s;flex-shrink:0;
  display:inline-flex;align-items:center;justify-content:center;
  width:10px;height:10px;
}
.tab-btn .tab-x:hover{opacity:1;color:var(--red)}
.tab-add{
  all:unset;cursor:pointer;font-family:var(--font);font-size:10px;font-weight:700;
  padding:3px 0;min-width:20px;display:flex;align-items:center;justify-content:center;
  border-radius:3px;color:var(--dim);border:1px solid var(--border2);transition:all .15s;flex-shrink:0;
}
.tab-add:hover{color:var(--accent);border-color:var(--accent)}
.tab-btn.dragging{opacity:.4}
.tab-btn.drag-over{border-color:var(--accent);box-shadow:inset 0 0 0 1px var(--accent)}

/* tab context menu */
.ctx-menu{
  position:fixed;z-index:9999;background:var(--bg3);border:1px solid var(--border2);
  border-radius:5px;padding:4px 0;min-width:180px;box-shadow:0 6px 20px rgba(0,0,0,.5);
  font-family:var(--font);font-size:11px;
}
.ctx-menu-item{
  padding:6px 14px;color:var(--text);cursor:pointer;transition:background .08s;
  display:flex;align-items:center;gap:8px;
}
.ctx-menu-item:hover{background:var(--bg4)}
.ctx-menu-item.danger{color:var(--red)}
.ctx-menu-item.danger:hover{background:rgba(248,113,113,.1)}
.ctx-sep{height:1px;background:var(--border2);margin:3px 0}

/* tab settings modal */
.modal-overlay{
  position:fixed;inset:0;z-index:9998;background:rgba(0,0,0,.55);
  display:flex;align-items:center;justify-content:center;
}
.modal-box{
  background:var(--bg2);border:1px solid var(--border2);border-radius:6px;
  padding:18px 22px;min-width:360px;max-width:90vw;
}
.modal-title{font-size:13px;font-weight:700;color:var(--accent);margin-bottom:14px;text-transform:uppercase;letter-spacing:.06em}
.modal-label{font-size:10px;color:var(--dim);text-transform:uppercase;letter-spacing:.08em;margin-bottom:4px}
.modal-input{
  width:100%;background:var(--bg3);border:1px solid var(--border2);border-radius:3px;
  color:var(--text);font-family:var(--font);font-size:12px;padding:6px 10px;outline:none;
  margin-bottom:12px;
}
.modal-input:focus{border-color:var(--accent)}
.modal-btns{display:flex;gap:8px;justify-content:flex-end;margin-top:6px}

/* row selection */
tbody tr.selected{background:rgba(232,197,71,.08)!important;box-shadow:inset 3px 0 0 var(--accent)}

.toolbar{
  flex-shrink:0;background:var(--bg2);border-bottom:1px solid var(--border);
  padding:5px 16px;display:flex;align-items:center;gap:10px;flex-wrap:wrap;
}
.tl{font-size:10px;color:var(--dim);text-transform:uppercase;letter-spacing:.1em;white-space:nowrap}
.finput{
  background:var(--bg3);border:1px solid var(--border2);border-radius:3px;
  color:var(--text);font-family:var(--font);font-size:12px;padding:3px 9px;width:180px;outline:none;
}
.finput:focus{border-color:var(--accent)}
.sort-group{display:flex;gap:4px}
.sort-btn{
  all:unset;cursor:pointer;font-family:var(--font);font-size:10px;
  padding:2px 8px;border-radius:2px;color:var(--dim);border:1px solid var(--border2);transition:all .15s;
}
.sort-btn:hover{color:var(--text);border-color:var(--dim)}
.sort-btn.active{color:var(--accent);border-color:var(--accent)}

.tw{flex:1;overflow-y:auto}
table{width:100%;border-collapse:collapse;table-layout:fixed}
/* virtual scroll spacer rows — no borders, no hover */
tbody tr.vspacer{background:transparent!important;border:0!important;pointer-events:none}
tbody tr.vspacer:hover{background:transparent!important}
tbody tr.vspacer td{border:0!important;padding:0!important}
thead{position:sticky;top:0;z-index:10;background:var(--bg2)}
thead th{
  padding:6px 10px;text-align:left;font-size:9px;text-transform:uppercase;
  letter-spacing:.12em;color:var(--dim);border-bottom:1px solid var(--border);white-space:nowrap;
}
thead th.ct {text-align:center}
thead th.cs {text-align:center}
thead th.cp2{text-align:center}
thead th.csp{text-align:center}
thead th.ca {text-align:center}
tbody tr{border-bottom:1px solid var(--border);transition:background .08s}
tbody tr:hover{background:var(--bg3)}
tbody tr.row-cp{background:#071a09!important;box-shadow:inset 3px 0 0 var(--green)}
tbody tr.row-ct{background:#060f1f!important;box-shadow:inset 3px 0 0 var(--blue)}
td{padding:5px 10px;vertical-align:middle;font-size:12px}
.ci{width:38px;color:var(--dim);font-size:11px;text-align:left}
.cn{min-width:150px;max-width:210px;text-align:left}
.ch{max-width:155px;text-align:left}.ct{width:88px;text-align:center}.cs{width:72px;text-align:center}
.cp2{width:110px;text-align:center}
.csp{width:130px;text-align:center}
.ca{width:220px;text-align:right}

.nc{display:flex;flex-direction:column;gap:2px}
.nm{white-space:nowrap;overflow:hidden;text-overflow:ellipsis;color:var(--text)}
.nh{color:var(--dim);font-size:10px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.et{font-size:10px;color:var(--red);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:205px}

.vc{display:flex;align-items:center;justify-content:center;gap:5px}
.pill{font-size:10px;font-weight:700;padding:2px 7px;border-radius:99px;min-width:66px;max-width:110px;text-align:center;letter-spacing:.02em;border:1px solid;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.pill.pending  {color:var(--dim);border-color:var(--dim2);background:transparent}
.pill.tp       {color:var(--orange);border-color:rgba(251,146,60,.4);background:rgba(251,146,60,.07);animation:blink .9s infinite}
.pill.ts       {color:var(--teal);border-color:rgba(45,212,191,.4);background:rgba(45,212,191,.07);animation:blink .9s infinite}

.pill.ok-fast  {color:var(--green); border-color:rgba(74,222,128,.4);  background:rgba(74,222,128,.08)}
.pill.ok-ping  {color:var(--accent);border-color:rgba(232,197,71,.4);  background:rgba(232,197,71,.07)}
.pill.ok-speed {color:var(--teal);border-color:rgba(45,212,191,.3);background:rgba(45,212,191,.07)}
.pill.failed   {color:var(--red);border-color:rgba(248,113,113,.3);background:rgba(248,113,113,.07)}
.pill.skipped  {color:var(--dim);border-color:var(--dim2);background:transparent;font-style:italic}
@keyframes blink{0%,100%{opacity:1}50%{opacity:.3}}

.nb{font-size:10px;padding:1px 5px;border-radius:2px;border:1px solid var(--border);color:var(--dim)}
.nb.ws{color:#818cf8;border-color:rgba(129,140,248,.4)}
.nb.grpc{color:var(--orange);border-color:rgba(251,146,60,.4)}
.nb.h2{color:var(--blue);border-color:rgba(96,165,250,.4)}
.nb.tcp{color:var(--green);border-color:rgba(74,222,128,.4)}
.nb.httpupgrade,.nb.splithttp,.nb.xhttp{color:var(--purple);border-color:rgba(192,132,252,.4)}
.sb{font-size:10px;padding:1px 5px;border-radius:2px;color:var(--dim)}
.sb.tls{color:var(--blue)}.sb.reality{color:var(--purple);font-weight:700}

.act-cell{display:flex;gap:3px;align-items:center;justify-content:flex-end}
.cpb{all:unset;cursor:pointer;color:var(--dim);font-size:12px;padding:2px 4px;border-radius:2px;transition:color .12s;display:inline-flex}
.cpb:hover{color:var(--accent)}.cpb.done{color:var(--green)}


.center-msg{display:flex;flex-direction:column;align-items:center;justify-content:center;gap:14px;height:100%;color:var(--dim)}
.center-msg .ico{font-size:40px;line-height:1}
.spinner{width:18px;height:18px;border:2px solid var(--dim2);border-top-color:var(--accent);border-radius:50%;animation:spin .7s linear infinite;display:inline-block}
@keyframes spin{to{transform:rotate(360deg)}}

::-webkit-scrollbar{width:5px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:var(--dim2);border-radius:3px}
::-webkit-scrollbar-thumb:hover{background:var(--dim)}



/* ── Custom title bar (standalone frameless mode) ─────────────────────────── */
#titlebar{
  display:none;flex-shrink:0;height:36px;
  background:var(--bg);
  border-bottom:1px solid var(--border);
  align-items:stretch;
  user-select:none;-webkit-user-select:none;
  -webkit-app-region:drag;
  position:relative;
}
#titlebar.active{display:flex}
.tb-drag{
  flex:1;display:flex;align-items:center;padding:0 10px;gap:7px;
  -webkit-app-region:drag;cursor:default;
}
.tb-logo{
  width:34px;height:34px;flex-shrink:0;
  display:flex;align-items:center;justify-content:center;
}
.tb-logo img{width:32px;height:32px;object-fit:contain;display:block;pointer-events:none;image-rendering:crisp-edges;}
.tb-appname{
  font-size:13px;font-weight:700;color:var(--dim);
  letter-spacing:.05em;white-space:nowrap;
}
.tb-btns{
  display:flex;align-items:stretch;flex-shrink:0;
  -webkit-app-region:no-drag;
}
.tb-btn{
  all:unset;
  width:40px;height:36px;
  display:flex;align-items:center;justify-content:center;
  cursor:default;color:var(--dim);
  transition:background .1s,color .1s;
  font-size:14px;line-height:1;
}
.tb-btn:hover{background:var(--bg4);color:var(--text)}
.tb-btn.tb-close:hover{background:#c42b1c;color:#fff}
.tb-btn svg{pointer-events:none;display:block}

</style>
</head>
<body>

<!-- ── Custom title bar (standalone/frameless mode) ── -->
<div id="titlebar">
  <div class="tb-drag" id="tb-drag">
    <div class="tb-logo"><img id="tb-logo-img" src="" alt=""></div>
    <span class="tb-appname">Vair</span>
  </div>
  <div class="tb-btns">
    <button class="tb-btn" id="tb-min" title="Minimize">
      <svg width="10" height="1" viewBox="0 0 10 1"><rect width="10" height="1" fill="currentColor"/></svg>
    </button>
    <button class="tb-btn" id="tb-max" title="Maximize">
      <svg width="9" height="9" viewBox="0 0 9 9"><rect x="0.5" y="0.5" width="8" height="8" fill="none" stroke="currentColor" stroke-width="1"/></svg>
    </button>
    <button class="tb-btn tb-close" id="tb-close" title="Close">
      <svg width="10" height="10" viewBox="0 0 10 10"><line x1="0" y1="0" x2="10" y2="10" stroke="currentColor" stroke-width="1.2"/><line x1="10" y1="0" x2="0" y2="10" stroke="currentColor" stroke-width="1.2"/></svg>
    </button>
  </div>
</div>

<!-- ── Header ── -->
<header>
  <div class="stats">
    <div class="stat"><span class="sv" id="s-tot">0</span><span class="sl">configs</span></div>
    <div class="stat"><span class="sv ok" id="s-ok">0</span><span class="sl">ping ok</span></div>
    <div class="stat"><span class="sv err" id="s-er">0</span><span class="sl">failed</span></div>
    <div class="stat"><span class="sv ms" id="s-ms">—</span><span class="sl">best ping</span></div>
    <div class="stat"><span class="sv sp" id="s-sp">—</span><span class="sl">best speed</span></div>
  </div>
  <div class="spacer"></div>
  <div class="ctrls">
    <button class="btn ghost" id="btn-reload"    onclick="doReload()">reload</button>
    <button class="btn ghost" id="btn-ping-all"  onclick="doPingAll()">ping all</button>
    <button class="btn ghost"  id="btn-speed-all" onclick="doSpeedAll()">speed all</button>
  </div>
</header>

<div class="prog-area">
  <div class="pbar-row"><div class="pbar-fill pbar-ping"  id="pb-main"></div></div>
</div>

<div class="toolbar">
  <div class="tab-bar" id="tab-bar">
    <button class="tab-btn active" data-id="main" onclick="switchTab('main')">Sources</button>
    <button class="tab-add" onclick="addTab()" title="New tab (Ctrl+V to paste configs)">+</button>
  </div>
  <div class="spacer"></div>
  <span class="tl">filter</span>
  <input class="finput" id="fi" placeholder="name / host / type…" oninput="applyFilter()">
  <span id="fc" style="font-size:11px;color:var(--dim)"></span>
  <span class="tl" style="margin-left:6px">sort</span>
  <div class="sort-group">
    <button class="sort-btn active" id="sort-idx"   onclick="setSort('idx')">default</button>
    <button class="sort-btn"        id="sort-ping"  onclick="setSort('ping')">ping ↑</button>
    <button class="sort-btn"        id="sort-speed" onclick="setSort('speed')">speed ↓</button>
  </div>
</div>

<div class="tw">
  <div class="center-msg" id="msg-area">
    <div class="ico"><span class="spinner"></span></div>
    <p>Loading configs…</p>
  </div>
  <table id="tbl" style="display:none">
    <thead><tr>
      <th class="ci">#</th>
      <th class="cn">Name</th>
      <th class="ch">Host</th>
      <th class="ct">Transport</th>
      <th class="cs">Security</th>
      <th class="cp2">Ping</th>
      <th class="csp">Speed</th>
      <th class="ca">Actions</th>
    </tr></thead>
    <tbody id="tb"></tbody>
  </table>
</div>

<!-- ── Connection Bar (bottom) ── -->
<div id="conn-bar">
  <div class="cdot" id="cdot"></div>
  <span id="clabel">DISCONNECTED</span>
  <span id="cdetail" style="flex:1;color:var(--dim)"></span>
  <div id="cports"></div>

  <!-- Mode selector (always visible) -->
  <div class="mode-wrap" id="mode-wrap">
    <button class="mpill sel-proxy" id="mp-proxy" onclick="setMode('proxy')">proxy</button>
    <button class="mpill"           id="mp-tun"   onclick="setMode('tun')">tun</button>
    <span class="mtip" id="mtip" style="display:none"></span>
  </div>

  <button class="btn red" id="btn-dc" onclick="doDisconnect()" style="display:none">disconnect</button>
</div>

<script>
// ── state ──────────────────────────────────────────────────────────
const entries={};
let sortMode='idx', filterText='';
let connState={status:'idle',entry_index:-1,mode:'proxy'};
let appInfo={singbox_available:false,is_admin:false,os:'windows'};
let selectedMode='proxy';

// ── SSE ───────────────────────────────────────────────────────────
const es=new EventSource('/api/stream');
es.onmessage=e=>{
  const ev=JSON.parse(e.data);
  // Filter tab-scoped events: ignore if event belongs to a different tab
  const evTab=ev.tab||'';
  const tabMatch=!evTab||evTab===activeTabId;
  if     (ev.type==='loading'&&tabMatch)             onLoading();
  else if(ev.type==='loaded'&&tabMatch)              onLoaded(ev.payload);
  else if(ev.type==='entry_update'&&tabMatch)        onUpdate(ev.payload);
  else if(ev.type==='conn_update')                   onConnUpdate(ev.payload);
  else if(ev.type==='app_info')                      onAppInfo(ev.payload);
  else if(ev.type==='bulk_ping_start')               startBulk('ping',ev.payload,evTab);
  else if(ev.type==='bulk_ping_progress'&&tabMatch)  progBulk('ping',ev.payload);
  else if(ev.type==='bulk_ping_done')                doneBulk('ping','ping all','btn-ping-all','ghost',evTab);
  else if(ev.type==='bulk_speed_start')              startBulk('speed',ev.payload,evTab);
  else if(ev.type==='bulk_speed_progress'&&tabMatch) progBulk('speed',ev.payload);
  else if(ev.type==='bulk_speed_done')               doneBulk('speed','speed all','btn-speed-all','ghost',evTab);
  else if(ev.type==='tabs_update')                   onTabsUpdate(ev.payload);
  else if(ev.type==='active_tab')                    onActiveTab(ev.payload);
};

// ── app info → update mode pills ──────────────────────────────────
function onAppInfo(info){
  appInfo=info;
  const tunBtn=document.getElementById('mp-tun');
  const tip=document.getElementById('mtip');
  const tunOk=info.singbox_available&&info.is_admin;
  if(!tunOk){
    tunBtn.classList.add('off');
    tip.style.display='';
    tip.textContent=!info.singbox_available?'sing-box not found':'requires admin/root';
  } else {
    tunBtn.classList.remove('off');
    tip.style.display='none';
  }
  rebuildTable();
}

function setMode(m){
  if(m==='tun'&&(!appInfo.singbox_available||!appInfo.is_admin))return;
  selectedMode=m;
  document.getElementById('mp-proxy').className='mpill'+(m==='proxy'?' sel-proxy':'');
  document.getElementById('mp-tun').className  ='mpill'+(m==='tun'?' sel-tun':'')
    +(!appInfo.singbox_available||!appInfo.is_admin?' off':'');
  rebuildTable();
}

// ── connection bar ─────────────────────────────────────────────────
function onConnUpdate(cs){
  connState=cs;
  const bar  =document.getElementById('conn-bar');
  const dot  =document.getElementById('cdot');
  const lbl  =document.getElementById('clabel');
  const det  =document.getElementById('cdetail');
  const ports=document.getElementById('cports');
  const btnDc=document.getElementById('btn-dc');
  const mwrap=document.getElementById('mode-wrap');

  bar.className=''; dot.className='cdot'; lbl.className='';

  const isp=cs.mode==='proxy', ist=cs.mode==='tun';
  const isActive = cs.status==='connected'||cs.status==='connecting'||cs.status==='error';

  // Hide mode switcher while connected — you can't change mode mid-session
  mwrap.style.display = isActive ? 'none' : '';

  if(cs.status==='connected'){
    const cls=isp?'cp':'conn-tun';
    bar.classList.add(cls); dot.classList.add(cls); lbl.className=cls;
    lbl.textContent=isp?'SYSTEM PROXY':'TUN MODE';
    det.innerHTML='via <span>'+x(cs.entry_name)+'</span>&ensp;·&ensp;'+fmtUptime(cs.uptime_sec);
    ports.style.display='flex';
    if(isp){
      ports.innerHTML=
        '<div class="pchip" onclick="cpText(\'127.0.0.1:'+cs.http_port+'\')">HTTP&nbsp;<b>'+cs.http_port+'</b></div>'+
        '<div class="pchip" onclick="cpText(\'127.0.0.1:'+cs.socks_port+'\')">SOCKS5&nbsp;<b>'+cs.socks_port+'</b></div>';
    } else {
      ports.innerHTML=
        '<div class="pchip">TUN&nbsp;<b>'+(cs.tun_iface||'vair-tun')+'</b></div>'+
        '<div class="pchip" style="pointer-events:none;color:var(--dim)">all traffic routed</div>';
    }
    btnDc.style.display='';
  } else if(cs.status==='connecting'){
    bar.classList.add('cc'); dot.classList.add('cc'); lbl.className='cc';
    lbl.textContent=ist?'STARTING TUN…':'CONNECTING…';
    det.innerHTML='<span>'+x(cs.entry_name)+'</span>';
    ports.style.display='none'; btnDc.style.display='';
  } else if(cs.status==='disconnecting'){
    bar.className=''; dot.className='cdot'; lbl.className='';
    lbl.textContent='DISCONNECTING…';
    det.textContent='';
    ports.style.display='none'; btnDc.style.display='none';
    mwrap.style.display='none';
  } else if(cs.status==='error'){
    bar.classList.add('ce'); dot.classList.add('ce'); lbl.className='ce';
    lbl.textContent='ERROR';
    det.innerHTML='<span style="color:var(--red)">'+x(cs.error||'unknown error')+'</span>';
    ports.style.display='none'; btnDc.style.display='';
  } else {
    lbl.textContent='DISCONNECTED';
    det.textContent='';
    ports.style.display='none'; btnDc.style.display='none';
  }

  // highlight row
  document.querySelectorAll('tbody tr.row-cp,tbody tr.row-ct').forEach(r=>{r.classList.remove('row-cp','row-ct')});
  if((cs.status==='connected'||cs.status==='connecting')&&cs.entry_index>=0&&(!cs.conn_tab||cs.conn_tab===activeTabId)){
    const row=document.getElementById('r'+cs.entry_index);
    if(row)row.classList.add(cs.mode==='tun'?'row-ct':'row-cp');
  }
  rebuildTable();
}

function fmtUptime(s){
  if(!s||s<0)return '';
  const h=Math.floor(s/3600),m=Math.floor((s%3600)/60),ss=s%60;
  return h>0?h+'h '+m+'m':(m>0?m+'m '+ss+'s':ss+'s');
}

// ── data ───────────────────────────────────────────────────────────
function onLoading(){
  document.getElementById('msg-area').style.display='flex';
  document.getElementById('tbl').style.display='none';
  document.getElementById('msg-area').innerHTML='<div class="ico"><span class="spinner"></span></div><p>Loading configs…</p>';
}
function onLoaded(list){
  Object.keys(entries).forEach(k=>delete entries[k]);
  document.getElementById('tb').innerHTML='';
  list.forEach(e=>entries[e.index]=e);
  document.getElementById('msg-area').style.display='none';
  document.getElementById('tbl').style.display='';
  selectedRows.clear();
  // Reset progress bar if we switched to a different tab than the one being tested
  if(bulkProgressTab&&bulkProgressTab!==activeTabId) setBar(0);
  rebuildTable(); recalcStats();
  document.getElementById('s-tot').textContent=list.length;
}
function onUpdate(e){ entries[e.index]=e; updateRow(e.index); recalcStats(); }

function recalcStats(){
  const all=Object.values(entries);
  document.getElementById('s-ok').textContent=all.filter(e=>e.ping_status==='ok').length;
  document.getElementById('s-er').textContent=all.filter(e=>e.ping_status==='failed').length;
  const dl=all.filter(e=>e.delay>0).map(e=>e.delay);
  const sp=all.filter(e=>e.speed_mbps>0).map(e=>e.speed_mbps);
  document.getElementById('s-ms').textContent=dl.length?Math.min(...dl)+'ms':'—';
  document.getElementById('s-sp').textContent=sp.length?(Math.max(...sp).toFixed(1)+' MB/s'):'—';
}

function startBulk(id,total,evTab){
  bulkProgressTab=evTab||activeTabId;
  if(bulkProgressTab===activeTabId) setBar(0);
  setBtn('btn-'+(id==='ping'?'ping':'speed')+'-all',true,id==='ping'?'pinging…':'ping→speed…');
}
function progBulk(id,p){
  if(bulkProgressTab===activeTabId) setBar(p.done/p.total*100);
}
function doneBulk(id,label,btnId,cls,evTab){
  var dt=evTab||bulkProgressTab;
  if(dt===activeTabId){ setBar(100); setTimeout(()=>{setBar(0);},1200); }
  bulkProgressTab='';
  const b=document.getElementById(btnId);
  b.disabled=false; b.textContent=label;
  b.className='btn ghost';
}
function setBar(pct){ document.getElementById('pb-main').style.width=pct+'%'; }
function setBtn(id,dis,txt){ const b=document.getElementById(id); b.disabled=dis; b.textContent=txt; }

function setSort(m){
  sortMode=m;
  ['idx','ping','speed'].forEach(s=>document.getElementById('sort-'+s).classList.toggle('active',s===m));
  rebuildTable();
}
function applyFilter(){ filterText=document.getElementById('fi').value.toLowerCase(); rebuildTable(); }
function matches(e){
  if(!filterText)return true;
  return(e.name||'').toLowerCase().includes(filterText)||(e.host||'').toLowerCase().includes(filterText)
    ||(e.network||'').toLowerCase().includes(filterText)||(e.security||'').toLowerCase().includes(filterText);
}

// ── table ──────────────────────────────────────────────────────────
// Virtual scroll: only renders visible rows + buffer. Works for 10,000+ configs.
// Keeps the full sorted list in sortedList, renders only the window.
// Top/bottom padding rows maintain correct scroll height.
let sortedList = [];
let rowHeight = 30; // measured on first render
let visibleWindow = { start: 0, end: 0 }; // inclusive start, exclusive end
const VBUFFER = 10; // rows rendered above/below viewport

function rebuildTable(){
  let list=Object.values(entries).filter(matches);
  if(sortMode==='ping'){
    list.sort((a,b)=>{if(a.delay>0&&b.delay>0)return a.delay-b.delay;if(a.delay>0)return -1;if(b.delay>0)return 1;return a.index-b.index;});
  }else if(sortMode==='speed'){
    // 1. Speed OK: sorted by speed descending
    // 2. Ping OK but speed failed/skipped: sorted by ping ascending
    // 3. Currently testing
    // 4. Ping failed: sorted by index
    function speedRank(e){
      if(e.speed_status==='ok' && e.speed_mbps>0) return 0;
      if(e.ping_status==='ok' && e.delay>0) return 1;
      if(e.speed_status==='testing_speed'||e.ping_status==='testing_ping') return 2;
      if(e.ping_status==='failed') return 3;
      return 4;
    }
    list.sort((a,b)=>{
      const ra=speedRank(a),rb=speedRank(b);
      if(ra!==rb) return ra-rb;
      if(ra===0) return b.speed_mbps-a.speed_mbps;
      if(ra===1) return a.delay-b.delay;
      return a.index-b.index;
    });
  }else{list.sort((a,b)=>a.index-b.index);}
  sortedList = list;
  document.getElementById('fc').textContent=filterText?(list.length+'/'+Object.keys(entries).length+' shown'):'';
  visibleWindow = { start: 0, end: 0 }; // force full redraw
  renderVisibleRows();
}

function renderVisibleRows(){
  const tw = document.querySelector('.tw');
  const tb = document.getElementById('tb');
  if(!tw || !tb) return;
  const total = sortedList.length;

  // Small list: render everything (no virtualization overhead)
  if(total <= 100){
    if(visibleWindow.start === 0 && visibleWindow.end === total && tb.children.length === total){
      return; // already rendered
    }
    tb.innerHTML = '';
    const frag = document.createDocumentFragment();
    for(let i=0;i<total;i++) frag.appendChild(buildRow(sortedList[i], i+1));
    tb.appendChild(frag);
    visibleWindow = { start: 0, end: total };
    restoreConnHighlight();
    return;
  }

  const scrollTop = tw.scrollTop;
  const viewH = tw.clientHeight;
  const firstVisible = Math.max(0, Math.floor(scrollTop / rowHeight) - VBUFFER);
  const lastVisible = Math.min(total, Math.ceil((scrollTop + viewH) / rowHeight) + VBUFFER);

  // Skip re-render if window hasn't changed meaningfully
  if(Math.abs(firstVisible - visibleWindow.start) < 3 &&
     Math.abs(lastVisible - visibleWindow.end) < 3 &&
     tb.children.length > 0){
    return;
  }

  tb.innerHTML = '';
  const frag = document.createDocumentFragment();

  // Top spacer
  if(firstVisible > 0){
    const sp = document.createElement('tr');
    sp.style.height = (firstVisible * rowHeight) + 'px';
    sp.className = 'vspacer';
    frag.appendChild(sp);
  }
  // Visible rows
  for(let i=firstVisible; i<lastVisible; i++){
    frag.appendChild(buildRow(sortedList[i], i+1));
  }
  // Bottom spacer
  if(lastVisible < total){
    const sp = document.createElement('tr');
    sp.style.height = ((total - lastVisible) * rowHeight) + 'px';
    sp.className = 'vspacer';
    frag.appendChild(sp);
  }
  tb.appendChild(frag);
  visibleWindow = { start: firstVisible, end: lastVisible };

  // Measure actual row height on first render for accuracy
  if(total > 0 && lastVisible > firstVisible){
    const firstRow = tb.querySelector('tr:not(.vspacer)');
    if(firstRow){
      const h = firstRow.offsetHeight;
      if(h > 0 && Math.abs(h - rowHeight) > 2){
        rowHeight = h;
      }
    }
  }
  restoreConnHighlight();
}

function restoreConnHighlight(){
  if((connState.status==='connected'||connState.status==='connecting')&&connState.entry_index>=0&&(!connState.conn_tab||connState.conn_tab===activeTabId)){
    const row=document.getElementById('r'+connState.entry_index);
    if(row)row.classList.add(connState.mode==='tun'?'row-ct':'row-cp');
  }
}

// Scroll listener for virtual window updates
(function attachVScroll(){
  const tw = document.querySelector('.tw');
  if(!tw) { setTimeout(attachVScroll, 100); return; }
  let scrollTimer = null;
  tw.addEventListener('scroll', ()=>{
    if(scrollTimer) return;
    scrollTimer = requestAnimationFrame(()=>{
      scrollTimer = null;
      renderVisibleRows();
    });
  }, {passive: true});
  window.addEventListener('resize', ()=>{
    renderVisibleRows();
  });
})();

function updateRow(idx){
  // If only a few items changed visually, targeted update is faster than full rebuild.
  // But for sorted views, position may change — rebuild.
  if(sortMode!=='idx'){ rebuildTable(); return; }
  const e=entries[idx]; if(!e)return;
  const old=document.getElementById('r'+idx);
  if(!old){
    // Row is outside virtual window, update underlying data only.
    // Next scroll/rebuild will render it correctly.
    return;
  }
  const pos=parseInt(old.cells[0].textContent)||idx+1;
  const nr=buildRow(e,pos);
  old.replaceWith(nr);
  if((connState.status==='connected'||connState.status==='connecting')&&connState.entry_index===idx)
    nr.classList.add(connState.mode==='tun'?'row-ct':'row-cp');
}

function buildRow(e,pos){
  const tr=document.createElement('tr'); tr.id='r'+e.index;
  if(selectedRows.has(e.index)) tr.classList.add('selected');
  tr.onclick=(ev)=>{
    if(ev.target.closest('.act-cell'))return; // don't select when clicking action buttons
    toggleRowSelect(e.index,ev);
  };

  let pp='pill pending',pt='—';
  if(e.ping_status==='testing_ping'){pp='pill tp';pt='pinging…';}
  else if(e.ping_status==='ok'){pp=e.delay<150?'pill ok-fast':'pill ok-ping';pt=e.delay+'ms';}
  else if(e.ping_status==='failed'){pp='pill failed';pt=e.ping_err||'timeout';}

  let sp='pill pending',st='—';
  if(e.speed_status==='testing_speed'){
    sp='pill ts';
    if(e.speed_live>0){st=e.speed_live.toFixed(1)+' MB/s';}
    else{st='connecting…';}
  }else if(e.speed_status==='ok'){sp='pill ok-speed';st=e.speed_mbps.toFixed(2)+' MB/s';}
  else if(e.speed_status==='failed'){sp='pill failed';st=e.speed_err||'failed';}
  else if(e.speed_status==='skipped'){sp='pill skipped';st='—';}

  const nc=(e.network||'tcp').toLowerCase().replace(/[^a-z]/g,'');
  const isConn=connState&&connState.status==='connected'&&connState.entry_index===e.index&&(!connState.conn_tab||connState.conn_tab===activeTabId);

  let connectBtn;
  if(isConn){
    connectBtn='<button class="btn sm-disc" onclick="doDisconnect()" title="Disconnect">disconnect</button>';
  } else {
    connectBtn='<button class="btn sm ghost" onclick="doConnect('+e.index+')" title="'+(selectedMode==='tun'?'TUN mode (all traffic)':'System Proxy (HTTP/SOCKS)')+'">connect</button>';
  }

  tr.innerHTML=
    '<td class="ci">'+pos+'</td>'+
    '<td class="cn"><div class="nc"><span class="nm" title="'+x(e.name)+'">'+x(e.name)+'</span>'+
    '</div></td>'+
    '<td class="ch"><span class="nh">'+x(e.host||'')+(e.port?':'+e.port:'')+'</span></td>'+
    '<td class="ct"><span class="nb '+nc+'">'+x(e.network||'tcp')+'</span></td>'+
    '<td class="cs"><span class="sb '+(e.security||'none')+'">'+x(e.security||'none')+'</span></td>'+
    '<td class="cp2"><div class="vc"><span class="'+pp+'" title="'+x(pt)+'">'+pt+'</span></div></td>'+
    '<td class="csp"><div class="vc"><span class="'+sp+'" title="'+x(st)+'">'+st+'</span></div></td>'+
    '<td class="ca"><div class="act-cell">'+
      connectBtn+
      '<button class="btn sm ghost" title="Ping" onclick="pingOne('+e.index+')">ping</button>'+
      '<button class="btn sm ghost"  title="Speed" onclick="speedOne('+e.index+')">speed</button>'+
      '<button class="cpb" title="Copy vless://" onclick="cpRaw(this,'+e.index+')">⎘</button>'+
    '</div></td>';
  return tr;
}

function x(s){return(s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;')}
function cpText(t){navigator.clipboard.writeText(t).catch(()=>{});}
function cpRaw(btn,idx){
  const e=entries[idx];if(!e)return;
  navigator.clipboard.writeText(e.raw).then(()=>{
    btn.classList.add('done');btn.textContent='✓';
    setTimeout(()=>{btn.classList.remove('done');btn.textContent='⎘';},1400);
  });
}

function doConnect(idx){
  fetch('/api/connect?idx='+idx+'&mode='+selectedMode,{method:'POST'}).catch(console.error);
}
function doDisconnect(){ fetch('/api/disconnect',{method:'POST'}).catch(console.error); }
function pingOne(idx)   { fetch('/api/ping/one?idx='+idx,{method:'POST'}).catch(console.error); }
function speedOne(idx)  { fetch('/api/speed/one?idx='+idx,{method:'POST'}).catch(console.error); }
function doPingAll()    { fetch('/api/ping/all',{method:'POST'}).catch(console.error); }
function doSpeedAll()   { fetch('/api/speed/all',{method:'POST'}).catch(console.error); }
function doReload()     { fetch('/api/reload',{method:'POST'}).catch(console.error); }

// ── Tab management ───────────────────────────────────────────────────────────
let activeTabId='main';
let tabsList=[{id:'main',name:'Sources',is_main:true,closable:false}];
let selectedRows=new Set(); // selected config indices for deletion
let bulkProgressTab=''; // which tab the current bulk operation belongs to

function onTabsUpdate(tabs){
  tabsList=tabs;
  renderTabs();
}
function onActiveTab(id){
  activeTabId=id;
  selectedRows.clear();
  // Reset progress bar - only show if the active tab has an ongoing bulk operation
  if(bulkProgressTab!==id) setBar(0);
  renderTabs();
}

function renderTabs(){
  const bar=document.getElementById('tab-bar');
  const addBtn=bar.querySelector('.tab-add');
  bar.querySelectorAll('.tab-btn').forEach(b=>b.remove());
  tabsList.forEach(t=>{
    const btn=document.createElement('button');
    btn.className='tab-btn'+(t.id===activeTabId?' active':'')+(t.closable?'':' no-close');
    btn.dataset.id=t.id;
    let html='<span class="tab-label">'+x(t.name)+'</span>';
    if(t.closable) html+='<span class="tab-x" onclick="event.stopPropagation();deleteTab(\''+t.id+'\')">✕</span>';
    btn.innerHTML=html;
    btn.onclick=()=>switchTab(t.id);
    btn.oncontextmenu=(e)=>{e.preventDefault();showTabMenu(e,t);};
    btn.onmousedown=(e)=>{if(e.button===0&&!e.target.closest('.tab-x'))startTabDrag(e,t.id);};
    bar.insertBefore(btn,addBtn);
  });
}

// ── Mouse-based tab drag reorder ─────────────────────────────────────────────
var tabDragState=null;
function startTabDrag(e,dragId){
  var startX=e.clientX,startY=e.clientY,moved=false;
  var onMove=function(me){
    if(!moved&&(Math.abs(me.clientX-startX)>5||Math.abs(me.clientY-startY)>5)){
      moved=true;
      var el=document.querySelector('.tab-btn[data-id="'+dragId+'"]');
      if(el)el.classList.add('dragging');
    }
    if(!moved)return;
    // Find which tab button we're over
    var tabs=document.querySelectorAll('.tab-btn');
    tabs.forEach(function(tb){tb.classList.remove('drag-over');});
    var target=document.elementFromPoint(me.clientX,me.clientY);
    if(target){
      var tbtn=target.closest('.tab-btn');
      if(tbtn&&tbtn.dataset.id!==dragId)tbtn.classList.add('drag-over');
    }
  };
  var onUp=function(ue){
    document.removeEventListener('mousemove',onMove);
    document.removeEventListener('mouseup',onUp);
    document.querySelectorAll('.tab-btn').forEach(function(tb){tb.classList.remove('dragging','drag-over');});
    if(!moved)return;
    var target=document.elementFromPoint(ue.clientX,ue.clientY);
    if(!target)return;
    var tbtn=target.closest('.tab-btn');
    if(!tbtn||tbtn.dataset.id===dragId)return;
    var toId=tbtn.dataset.id;
    var ids=tabsList.map(function(tt){return tt.id;});
    var fi=ids.indexOf(dragId),ti=ids.indexOf(toId);
    if(fi<0||ti<0)return;
    ids.splice(fi,1);ids.splice(ti,0,dragId);
    fetch('/api/tab/reorder',{method:'POST',body:JSON.stringify(ids)}).catch(console.error);
  };
  document.addEventListener('mousemove',onMove);
  document.addEventListener('mouseup',onUp);
}

function switchTab(id){
  fetch('/api/tab/switch?id='+id,{method:'POST'}).catch(console.error);
}

function addTab(){
  fetch('/api/tab/create',{method:'POST'}).then(r=>r.json()).then(tab=>{
    switchTab(tab.id);
  }).catch(console.error);
}

function deleteTab(id){
  fetch('/api/tab/delete?id='+id,{method:'POST'}).catch(console.error);
}

// ── Context menu ─────────────────────────────────────────────────────────────
function showTabMenu(e,tab){
  closeCtxMenu();
  const m=document.createElement('div');
  m.className='ctx-menu';m.id='ctx-menu';
  if(!tab.is_main){
    m.innerHTML+='<div class="ctx-menu-item" onclick="openTabSettings(\''+tab.id+'\')">Settings</div>';
    m.innerHTML+='<div class="ctx-sep"></div>';
    m.innerHTML+='<div class="ctx-menu-item danger" onclick="deleteTab(\''+tab.id+'\');closeCtxMenu()">Delete tab</div>';
  } else {
    m.innerHTML+='<div class="ctx-menu-item" style="color:var(--dim);cursor:default">Main tab (read-only)</div>';
  }
  m.style.left=Math.min(e.clientX,window.innerWidth-200)+'px';
  m.style.top=Math.min(e.clientY,window.innerHeight-120)+'px';
  document.body.appendChild(m);
  setTimeout(()=>document.addEventListener('click',closeCtxMenu,{once:true}),10);
}
function closeCtxMenu(){
  const m=document.getElementById('ctx-menu');
  if(m)m.remove();
}

// ── Tab settings modal ───────────────────────────────────────────────────────
function openTabSettings(tabId){
  closeCtxMenu();
  const tab=tabsList.find(t=>t.id===tabId);
  if(!tab)return;
  const ov=document.createElement('div');
  ov.className='modal-overlay';ov.id='tab-modal';
  ov.onclick=(e)=>{if(e.target===ov)ov.remove();};
  ov.innerHTML=
    '<div class="modal-box">'+
      '<div class="modal-title">Tab Settings</div>'+
      '<div class="modal-label">Name</div>'+
      '<input class="modal-input" id="ms-name" value="'+x(tab.name)+'" maxlength="40">'+
      '<div class="modal-label">Source URL (raw GitHub link)</div>'+
      '<input class="modal-input" id="ms-url" value="'+x(tab.source_url||'')+'" placeholder="https://raw.githubusercontent.com/...">'+
      '<div class="modal-btns">'+
        '<button class="btn ghost" onclick="document.getElementById(\'tab-modal\').remove()">cancel</button>'+
        '<button class="btn ghost" onclick="saveTabSettings(\''+tabId+'\')">save</button>'+
      '</div>'+
    '</div>';
  document.body.appendChild(ov);
  document.getElementById('ms-name').focus();
  document.getElementById('ms-name').select();
}
function saveTabSettings(tabId){
  const name=document.getElementById('ms-name').value.trim();
  const url=document.getElementById('ms-url').value.trim();
  document.getElementById('tab-modal').remove();
  if(name){
    fetch('/api/tab/rename?id='+encodeURIComponent(tabId)+'&name='+encodeURIComponent(name),{method:'POST'}).catch(console.error);
  }
  fetch('/api/tab/set-url?id='+encodeURIComponent(tabId)+'&url='+encodeURIComponent(url),{method:'POST'}).catch(console.error);
}

// ── Row selection + delete ───────────────────────────────────────────────────
document.addEventListener('keydown',function(e){
  // Ctrl+A: select all rows (prevent text selection always)
  if(e.ctrlKey&&(e.key==='a'||e.key==='A')){
    if(document.activeElement&&(document.activeElement.tagName==='INPUT'||document.activeElement.tagName==='TEXTAREA'))return;
    e.preventDefault();
    e.stopPropagation();
    if(activeTabId!=='main'){
      selectedRows.clear();
      Object.values(entries).forEach(en=>selectedRows.add(en.index));
      rebuildTable();
    }
    return;
  }
  if((e.key==='Delete'||e.key==='Backspace')&&selectedRows.size>0&&activeTabId!=='main'){
    if(document.activeElement&&(document.activeElement.tagName==='INPUT'||document.activeElement.tagName==='TEXTAREA'))return;
    e.preventDefault();
    deleteSelectedRows();
  }
  if(e.key==='Escape'){
    if(document.getElementById('tab-modal'))document.getElementById('tab-modal').remove();
    if(selectedRows.size>0){selectedRows.clear();rebuildTable();}
  }
},true);

function toggleRowSelect(idx,e){
  if(activeTabId==='main')return;
  if(e&&e.shiftKey&&selectedRows.size>0){
    // Range select
    const all=Object.values(entries).filter(matches).sort((a,b)=>a.index-b.index);
    const idxs=all.map(en=>en.index);
    const lastSel=[...selectedRows].pop();
    const from=idxs.indexOf(lastSel),to=idxs.indexOf(idx);
    if(from>=0&&to>=0){
      const lo=Math.min(from,to),hi=Math.max(from,to);
      for(let i=lo;i<=hi;i++) selectedRows.add(idxs[i]);
    }
  } else if(e&&e.ctrlKey){
    if(selectedRows.has(idx)) selectedRows.delete(idx); else selectedRows.add(idx);
  } else {
    if(selectedRows.has(idx)&&selectedRows.size===1) selectedRows.clear();
    else { selectedRows.clear(); selectedRows.add(idx); }
  }
  rebuildTable();
}

function deleteSelectedRows(){
  const indices=[...selectedRows];
  selectedRows.clear();
  fetch('/api/tab/delete-entries?id='+activeTabId,{method:'POST',body:JSON.stringify(indices)}).catch(console.error);
}

// ── Ctrl+V paste handler ─────────────────────────────────────────────────────
document.addEventListener('paste', function(e){
  if(document.activeElement&&(document.activeElement.tagName==='INPUT'||document.activeElement.tagName==='TEXTAREA'))return;
  const text=(e.clipboardData||window.clipboardData).getData('text');
  if(!text||!text.includes('vless://'))return;
  if(activeTabId==='main'){
    fetch('/api/tab/create',{method:'POST'}).then(r=>r.json()).then(tab=>{
      fetch('/api/tab/switch?id='+tab.id,{method:'POST'}).then(()=>{
        fetch('/api/tab/paste?id='+tab.id,{method:'POST',body:text}).catch(console.error);
      });
    }).catch(console.error);
    return;
  }
  fetch('/api/tab/paste?id='+activeTabId,{method:'POST',body:text}).catch(console.error);
});

// ── Custom title bar (standalone frameless mode) ─────────────────────────────
(function(){
  if(typeof _goWinClose !== 'function') return;

  const titlebar = document.getElementById('titlebar');
  const btnMax   = document.getElementById('tb-max');
  titlebar.classList.add('active');

  // Load logo from Go binding
  if(typeof _goLogoBase64 === 'function'){
    _goLogoBase64().then(b64=>{
      if(b64) document.getElementById('tb-logo-img').src='data:image/png;base64,'+b64;
    }).catch(()=>{});
  }

  const svgMax     = '<svg width="9" height="9" viewBox="0 0 9 9"><rect x="0.5" y="0.5" width="8" height="8" fill="none" stroke="currentColor" stroke-width="1"/></svg>';
  const svgRestore = '<svg width="11" height="11" viewBox="0 0 11 11"><rect x="2.5" y="0.5" width="8" height="8" fill="none" stroke="currentColor" stroke-width="1"/><rect x="0.5" y="2.5" width="8" height="8" fill="var(--bg)" stroke="currentColor" stroke-width="1"/></svg>';

  function syncMaxIcon(){
    _goWinIsMaximized().then(v=>{
      btnMax.innerHTML = v ? svgRestore : svgMax;
      btnMax.title = v ? 'Restore' : 'Maximize';
    }).catch(()=>{});
  }
  syncMaxIcon();
  window.addEventListener('resize', syncMaxIcon);

  document.getElementById('tb-close').addEventListener('click', ()=>_goWinClose());
  document.getElementById('tb-min').addEventListener('click',   ()=>_goWinMinimize());
  btnMax.addEventListener('click', ()=>_goWinMaximize().then(syncMaxIcon));

  document.getElementById('tb-drag').addEventListener('mousedown', e=>{
    if(e.button===0 && !e.target.closest('.tb-btns')) _goWinDragStart();
  });
  document.getElementById('tb-drag').addEventListener('dblclick', e=>{
    if(!e.target.closest('.tb-btns')) _goWinMaximize().then(syncMaxIcon);
  });
})();

</script>
</body>
</html>`

// ── Track spawned test processes for cleanup ─────────────────────
var (
	spawnedPIDs   []int
	spawnedPIDsMu sync.Mutex
)

func trackPID(pid int) {
	spawnedPIDsMu.Lock()
	spawnedPIDs = append(spawnedPIDs, pid)
	spawnedPIDsMu.Unlock()
}

func untrackPID(pid int) {
	spawnedPIDsMu.Lock()
	for i, p := range spawnedPIDs {
		if p == pid {
			spawnedPIDs = append(spawnedPIDs[:i], spawnedPIDs[i+1:]...)
			break
		}
	}
	spawnedPIDsMu.Unlock()
}

// killOrphanedXray kills any test xray processes left behind.
func killOrphanedXray() {
	spawnedPIDsMu.Lock()
	pids := make([]int, len(spawnedPIDs))
	copy(pids, spawnedPIDs)
	spawnedPIDsMu.Unlock()
	for _, pid := range pids {
		if p, err := os.FindProcess(pid); err == nil {
			p.Kill() //nolint:errcheck
		}
	}
}

// ─────────────────────────── route registration ──────────────────

func registerRoutes() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(indexHTML))
	})
	http.HandleFunc("/api/stream", handleSSE)
	http.HandleFunc("/api/connect", handleConnect)
	http.HandleFunc("/api/disconnect", handleDisconnect)
	http.HandleFunc("/api/conn/state", handleConnState)
	http.HandleFunc("/api/ping/one", handlePingOne)
	http.HandleFunc("/api/ping/all", handlePingAll)
	http.HandleFunc("/api/speed/one", handleSpeedOne)
	http.HandleFunc("/api/speed/all", handleSpeedAll)
	http.HandleFunc("/api/reload", handleReload)
	http.HandleFunc("/api/tab/create", handleTabCreate)
	http.HandleFunc("/api/tab/delete", handleTabDelete)
	http.HandleFunc("/api/tab/switch", handleTabSwitch)
	http.HandleFunc("/api/tab/paste", handleTabPaste)
	http.HandleFunc("/api/tab/rename", handleTabRename)
	http.HandleFunc("/api/tab/set-url", handleTabSetURL)
	http.HandleFunc("/api/tab/delete-entries", handleTabDeleteEntries)
	http.HandleFunc("/api/tab/reorder", handleTabReorder)
}

func httpListenAndServe() error {
	if err := http.ListenAndServe(fmt.Sprintf(":%d", webPort), nil); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

// ─────────────────────────── main ────────────────────────────────

func main() {
	// On Windows the standalone build calls standaloneMain() which uses
	// embedded binaries and shows a native Fyne UI window (no browser needed).
	// On other platforms (or when LEGACY=1 env is set), fall through to
	// the classic CLI mode that takes xray/sing-box paths as arguments.
	if os.Getenv("LEGACY") == "" {
		standaloneMain()
		return
	}

	// ── Legacy / cross-platform CLI mode ─────────────────────────────
	xrayBin := "xray"
	if len(os.Args) > 1 {
		xrayBin = os.Args[1]
	}
	if _, err := exec.LookPath(xrayBin); err != nil {
		if _, err2 := os.Stat(xrayBin); err2 != nil {
			fmt.Fprintf(os.Stderr,
				"❌  xray not found.\n    Usage: %s [xray] [sing-box]\n    Releases: https://github.com/XTLS/Xray-core/releases\n",
				os.Args[0])
			os.Exit(1)
		}
	}
	state.xrayBin = xrayBin

	if len(os.Args) > 2 {
		sb := os.Args[2]
		if _, err := exec.LookPath(sb); err == nil {
			state.singboxBin = sb
		} else if _, err2 := os.Stat(sb); err2 == nil {
			state.singboxBin = sb
		} else {
			fmt.Fprintf(os.Stderr, "⚠  sing-box not found at %q — TUN mode unavailable\n", sb)
		}
	} else if path, err := exec.LookPath("sing-box"); err == nil {
		state.singboxBin = path
		fmt.Printf("ℹ  sing-box auto-detected: %s\n", path)
	}

	isAdmin := checkAdmin()
	fmt.Printf("✅  vair → http://localhost:%d\n    xray:     %s\n", webPort, xrayBin)
	if state.singboxBin != "" {
		fmt.Printf("    sing-box: %s\n", state.singboxBin)
	} else {
		fmt.Printf("    sing-box: not found (TUN unavailable)\n")
	}
	fmt.Printf("    admin: %v\n\n", isAdmin)
	if state.singboxBin != "" && !isAdmin {
		fmt.Println("⚠  Run as administrator to enable TUN mode.")
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		fmt.Println("\n⏹  Shutting down — cleaning up…")
		stopConnection()
		killOrphanedXray()
		os.Exit(0)
	}()

	registerRoutes()
	loadTabs()
	go fetchAndInit()
	if err := httpListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
