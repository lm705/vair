package core

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"vair/helper/api"
)

// tunRunsInHelper — the app never becomes root here, so the TUN engine belongs
// to the privileged helper.
func tunRunsInHelper() bool { return true }

// startTUNInHelper hands a validated configuration over and returns a handle
// teardown can stop.
//
// Failures come back as errors rather than as a crashed child to inspect: the
// helper validates before it starts anything and answers with the reason, which
// is a better diagnostic than the last three lines of somebody's stderr.
func startTUNInHelper(cfg map[string]interface{}) (tunStopper, error) {
	h, err := dialHelper()
	if err != nil {
		return nil, err
	}
	// Before the config, because it rewrites the paths inside it: the engine
	// must be handed a document that already points at the files the helper
	// holds. Staging is per connection — the helper drops a session's files when
	// the session ends, so this is not a cache that can go stale.
	resolveRuleSets(cfg, h.PutRuleSet)

	raw, err := json.Marshal(cfg)
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("serialise config: %w", err)
	}
	st, err := h.StartTUN(raw, killSwitchEnabled(), allowLANTraffic(), serverIPsFromConfig(cfg))
	if err != nil {
		h.Close()
		return nil, err
	}
	vlog("info", "tunnel up on %s (pid %d, run by the helper)", st.Iface, st.PID)
	return h, nil
}

// startTUNViaHelper is the pure-sing-box TUN connect sequence when the engine
// runs in the helper.
//
// Deliberately not a variant of the local one. That function is mostly about
// owning a process — a stderr pipe scanned for a crash reason, a startup
// timeout racing an early exit, a context to cancel. None of it applies here:
// the helper validates before starting, answers with the reason if it refuses,
// and streams the engine's output to us over the socket.
func startTUNViaHelper(cm *connManager, entry *ConfigEntry, name, connTab, tunIface string, cfg map[string]interface{}, chain, chainRaws []string) {
	// Same debug artefact the local path writes, so a bug report looks the same
	// on both platforms.
	if data, err := json.MarshalIndent(cfg, "", "  "); err == nil {
		os.WriteFile(runtimePath("last-singbox-tun.json"), data, 0o600) //nolint:errcheck
	}

	remote, err := startTUNInHelper(cfg)
	if err != nil {
		setConnError(cm, entry, "helper: "+err.Error())
		return
	}

	cm.mu.Lock()
	cm.tunRemote = remote
	// No local process, no counter: the engine is not our child and there is no
	// local hop to instrument, exactly as in the local pure-sing-box path.
	cm.cmd, cm.cancel, cm.tmpCfg = nil, nil, ""
	cm.xrayCmd, cm.xrayCancel, cm.xrayTmpCfg = nil, nil, ""
	cm.counter, cm.fwdCancel = nil, nil
	cm.state = ConnState{
		Status: ConnConnected, Mode: ModeTUN, ConnTab: connTab, ConnRaw: entry.Raw,
		EntryIndex: entry.Index, EntryName: name,
		TUNIface: tunIface, StartedAt: time.Now(),
		StatsUnavailable: true,
		Chain:            chain, ChainRaws: chainRaws,
	}
	cm.mu.Unlock()

	engagePersistentKillSwitch(tunIface)
	recordLastConnected(entry.Raw, ModeTUN)
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	startUptimeTicker(cm)
}

// tunPreconditionError — the app is never root here, so the question is whether
// the privileged helper is installed and reachable. The message names the fix
// the user can actually perform.
func tunPreconditionError() string {
	_, msg := helperProblem()
	return msg
}

