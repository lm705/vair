package core

import "fmt"

// Chains that need both engines.
//
// A chain used to have to be all-xray, then all-of-one-engine. Both rules run
// into the same wall: xray cannot dial through a WireGuard peer or a Hysteria2
// server, and sing-box — while it understands VLESS perfectly well on paper —
// does not get through every REALITY server that xray does. A chain of
// "Hysteria2 → VLESS/REALITY" is refused by the first rule and silently carries
// nothing under the second, which is where this started.
//
// So run both. The hops are split into runs, each run goes to the engine that
// can carry it, and the boundary between two runs is a local SOCKS port: the
// upstream process listens on it, the downstream process dials its first hop
// through it. That is the same trick the hybrid TUN path has always used to put
// sing-box's tunnel in front of xray's protocol — here it joins two halves of a
// chain instead.
//
// One boundary, two processes. A chain that alternates engines more than once
// would need three or more, and nobody has asked for a four-hop chain that
// changes engine twice; it is refused with a sentence rather than half-built.

// chainSegment is one run of consecutive hops that a single engine carries.
type chainSegment struct {
	Engine string // "xray" or "singbox"
	Nodes  []*Node
}

// engineForChainHop picks the engine for one hop of a chain.
//
// xray whenever it can: that is where the REALITY behaviour this program relies
// on lives, and where every chain ran before. sing-box only for what xray
// cannot dial at all.
func engineForChainHop(n *Node) string {
	if n == nil {
		return ""
	}
	if engineFor(n.Kind) == "xray" && xrayOutboundForNode(n) != nil {
		return "xray"
	}
	if singboxOutboundForNode(n) != nil || singboxEndpointForNode(n) != nil {
		return "singbox"
	}
	return ""
}

// splitChain groups the hops into consecutive same-engine runs.
//
// Returns a reason instead when no engine can carry a hop, or when the chain
// changes engine more than once.
func splitChain(nodes []*Node) ([]chainSegment, string) {
	if len(nodes) < 2 {
		return nil, "a chain needs at least 2 configs"
	}
	var segs []chainSegment
	for _, n := range nodes {
		eng := engineForChainHop(n)
		if eng == "" {
			if n == nil {
				return nil, "unparseable config in selection"
			}
			return nil, fmt.Sprintf("%s can't be chained — no engine here can dial it", n.Kind)
		}
		if len(segs) > 0 && segs[len(segs)-1].Engine == eng {
			segs[len(segs)-1].Nodes = append(segs[len(segs)-1].Nodes, n)
			continue
		}
		segs = append(segs, chainSegment{Engine: eng, Nodes: []*Node{n}})
	}
	if len(segs) > 2 {
		return nil, "this chain changes engine more than once — reorder it so all the WireGuard/Hysteria2/TUIC hops sit together"
	}
	return segs, ""
}

// buildChainSegmentUpstream builds the config for the FIRST half of a split
// chain: its hops, chained, with nothing but a local SOCKS server in front. The
// downstream half dials through that port.
func buildChainSegmentUpstream(seg chainSegment, socksPort int) map[string]interface{} {
	switch seg.Engine {
	case "xray":
		cfg := buildXrayChainConfig(seg.Nodes, 0, socksPort, "", "", "", "")
		if cfg == nil && len(seg.Nodes) == 1 {
			cfg = buildXrayConfigForNode(seg.Nodes[0], 0, socksPort, "", "", "", "")
		}
		return cfg
	case "singbox":
		var cfg map[string]interface{}
		if len(seg.Nodes) == 1 {
			cfg = buildSingboxProxyConfig(seg.Nodes[0], 0, socksPort)
		} else {
			cfg = buildSingboxChainProxyConfig(seg.Nodes, 0, socksPort)
		}
		return withoutIdleInbound(cfg, "http-in")
	}
	return nil
}

// withoutIdleInbound drops an inbound this half has no use for. The upstream
// half only ever needs its SOCKS server; leaving the HTTP one in would bind a
// second, arbitrary local port for nobody.
func withoutIdleInbound(cfg map[string]interface{}, tag string) map[string]interface{} {
	if cfg == nil {
		return nil
	}
	in, _ := cfg["inbounds"].([]interface{})
	kept := make([]interface{}, 0, len(in))
	for _, raw := range in {
		if m, ok := raw.(map[string]interface{}); ok && m["tag"] == tag {
			continue
		}
		kept = append(kept, raw)
	}
	cfg["inbounds"] = kept
	return cfg
}

