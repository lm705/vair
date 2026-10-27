package core

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// What this session is actually made of.
//
// Nearly every setting in this program applies from the next connection, which
// means the settings dialog answers "what will happen", not "what is happening".
// Nothing answered the second question: a user who changed the routing mode an
// hour ago, or turned the kill switch off after connecting, had no way to see
// which of the two the running tunnel was built with — and neither did a bug
// report.
//
// So the values are recorded at the moment the engine is started, and shown
// beside what the settings say now. Where the two differ, that difference is
// the answer to most of "why is it behaving like that".

// sessionFacts is the effective configuration of one session, taken when it
// started. Strings rather than the settings' own types: this is a record of
// what was decided, and it is only ever read back to be displayed.
type sessionFacts struct {
	Engine      string // xray | sing-box | xray + sing-box
	RoutingMode string
	KillSwitch  bool
	Persistent  bool
	Stack       string
	FakeIP      bool
	LeakProtect bool
	IPv6        string
	MTU         int
	BlockLAN    bool
	ShareLAN    bool
	SysProxy    bool // whether this session was asked to set the system proxy
	DNSBoot     string
	DNSDirect   string
	DNSRemote   string
	DirectApps  int
	RuleCounts  [3]int // without VPN, through VPN, blocked
	// RulesSig and AppsSig are the lists themselves, not their length: a site
	// swapped for another is an edit the session has not seen either.
	RulesSig string
	AppsSig  string
	// DPI bypass: the ClientHello as the session sends it.
	Fragment     bool
	FragLength   string
	FragInterval string
	Fingerprint  string
	// AllTunnelled: everything through the VPN, nothing kept out by own rules
	// or by the programs list. What the bar's "all traffic routed" claims.
	AllTunnelled bool
	// ProxyKS is the proxy-mode kill-switch, shown for a proxy session.
	ProxyKS bool
}

// listOnlyMode reports whether a routing mode sends only listed sites through
// the tunnel. Those build their own DNS module, leak protection or not.
func listOnlyMode(mode string) bool {
	return mode == "only_blocked" || mode == "only_blocked_cn" || mode == "only_selected"
}

// fakeIPUsed is whether the session's DNS answered with placeholder addresses:
// the switch, and leak protection, and a mode that resolves the tunnelled names
// itself rather than one of the list-only modes.
func fakeIPUsed(f *sessionFacts) bool {
	return f.FakeIP && f.LeakProtect && !listOnlyMode(f.RoutingMode)
}