// startHybridTUNViaHelper finishes a hybrid TUN connect when the sing-box
// front-end runs in the helper.
//
// The xray child is already running as our own unprivileged process and stays
// that way — it speaks the VLESS/Trojan/… protocol to the real server and
// offers a local SOCKS hop, none of which needs privilege. The helper's
// sing-box dials that hop over loopback.
//
// Which leaves the loop this arrangement invites: xray's own connection to the
// server would itself be captured by the tunnel and fed back through the proxy.
// buildHybridTUNConfig already guards it twice — a carve-out for the engine
// binaries by path (which works here, since the unit grants CAP_SYS_PTRACE)
// and, as the backstop that actually carries the weight, a direct route for
// the server's IP.
func startHybridTUNViaHelper(cm *connManager, entry *ConfigEntry, n *Node, connTab, tunIface string, cfg map[string]interface{}, parts hybridParts) {
	if data, err := json.MarshalIndent(cfg, "", "  "); err == nil {
		os.WriteFile(runtimePath("last-singbox-hybrid.json"), data, 0o600) //nolint:errcheck
	}

	remote, err := startTUNInHelper(cfg)
	if err != nil {
		// The front-end never came up, so the session does not exist — take the
		// xray child and its accounting down with it rather than leaving an
		// orphan listening on a local port.
		parts.stop()
		setConnError(cm, entry, "helper: "+err.Error())
		return
	}

	connName := n.Name
	if parts.isChain {
		connName = strings.Join(parts.chainLabels, " → ")
	}

	cm.mu.Lock()
	cm.tunRemote = remote
	cm.cmd, cm.cancel, cm.tmpCfg = nil, nil, "" // the front-end is not our child
	cm.xrayCmd = parts.xrayCmd
	cm.xrayCancel = parts.xrayCancel
	cm.xrayTmpCfg = parts.xrayTmp
	cm.counter = parts.counter
	cm.fwdCancel = parts.fwdCancel
	cm.state = ConnState{
		Status: ConnConnected, Mode: ModeTUN, ConnTab: connTab, ConnRaw: entry.Raw,
		EntryIndex: entry.Index, EntryName: connName,
		TUNIface: tunIface, StartedAt: time.Now(),
		Chain: parts.chainLabels, ChainRaws: parts.chainRaws,
	}
	cm.mu.Unlock()

	if parts.isChain {
		recordLastConnected(parts.chainRaws[len(parts.chainRaws)-1], ModeTUN)
	} else {
		recordLastConnected(entry.Raw, ModeTUN)
	}
	state.broadcast(SSEEvent{Type: "conn_update", Payload: cm.snap()})
	startUptimeTicker(cm)
	// Traffic stats DO work here, unlike the pure path: the local SOCKS hop
	// between the helper's sing-box and our xray is ours to count.
	startStatsTicker(cm)
}

// tunnelLost is called when the helper reports the tunnel gone without us
// asking. It brings the app's state in line with reality — and, because a
// hybrid session still has our own xray child and its forwarder running, it
// goes through the normal teardown rather than only rewriting the status.
//
// Restricted to TUN: the helper only ever speaks about the tunnel it runs, and
// by the time its message arrives the user may have connected in proxy mode.
func tunnelLost(reason string) { connectionLost(reason, ModeTUN) }

// serverIPsFromConfig collects the addresses the engine will dial, for the
// kill-switch allow-list.
//
// Read out of the configuration rather than passed alongside it, so the two can
// never disagree: what the block permits is exactly what the engine was told to
// connect to. The app resolves a server's name before connecting (see
// preResolveHost), so these are addresses by the time they get here; anything
// that is still a name is skipped rather than guessed at, and the cgroup rule
// in the helper is what covers that case.
func serverIPsFromConfig(cfg map[string]interface{}) []string {
	var out []string
	seen := map[string]bool{}
	add := func(v interface{}) {
		s, _ := v.(string)
		if s == "" || seen[s] || net.ParseIP(s) == nil {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, key := range []string{"outbounds", "endpoints"} {
		list, _ := cfg[key].([]interface{})
		for _, o := range list {
			m, _ := o.(map[string]interface{})
			add(m["server"])
			// A WireGuard endpoint carries its address under a different name,
			// and a chain's hops are outbounds of their own — all of them are
			// dialled, so all of them are allowed.
			if peers, ok := m["peers"].([]interface{}); ok {
				for _, p := range peers {
					pm, _ := p.(map[string]interface{})
					add(pm["address"])
				}
			}
		}
	}
	if len(out) > api.MaxServerIPs {
		out = out[:api.MaxServerIPs]
	}
	return out
}