// buildChainSegmentDownstream builds the SECOND half: its hops, chained, with
// the user-facing HTTP/SOCKS inbounds, and its first hop dialing through the
// upstream half's SOCKS port.
func buildChainSegmentDownstream(seg chainSegment, httpPort, socksPort, upstreamPort int) map[string]interface{} {
	switch seg.Engine {
	case "xray":
		var cfg map[string]interface{}
		if len(seg.Nodes) == 1 {
			su, sp := proxySocksCreds()
			hu, hp := proxyHttpCreds()
			cfg = buildXrayConfigForNode(seg.Nodes[0], httpPort, socksPort, su, sp, hu, hp)
		} else {
			su, sp := proxySocksCreds()
			hu, hp := proxyHttpCreds()
			cfg = buildXrayChainConfig(seg.Nodes, httpPort, socksPort, su, sp, hu, hp)
		}
		if cfg == nil {
			return nil
		}
		return withXrayUpstream(cfg, seg, upstreamPort)
	case "singbox":
		var cfg map[string]interface{}
		if len(seg.Nodes) == 1 {
			cfg = buildSingboxProxyConfig(seg.Nodes[0], httpPort, socksPort)
		} else {
			cfg = buildSingboxChainProxyConfig(seg.Nodes, httpPort, socksPort)
		}
		if cfg == nil {
			return nil
		}
		return withSingboxUpstream(cfg, seg, upstreamPort)
	}
	return nil
}

// upstreamTag is the local SOCKS hop both engines dial the other half through.
const upstreamTag = "upstream"

// withXrayUpstream points the segment's ENTRY hop at the upstream SOCKS.
//
// The entry hop is the one with no dialerProxy of its own — in a single-hop
// segment that is the only outbound ("proxy"), in a longer one it is "hop0".
func withXrayUpstream(cfg map[string]interface{}, seg chainSegment, upstreamPort int) map[string]interface{} {
	outs, _ := cfg["outbounds"].([]interface{})
	entryTag := "proxy"
	if len(seg.Nodes) > 1 {
		entryTag = "hop0"
	}
	found := false
	for _, raw := range outs {
		ob, _ := raw.(map[string]interface{})
		if ob == nil || ob["tag"] != entryTag {
			continue
		}
		ss, _ := ob["streamSettings"].(map[string]interface{})
		if ss == nil {
			ss = map[string]interface{}{}
			ob["streamSettings"] = ss
		}
		sockopt, _ := ss["sockopt"].(map[string]interface{})
		if sockopt == nil {
			sockopt = map[string]interface{}{}
			ss["sockopt"] = sockopt
		}
		sockopt["dialerProxy"] = upstreamTag
		found = true
	}
	if !found {
		return nil
	}
	cfg["outbounds"] = append(outs, map[string]interface{}{
		"tag":      upstreamTag,
		"protocol": "socks",
		"settings": map[string]interface{}{
			"servers": []interface{}{
				map[string]interface{}{"address": "127.0.0.1", "port": upstreamPort},
			},
		},
	})
	return cfg
}

// withSingboxUpstream is the same for sing-box, where the link is "detour".
func withSingboxUpstream(cfg map[string]interface{}, seg chainSegment, upstreamPort int) map[string]interface{} {
	entryTag := "proxy"
	if len(seg.Nodes) > 1 {
		entryTag = "hop0"
	}
	found := false
	point := func(list []interface{}) {
		for _, raw := range list {
			eg, _ := raw.(map[string]interface{})
			if eg == nil || eg["tag"] != entryTag {
				continue
			}
			eg["detour"] = upstreamTag
			found = true
		}
	}
	outs, _ := cfg["outbounds"].([]interface{})
	eps, _ := cfg["endpoints"].([]interface{})
	point(outs)
	point(eps)
	if !found {
		return nil
	}
	cfg["outbounds"] = append(outs, map[string]interface{}{
		"type": "socks", "tag": upstreamTag,
		"server": "127.0.0.1", "server_port": upstreamPort,
	})
	return cfg
}