// captureSessionFacts reads the settings that decide how a tunnel behaves. Call
// under no lock: it takes the settings lock itself.
func captureSessionFacts(engine string) *sessionFacts {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	f := &sessionFacts{
		Engine:      engine,
		RoutingMode: appSettings.RoutingMode,
		KillSwitch:  appSettings.KillSwitch,
		Persistent:  appSettings.KillSwitchPersistent,
		Stack:       appSettings.TUNStack,
		FakeIP:      !appSettings.FakeIPDisabled,
		LeakProtect: appSettings.DNSLeakProtection,
		IPv6:        appSettings.IPv6Mode,
		MTU:         appSettings.TUNMTU,
		BlockLAN:    appSettings.BlockLAN,
		ShareLAN:    appSettings.TunShareProxy,
		SysProxy:    !appSettings.NoSystemProxy,
		DNSBoot:     appSettings.BootstrapDNS,
		DNSDirect:   appSettings.DirectDNS,
		DNSRemote:   appSettings.RemoteDNS,
		DirectApps:  len(appSettings.DirectApps),
	}
	if f.RoutingMode == "" {
		f.RoutingMode = "proxy_all"
	}
	if f.Stack == "" {
		f.Stack = "gvisor"
	}
	if f.IPv6 == "" {
		f.IPv6 = "blocked"
	}
	f.MTU = effectiveMTU(f.MTU, f.IPv6) // what the tunnel gets, not what was typed
	f.RuleCounts = [3]int{
		len(appSettings.DirectDomains), len(appSettings.ProxyDomains), len(appSettings.BlockDomains),
	}
	f.RulesSig = fmt.Sprintf("%q %v %q %v %q %v %q %q %v", appSettings.DirectDomains, appSettings.DirectDomainsDisabled,
		appSettings.ProxyDomains, appSettings.ProxyDomainsDisabled, appSettings.BlockDomains, appSettings.BlockDomainsDisabled,
		appSettings.BypassCountries, appSettings.BlocklistURLs, appSettings.BlocklistURLsDisabled)
	f.AppsSig = fmt.Sprintf("%q %v", appSettings.DirectApps, appSettings.DirectAppsDisabled)
	// The values the engines get, not the text in the fields (see
	// currentTLSFragmentParams and currentTLSFingerprint, which take the lock
	// this already holds).
	f.Fragment = appSettings.TLSFragment
	var ok bool
	if f.FragLength, ok = normalizeFragmentSpec(appSettings.TLSFragmentLength, fragLengthMin, fragLengthMax); !ok {
		f.FragLength = defaultTLSFragmentLength
	}
	if f.FragInterval, ok = normalizeFragmentSpec(appSettings.TLSFragmentInterval, fragIntervalMin, fragIntervalMax); !ok {
		f.FragInterval = defaultTLSFragmentInterval
	}
	f.Fingerprint = strings.TrimSpace(appSettings.TLSFingerprint)
	if !validTLSFingerprints[f.Fingerprint] {
		f.Fingerprint = "chrome"
	}
	f.AllTunnelled = f.RoutingMode == "proxy_all" &&
		(appSettings.DirectDomainsDisabled || len(appSettings.DirectDomains) == 0) &&
		(appSettings.DirectAppsDisabled || len(appSettings.DirectApps) == 0)
	f.ProxyKS = appSettings.ProxyKillSwitch
	return f
}

// certRow is how the session's server certificate is checked. For a chain, the
// weakest of its hops, with a note that they differ.
func certRow(cs ConnState) (SessionFactRow, bool) {
	raws := cs.ChainRaws
	if len(raws) == 0 {
		raws = []string{cs.ConnRaw}
	}
	rank := map[string]int{"not checked (allowInsecure)": 0, "checked against the name in the link": 1, "checked": 2, "pinned (SHA-256)": 3, "REALITY key": 3}
	worst, seen := "", map[string]bool{}
	for _, r := range raws {
		n, err := parseNode(r)
		if err != nil {
			continue
		}
		v := certCheckLabel(n)
		if v == "" {
			continue
		}
		seen[v] = true
		if worst == "" || rank[v] < rank[worst] {
			worst = v
		}
	}
	if worst == "" {
		return SessionFactRow{}, false
	}
	row := SessionFactRow{Label: "Server certificate", Value: worst}
	if len(seen) > 1 {
		row.Note = "The hops of the chain differ; this is the weakest of them."
	}
	return row, true
}

// ksNeedsLeakProtection is the note under a TUN kill-switch that is switched on
// and cannot work: it needs TUN DNS leak protection.
const ksNeedsLeakProtection = "On in the settings, but it works only together with TUN DNS leak protection."

// listsEdited is the note under a rule row whose lists changed after the
// session started.
const listsEdited = "Edited after this connection started; the change applies on the next connection."

// listRow is a rule row: the counts the session started with and, when the
// lists have been edited since — added to, removed from, or one entry swapped
// for another — the counts now and why the row is marked.
func listRow(label, then, now, thenSig, nowSig string) SessionFactRow {
	r := SessionFactRow{Label: label, Value: then}
	if thenSig != nowSig {
		r.Now = now
		r.Note = listsEdited
	}
	return r
}

// ruleCounts is the rule row's value: how many lines each list has.
func ruleCounts(f *sessionFacts) string {
	return itoa(f.RuleCounts[0]) + " / " + itoa(f.RuleCounts[1]) + " / " + itoa(f.RuleCounts[2])
}

