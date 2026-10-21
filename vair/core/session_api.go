package core

import (
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
	if f.MTU == 0 {
		f.MTU = 9000
	}
	f.RuleCounts = [3]int{
		len(appSettings.DirectDomains), len(appSettings.ProxyDomains), len(appSettings.BlockDomains),
	}
	return f
}

// SessionFactRow is one line of the session panel: what it is, what this session
// has, and — when they differ — what the settings say now.
type SessionFactRow struct {
	Label string `json:"label"`
	Value string `json:"value"`
	// Now is the current setting when it no longer matches Value, empty
	// otherwise. The panel draws it as "…, settings say X".
	Now string `json:"now,omitempty"`
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
		SessionFactRow{Label: "Own rules (without VPN / through / blocked)",
			Value: itoa(f.RuleCounts[0]) + " / " + itoa(f.RuleCounts[1]) + " / " + itoa(f.RuleCounts[2]),
			Now:   cmp(itoa(f.RuleCounts[0]), itoa(now.RuleCounts[0]))},
		SessionFactRow{Label: "Apps without VPN", Value: itoa(f.DirectApps), Now: cmp(itoa(f.DirectApps), itoa(now.DirectApps))},
		SessionFactRow{Label: "IPv6", Value: f.IPv6, Now: cmp(f.IPv6, now.IPv6)},
	)

	add("Tunnel",
		SessionFactRow{Label: "Kill-switch", Value: yes(f.KillSwitch), Now: cmp(yes(f.KillSwitch), yes(now.KillSwitch))},
		SessionFactRow{Label: "Keep blocking after the tunnel is gone", Value: yes(f.Persistent), Now: cmp(yes(f.Persistent), yes(now.Persistent))},
		// What that block is actually doing, which is a different question from
		// what was asked for: Windows applies an outbound default only on
		// profiles whose firewall is switched on, so a machine can accept every
		// command and filter nothing. The row is the fact; the note is why.
		blockRow(),
		SessionFactRow{Label: "TCP/IP stack", Value: f.Stack, Now: cmp(f.Stack, now.Stack)},
		SessionFactRow{Label: "MTU", Value: itoa(f.MTU), Now: cmp(itoa(f.MTU), itoa(now.MTU))},
		SessionFactRow{Label: "LAN through the tunnel", Value: yes(f.BlockLAN), Now: cmp(yes(f.BlockLAN), yes(now.BlockLAN))},
		SessionFactRow{Label: "Proxy shared over LAN", Value: yes(f.ShareLAN), Now: cmp(yes(f.ShareLAN), yes(now.ShareLAN))},
	)

	add("DNS",
		SessionFactRow{Label: "Leak protection", Value: yes(f.LeakProtect), Now: cmp(yes(f.LeakProtect), yes(now.LeakProtect))},
		SessionFactRow{Label: "FakeIP", Value: yes(f.FakeIP), Now: cmp(yes(f.FakeIP), yes(now.FakeIP))},
		// The address itself, never the operator's name: a resolver is only
		// identifiable by where the queries actually went.
		SessionFactRow{Label: "Bootstrap", Value: dns(f.DNSBoot, defaultBootstrapDNS),
			Now: cmp(dns(f.DNSBoot, defaultBootstrapDNS), dns(now.DNSBoot, defaultBootstrapDNS))},
		SessionFactRow{Label: "Direct", Value: dns(f.DNSDirect, defaultDirectDNS),
			Now: cmp(dns(f.DNSDirect, defaultDirectDNS), dns(now.DNSDirect, defaultDirectDNS))},
		SessionFactRow{Label: "Through the tunnel", Value: dns(f.DNSRemote, defaultRemoteDNS),
			Now: cmp(dns(f.DNSRemote, defaultRemoteDNS), dns(now.DNSRemote, defaultRemoteDNS))},
	)

	xray, singbox := engineBins()
	engines := []SessionFactRow{}
	if v := singboxVersionString(); v != "" {
		engines = append(engines, SessionFactRow{Label: "sing-box", Value: v})
	} else if singbox != "" {
		engines = append(engines, SessionFactRow{Label: "sing-box", Value: "present"})
	}
	if xray != "" {
		engines = append(engines, SessionFactRow{Label: "xray", Value: "present"})
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
		return SessionFactRow{Label: label, Value: "not blocking"}
	}
	if gap := BlockGapReason(); gap != "" {
		return SessionFactRow{Label: label, Value: "blocking, but not in force", Now: gap}
	}
	return SessionFactRow{Label: label, Value: "blocking"}
}
