package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─────────────────────────── xray lifecycle (testing) ────────────

func withXray(n *Node, ttl time.Duration, fn func(httpPort int, tr *http.Transport) error) error {
	// Through the accessor, not off the struct: a probe runs on its own
	// goroutine, ten of them at a time, and is the one reader of these paths
	// that does NOT hold state.mu — every other reader is inside a connect
	// path, which is called with the lock already held.
	xrayBin, _ := engineBins()
	httpPort, err := findFreePort()
	if err != nil {
		return fmt.Errorf("no free port")
	}
	cfg := buildXrayConfigForNode(n, httpPort, -1, "", "", "", "")
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
	cmd := exec.CommandContext(ctx, xrayBin, "run", "-c", tmpPath)
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
	prepareProc(cmd)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("xray start: %w", err)
	}
	tieToVair(cmd)
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
		return fmt.Errorf("%s", engineErrorText("xray", errMsg))
	case ready := <-portResult:
		if !ready {
			return fmt.Errorf("xray: port not ready after %s", xrayStartupTimeout)
		}
	}
	return fn(httpPort, makeSharedTransport(httpPort, "", ""))
}

// withSingbox is the sing-box mirror of withXray: it spins up a throwaway
// sing-box process exposing a local HTTP proxy on a free port, waits for the
// port to open, then hands a ready *http.Transport to fn for the duration of
// ttl. Used by the ping/speed runners for UDP-family nodes (Hysteria2/TUIC)
// that xray can't dial. Same lifecycle contract as withXray — process is
// killed and PID untracked on return regardless of outcome.
func withSingbox(n *Node, ttl time.Duration, fn func(httpPort int, tr *http.Transport) error) error {
	// Same as withXray: a probe is the reader that holds no lock.
	_, singboxBin := engineBins()
	if singboxBin == "" {
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
	cmd := exec.CommandContext(ctx, singboxBin, "run", "-c", tmpPath)
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
	prepareProc(cmd)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("sing-box start: %w", err)
	}
	tieToVair(cmd)
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
	return fn(httpPort, makeSharedTransport(httpPort, "", ""))
}

// withEngine dispatches a throwaway-proxy probe to the right backend based on
// the node's protocol: xray for the TCP-family, sing-box for the UDP-family.
// Ping/speed runners call this instead of withXray directly so a mixed list
// (VLESS + Hysteria2 + …) just works without per-call-site branching.
func withEngine(n *Node, ttl time.Duration, fn func(httpPort int, tr *http.Transport) error) error {
	// The server's name is asked the way a connection asks it — the bootstrap
	// server, over DNS over HTTPS (lookupServerIP) — and not left to the engine,
	// which hands it to the system's DNS client: with no connection, or in
	// proxy mode, that is the provider's resolver, in the clear, once for every
	// server a PING ALL or an AUTO sweep touches. The test itself still goes
	// straight to the server; that is what it measures.
	if err := preResolveHost(n); err != nil {
		return err
	}
	if engineForNode(n) == "singbox" {
		return withSingbox(n, ttl, fn)
	}
	return withXray(n, ttl, fn)
}

// The system-proxy implementation moved to core/sysproxy.go and its
// per-platform backends: Windows drives one WinINET registry switch that
// nearly everything honours, Linux has no such switch and needs a backend
// per desktop environment.

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
	stopConnectionLocked(cm, true, false) // proxy→proxy: keep the system proxy set

	// A proxy session has no adapter, but it does have an engine, and the sweep
	// that runs at a start kills engines by path — including the one this is
	// about to spawn. The barrier is the same one the TUN paths take.
	awaitTUNReap()

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
// one engine can carry them all (chainEngine), then builds a single
// multi-outbound xray config (buildXrayChainConfig) and connects. The
// representative entry for ConnState is the entry hop (entries[0]); the
// conn-bar shows the full "A → B" chain.
func startChainOnTab(entries []*ConfigEntry, connTab string, mode ConnMode) {
	cm := state.conn
	stopConnectionLocked(cm, mode != ModeTUN, false) // keep proxy only for a proxy chain

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
		noteCertCheckOnXray(n)
		e.mu.Lock()
		e.Protocol = string(n.Kind)
		e.mu.Unlock()
	}

	// Which engine carries what. One is the common case; two when the chain
	// mixes hops that only xray gets through (REALITY) with hops only sing-box
	// can dial at all (WireGuard, Hysteria2, TUIC).
	segs, reason := splitChain(nodes)
	if reason != "" {
		setConnError(cm, entries[0], "chain: "+reason, connTab)
		return
	}
	engine := segs[0].Engine
	if len(segs) == 1 && engine == "singbox" {
		// A single sing-box run still prefers xray when xray could carry the lot
		// — that is where every chain ran before this.
		if eng, why := chainEngine(nodes); why == "" {
			engine = eng
		}
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
		awaitTUNReap()
		tunIface := tunIfaceName()
		setLiveTUNIface(tunIface) // the ghost sweep skips this one
		if len(segs) == 2 {
			setConnError(cm, entries[0], "chain: a chain that needs both engines runs in proxy mode for now — TUN is not wired for it yet", connTab)
			return
		}
		if engine == "singbox" {
			startTUNChainSingbox(cm, entries[0], nodes, chainName, connTab, tunIface, labels, raws)
			return
		}
		startTUNConnectionHybrid(cm, entries[0], nodes[0], connTab, tunIface, nodes, labels, raws)
		return
	}

	cm.mu.Lock()
	cm.state = ConnState{Status: ConnConnecting, Mode: ModeProxy, EntryIndex: entries[0].Index, EntryName: chainName, ConnTab: connTab, ConnRaw: entries[0].Raw, Chain: labels, ChainRaws: raws}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})

	if len(segs) == 2 {
		startSplitChainProxy(cm, entries[0], segs, chainName, connTab, labels, raws)
		return
	}
	if engine == "singbox" {
		runSingboxProxyConfig(cm, entries[0], chainName, connTab, labels, raws,
			func(intHTTPPort, intSOCKSPort int) map[string]interface{} {
				return buildSingboxChainProxyConfig(nodes, intHTTPPort, intSOCKSPort)
			})
		return
	}
	runXrayProxyConfig(cm, entries[0], chainName, connTab, labels, raws, func(intHTTPPort, intSOCKSPort int) map[string]interface{} {
		// Proxy mode = user-facing HTTP+SOCKS → use the configured auth credentials
		// (empty when the respective setting is off).
		su, sp := proxySocksCreds()
		hu, hp := proxyHttpCreds()
		return buildXrayChainConfig(nodes, intHTTPPort, intSOCKSPort, su, sp, hu, hp)
	})
}