// SessionFactRow is one line of the session panel: what it is, what this session
// has, and — when they differ — what the settings say now.
type SessionFactRow struct {
	Label string `json:"label"`
	Value string `json:"value"`
	// Now is the current setting when it no longer matches Value, empty
	// otherwise. The panel draws it as "…, settings say X".
	Now string `json:"now,omitempty"`
	// Note is a sentence under the row saying why the value is what it is —
	// a fixed English phrase the panel translates. Empty for most rows.
	Note string `json:"note,omitempty"`
}

// SessionInfoDTO is the whole panel.
type SessionInfoDTO struct {
	Connected bool               `json:"connected"`
	Groups    []string           `json:"groups"`
	Rows      [][]SessionFactRow `json:"rows"` // parallel to Groups
}

// SessionInfo describes the running session, or — when nothing is running —
// what the settings would produce, which is the same question asked in advance.
func SessionInfo() SessionInfoDTO {
	cm := state.conn
	cs := cm.snap()
	cm.mu.Lock()
	was := cm.startedWith
	cm.mu.Unlock()
	live := cs.Status == ConnConnected && was != nil
	now := captureSessionFacts("")
	f := now
	if live {
		f = was
	}

	// Only a live session can disagree with the settings; before one exists
	// there is nothing to disagree with.
	cmp := func(v, n string) string {
		if !live || v == n {
			return ""
		}
		return n
	}
	yes := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	dns := func(v, dflt string) string {
		if strings.TrimSpace(v) == "" {
			return dflt
		}
		return v
	}

	// What answered, not what the fields hold: the system resolver where that
	// is what the session used — for the tunnelled names with leak protection
	// off outside the list-only modes, for the direct ones when chosen.
	direct := func(f *sessionFacts) string {
		if strings.EqualFold(strings.TrimSpace(f.DNSDirect), directDNSSystem) {
			return "system resolver"
		}
		return dns(f.DNSDirect, defaultDirectDNS)
	}
	tunnelled := func(f *sessionFacts) string {
		if !f.LeakProtect && !listOnlyMode(f.RoutingMode) {
			return "system resolver, outside the tunnel"
		}
		return dns(f.DNSRemote, defaultRemoteDNS)
	}

	out := SessionInfoDTO{Connected: live}
	add := func(name string, rows ...SessionFactRow) {
		out.Groups = append(out.Groups, name)
		out.Rows = append(out.Rows, rows)
	}

	conn := []SessionFactRow{}
	if live {
		mode := "proxy"
		if cs.Mode == ModeTUN {
			mode = "TUN"
		}
		conn = append(conn,
			SessionFactRow{Label: "Mode", Value: mode},
			SessionFactRow{Label: "Engine", Value: f.Engine},
			SessionFactRow{Label: "Server", Value: cs.EntryName},
		)
		if len(cs.Chain) > 1 {
			conn = append(conn, SessionFactRow{Label: "Chain", Value: strings.Join(cs.Chain, " → ")})
		}
		if cs.TUNIface != "" {
			conn = append(conn, SessionFactRow{Label: "Interface", Value: cs.TUNIface})
		}
		if cs.HTTPPort > 0 {
			conn = append(conn, SessionFactRow{Label: "Local ports", Value: itoa(cs.HTTPPort) + " HTTP, " + itoa(cs.SOCKSPort) + " SOCKS"})
		}
		if r, ok := certRow(cs); ok {
			conn = append(conn, r)
		}
		if cs.Mode == ModeProxy {
			// Only in proxy mode: TUN neither sets the system proxy nor is
			// asked to, so holding it against the setting reported a change
			// nobody had made.
			conn = append(conn, SessionFactRow{Label: "System proxy", Value: yes(cs.SystemProxy),
				Now: cmp(yes(f.SysProxy), yes(now.SysProxy))})
		}
	} else {
		conn = append(conn, SessionFactRow{Label: "Mode", Value: "not connected"})
	}
	add("Connection", conn...)

	add("Routing",
		SessionFactRow{Label: "Mode", Value: f.RoutingMode, Now: cmp(f.RoutingMode, now.RoutingMode)},
		// The whole lists are compared, not the first count: the row used to
		// notice only the without-VPN list growing or shrinking.
		listRow("Own rules (without VPN / through / blocked)", ruleCounts(f), ruleCounts(now), f.RulesSig, now.RulesSig),
		listRow("Apps without VPN", itoa(f.DirectApps), itoa(now.DirectApps), f.AppsSig, now.AppsSig),
		SessionFactRow{Label: "IPv6", Value: f.IPv6, Now: cmp(f.IPv6, now.IPv6)},
	)

	// Which mode the rows describe: the session's, or — with nothing connected —
	// the one the main window has selected. A proxy session was shown the TUN
	// kill-switch, stack, MTU and DNS protection, none of which applies to it.
	tunView := live && cs.Mode == ModeTUN
	if !live {
		settingsMu.RLock()
		tunView = appSettings.SelectedMode == string(ModeTUN)
		settingsMu.RUnlock()
	}
	// The kill-switches take effect the moment they are switched, so their rows
	// are what holds now, not what the session started with; and a TUN switch
	// that cannot work without DNS leak protection says so instead of "on".
	bootstrap := SessionFactRow{Label: "Bootstrap", Value: dns(f.DNSBoot, defaultBootstrapDNS),
		Now: cmp(dns(f.DNSBoot, defaultBootstrapDNS), dns(now.DNSBoot, defaultBootstrapDNS))}
	if tunView {
		ks := SessionFactRow{Label: "Kill-switch", Value: yes(now.KillSwitch && now.LeakProtect)}
		if now.KillSwitch && !now.LeakProtect {
			ks.Note = ksNeedsLeakProtection
		}
		add("Tunnel",
			ks,
			SessionFactRow{Label: "Persistent kill-switch", Value: yes(now.Persistent && now.KillSwitch && now.LeakProtect)},
			// What that block is actually doing, which is a different question
			// from what was asked for: Windows applies an outbound default only on
			// profiles whose firewall is switched on, so a machine can accept every
			// command and filter nothing. The row is the fact; the note is why.
			blockRow(),
			leakRowIfLive(live),
			SessionFactRow{Label: "TCP/IP stack", Value: f.Stack, Now: cmp(f.Stack, now.Stack)},
			SessionFactRow{Label: "MTU", Value: itoa(f.MTU), Now: cmp(itoa(f.MTU), itoa(now.MTU))},
			SessionFactRow{Label: "LAN through the tunnel", Value: yes(f.BlockLAN), Now: cmp(yes(f.BlockLAN), yes(now.BlockLAN))},
			SessionFactRow{Label: "Proxy shared over LAN", Value: yes(f.ShareLAN), Now: cmp(yes(f.ShareLAN), yes(now.ShareLAN))},
		)
		add("DNS",
			SessionFactRow{Label: "Leak protection", Value: yes(f.LeakProtect), Now: cmp(yes(f.LeakProtect), yes(now.LeakProtect))},
			SessionFactRow{Label: "FakeIP", Value: yes(fakeIPUsed(f)), Now: cmp(yes(fakeIPUsed(f)), yes(fakeIPUsed(now)))},
			// The address itself, never the operator's name: a resolver is only
			// identifiable by where the queries actually went.
			bootstrap,
			SessionFactRow{Label: "Direct", Value: direct(f), Now: cmp(direct(f), direct(now))},
			SessionFactRow{Label: "Through the tunnel", Value: tunnelled(f), Now: cmp(tunnelled(f), tunnelled(now))},
		)
	} else {
		add("Protection",
			SessionFactRow{Label: "Kill-switch (proxy)", Value: yes(now.ProxyKS)},
			blockRow(),
			leakRowIfLive(live),
		)
		// In proxy mode a site's name goes to the proxy with the request and the
		// VPN server looks it up; the engine resolves only what its rules send
		// directly, with the system resolver — its configuration has no DNS of
		// its own. The rows used to name the TUN resolvers.
		add("DNS",
			bootstrap,
			SessionFactRow{Label: "Sites through the proxy", Value: "the VPN server"},
			SessionFactRow{Label: "Sites outside the proxy", Value: "system resolver"},
		)
	}

	dpi := []SessionFactRow{
		{Label: "TLS fragmentation (DPI bypass)", Value: yes(f.Fragment), Now: cmp(yes(f.Fragment), yes(now.Fragment))},
	}
	if f.Fragment || now.Fragment {
		dpi = append(dpi,
			SessionFactRow{Label: "Fragment length", Value: f.FragLength, Now: cmp(f.FragLength, now.FragLength)},
			SessionFactRow{Label: "Fragment interval (ms)", Value: f.FragInterval, Now: cmp(f.FragInterval, now.FragInterval)},
		)
	}
	dpi = append(dpi, SessionFactRow{Label: "TLS fingerprint (uTLS)", Value: f.Fingerprint, Now: cmp(f.Fingerprint, now.Fingerprint)})
	add("DPI bypass", dpi...)

	xray, singbox := engineBins()
	engines := []SessionFactRow{}
	if v := singboxVersionString(); v != "" {
		engines = append(engines, SessionFactRow{Label: "sing-box", Value: v})
	} else if singbox != "" {
		engines = append(engines, SessionFactRow{Label: "sing-box", Value: "present"})
		// Not answered yet (or every attempt at startup failed): ask again, so the
		// next look at the panel has the number.
		go detectSingboxVersion()
	}
	if v := xrayVersionString(); v != "" {
		engines = append(engines, SessionFactRow{Label: "xray", Value: v})
	} else if xray != "" {
		engines = append(engines, SessionFactRow{Label: "xray", Value: "present"})
		go detectXrayVersion()
	}
	if len(engines) > 0 {
		add("Engines", engines...)
	}
	return out
}

