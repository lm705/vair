package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────── xray lifecycle (testing) ────────────

func withXray(n *Node, ttl time.Duration, fn func(httpPort int, tr *http.Transport) error) error {
	httpPort, err := findFreePort()
	if err != nil {
		return fmt.Errorf("no free port")
	}
	cfg := buildXrayConfigForNode(n, httpPort, -1, "", "")
	if cfg == nil {
		return fmt.Errorf("xray: unsupported protocol %s", n.Kind)
	}
	traceTest := logTestsEnabled()
	if traceTest {
		// Full diagnostics for this probe: force info level and re-enable the
		// access log so failure reasons (dial timeouts, TLS handshake errors,
		// …) show in the panel regardless of the global Verbose setting.
		cfg["log"] = map[string]interface{}{"loglevel": "info"}
	}
	tmpPath, err := writeTempJSON(cfg, "xray-test")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	defer os.Remove(tmpPath)
	ctx, cancel := context.WithTimeout(context.Background(), ttl)
	defer cancel()
	cmd := exec.CommandContext(ctx, state.xrayBin, "run", "-c", tmpPath)
	var stderrBuf strings.Builder
	if traceTest {
		// xray logs to stdout; also tee stderr into the panel while keeping a
		// copy in stderrBuf for the crash-error message below.
		label := nodeLogLabel(n)
		cmd.Stdout = &lineSink{engine: "xray", name: label}
		cmd.Stderr = io.MultiWriter(&stderrBuf, &lineSink{engine: "xray", name: label})
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = &stderrBuf
	}
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

// withSingbox is the sing-box mirror of withXray: it spins up a throwaway
// sing-box process exposing a local HTTP proxy on a free port, waits for the
// port to open, then hands a ready *http.Transport to fn for the duration of
// ttl. Used by the ping/speed runners for UDP-family nodes (Hysteria2/TUIC)
// that xray can't dial. Same lifecycle contract as withXray — process is
// killed and PID untracked on return regardless of outcome.
func withSingbox(n *Node, ttl time.Duration, fn func(httpPort int, tr *http.Transport) error) error {
	if state.singboxBin == "" {
		return fmt.Errorf("sing-box not found (pass its path as 2nd arg)")
	}
	httpPort, err := findFreePort()
	if err != nil {
		return fmt.Errorf("no free port")
	}
	cfg := buildSingboxTestConfig(n, httpPort)
	if cfg == nil {
		return fmt.Errorf("sing-box: unsupported protocol %s", n.Kind)
	}
	traceTest := logTestsEnabled()
	if traceTest {
		// Full diagnostics: force info level so failure reasons appear.
		cfg["log"] = map[string]interface{}{"level": "info", "timestamp": true}
	}
	tmpPath, err := writeTempJSON(cfg, "singbox-test")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	defer os.Remove(tmpPath)
	ctx, cancel := context.WithTimeout(context.Background(), ttl)
	defer cancel()
	cmd := exec.CommandContext(ctx, state.singboxBin, "run", "-c", tmpPath)
	var stderrBuf strings.Builder
	if traceTest {
		// sing-box logs to stderr; tee it into the panel and keep a copy in
		// stderrBuf for the crash-error message below.
		label := nodeLogLabel(n)
		cmd.Stdout = &lineSink{engine: "singbox", name: label}
		cmd.Stderr = io.MultiWriter(&stderrBuf, &lineSink{engine: "singbox", name: label})
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = &stderrBuf
	}
	hideProcess(cmd)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("sing-box start: %w", err)
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
		portResult <- waitForPort(httpPort, time.Now().Add(singboxStartupTimeout))
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
		return fmt.Errorf("sing-box: %s", errMsg)
	case ready := <-portResult:
		if !ready {
			return fmt.Errorf("sing-box: port not ready after %s", singboxStartupTimeout)
		}
	}
	return fn(httpPort, makeSharedTransport(httpPort))
}

// withEngine dispatches a throwaway-proxy probe to the right backend based on
// the node's protocol: xray for the TCP-family, sing-box for the UDP-family.
// Ping/speed runners call this instead of withXray directly so a mixed list
// (VLESS + Hysteria2 + …) just works without per-call-site branching.
func withEngine(n *Node, ttl time.Duration, fn func(httpPort int, tr *http.Transport) error) error {
	if engineForNode(n) == "singbox" {
		return withSingbox(n, ttl, fn)
	}
	return withXray(n, ttl, fn)
}

// ─────────────────────────── system proxy ────────────────────────

// proxyLockPath marks that WE currently have the Windows system proxy
// enabled. Written when we set it, removed when we unset it. If it survives
// to the next startup, the app was killed / the PC shut down without a
// clean disconnect — see clearStaleProxy.
func proxyLockPath() string { return runtimePath("proxy.active") }

// appliedProxyPort is the local port the Windows system proxy currently points
// at, as set by us (0 = not set). It lets setSystemProxy skip the WinINET
// registry churn when nothing actually changes — the key speedup for switching
// between configs in proxy mode, where the public port stays the same and only
// the core underneath swaps. Only touched from the connect/disconnect paths,
// which are serialised by cm.actionMu (plus clearStaleProxy at startup, before
// any connection), so it needs no separate lock.
var appliedProxyPort int

func setSystemProxy(port int) error {
	if appliedProxyPort == port {
		// Already pointed here — skip the 3 reg writes + the inetcpl cache flush
		// (which broadcasts WM_SETTINGCHANGE to every top-level window). This is
		// what makes a same-port proxy switch feel instant.
		return nil
	}
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
	// Record that the proxy is ours and active, so a dirty shutdown can be
	// detected and recovered on next launch.
	os.MkdirAll(tabsDir(), 0755)                                               //nolint:errcheck
	os.WriteFile(proxyLockPath(), []byte(addr), 0644)                          //nolint:errcheck
	runHidden("rundll32.exe", "inetcpl.cpl,ClearMyTracksByProcess", "8").Run() //nolint:errcheck
	appliedProxyPort = port
	return nil
}

