package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// startSplitChainProxy runs a chain that needs both engines.
//
// Two processes, joined by a local SOCKS port: the upstream half listens on it
// and carries the hops nearest the user's machine, the downstream half dials
// its first hop through it and exposes the ports the app's byte counter binds.
// Which half is which follows the chain's own order — hop 0 is what this
// machine dials first.
//
// The connection manager already has two process slots (the hybrid TUN path
// keeps sing-box in one and xray in the other), so teardown, crash handling and
// the orphan reaper need no changes: whichever engine ends up in which slot,
// both are killed together.
func startSplitChainProxy(cm *connManager, entry *ConfigEntry, segs []chainSegment, name, connTab string, chain, chainRaws []string) {
	httpPort, socksPort, perr := claimProxyPorts()
	if perr != nil {
		setConnError(cm, entry, perr.Error())
		return
	}
	intHTTPPort, e1 := findFreePort()
	intSOCKSPort, e2 := findFreePort()
	bridgePort, e3 := findFreePort()
	if e1 != nil || e2 != nil || e3 != nil {
		setConnError(cm, entry, "no free port for the chain")
		return
	}

	upCfg := buildChainSegmentUpstream(segs[0], bridgePort)
	if upCfg == nil {
		setConnError(cm, entry, "chain: could not build the first half")
		return
	}
	downCfg := buildChainSegmentDownstream(segs[1], intHTTPPort, intSOCKSPort, bridgePort)
	if downCfg == nil {
		setConnError(cm, entry, "chain: could not build the second half")
		return
	}
	// Source fetches ride the tunnel, same as every other proxy path.
	intSrcPort := 0
	if pf, e := findFreePort(); e == nil {
		intSrcPort = pf
		if segs[1].Engine == "xray" {
			injectSourceFetchInbound(downCfg, pf)
		} else {
			injectSourceFetchInboundSingbox(downCfg, pf)
		}
	}

	upCmd, upCancel, upPath, err := startChainHalf(segs[0].Engine, upCfg, "chain-up", bridgePort)
	if err != nil {
		setConnError(cm, entry, "chain (first half): "+err.Error())
		return
	}
	downCmd, downCancel, downPath, err := startChainHalf(segs[1].Engine, downCfg, "chain-down", intHTTPPort)
	if err != nil {
		upCancel()
		os.Remove(upPath)
		setConnError(cm, entry, "chain (second half): "+err.Error())
		return
	}

	// The downstream half is the one the counter talks to, so it takes the main
	// slot; the upstream half rides in the slot the hybrid path uses for its
	// second engine. finalizeProxyConnection wires the rest.
	cm.mu.Lock()
	cm.xrayCmd = upCmd
	cm.xrayCancel = upCancel
	cm.xrayTmpCfg = upPath
	cm.mu.Unlock()

	vlog("info", "chain: %s carries %d hop(s), %s carries %d — joined on 127.0.0.1:%d",
		segs[0].Engine, len(segs[0].Nodes), segs[1].Engine, len(segs[1].Nodes), bridgePort)

	// nil exit channel: a split chain runs two engines and startChainHalf keeps
	// their exit signals to itself, so there is no single process to follow.
	finalizeProxyConnection(cm, entry, name, connTab, downCmd, downCancel, downPath, nil,
		httpPort, socksPort, intHTTPPort, intSOCKSPort, intSrcPort, chain, chainRaws)
}

// startChainHalf spawns one half and waits for the port it must be listening on
// before the other half is pointed at it.
func startChainHalf(engine string, cfg map[string]interface{}, label string, waitPort int) (*exec.Cmd, context.CancelFunc, string, error) {
	bin := state.xrayBin
	if engine == "singbox" {
		bin = state.singboxBin
	}
	if bin == "" {
		return nil, nil, "", fmt.Errorf("%s not found", engine)
	}
	path, err := writeTempJSON(cfg, label)
	if err != nil {
		return nil, nil, "", err
	}
	if data, e := json.MarshalIndent(cfg, "", "  "); e == nil {
		os.WriteFile(runtimePath("last-"+label+".json"), data, 0o600) //nolint:errcheck
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "run", "-c", path)
	cmd.Stdout = io.Discard
	prepareProc(cmd)
	stderrPipe, _ := cmd.StderrPipe()
	if engine == "xray" {
		// xray writes its console log to stdout, and the access lines there are
		// what the rule counters read.
		stdoutPipe, _ := cmd.StdoutPipe()
		cmd.Stdout = nil
		go pumpProcLog("xray", stdoutPipe, nil)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		os.Remove(path)
		return nil, nil, "", err
	}
	tieToVair(cmd)
	go pumpProcLog(engine, stderrPipe, nil)

	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }() //nolint:errcheck
	ready := make(chan bool, 1)
	go func() { ready <- waitForPort(waitPort, time.Now().Add(singboxConnTimeout)) }()
	select {
	case <-exited:
		cancel()
		os.Remove(path)
		return nil, nil, "", fmt.Errorf("%s exited at startup", engine)
	case ok := <-ready:
		if !ok {
			cancel()
			os.Remove(path)
			killTree(cmd)
			return nil, nil, "", fmt.Errorf("%s did not open its port", engine)
		}
	}
	return cmd, cancel, path, nil
}