// singboxVersionString is what the engine reported at startup, or "" before it
// has been asked.
func singboxVersionString() string {
	singboxVer.RLock()
	defer singboxVer.RUnlock()
	if !singboxVer.known {
		return ""
	}
	return itoa(singboxVer.major) + "." + itoa(singboxVer.minor)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// engineName names the binary a session was started with, for the panel.
func engineName(cmd *exec.Cmd) string {
	if cmd == nil {
		return ""
	}
	base := strings.ToLower(filepath.Base(cmd.Path))
	switch {
	case strings.Contains(base, "sing"):
		return "sing-box"
	case strings.Contains(base, "xray"):
		return "xray"
	}
	return base
}

// blockRow reports the firewall block as it stands right now.
//
// The row above it carries the SETTING, under the name it has in Settings —
// "Blocking held after Vair stops" was a third name for a thing that already
// had two, and nothing on either screen said they were the same. This row is
// the state, so it is named as a state.
func blockRow() SessionFactRow {
	const label = "Firewall block right now"
	if !blockStanding() {
		// The tunnel's own block, when it is the only one: everything is
		// stopped except what goes through the tunnel.
		if tunnelBlockHeld() {
			state.conn.mu.Lock()
			mode := state.conn.state.Mode
			state.conn.mu.Unlock()
			if mode == ModeProxy {
				return SessionFactRow{Label: label, Value: "everything outside the proxy"}
			}
			return SessionFactRow{Label: label, Value: "everything outside the tunnel"}
		}
		return SessionFactRow{Label: label, Value: "not blocking"}
	}
	if gap := BlockGapReason(); gap != "" {
		return SessionFactRow{Label: label, Value: "blocking, but not in force", Now: gap}
	}
	return SessionFactRow{Label: label, Value: "blocking"}
}