func unsetSystemProxy() {
	rp := `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	runHidden("reg", "add", rp, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "0", "/f").Run() //nolint:errcheck
	os.Remove(proxyLockPath())                                                                 //nolint:errcheck
	appliedProxyPort = 0
}

// clearStaleProxy runs once at startup. If the proxy-lock file from a prior
// session is still present, the app exited (or the machine powered off)
// while the Windows system proxy was pointed at a localhost port that no
// longer has anything listening — which breaks ALL internet until cleared.
// We restore connectivity by disabling the proxy. A clean disconnect
// removes the lock, so this is a no-op in the normal case.
func clearStaleProxy() {
	if _, err := os.Stat(proxyLockPath()); err == nil {
		unsetSystemProxy()
	}
}

// ─────────────────────────── connection manager ──────────────────

// startProxyConnection is the user-facing entry point (called via
// `go startProxyConnection` from handleConnect). It holds actionMu for the
// whole sequence and connects on the active tab.
func startProxyConnection(entry *ConfigEntry) {
	cm := state.conn
	cm.actionMu.Lock()
	defer cm.actionMu.Unlock()
	state.mu.RLock()
	connTab := state.activeTab
	state.mu.RUnlock()
	startProxyConnectionOnTab(entry, connTab)
}

// startProxyConnectionOnTab runs the proxy connect sequence for a config that
// lives on the given tab. The caller MUST already hold cm.actionMu (the public
// wrapper above, or the auto-supervisor via TryLock) — this serialises against
// concurrent connect/disconnect flows. Passing connTab explicitly lets the
// supervisor connect a candidate on a non-active tab and still label ConnState
// with the right tab.
func startProxyConnectionOnTab(entry *ConfigEntry, connTab string) {
	cm := state.conn
	stopConnectionLocked(cm, true) // proxy→proxy: keep the system proxy set

	n, err := parseNode(entry.Raw)
	if err != nil {
		setConnError(cm, entry, err.Error(), connTab)
		return
	}
	entry.mu.Lock()
	entry.Protocol = string(n.Kind)
	entry.mu.Unlock()

	if reason := nodeUnsupportedReason(n); reason != "" {
		setConnError(cm, entry, reason, connTab)
		return
	}

	// Pre-resolve the VPN server hostname on the Go side so xray gets a
	// numeric IP for its outbound. Cheap, idempotent (no-op if the host
	// is already an IP literal), and lets the kill-switch path actually
	// boot — when strict_route is on, xray can't reach the OS resolver
	// on UDP/53 to do this resolution itself.
	if err := preResolveHost(n); err != nil {
		setConnError(cm, entry, "resolve server: "+err.Error(), connTab)
		return
	}

	cm.mu.Lock()
	cm.state = ConnState{Status: ConnConnecting, Mode: ModeProxy, EntryIndex: entry.Index, EntryName: n.Name, ConnTab: connTab, ConnRaw: entry.Raw}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})

	// Engine branch: TCP-family (VLESS/VMess/Trojan/SS) goes through xray;
	// UDP-family (Hysteria2/TUIC) goes through a pure-sing-box process.
	// Both converge on finalizeProxyConnection for the counter/system-proxy
	// /state wiring.
	if engineForNode(n) == "singbox" {
		startProxyConnectionSingbox(cm, entry, n, connTab)
		return
	}
	startProxyConnectionXray(cm, entry, n, connTab)
}

// startProxyChain is the user-facing entry point for a multi-hop chain (called
// via `go startProxyChain` from handleConnectChain). It holds actionMu for the
// whole sequence and connects on the active tab. entries[0] is the FIRST hop
// (entry), entries[len-1] is the EXIT. Chains are xray-only; mode selects
// proxy or TUN.
func startChain(entries []*ConfigEntry, mode ConnMode) {
	cm := state.conn
	cm.actionMu.Lock()
	defer cm.actionMu.Unlock()
	state.mu.RLock()
	connTab := state.activeTab
	state.mu.RUnlock()
	startChainOnTab(entries, connTab, mode)
}

// startChainOnTab runs the chain connect sequence (proxy or TUN per mode).
// Caller MUST hold cm.actionMu. It parses + pre-resolves every hop, validates
// they're all xray-family (chainEngineReason), then builds a single
// multi-outbound xray config (buildXrayChainConfig) and connects. The
// representative entry for ConnState is the entry hop (entries[0]); the
// conn-bar shows the full "A → B" chain.
func startChainOnTab(entries []*ConfigEntry, connTab string, mode ConnMode) {
	cm := state.conn
	stopConnectionLocked(cm, mode != ModeTUN) // keep proxy only for a proxy chain

	if len(entries) < 2 {
		if len(entries) == 1 {
			// Degenerate "chain" of one — just connect it normally.
			if mode == ModeTUN {
				startTUNConnectionOnTab(entries[0], connTab)
			} else {
				startProxyConnectionOnTab(entries[0], connTab)
			}
			return
		}
		setConnError(cm, &ConfigEntry{}, "a chain needs at least 2 configs", connTab)
		return
	}

	nodes := make([]*Node, len(entries))
	labels := make([]string, len(entries))
	raws := make([]string, len(entries))
	for i, e := range entries {
		n, err := parseNode(e.Raw)
		if err != nil {
			setConnError(cm, e, "chain hop parse: "+err.Error(), connTab)
			return
		}
		nodes[i] = n
		labels[i] = n.Name
		raws[i] = e.Raw
		e.mu.Lock()
		e.Protocol = string(n.Kind)
		e.mu.Unlock()
	}

	// Single-engine guard: all hops must be xray-family.
	if reason := chainEngineReason(nodes); reason != "" {
		setConnError(cm, entries[0], "chain: "+reason, connTab)
		return
	}

	// Pre-resolve every hop's server hostname (same reason as the single-node
	// path: lets the kill-switch/strict-route boot, and xray gets numeric IPs).
	for i, n := range nodes {
		if err := preResolveHost(n); err != nil {
			setConnError(cm, entries[0], "chain: resolve hop "+labels[i]+": "+err.Error(), connTab)
			return
		}
	}

	chainName := strings.Join(labels, " → ")
	if mode == ModeTUN {
		cm.mu.Lock()
		cm.state = ConnState{Status: ConnConnecting, Mode: ModeTUN, EntryIndex: entries[0].Index, EntryName: chainName, ConnTab: connTab, ConnRaw: entries[0].Raw, Chain: labels, ChainRaws: raws}
		cm.mu.Unlock()
		state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
		tunIfaceName := fmt.Sprintf("vair-%d", time.Now().Unix()%10000)
		startTUNConnectionHybrid(cm, entries[0], nodes[0], connTab, tunIfaceName, nodes, labels, raws)
		return
	}

	cm.mu.Lock()
	cm.state = ConnState{Status: ConnConnecting, Mode: ModeProxy, EntryIndex: entries[0].Index, EntryName: chainName, ConnTab: connTab, ConnRaw: entries[0].Raw, Chain: labels, ChainRaws: raws}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})

	runXrayProxyConfig(cm, entries[0], chainName, connTab, labels, raws, func(intHTTPPort, intSOCKSPort int) map[string]interface{} {
		// Proxy mode = user-facing SOCKS → use the configured SOCKS-auth credentials
		// (empty when the setting is off).
		su, sp := proxySocksCreds()
		return buildXrayChainConfig(nodes, intHTTPPort, intSOCKSPort, su, sp)
	})
}

// startProxyConnectionXray runs the xray-backed proxy path for a single node:
// build the config and hand off to runXrayProxyConfig for the shared spawn/
// wait/finalize sequence (also used by the chain path).
func startProxyConnectionXray(cm *connManager, entry *ConfigEntry, n *Node, connTab string) {
	runXrayProxyConfig(cm, entry, n.Name, connTab, nil, nil, func(intHTTPPort, intSOCKSPort int) map[string]interface{} {
		// Proxy mode = user-facing SOCKS → use the configured SOCKS-auth credentials
		// (empty when the setting is off).
		su, sp := proxySocksCreds()
		return buildXrayConfigForNode(n, intHTTPPort, intSOCKSPort, su, sp)
	})
}

// runXrayProxyConfig is the shared xray proxy spawn path used by both the
// single-node connect and the multi-hop chain connect. It allocates the user-
// visible + internal ports, builds the config via build() (given the internal
// ports), spawns xray, waits for readiness (or an early crash), and hands off
// to finalizeProxyConnection. chain/chainRaws are the ordered hop labels/raws
// for a chain connection (both nil for a single node), passed through to
// ConnState. entry is the representative entry (the chain's entry hop) used for
// index/raw/error reporting; name is what the conn-bar shows.
func runXrayProxyConfig(cm *connManager, entry *ConfigEntry, name, connTab string, chain, chainRaws []string, build func(intHTTPPort, intSOCKSPort int) map[string]interface{}) {
	httpPort, socksPort := currentProxyPorts()
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

	// The user-visible ports above are what apps connect to (system proxy
	// points at httpPort). xray itself listens on internal ports — bytes
	// flow ext → counter → int → xray. The forwarder is a transparent
	// localhost TCP relay that tallies bytes per direction.
	intHTTPPort, e1 := findFreePort()
	if e1 != nil {
		setConnError(cm, entry, "no free port for xray http")
		return
	}
	intSOCKSPort, e2 := findFreePort()
	if e2 != nil {
		setConnError(cm, entry, "no free port for xray socks")
		return
	}

	xrayCfg := build(intHTTPPort, intSOCKSPort)
	if xrayCfg == nil {
		setConnError(cm, entry, "xray: could not build config")
		return
	}
	tmpPath, err := writeTempJSON(xrayCfg, "xray-conn")
	if err != nil {
		setConnError(cm, entry, err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, state.xrayBin, "run", "-c", tmpPath)
	// Point xray at the asset dir so external geo files (ext:geosite-ru-blocked.dat
	// etc.) resolve even if the exe-dir fallback ever changes.
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+filepath.Dir(state.xrayBin))
	hideProcess(cmd)

	// xray writes its console log to STDOUT (unlike sing-box, which uses
	// stderr), so capture both. Stream them into the Logs panel and keep a
	// short tail so we can still report the real error if xray crashes at
	// startup.
	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()
	var tailMu sync.Mutex
	var tail []string
	sink := func(line string) {
		tailMu.Lock()
		tail = append(tail, line)
		if len(tail) > 6 {
			tail = tail[len(tail)-6:]
		}
		tailMu.Unlock()
	}

	if err = cmd.Start(); err != nil {
		cancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "xray start: "+err.Error())
		return
	}

	go pumpProcLog("xray", stdoutPipe, sink)
	go pumpProcLog("xray", stderrPipe, sink)

	// Watch for immediate crash in background.
	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()

	// Wait for xray HTTP port OR an immediate crash, whichever comes first.
	portResult := make(chan bool, 1)
	go func() {
		portResult <- waitForPort(intHTTPPort, time.Now().Add(xrayConnTimeout))
	}()

	select {
	case exitErr := <-exitCh:
		// xray exited before the port opened — report the last stderr line.
		cancel()
		os.Remove(tmpPath)
		tailMu.Lock()
		errMsg := ""
		for i := len(tail) - 1; i >= 0; i-- {
			if s := strings.TrimSpace(tail[i]); s != "" {
				errMsg = s
				break
			}
		}
		tailMu.Unlock()
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

	// xray is now listening on the internal ports — hand off to the shared
	// post-spawn block (byte counters, system proxy, ConnState, tickers).
	finalizeProxyConnection(cm, entry, name, connTab, cmd, cancel, tmpPath,
		httpPort, socksPort, intHTTPPort, intSOCKSPort, chain, chainRaws)
}

// finalizeProxyConnection performs the post-spawn steps shared by every
// proxy backend (xray or sing-box): stand up the byte-counting forwarders
// in front of the engine's internal HTTP/SOCKS ports, point the system
// proxy at httpPort, record the connected ConnState, and start the uptime
// /stats tickers. On forwarder failure it tears the just-spawned engine
// down (mainCancel + tmp cleanup) and reports the error. The engine
// process must already be listening on intHTTPPort/intSOCKSPort.
func finalizeProxyConnection(cm *connManager, entry *ConfigEntry, name, connTab string,
	mainCmd *exec.Cmd, mainCancel context.CancelFunc, tmpPath string,
	httpPort, socksPort, intHTTPPort, intSOCKSPort int, chain, chainRaws []string) {

	counter := &trafficCounter{}
	fwdCtx, fwdCancel := context.WithCancel(context.Background())
	bindHost := proxyBindHost() // 127.0.0.1, or 0.0.0.0 when "allow LAN" is on
	if _, err := startCountingForwarder(fwdCtx, bindHost, httpPort, intHTTPPort, counter, "proxy-http"); err != nil {
		fwdCancel()
		mainCancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "proxy http counter: "+err.Error())
		return
	}
	if _, err := startCountingForwarder(fwdCtx, bindHost, socksPort, intSOCKSPort, counter, "proxy-socks"); err != nil {
		fwdCancel()
		mainCancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "proxy socks counter: "+err.Error())
		return
	}

	if err := setSystemProxy(httpPort); err != nil {
		fmt.Fprintf(os.Stderr, "⚠  setSystemProxy: %v\n", err)
	}

	cm.mu.Lock()
	cm.cmd = mainCmd
	cm.cancel = mainCancel
	cm.tmpCfg = tmpPath
	cm.counter = counter
	cm.fwdCancel = fwdCancel
	cm.state = ConnState{
		Status: ConnConnected, Mode: ModeProxy, ConnTab: connTab, ConnRaw: entry.Raw,
		EntryIndex: entry.Index, EntryName: name,
		HTTPPort: httpPort, SOCKSPort: socksPort,
		StartedAt: time.Now(),
		Chain:     chain,
		ChainRaws: chainRaws,
	}
	cm.mu.Unlock()
	// "last connected" badge: for a single node it's that node; for a chain it's
	// the EXIT hop (the egress config — what the user thinks of as "where I came
	// out"), i.e. the last raw in chainRaws.
	if len(chainRaws) > 0 {
		recordLastConnected(chainRaws[len(chainRaws)-1], ModeProxy)
	} else {
		recordLastConnected(entry.Raw, ModeProxy)
	}
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	startUptimeTicker(cm)
	startStatsTicker(cm)
	if len(chain) > 0 {
		vlog("info", "connected (chain): %s — HTTP :%d / SOCKS :%d", name, httpPort, socksPort)
	} else {
		vlog("info", "connected (proxy): %s — HTTP :%d / SOCKS :%d", name, httpPort, socksPort)
	}
}

// startProxyConnectionSingbox runs the pure-sing-box proxy path for the
// UDP-family protocols (Hysteria2/TUIC) that xray can't dial. Same shape as
// the xray path — spawn sing-box on internal HTTP+SOCKS ports, wait for the
// HTTP port (or an early crash), then hand off to finalizeProxyConnection.
// The byte counters work here exactly as in the xray path: proxy mode has a
// real local HTTP/SOCKS hop to instrument (unlike pure-sing-box TUN).
func startProxyConnectionSingbox(cm *connManager, entry *ConfigEntry, n *Node, connTab string) {
	httpPort, socksPort := currentProxyPorts()
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
	intHTTPPort, e1 := findFreePort()
	if e1 != nil {
		setConnError(cm, entry, "no free port for sing-box http")
		return
	}
	intSOCKSPort, e2 := findFreePort()
	if e2 != nil {
		setConnError(cm, entry, "no free port for sing-box socks")
		return
	}

	cfg := buildSingboxProxyConfig(n, intHTTPPort, intSOCKSPort)
	if cfg == nil {
		setConnError(cm, entry, fmt.Sprintf("sing-box: unsupported protocol %s", n.Kind))
		return
	}
	tmpPath, err := writeTempJSON(cfg, "singbox-conn")
	if err != nil {
		setConnError(cm, entry, err.Error())
		return
	}
	if debugPath := runtimePath("last-singbox-proxy.json"); true {
		data, _ := json.MarshalIndent(cfg, "", "  ")
		os.WriteFile(debugPath, data, 0644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, state.singboxBin, "run", "-c", tmpPath)
	cmd.Stdout = io.Discard
	hideProcess(cmd)
	stderrPipe, _ := cmd.StderrPipe()
	var tailMu sync.Mutex
	var tail []string

	if err = cmd.Start(); err != nil {
		cancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "sing-box start: "+err.Error())
		return
	}

	go pumpProcLog("singbox", stderrPipe, func(line string) {
		tailMu.Lock()
		tail = append(tail, line)
		if len(tail) > 6 {
			tail = tail[len(tail)-6:]
		}
		tailMu.Unlock()
	})

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	portResult := make(chan bool, 1)
	go func() {
		portResult <- waitForPort(intHTTPPort, time.Now().Add(singboxConnTimeout))
	}()

	select {
	case exitErr := <-exitCh:
		cancel()
		os.Remove(tmpPath)
		tailMu.Lock()
		errMsg := ""
		for i := len(tail) - 1; i >= 0; i-- {
			if s := strings.TrimSpace(tail[i]); s != "" {
				errMsg = s
				break
			}
		}
		tailMu.Unlock()
		if errMsg == "" {
			if exitErr != nil {
				errMsg = exitErr.Error()
			} else {
				errMsg = "sing-box exited unexpectedly"
			}
		}
		if len(errMsg) > 200 {
			errMsg = "..." + errMsg[len(errMsg)-200:]
		}
		setConnError(cm, entry, "sing-box: "+errMsg)
		return
	case ready := <-portResult:
		if !ready {
			cancel()
			os.Remove(tmpPath)
			setConnError(cm, entry, "sing-box: port not ready after timeout")
			return
		}
	}

	finalizeProxyConnection(cm, entry, n.Name, connTab, cmd, cancel, tmpPath,
		httpPort, socksPort, intHTTPPort, intSOCKSPort, nil, nil)
}

// startTUNConnection is the user-facing entry point (called via
// `go startTUNConnection` from handleConnect). It holds actionMu for the whole
// sequence and connects on the active tab.
func startTUNConnection(entry *ConfigEntry) {
	cm := state.conn
	cm.actionMu.Lock()
	defer cm.actionMu.Unlock()
	state.mu.RLock()
	connTab := state.activeTab
	state.mu.RUnlock()
	startTUNConnectionOnTab(entry, connTab)
}

// startTUNConnectionOnTab runs the TUN connect sequence for a config on the
// given tab. The caller MUST already hold cm.actionMu (public wrapper above, or
// the auto-supervisor via TryLock). See startProxyConnectionOnTab for the
// rationale behind the explicit connTab parameter.
func startTUNConnectionOnTab(entry *ConfigEntry, connTab string) {
	if !checkAdmin() {
		setConnError(state.conn, entry, "TUN mode requires administrator/root. Run the program as admin.")
		return
	}
	if state.singboxBin == "" {
		setConnError(state.conn, entry, "sing-box not found. Pass path as 2nd arg: vair xray.exe sing-box.exe")
		return
	}

	cm := state.conn
	stopConnectionLocked(cm, false) // TUN: drop any system proxy from a prior proxy session

	// NOTE: no removeTUNAdapter() here anymore. Adapter names are unique per
	// session (vair-<unixtime>), so a fresh connect never collides with a stale
	// adapter, and stopConnectionLocked already removed the previous TUN adapter
	// on teardown. Ghost adapters left by a *crash* are swept once at startup
	// (see cleanupStaleTUNAdapters). Dropping this saves a full PowerShell launch
	// (~300–700ms) off every TUN connect.

	n, err := parseNode(entry.Raw)
	if err != nil {
		setConnError(cm, entry, err.Error())
		return
	}
	entry.mu.Lock()
	entry.Protocol = string(n.Kind)
	entry.mu.Unlock()

	if reason := nodeUnsupportedReason(n); reason != "" {
		setConnError(cm, entry, reason)
		return
	}

	// Pre-resolve the VPN server hostname so xray gets a numeric IP for
	// its outbound. Critical in protected mode: strict_route + WFP
	// filter would block xray's UDP/53 attempt to the OS resolver, and
	// the tunnel would deadlock at startup waiting for DNS. Cheap
	// no-op in all other cases.
	if err := preResolveHost(n); err != nil {
		setConnError(cm, entry, "resolve server: "+err.Error())
		return
	}

	cm.mu.Lock()
	cm.state = ConnState{Status: ConnConnecting, Mode: ModeTUN, EntryIndex: entry.Index, EntryName: n.Name, ConnTab: connTab, ConnRaw: entry.Raw}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})

	// Unique interface name per session avoids "file already exists".
	// Even if Windows hasn't fully cleaned the previous adapter kernel-side,
	// a new name means sing-box never conflicts with the old one.
	tunIfaceName := fmt.Sprintf("vair-%d", time.Now().Unix()%10000)

	// Engine branch. TCP-family protocols use the hybrid path (sing-box TUN
	// front-end → xray outbound). UDP-family (Hysteria2/TUIC) run as a
	// single pure-sing-box process: cm.cmd = sing-box, cm.xrayCmd = nil.
	// stopConnectionLocked already no-ops on a nil xrayCmd, so teardown
	// needs no special-casing.
	if engineForNode(n) == "singbox" {
		startTUNConnectionSingbox(cm, entry, n, connTab, tunIfaceName)
		return
	}
	startTUNConnectionHybrid(cm, entry, n, connTab, tunIfaceName, nil, nil, nil)
}

// startTUNConnectionHybrid runs the hybrid TUN path for the TCP-family
// protocols: sing-box owns the TUN device and routing, an xray child holds
// the actual protocol outbound, and a byte-counter forwarder sits between
// them so per-session traffic stats work.
//
// chainNodes/chainLabels/chainRaws are set for a multi-hop chain (otherwise
// nil): the xray child then runs a chain config (entry → … → exit) instead of a
// single outbound. The sing-box TUN front-end and the entry-host carve-out are
// identical — only the xray outbound topology differs — so the rest of the path
// is unchanged. n is the ENTRY node (used for the host carve-out / QUIC block /
// ConnState representative); for a single node it's the only node.
func startTUNConnectionHybrid(cm *connManager, entry *ConfigEntry, n *Node, connTab, tunIfaceName string, chainNodes []*Node, chainLabels, chainRaws []string) {
	isChain := len(chainNodes) > 1
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
	var xrayCfg map[string]interface{}
	// TUN hybrid: this xray SOCKS is the INTERNAL handoff sing-box dials (with the
	// matching credentials) — never user-facing — so it always keeps auth on with
	// the per-launch random credentials, independent of the user's SOCKS-auth
	// setting.
	if isChain {
		xrayCfg = buildXrayChainConfig(chainNodes, xrayHTTPPort, xraySocksPort, proxyAuthUser, proxyAuthPass)
	} else {
		xrayCfg = buildXrayConfigForNode(n, xrayHTTPPort, xraySocksPort, proxyAuthUser, proxyAuthPass)
		// In hybrid TUN the proxy-vs-direct split is made by sing-box; the xray
		// child must send everything it receives straight to the proxy. Otherwise
		// the child's own routing (e.g. only_blocked default→direct) could push a
		// connection — or a DoH DNS query — out the child's direct outbound,
		// bypassing the VPN. (Chains keep their internal hop routing.)
		if xrayCfg != nil {
			xrayCfg["routing"] = xrayRoutingAllProxy()
		}
	}
	if xrayCfg == nil {
		setConnError(cm, entry, fmt.Sprintf("xray: unsupported protocol %s", n.Kind))
		return
	}
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
	if debugPath := runtimePath("last-xray-hybrid.json"); true {
		data, _ := json.MarshalIndent(xrayCfg, "", "  ")
		os.WriteFile(debugPath, data, 0644)
	}
	xrayCtx, xrayCancel := context.WithCancel(context.Background())
	xrayCmd := exec.CommandContext(xrayCtx, state.xrayBin, "run", "-c", xrayTmpPath)
	xrayCmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+filepath.Dir(state.xrayBin))
	// xray logs to stdout (sing-box uses stderr) — capture both into the panel.
	xrayStdoutPipe, _ := xrayCmd.StdoutPipe()
	xrayStderrPipe, _ := xrayCmd.StderrPipe()
	hideProcess(xrayCmd)
	if err = xrayCmd.Start(); err != nil {
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "xray hybrid start: "+err.Error())
		return
	}
	go pumpProcLog("xray", xrayStdoutPipe, nil)
	go pumpProcLog("xray", xrayStderrPipe, nil)
	// Wait for xray port
	if !waitForPort(xrayHTTPPort, time.Now().Add(xrayStartupTimeout)) {
		xrayCmd.Process.Kill() //nolint:errcheck
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "xray hybrid: port not ready")
		return
	}
	vlog("info", "hybrid TUN: xray proxy on :%d/%d for %s", xrayHTTPPort, xraySocksPort, n.Network)

	// Insert a byte-counting forwarder between sing-box and xray's SOCKS
	// inbound. sing-box's `proxy` outbound dials this forwarder; the
	// forwarder relays into xray. xray remains unmodified. This is the
	// same trick used in proxy mode and gives us per-session traffic
	// stats with no protocol-specific code.
	counter := &trafficCounter{}
	fwdCtx, fwdCancel := context.WithCancel(context.Background())
	counterSocksPort, err := startCountingForwarder(fwdCtx, "127.0.0.1", 0, xraySocksPort, counter, "tun-socks")
	if err != nil {
		fwdCancel()
		xrayCmd.Process.Kill() //nolint:errcheck
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "tun counter: "+err.Error())
		return
	}
	// 2. Build sing-box TUN config routing through xray proxy (via counter).
	// VLESS with an XTLS flow (xtls-rprx-vision) can't carry QUIC/UDP-443 —
	// block it so browsers fall back to TCP instead of failing on HTTPS sites.
	blockQUIC := n.Vless != nil && strings.TrimSpace(n.Vless.Flow) != ""
	cfg := buildHybridTUNConfig(tunIfaceName, n.Host, xrayHTTPPort, counterSocksPort, blockQUIC)
	tmpPath, err := writeTempJSON(cfg, "singbox-tun")
	if err != nil {
		fwdCancel()
		xrayCmd.Process.Kill() //nolint:errcheck
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "singbox config write: "+err.Error())
		return
	}
	fmt.Printf("ℹ  sing-box hybrid config: %s\n", tmpPath)
	// Save debug copy
	if debugPath := runtimePath("last-singbox-hybrid.json"); true {
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
		fwdCancel()
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
	logPath := runtimePath("last-singbox.log")
	logFile, _ := os.Create(logPath)
	go func() {
		if stderrPipe == nil {
			return
		}
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			// Gate on raw bytes before allocating — and cap the crash-tail
			// slice. Without the cap, stderrLines grew unbounded under a
			// verbose TUN flood (the source of the ~1 GB RAM spike).
			if !logs.gate(levelErrWarnBytes(scanner.Bytes())) {
				continue
			}
			line := scanner.Text()
			stderrMu.Lock()
			stderrLines = append(stderrLines, line)
			if len(stderrLines) > 60 {
				stderrLines = append(stderrLines[:0], stderrLines[len(stderrLines)-50:]...)
			}
			stderrMu.Unlock()
			if logFile != nil {
				fmt.Fprintln(logFile, line)
			}
			logs.add("singbox", parseLevel("singbox", line), line)
		}
		if logFile != nil {
			logFile.Close()
		}
	}()

	select {
	case <-exitCh:
		cancel()
		os.Remove(tmpPath)
		fwdCancel()
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
	cm.counter = counter
	cm.fwdCancel = fwdCancel
	connName := n.Name
	if isChain {
		connName = strings.Join(chainLabels, " → ")
	}
	cm.state = ConnState{
		Status: ConnConnected, Mode: ModeTUN, ConnTab: connTab, ConnRaw: entry.Raw,
		EntryIndex: entry.Index, EntryName: connName,
		TUNIface: tunIfaceName, StartedAt: time.Now(),
		Chain: chainLabels, ChainRaws: chainRaws,
	}
	cm.mu.Unlock()
	// TUN's kill-switch is sing-box's own strict_route (see buildHybridTUNConfig).
	// last-connected badge → exit hop for a chain, else the single node.
	if isChain {
		recordLastConnected(chainRaws[len(chainRaws)-1], ModeTUN)
	} else {
		recordLastConnected(entry.Raw, ModeTUN)
	}
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	startUptimeTicker(cm)
	startStatsTicker(cm)
}

// startTUNConnectionSingbox runs the pure-sing-box TUN path for the
// UDP-family protocols (Hysteria2/TUIC). A single sing-box process owns the
// TUN device AND holds the protocol outbound — there is no xray child and
// no local SOCKS hop, so the byte counter cannot be inserted and traffic
// stats are unavailable for the session (the UI surfaces this). cm.xrayCmd
// stays nil; stopConnectionLocked already no-ops on a nil xrayCmd.
func startTUNConnectionSingbox(cm *connManager, entry *ConfigEntry, n *Node, connTab, tunIfaceName string) {
	cfg := buildSingboxTUNConfig(n, tunIfaceName)
	if cfg == nil {
		setConnError(cm, entry, fmt.Sprintf("sing-box: unsupported protocol %s", n.Kind))
		return
	}
	tmpPath, err := writeTempJSON(cfg, "singbox-tun")
	if err != nil {
		setConnError(cm, entry, "singbox config write: "+err.Error())
		return
	}
	fmt.Printf("ℹ  pure sing-box TUN config: %s\n", tmpPath)
	if debugPath := runtimePath("last-singbox-tun.json"); true {
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
		setConnError(cm, entry, "sing-box start: "+err.Error())
		return
	}

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	var stderrLines []string
	var stderrMu sync.Mutex
	logPath := runtimePath("last-singbox.log")
	logFile, _ := os.Create(logPath)
	go func() {
		if stderrPipe == nil {
			return
		}
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			// Gate on raw bytes before allocating — and cap the crash-tail
			// slice. Without the cap, stderrLines grew unbounded under a
			// verbose TUN flood (the source of the ~1 GB RAM spike).
			if !logs.gate(levelErrWarnBytes(scanner.Bytes())) {
				continue
			}
			line := scanner.Text()
			stderrMu.Lock()
			stderrLines = append(stderrLines, line)
			if len(stderrLines) > 60 {
				stderrLines = append(stderrLines[:0], stderrLines[len(stderrLines)-50:]...)
			}
			stderrMu.Unlock()
			if logFile != nil {
				fmt.Fprintln(logFile, line)
			}
			logs.add("singbox", parseLevel("singbox", line), line)
		}
		if logFile != nil {
			logFile.Close()
		}
	}()

	select {
	case <-exitCh:
		cancel()
		os.Remove(tmpPath)
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
	// No xray child, no byte counter in the pure-sing-box TUN path.
	cm.xrayCmd = nil
	cm.xrayCancel = nil
	cm.xrayTmpCfg = ""
	cm.counter = nil
	cm.fwdCancel = nil
	cm.state = ConnState{
		Status: ConnConnected, Mode: ModeTUN, ConnTab: connTab, ConnRaw: entry.Raw,
		EntryIndex: entry.Index, EntryName: n.Name,
		TUNIface: tunIfaceName, StartedAt: time.Now(),
		StatsUnavailable: true,
	}
	cm.mu.Unlock()
	// TUN's kill-switch is sing-box's own strict_route (see buildHybridTUNConfig).
	recordLastConnected(entry.Raw, ModeTUN)
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	startUptimeTicker(cm)
	// startStatsTicker self-terminates immediately on a nil counter — no
	// traffic stats for this path, by design (see buildSingboxTUNConfig).
}

func stopConnection() {
	// Hold actionMu so a teardown can't interleave with a connect/failover
	// sequence (which would double-kill / clobber cm fields). See connManager.
	state.conn.actionMu.Lock()
	defer state.conn.actionMu.Unlock()
	stopConnectionLocked(state.conn, false)
	state.broadcast(SSEEvent{Type: "conn_update", Payload: state.conn.snap()})
	// A verbose connection can churn a lot of transient heap (log lines, JSON
	// batches). Go won't hand that back to the OS for ~5 min on its own, which
	// is why RSS stayed elevated after disconnect. Force it back now.
	debug.FreeOSMemory()
}

// removeTUNAdapter removes the WinTUN/TUN adapter by name so it doesn't linger
// in the system and doesn't cause "file already exists" on reconnect.
func removeTUNAdapter() {
	runHidden("powershell", "-NonInteractive", "-Command",
		`Get-NetAdapter -Name 'vair-*','xc-tun-*' -ErrorAction SilentlyContinue | Remove-NetAdapter -Confirm:$false`,
	).Run() //nolint:errcheck
}

// stopConnectionLocked tears down the live connection. keepProxy=true is passed
// by the proxy-connect path: it means "we're about to reconnect in proxy mode,
// so leave the Windows system-proxy setting in place". Combined with the
// setSystemProxy dedup, a same-port proxy→proxy switch then does ZERO WinINET
// work (no unset+reset churn). It also avoids the old brief window where the
// proxy was disabled mid-switch — during which apps would leak direct. Every
// other caller (real disconnect, switch to TUN) passes false so the proxy is
// properly removed.
func stopConnectionLocked(cm *connManager, keepProxy bool) {
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
	fwdCancel := cm.fwdCancel
	counter := cm.counter

	// Clear references immediately so a concurrent call sees them as nil
	cm.cmd = nil
	cm.cancel = nil
	cm.tmpCfg = ""
	cm.xrayCmd = nil
	cm.xrayCancel = nil
	cm.xrayTmpCfg = ""
	cm.fwdCancel = nil
	cm.counter = nil
	cm.mu.Unlock()

	// Stop accepting new proxied connections immediately. In-flight relays
	// drain naturally (their source/dest closes propagate); we don't wait
	// because the xray kill below will close the destinations for them.
	if fwdCancel != nil {
		fwdCancel()
	}

	// Roll any unpersisted bytes from this session into the lifetime
	// total before xray dies. The counter is in-process so its values
	// are available regardless of what xray does. The 30-second ticker
	// has already persisted most of it; this picks up the tail.
	//
	// We skip the fold-in when the user has stats disabled — the toggle
	// semantics are "don't grow the lifetime counter while I'm not
	// watching", and folding in here would silently bump it on every
	// disconnect even with stats off. The session bytes themselves stay
	// in the in-memory counter (which is about to be discarded), so the
	// next session starts from a clean slate either way.
	if counter != nil && statsEnabled() {
		persistCounterDelta(counter)
		state.broadcast(SSEEvent{Type: "stats_update", Payload: statsSnapshot(nil)})
	}

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
		// Keep the system proxy set when we're switching proxy→proxy (the new
		// connect re-binds the same public port and setSystemProxy dedups to a
		// no-op). Only actually clear it on a real disconnect / switch to TUN.
		if !keepProxy {
			unsetSystemProxy()
		}
		// Still wait for the public port to free so the new forwarder can bind it
		// (exits early via portFree — usually well under the 400ms cap).
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
	cm.state = ConnState{Status: ConnError, EntryIndex: entry.Index, EntryName: name, ErrMsg: msg, ConnTab: connTab, ConnRaw: entry.Raw}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	vlog("error", "connect %s: %s", name, msg)
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

// statsSnapshot returns a JSON-friendly view of current traffic
// counters. session_* fields are zero when the counter is nil (idle).
// total_* always reflects the value the lifetime total would take if
// we persisted right now — i.e. the value on disk plus whatever the
// live counter has accumulated since the last 30-second checkpoint.
// Without this fold-in, the displayed total would only tick once
// every 30 s (and on disconnect), confusing users who watch it.
func statsSnapshot(counter *trafficCounter) map[string]interface{} {
	settingsMu.RLock()
	totalUp := appSettings.StatsTotalUp
	totalDown := appSettings.StatsTotalDown
	enabled := !appSettings.StatsDisabled
	settingsMu.RUnlock()
	var sessUp, sessDown int64
	if counter != nil {
		sessUp = counter.Up.Load()
		sessDown = counter.Down.Load()
		// Fold in the not-yet-persisted portion of this session so the
		// displayed total grows in lockstep with session bytes. The
		// next persist run will move these bytes into appSettings and
		// bump LastPersisted — at which point the same display value
		// comes from the disk side and the delta drops to zero, so the
		// total never jumps.
		totalUp += sessUp - counter.LastPersistedUp.Load()
		totalDown += sessDown - counter.LastPersistedDown.Load()
	}
	return map[string]interface{}{
		"session_up":   sessUp,
		"session_down": sessDown,
		"total_up":     totalUp,
		"total_down":   totalDown,
		"enabled":      enabled,
	}
}

// startStatsTicker broadcasts the current traffic counters once per
// second while the session is alive. It also folds session deltas into
// the lifetime total every 30 seconds so a crash never loses more than
// half a minute of data. Final fold-in happens in stopConnectionLocked.
func startStatsTicker(cm *connManager) {
	go func() {
		t := time.NewTicker(1 * time.Second)
		defer t.Stop()
		var ticksSincePersist int
		for range t.C {
			cm.mu.Lock()
			ok := cm.state.Status == ConnConnected
			counter := cm.counter
			cm.mu.Unlock()
			if !ok || counter == nil {
				return
			}
			if !statsEnabled() {
				continue
			}
			// Lossy — stats_update fires every second (or faster) and the
			// next pulse fully supersedes this one's byte tallies. Worst
			// case under buffer pressure the on-screen counter pauses for
			// one tick; never matters because the next tick replaces it.
			state.broadcast(SSEEvent{Type: "stats_update", Payload: statsSnapshot(counter), Lossy: true})
			ticksSincePersist++
			if ticksSincePersist >= 30 {
				ticksSincePersist = 0
				persistCounterDelta(counter)
			}
		}
	}()
}

// persistCounterDelta folds the unpersisted portion of the session
// counter into the lifetime total and updates the LastPersisted markers
// so the same bytes aren't counted again. Safe to call from anywhere;
// no-op when there's nothing new.
func persistCounterDelta(counter *trafficCounter) {
	curUp := counter.Up.Load()
	curDown := counter.Down.Load()
	deltaUp := curUp - counter.LastPersistedUp.Load()
	deltaDown := curDown - counter.LastPersistedDown.Load()
	if deltaUp <= 0 && deltaDown <= 0 {
		return
	}
	settingsMu.Lock()
	if deltaUp > 0 {
		appSettings.StatsTotalUp += deltaUp
	}
	if deltaDown > 0 {
		appSettings.StatsTotalDown += deltaDown
	}
	settingsMu.Unlock()
	counter.LastPersistedUp.Store(curUp)
	counter.LastPersistedDown.Store(curDown)
	saveSettings()
}