// startProxyConnectionXray runs the xray-backed proxy path for a single node:
// build the config and hand off to runXrayProxyConfig for the shared spawn/
// wait/finalize sequence (also used by the chain path).
func startProxyConnectionXray(cm *connManager, entry *ConfigEntry, n *Node, connTab string) {
	noteCertCheckOnXray(n)
	runXrayProxyConfig(cm, entry, n.Name, connTab, nil, nil, func(intHTTPPort, intSOCKSPort int) map[string]interface{} {
		// Proxy mode = user-facing HTTP+SOCKS → use the configured auth credentials
		// (empty when the respective setting is off).
		su, sp := proxySocksCreds()
		hu, hp := proxyHttpCreds()
		return buildXrayConfigForNode(n, intHTTPPort, intSOCKSPort, su, sp, hu, hp)
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
// injectSourceFetchInbound adds the dedicated source-fetch HTTP inbound to an
// XRAY proxy config and pins its traffic to the proxy outbound with a rule
// prepended ahead of the routing-mode rules (first match wins in xray).
func injectSourceFetchInbound(cfg map[string]interface{}, port int) {
	// With a login of its own, made for this session (see srcfetch.go).
	user, pass := newSrcFetchCreds()
	inb, _ := cfg["inbounds"].([]interface{})
	cfg["inbounds"] = append(inb, map[string]interface{}{
		"tag": "src-fetch", "listen": "127.0.0.1", "port": port,
		"protocol": "http",
		"settings": map[string]interface{}{
			"accounts": []interface{}{map[string]interface{}{"user": user, "pass": pass}},
		},
	})
	if routing, ok := cfg["routing"].(map[string]interface{}); ok {
		if rules, ok := routing["rules"].([]interface{}); ok {
			routing["rules"] = append([]interface{}{
				map[string]interface{}{"type": "field", "inboundTag": []string{"src-fetch"}, "outboundTag": "proxy"},
			}, rules...)
		}
	}
}

// injectSourceFetchInboundSingbox is the sing-box flavour of the same pin
// (proxy-mode Hysteria2/TUIC sessions).
func injectSourceFetchInboundSingbox(cfg map[string]interface{}, port int) {
	user, pass := newSrcFetchCreds()
	inb, _ := cfg["inbounds"].([]interface{})
	cfg["inbounds"] = append(inb, map[string]interface{}{
		"type": "http", "tag": "src-fetch",
		"listen": "127.0.0.1", "listen_port": port,
		"users": singboxInboundUsers(user, pass),
	})
	if route, ok := cfg["route"].(map[string]interface{}); ok {
		if rules, ok := route["rules"].([]interface{}); ok {
			route["rules"] = append([]interface{}{
				map[string]interface{}{"inbound": []string{"src-fetch"}, "outbound": "proxy"},
			}, rules...)
		}
	}
}

func runXrayProxyConfig(cm *connManager, entry *ConfigEntry, name, connTab string, chain, chainRaws []string, build func(intHTTPPort, intSOCKSPort int) map[string]interface{}) {
	httpPort, socksPort, perr := claimProxyPorts()
	if perr != nil {
		setConnError(cm, entry, perr.Error())
		return
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
	// Dedicated source-fetch inbound: subscription refreshes connect here and are
	// PINNED to the proxy outbound ahead of the mode rules, so they always ride
	// the tunnel. Through the regular inbound they'd be routed by destination —
	// and both RU modes can classify a subscription host as "direct", which on a
	// censored network means the refresh dies the moment the VPN is the only way
	// out (the exact case the user connects for). Best-effort: on a port error
	// the fetch falls back to the regular proxy port.
	intSrcPort := 0
	if pf, e3 := findFreePort(); e3 == nil {
		intSrcPort = pf
		injectSourceFetchInbound(xrayCfg, pf)
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
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+ruleSetRoot())
	prepareProc(cmd)

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
	tieToVair(cmd)

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
		setConnError(cm, entry, engineErrorText("xray", errMsg))
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
	finalizeProxyConnection(cm, entry, name, connTab, cmd, cancel, tmpPath, exitCh,
		httpPort, socksPort, intHTTPPort, intSOCKSPort, intSrcPort, chain, chainRaws)
}

// finalizeProxyConnection performs the post-spawn steps shared by every
// proxy backend (xray or sing-box): stand up the byte-counting forwarders
// in front of the engine's internal HTTP/SOCKS ports, point the system
// proxy at httpPort, record the connected ConnState, and start the uptime
// /stats tickers. On forwarder failure it tears the just-spawned engine
// down (mainCancel + tmp cleanup) and reports the error. The engine
// process must already be listening on intHTTPPort/intSOCKSPort.
func finalizeProxyConnection(cm *connManager, entry *ConfigEntry, name, connTab string,
	mainCmd *exec.Cmd, mainCancel context.CancelFunc, tmpPath string, exitCh <-chan error,
	httpPort, socksPort, intHTTPPort, intSOCKSPort, intSrcPort int, chain, chainRaws []string) {

	counter := &trafficCounter{}
	// What this session is being built with, for the panel that answers "what is
	// actually running" — the settings dialog only ever answers "what will run
	// next time". Taken before anything can change underneath it.
	facts := captureSessionFacts(engineName(mainCmd))
	fwdCtx, fwdCancel := context.WithCancel(context.Background())
	// 127.0.0.1, 0.0.0.0 when "allow LAN" is on, or a custom address together
	// with 127.0.0.1 (see proxyListenHosts).
	for _, host := range proxyListenHosts() {
		if _, err := startCountingForwarder(fwdCtx, host, httpPort, intHTTPPort, counter, "proxy-http"); err != nil {
			fwdCancel()
			mainCancel()
			os.Remove(tmpPath)
			setConnError(cm, entry, "proxy http counter: "+err.Error())
			return
		}
		if _, err := startCountingForwarder(fwdCtx, host, socksPort, intSOCKSPort, counter, "proxy-socks"); err != nil {
			fwdCancel()
			mainCancel()
			os.Remove(tmpPath)
			setConnError(cm, entry, "proxy socks counter: "+err.Error())
			return
		}
	}

	// Non-system-proxy mode: leave the Windows proxy settings alone — only apps
	// explicitly pointed at the local HTTP/SOCKS ports go through the tunnel.
	if !noSystemProxyEnabled() {
		if err := setSystemProxy(httpPort); err != nil {
			fmt.Fprintf(os.Stderr, "⚠  setSystemProxy: %v\n", err)
		}
	}

	cm.mu.Lock()
	cm.cmd = mainCmd
	cm.cancel = mainCancel
	cm.tmpCfg = tmpPath
	cm.counter = counter
	cm.fwdCancel = fwdCancel
	cm.startedWith = facts
	cm.state = ConnState{
		Status: ConnConnected, Checking: true, Mode: ModeProxy, ConnTab: connTab, ConnRaw: entry.Raw,
		EntryIndex: entry.Index, EntryName: name,
		HTTPPort: httpPort, SOCKSPort: socksPort, SrcFetchPort: intSrcPort,
		StartedAt: time.Now(),
		Chain:     chain,
		ChainRaws: chainRaws,
	}
	cm.mu.Unlock()
	// A proxy session is not a tunnel: a block the last TUN session left for
	// its successor has no successor, and holding everyone but the proxy's
	// users offline is the proxy kill-switch — which has a setting of its own.
	// When that switch is on its block is the session's own too, and the
	// engage below replaces the one standing in a single step: releasing it
	// here first left a moment with neither.
	if !proxyKillSwitchEnabled() || persistentKillSwitchEnabled() || !proxyBlockIsSessions {
		releaseTunnelBlock()
	}
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	// Before the question, not after — see the TUN path. A failed check tears the
	// session down and takes the system proxy with it; without the block already
	// on, the machine would be handed straight back to its ordinary connection.
	engageProxyKillSwitch()
	dropBlockNobodyWants(ModeProxy)
	// Same question TUN asks before it believes itself: does anything actually
	// go through? A listener that answers the browser and nothing else is the
	// proxy-mode shape of "connected with no traffic".
	if !verifyProxyCarriesTraffic(cm, mainCmd, entry, connTab) {
		return
	}
	// "last connected" badge: for a single node it's that node; for a chain it's
	// the EXIT hop (the egress config — what the user thinks of as "where I came
	// out"), i.e. the last raw in chainRaws. After the check, as the TUN paths
	// do: recorded before it, a session that never carried anything became the
	// one a restore on the next start would bring back.
	if len(chainRaws) > 0 {
		recordLastConnected(chainRaws[len(chainRaws)-1], chainRaws, ModeProxy)
	} else {
		recordLastConnected(entry.Raw, nil, ModeProxy)
	}
	// No interface to watch in proxy mode; the engine dying is the whole of it,
	// and it left "connected" on screen with a listener nobody was serving.
	watchSession(cm, mainCmd, exitCh, "", ModeProxy)
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
	runSingboxProxyConfig(cm, entry, n.Name, connTab, nil, nil,
		func(intHTTPPort, intSOCKSPort int) map[string]interface{} {
			return buildSingboxProxyConfig(n, intHTTPPort, intSOCKSPort)
		})
}

// runSingboxProxyConfig is the shared sing-box proxy spawn path: allocate the
// user-visible and internal ports, build the config with build(), spawn, wait
// for the port (or an early crash), then finalize. Used by the single-node
// connect and by a chain whose hops sing-box carries.
func runSingboxProxyConfig(cm *connManager, entry *ConfigEntry, name, connTab string,
	chain, chainRaws []string, build func(intHTTPPort, intSOCKSPort int) map[string]interface{}) {
	httpPort, socksPort, perr := claimProxyPorts()
	if perr != nil {
		setConnError(cm, entry, perr.Error())
		return
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

	cfg := build(intHTTPPort, intSOCKSPort)
	if cfg == nil {
		setConnError(cm, entry, "sing-box: this configuration cannot be built for sing-box")
		return
	}
	// Pin source fetches to the tunnel (see runXrayProxyConfig for the rationale).
	intSrcPort := 0
	if pf, e3 := findFreePort(); e3 == nil {
		intSrcPort = pf
		injectSourceFetchInboundSingbox(cfg, pf)
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
	prepareProc(cmd)
	stderrPipe, _ := cmd.StderrPipe()
	var tailMu sync.Mutex
	var tail []string

	if err = cmd.Start(); err != nil {
		cancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "sing-box start: "+err.Error())
		return
	}
	tieToVair(cmd)

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

	finalizeProxyConnection(cm, entry, name, connTab, cmd, cancel, tmpPath, exitCh,
		httpPort, socksPort, intHTTPPort, intSOCKSPort, intSrcPort, chain, chainRaws)
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
	// What TUN mode needs before it can start is platform-specific, and the old
	// check — "are we root?" — is the Windows question. On Linux the app is
	// deliberately never root; what it needs is a helper it can reach.
	if reason := tunPreconditionError(); reason != "" {
		setConnError(state.conn, entry, reason)
		return
	}
	if state.singboxBin == "" {
		setConnError(state.conn, entry, "sing-box not found. Pass path as 2nd arg: vair xray.exe sing-box.exe")
		return
	}
	cm := state.conn
	stopConnectionLocked(cm, false, false) // TUN: drop any system proxy from a prior proxy session

	// AFTER the teardown, not before it. This barrier is for a sweep an earlier
	// DISCONNECT left running; the teardown above no longer starts one, because
	// it was that sweep — a PowerShell launch 300-700ms behind the connect it
	// was supposed to precede — that removed the adapter the new session had
	// just created. sing-box then ran with no interface, the window still said
	// connected, and every packet went out the physical one. Switching again
	// only repeated it; the sweep on an explicit disconnect is what cleared it,
	// which is exactly what the user saw.
	awaitTUNReap()

	// NOTE: no removeTUNAdapter() here. Adapter names are unique per session
	// (vair-<unixtime>), so a fresh connect never collides with a stale adapter,
	// and wintun takes the previous one down when its handle closes.
	//
	// Ghosts left by a crashed engine ARE swept at startup — see
	// sweepGhostTunnelAtStart, which is what gives a machine its network back
	// after Vair was killed with a tunnel up. What that sweep does not touch is
	// the 1.10 adapter name, because a copy of 1.10 running right now is the one
	// thing that could own one; and it holds the same barrier this connect takes,
	// so it can never overlap the session being started here.

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
	tunIface := tunIfaceName()
	setLiveTUNIface(tunIface) // the ghost sweep skips this one

	// Engine branch. TCP-family protocols use the hybrid path (sing-box TUN
	// front-end → xray outbound). UDP-family (Hysteria2/TUIC) run as a
	// single pure-sing-box process: cm.cmd = sing-box, cm.xrayCmd = nil.
	// stopConnectionLocked already no-ops on a nil xrayCmd, so teardown
	// needs no special-casing.
	if engineForNode(n) == "singbox" {
		startTUNConnectionSingbox(cm, entry, n, connTab, tunIface)
		return
	}
	startTUNConnectionHybrid(cm, entry, n, connTab, tunIface, nil, nil, nil)
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
func startTUNConnectionHybrid(cm *connManager, entry *ConfigEntry, n *Node, connTab, tunIface string, chainNodes []*Node, chainLabels, chainRaws []string) {

	isChain := len(chainNodes) > 1
	if !isChain {
		noteCertCheckOnXray(n) // a chain's hops are noted where it is assembled
	}
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
	// setting. The xray HTTP inbound here is unused (sing-box dials the SOCKS one)
	// takes the same per-launch login: unused is not the same as closed, and a
	// listener without one carried anything any program sent it into the VPN.
	// The user-facing LAN share is a separate sing-box inbound
	// (lanProxyInbounds), which applies the configured credentials.
	if isChain {
		xrayCfg = buildXrayChainConfig(chainNodes, xrayHTTPPort, xraySocksPort, proxyAuthUser, proxyAuthPass, proxyAuthUser, proxyAuthPass)
	} else {
		xrayCfg = buildXrayConfigForNode(n, xrayHTTPPort, xraySocksPort, proxyAuthUser, proxyAuthPass, proxyAuthUser, proxyAuthPass)
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
	xrayCmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+ruleSetRoot())
	// xray logs to stdout (sing-box uses stderr) — capture both into the panel.
	xrayStdoutPipe, _ := xrayCmd.StdoutPipe()
	xrayStderrPipe, _ := xrayCmd.StderrPipe()
	prepareProc(xrayCmd)
	if err = xrayCmd.Start(); err != nil {
		xrayCancel()
		os.Remove(xrayTmpPath)
		setConnError(cm, entry, "xray hybrid start: "+err.Error())
		return
	}
	tieToVair(xrayCmd)
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
	// See finalizeProxyConnection: the configuration this session is built with,
	// recorded before it can change.
	facts := captureSessionFacts("xray + sing-box")
	fwdCtx, fwdCancel := context.WithCancel(context.Background())
	counterSocksPort, err := startCountingForwarder(fwdCtx, "127.0.0.1", 0, xraySocksPort, counter, relayLabelTUN)
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
	cfg := buildHybridTUNConfig(tunIface, n.Host, xrayHTTPPort, counterSocksPort, blockQUIC)

	// Everything above is unprivileged and stays ours on both platforms: the
	// xray child, the byte counter, the forwarder. Only the sing-box front-end
	// needs the tunnel device, so only it goes to the helper.
	if tunRunsInHelper() {
		startHybridTUNViaHelper(cm, entry, n, connTab, tunIface, cfg, hybridParts{
			xrayCmd: xrayCmd, xrayCancel: xrayCancel, xrayTmp: xrayTmpPath,
			counter: counter, fwdCancel: fwdCancel,
			isChain: isChain, chainLabels: chainLabels, chainRaws: chainRaws,
		})
		return
	}

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
	prepareProc(cmd)

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
	tieToVair(cmd)

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
			// The connections panel and the rule counters read these lines, and
			// in TUN this pipe is their only source: the traffic itself never
			// passes through us. Before the gate, for the reason given there.
			offerToCounters("singbox", scanner.Bytes())
			// Gate on raw bytes before allocating — and cap the crash-tail
			// slice. Without the cap, stderrLines grew unbounded under a
			// verbose TUN flood (the source of the ~1 GB RAM spike).
			if !engineLineWanted("singbox", scanner.Bytes()) {
				continue
			}
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
	cm.startedWith = facts
	connName := n.Name
	if isChain {
		connName = strings.Join(chainLabels, " → ")
	}
	cm.state = ConnState{
		Status: ConnConnected, Checking: true, Mode: ModeTUN, ConnTab: connTab, ConnRaw: entry.Raw,
		EntryIndex: entry.Index, EntryName: connName,
		TUNIface: tunIface, StartedAt: time.Now(),
		Chain: chainLabels, ChainRaws: chainRaws,
	}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	// The block goes on BEFORE the tunnel is put to the question, not after.
	// The check can end the session, and a session ended without the block is a
	// machine back on its ordinary connection with its own address — which is
	// the one thing this setting exists to prevent. Engaging first means a
	// tunnel that turns out to carry nothing leaves the traffic stopped, and the
	// error says so.
	//
	// Two blocks: the tunnel's, which stops everything outside the tunnel for
	// as long as it is up (strict_route alone stops only DNS — see
	// tunnelblock_windows.go), and the persistent one, which outlives it.
	// The engine's inbound door, for the stacks that terminate connections in
	// the operating system. Here rather than at start-up: the rule names the
	// adapter, and the adapter does not exist until the engine has made it.
	if engineNeedsFirewallRule() {
		ensureEngineFirewallRule(tunIface)
	}
	engageTunnelBlock(tunIface)
	engagePersistentKillSwitch(tunIface)
	dropBlockNobodyWants(ModeTUN)
	if !verifyTUNCarriesTraffic(cm, cmd, entry, connTab) {
		return
	}
	watchSession(cm, cmd, exitCh, tunIface, ModeTUN)
	// last-connected badge → exit hop for a chain, else the single node.
	if isChain {
		recordLastConnected(chainRaws[len(chainRaws)-1], chainRaws, ModeTUN)
	} else {
		recordLastConnected(entry.Raw, nil, ModeTUN)
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
func startTUNConnectionSingbox(cm *connManager, entry *ConfigEntry, n *Node, connTab, tunIface string) {
	cfg := buildSingboxTUNConfig(n, tunIface)
	if cfg == nil {
		setConnError(cm, entry, fmt.Sprintf("sing-box: unsupported protocol %s", n.Kind))
		return
	}
	startTUNSingboxCfg(cm, entry, cfg, n.Name, connTab, tunIface, nil, nil)
}

// startTUNChainSingbox is the same for a chain sing-box carries — a WireGuard
// exit behind a proxy hop being the case that needs it.
func startTUNChainSingbox(cm *connManager, entry *ConfigEntry, nodes []*Node, name, connTab, tunIface string, chain, chainRaws []string) {
	cfg := buildSingboxChainTUNConfig(nodes, tunIface)
	if cfg == nil {
		setConnError(cm, entry, "sing-box: this chain cannot be built")
		return
	}
	startTUNSingboxCfg(cm, entry, cfg, name, connTab, tunIface, chain, chainRaws)
}

// startTUNSingboxCfg spawns (or hands to the helper) a ready sing-box TUN
// config. name is what the connection bar shows; chain/chainRaws are set for a
// multi-hop connection and nil otherwise.
func startTUNSingboxCfg(cm *connManager, entry *ConfigEntry, cfg map[string]interface{}, name, connTab, tunIface string, chain, chainRaws []string) {
	if tunRunsInHelper() {
		startTUNViaHelper(cm, entry, name, connTab, tunIface, cfg, chain, chainRaws)
		return
	}

	facts := captureSessionFacts("sing-box")
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
	prepareProc(cmd)

	if err = cmd.Start(); err != nil {
		cancel()
		os.Remove(tmpPath)
		setConnError(cm, entry, "sing-box start: "+err.Error())
		return
	}
	tieToVair(cmd)

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
			// The connections panel and the rule counters read these lines, and
			// in TUN this pipe is their only source: the traffic itself never
			// passes through us. Before the gate, for the reason given there.
			offerToCounters("singbox", scanner.Bytes())
			// Gate on raw bytes before allocating — and cap the crash-tail
			// slice. Without the cap, stderrLines grew unbounded under a
			// verbose TUN flood (the source of the ~1 GB RAM spike).
			if !engineLineWanted("singbox", scanner.Bytes()) {
				continue
			}
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
	cm.startedWith = facts
	cm.state = ConnState{
		Status: ConnConnected, Checking: true, Mode: ModeTUN, ConnTab: connTab, ConnRaw: entry.Raw,
		EntryIndex: entry.Index, EntryName: name,
		TUNIface: tunIface, StartedAt: time.Now(),
		StatsUnavailable: true,
		Chain:            chain, ChainRaws: chainRaws,
	}
	cm.mu.Unlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	// The block goes on BEFORE the tunnel is put to the question, not after.
	// The check can end the session, and a session ended without the block is a
	// machine back on its ordinary connection with its own address — which is
	// the one thing this setting exists to prevent. Engaging first means a
	// tunnel that turns out to carry nothing leaves the traffic stopped, and the
	// error says so.
	//
	// Two blocks: the tunnel's, which stops everything outside the tunnel for
	// as long as it is up (strict_route alone stops only DNS — see
	// tunnelblock_windows.go), and the persistent one, which outlives it.
	// The engine's inbound door, for the stacks that terminate connections in
	// the operating system. Here rather than at start-up: the rule names the
	// adapter, and the adapter does not exist until the engine has made it.
	if engineNeedsFirewallRule() {
		ensureEngineFirewallRule(tunIface)
	}
	engageTunnelBlock(tunIface)
	engagePersistentKillSwitch(tunIface)
	dropBlockNobodyWants(ModeTUN)
	if !verifyTUNCarriesTraffic(cm, cmd, entry, connTab) {
		return
	}
	watchSession(cm, cmd, exitCh, tunIface, ModeTUN)
	recordLastConnected(entry.Raw, nil, ModeTUN)
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
	stopConnectionLocked(state.conn, false, true)
	// The user said disconnect: nothing is coming next to hand the block to.
	releaseTunnelBlock()
	state.broadcast(SSEEvent{Type: "conn_update", Payload: state.conn.snap()})
	// A verbose connection can churn a lot of transient heap (log lines, JSON
	// batches). Go won't hand that back to the OS for ~5 min on its own, which
	// is why RSS stayed elevated after disconnect. Force it back now.
	debug.FreeOSMemory()
}

// stopConnectionLocked tears down the live connection. keepProxy=true is passed
// by the proxy-connect path: it means "we're about to reconnect in proxy mode,
// so leave the Windows system-proxy setting in place". Combined with the
// setSystemProxy dedup, a same-port proxy→proxy switch then does ZERO WinINET
// work (no unset+reset churn). It also avoids the old brief window where the
// proxy was disabled mid-switch — during which apps would leak direct. Every
// other caller (real disconnect, switch to TUN) passes false so the proxy is
// properly removed.
// stopConnectionLocked tears the current session down. sweepGhosts asks it to
// also collect leftover TUN adapters from previous crashes; a teardown that is
// only making room for the next connect passes false, because wintun removes
// this session's adapter when the handle closes and the sweep would otherwise
// be a PowerShell launch racing the connect right behind it.
func stopConnectionLocked(cm *connManager, keepProxy, sweepGhosts bool) {
	cm.mu.Lock()
	if cm.state.Status == ConnIdle {
		cm.mu.Unlock()
		return
	}
	// Every teardown passes through here, which makes it the one place that can
	// close the session log's row. What ended it came from the caller (see
	// noteSessionEndReason); everything else is on the state about to be cleared.
	noteSessionEnded(cm.state, cm.counter)
	wasTUN := cm.state.Mode == ModeTUN
	wasProxy := cm.state.Mode == ModeProxy
	prevHTTPPort := cm.state.HTTPPort
	cm.state.Status = ConnDisconnecting

	// Extract process references under lock, then release lock for killing
	mainCmd := cm.cmd
	mainCancel := cm.cancel
	mainTmpCfg := cm.tmpCfg
	tunRemote := cm.tunRemote
	xrayCmd := cm.xrayCmd
	xrayCancel := cm.xrayCancel
	xrayTmpCfg := cm.xrayTmpCfg
	fwdCancel := cm.fwdCancel
	counter := cm.counter

	// Clear references immediately so a concurrent call sees them as nil
	cm.cmd = nil
	cm.cancel = nil
	cm.tmpCfg = ""
	cm.tunRemote = nil
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

	// A helper-run tunnel first: it is a request over a socket, not a signal,
	// and the helper does the teardown we cannot (the interface, the routes,
	// the nftables rules). Closing the connection alone would also work — the
	// helper treats a dropped owner as a teardown — but asking lets it report
	// a problem instead of leaving us to guess.
	if tunRemote != nil {
		// With its block kept for the next tunnel, as the Windows block is
		// (handOverTunnelBlock below): most teardowns are a switch. A
		// disconnect takes it down right after — stopConnection.
		stop := tunRemote.Stop
		if h, ok := tunRemote.(interface{ StopForHandover() error }); ok {
			stop = h.StopForHandover
		}
		if err := stop(); err != nil {
			vlog("warn", "helper stop: %v", err)
		}
		tunRemote.Close()
	}

	// Cancel contexts first (sends signals)
	if mainCancel != nil {
		mainCancel()
	}
	if xrayCancel != nil {
		xrayCancel()
	}

	// Kill sing-box and xray in parallel. How a process tree is torn down differs
	// enough between platforms to live in core/process_{windows,linux}.go: Windows
	// needs taskkill /F /T because Go's Kill() is soft there, Linux signals the
	// process group so the xray grandchild goes with its parent.
	killProc := func(cmd *exec.Cmd, done chan<- struct{}) {
		defer close(done)
		killTree(cmd)
	}

	mainDone := make(chan struct{})
	xrayDone := make(chan struct{})
	go killProc(mainCmd, mainDone)
	go killProc(xrayCmd, xrayDone)
	<-mainDone
	<-xrayDone

	// The tunnel's block is handed to whatever comes next rather than taken
	// down: most teardowns are a switch — the user picking another server, AUTO
	// moving on, a failed candidate making room for the next — and between the
	// old engine dying and the new one up, the machine had no tunnel and no
	// block, so for those seconds everything went out with its own address. A
	// new TUN session takes the block over; nothing coming back within the
	// grace period takes it down (see handOverTunnelBlock). The user's own
	// disconnect takes it down at once — stopConnection.
	handOverTunnelBlock()

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
		// The engine is gone, and with it the log that would have ended each
		// of its connections' rows.
		endTunConns()
	}
	if wasTUN && !sessionEnding.Load() {
		// The inbound door for the engine closes with the session. It names the
		// adapter this session had, and the next one gets a rule of its own.
		// Not while Windows is ending the session: it is a PowerShell run, and a
		// new process then can hang — and the restart with it. The next start
		// removes the rule anyway (core.Init).
		removeEngineFirewallRule()
	}
	if wasTUN && sweepGhosts {
		// Off the disconnect path: this is half a second of PowerShell, and the
		// user is waiting for a button. The engine has already been killed and
		// wintun removes the adapter when its handle closes, so this is a sweep
		// for ghosts rather than the teardown itself.
		//
		// Serialised with tunReapMu, because the sweep matches vair-* and a new
		// tunnel would match it too: a connect takes the same lock before it
		// starts an engine, so the sweep can never run beside a live adapter.
		setLiveTUNIface("") // this session's adapter is a ghost now, sweep it too
		go func() {
			tunReapMu.Lock()
			defer tunReapMu.Unlock()
			removeTUNAdapter()
			tunReapDone++
		}()
	}

	// Re-acquire lock to set final state
	cm.mu.Lock()
	cm.state = ConnState{Status: ConnIdle, EntryIndex: -1}
	cm.mu.Unlock()
}

func setConnError(cm *connManager, entry *ConfigEntry, msg string, tab ...string) {
	// Engine output arrives with its terminal colours: "ESC[31mFATAL ESC[0m…"
	// is what the connection bar showed.
	msg = stripANSI(msg)
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

// connectionLost tears down a connection that is up on paper but is not
// carrying traffic, and leaves the reason where the user will see it.
//
// wantMode, when non-empty, restricts this to connections in that mode — the
// helper's "the engine died" notification is about the TUN session specifically,
// and must not tear down a proxy connection the user has since started.
//
// The teardown, rather than only a warning, is the honest end of the two
// possibilities. In TUN mode a tunnel that has stopped passing traffic has also
// taken the machine's networking with it, and removing it is what gives the user
// their connection back; in proxy mode it is a listener that answers and then
// fails every request. Either way "connected" was the wrong thing to be showing.
func connectionLost(reason string, wantMode ConnMode) {
	cm := state.conn
	cm.actionMu.Lock()
	defer cm.actionMu.Unlock()

	cm.mu.Lock()
	live := cm.state.Status == ConnConnected && (wantMode == "" || cm.state.Mode == wantMode)
	entryRaw := cm.state.ConnRaw
	connTab := cm.state.ConnTab
	cm.mu.Unlock()
	if !live {
		return // already torn down, or never up
	}

	vlog("error", "connection lost: %s", reason)
	noteSessionEndReason(reason)
	stopConnectionLocked(cm, false, true)
	if e, tab, ok := memEntryByRaw(connTab, entryRaw); ok {
		setConnError(cm, e, reason, tab)
	} else {
		state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	}
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
		// The session as statistics saw it: what crossed while they were off
		// is not part of it.
		sessUp = counter.Up.Load() - counter.SkippedUp.Load()
		sessDown = counter.Down.Load() - counter.SkippedDown.Load()
		// Fold in the not-yet-persisted portion of this session so the
		// displayed total grows in lockstep with session bytes. The
		// next persist run will move these bytes into appSettings and
		// bump LastPersisted — at which point the same display value
		// comes from the disk side and the delta drops to zero, so the
		// total never jumps.
		totalUp += counter.Up.Load() - counter.LastPersistedUp.Load()
		totalDown += counter.Down.Load() - counter.LastPersistedDown.Load()
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
		wasOn := statsEnabled()
		for range t.C {
			cm.mu.Lock()
			ok := cm.state.Status == ConnConnected
			counter := cm.counter
			cm.mu.Unlock()
			if !ok || counter == nil {
				return
			}
			if !statsTick(counter, statsEnabled(), &wasOn, &ticksSincePersist) {
				continue
			}
			// Lossy — stats_update fires every second (or faster) and the
			// next pulse fully supersedes this one's byte tallies. Worst
			// case under buffer pressure the on-screen counter pauses for
			// one tick; never matters because the next tick replaces it.
			state.broadcast(SSEEvent{Type: "stats_update", Payload: statsSnapshot(counter), Lossy: true})
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

// watchSession follows a connection for as long as it is up, and ends it when
// what it rests on is gone.
//
// Two things are watched, and neither costs traffic:
//
//   - the engine process. exitCh used to be read only during the startup
//     window, so an engine that died later — a crash, a driver fault, a
//     resume from sleep, someone killing it — left "connected" on screen with
//     the machine back on its ordinary route and the user's own address
//     showing. This is the same channel, read for the whole session.
//   - the TUN interface. sing-box does not always exit when its adapter is
//     removed underneath it, so the process being alive is not proof there is
//     a tunnel. net.InterfaceByName is a syscall against a list the kernel
//     already has; every few seconds is nothing.
//
// Both end in connectionLost, which tears the session down and puts the reason
// where the user will see it, rather than leaving a window that says connected.
//
// The session is identified by cmd: stopConnectionLocked clears cm.cmd, so a
// teardown we performed ourselves is seen here as "not ours any more" and the
// watcher retires quietly instead of reporting a loss.
// tunWatchEvery is how often the interface is looked for. A variable so a test
// does not have to wait five seconds to see the watcher act.
var tunWatchEvery = 5 * time.Second

func watchSession(cm *connManager, cmd *exec.Cmd, exitCh <-chan error, iface string, mode ConnMode) {
	if exitCh == nil && iface == "" {
		return // nothing to follow (a split chain: its halves are tracked apart)
	}
	go func() {
		tick := time.NewTicker(tunWatchEvery)
		defer tick.Stop()
		for {
			select {
			case <-exitCh:
				if !sessionStillOurs(cm, cmd) {
					return // we killed it: an ordinary disconnect or switch
				}
				connectionLost("the engine stopped", mode)
				return
			case <-tick.C:
				if !sessionStillOurs(cm, cmd) {
					return
				}
				if iface == "" {
					continue
				}
				if _, err := net.InterfaceByName(iface); err != nil {
					// Re-check that the session is still ours before acting: a
					// disconnect may have landed between the lookup and here.
					if !sessionStillOurs(cm, cmd) {
						return
					}
					connectionLost("the tunnel interface is gone", mode)
					return
				}
			}
		}
	}()
}

// sessionStillOurs reports whether the connection the watcher was started for
// is the one that is up now.
func sessionStillOurs(cm *connManager, cmd *exec.Cmd) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.state.Status == ConnConnected && cm.cmd == cmd
}

// verifyTUNCarriesTraffic checks that the tunnel just brought up actually
// passes something, and ends the session with a readable error when it does
// not.
//
// "Connected" in TUN mode used to mean "sing-box did not crash in the first few
// seconds" — nothing looked at the interface, the routes, or a single packet.
// A tunnel whose routes never installed (another VPN adapter with a better
// metric, auto_route refused, the adapter removed underneath it) reads exactly
// like a working one, and the user finds out from a website showing their own
// address.
//
// One request, through the same probe AUTO uses, with the warm-up and the one
// retry it does: about a second on a good link, and up to eight on a dead one —
// which is time spent telling the truth instead of a session that was never
// going to carry anything.
func verifyTUNCarriesTraffic(cm *connManager, cmd *exec.Cmd, entry *ConfigEntry, connTab string) bool {
	return verifyCarriesTraffic(cm, cmd, entry, connTab, ModeTUN)
}

// failedOnOurOwnBlock carries one bit from a failed attempt to the AUTO loop
// that has to decide whether the config deserves the blame for it.
//
// It is set only by failures we can see are ours, read once by the attempt that
// follows, and cleared before every attempt — so a note left by one candidate
// can never excuse the next.
var failedOnOurOwnBlock atomic.Bool

func noteFailureWasOurs()  { failedOnOurOwnBlock.Store(true) }
func clearFailureBlame()   { failedOnOurOwnBlock.Store(false) }
func failureWasOurs() bool { return failedOnOurOwnBlock.Load() }

// noTrafficMessage is what the row says when a session came up and carried
// nothing.
//
// When the kill-switch is holding, that is the more urgent half of the news:
// the machine is not back on its ordinary connection, it is stopped, and
// nothing will load until the user disconnects. Saying only "no traffic goes
// through it" would leave them looking for a network fault that is the
// program's own doing.
func noTrafficMessage(mode ConnMode, blocked bool) string {
	return noTrafficMessageFor(mode, blocked, false)
}

// noTrafficMessageFor is noTrafficMessage for a session that may be a chain. A
// chain that carries nothing has a hop that does not answer, and the check
// cannot tell which; the message says where to look instead of naming the
// mode.
func noTrafficMessageFor(mode ConnMode, blocked, chain bool) string {
	what := "the tunnel started but no traffic goes through it"
	if mode == ModeProxy {
		what = "the proxy started but no traffic goes through it"
	}
	if chain {
		what = "the chain started but no traffic goes through it — test its configs one by one to find the one that does not answer"
	}
	if blocked && blockWillLapse() {
		return what + " — traffic stays blocked for 20 s (kill-switch), then the normal connection returns"
	}
	if blocked {
		return what + " — traffic stays blocked (kill-switch); disconnect to release it"
	}
	return what
}

// verifyProxyCarriesTraffic is the same check for a proxy session.
//
// It was TUN-only, and the asymmetry showed: a server that accepts the
// connection and answers nothing — a dead node still listening, an account out
// of quota, a blocked route — left the proxy "connected", the listener up, and
// every request through it hanging. The user's evidence was a browser that
// never loaded, against a program that said everything was fine.
//
// The engine is not lying in that case and neither is the port: the only thing
// that knows is a request, which is what this makes. It costs the same second
// on a working link that TUN has been paying since 2.6.0.
func verifyProxyCarriesTraffic(cm *connManager, cmd *exec.Cmd, entry *ConfigEntry, connTab string) bool {
	return verifyCarriesTraffic(cm, cmd, entry, connTab, ModeProxy)
}

func verifyCarriesTraffic(cm *connManager, cmd *exec.Cmd, entry *ConfigEntry, connTab string, mode ConnMode) bool {
	// A new session has no verdict yet. Without this the panel went on showing
	// the last session's — "does not apply" from a proxy session over a TUN one —
	// until this session's check came in. A new run, so that a check of the
	// last session still probing cannot write over it either.
	setLeakResultOf(newLeakRun(), leakCheckResult{verdict: leakNotRun})
	// Twice, with a pause first — the same shape auto-connect uses. Routes are
	// installed a moment after the engine says it is up, and a single failed
	// request is a thin reason to throw a session away.
	for attempt := 0; attempt < 2; attempt++ {
		// TUN waits before the first try: auto_route installs the routes a moment
		// after the engine reports itself up, and probing into that gap fails a
		// tunnel that was about to work. A proxy has no routes to wait for — the
		// port is already accepting — so its first request goes straight out and
		// an ordinary connect stays as quick as it was.
		if mode == ModeTUN || attempt > 0 {
			time.Sleep(800 * time.Millisecond)
		}
		if !sessionStillOurs(cm, cmd) {
			return true // torn down or replaced while we were probing
		}
		if alive, _ := probeLiveTunnel(cm); alive {
			cm.mu.Lock()
			if cm.state.Status == ConnConnected && cm.cmd == cmd {
				cm.state.Checking = false
			}
			cm.mu.Unlock()
			state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
			if mode == ModeTUN {
				go noteIPv6Reachability(cm, cmd)
			}
			go runLeakCheckAfterConnect(cm, cmd, mode)
			// What the program put off until there was a tunnel to carry it.
			afterSessionUp()
			return true
		}
	}
	if !sessionStillOurs(cm, cmd) {
		return true
	}
	warn := "tunnel came up but carries no traffic — check for another VPN adapter or a route conflict"
	if mode == ModeProxy {
		warn = "the proxy came up but carries no traffic — the server accepts connections and answers nothing"
	}
	blocked := PersistentKillSwitchHeld()
	if blocked {
		// Do not let AUTO charge this to the server. With our own block standing,
		// a probe that gets nowhere proves nothing about the far end — the packet
		// may never have left this machine, and the allow list, not the config,
		// is the suspect. Marking the row failed there drives a good server down
		// the ranking over a fault on this side of the wire.
		noteFailureWasOurs()
	}
	// The message counts the session's own block too (trafficHeldNow); the
	// charge above stays with the persistent set, which can stop the probe.
	cm.mu.Lock()
	isChain := len(cm.state.Chain) > 1
	cm.mu.Unlock()
	msg := noTrafficMessageFor(mode, trafficHeldNow(), isChain)
	vlog("warning", "%s", warn)
	noteSessionEndReason("came up but carried no traffic")
	// The system proxy stays pointed at the local port while AUTO still has
	// candidates to try, and comes off when it does not.
	//
	// Removing it here unconditionally was a hole of its own: between one failed
	// candidate and the next, applications that honour the setting went out
	// directly with the machine's own address. Left in place, they get a refused
	// connection instead — the same fail-closed second an ordinary switch has —
	// and autoConnectFromPool clears it for good once every candidate is spent.
	keepProxy := mode == ModeProxy && autoSweepInFlight.Load()
	stopConnectionLocked(cm, keepProxy, mode == ModeTUN)
	setConnError(cm, entry, msg, connTab)
	return false
}

// noteIPv6Reachability says once, in the log, whether the server actually
// carries the IPv6 the user asked it to carry.
//
// Nothing can be known about this before connecting: an AAAA record on the
// server's own address says how we reach IT, not what it can reach. So the
// question is asked afterwards, and only when the answer would matter — the
// mode where v6 is supposed to go through. A failure is not an error: IPv4
// works, the session is fine, and only sites that exist solely over IPv6 are
// affected. But it is said out loud, because a silently half-working setting is
// the thing this whole area was fixed for.
func noteIPv6Reachability(cm *connManager, cmd *exec.Cmd) {
	if ipv6Mode() != ipv6Tunnel {
		return
	}
	if !sessionStillOurs(cm, cmd) {
		return
	}
	if probeIPv6(cm) {
		vlog("info", "IPv6 goes through this server")
		return
	}
	vlog("warning", "this server does not carry IPv6 — sites reachable only over IPv6 will not open (IPv4 is unaffected)")
}
