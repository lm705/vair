package winsys

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Windows Firewall rules of Vair's own, each under a fixed name.

// The inbound door for the engine when the tunnel is not carrying its own TCP.
//
// With the gvisor stack, sing-box implements TCP in user space: nothing on the
// machine ever listens, and Windows Firewall has nothing to have an opinion
// about. The system and mixed stacks hand that work back to the operating
// system — which means real sockets, which means the firewall's default
// "block inbound unless allowed" applies to them. The tunnel then comes up,
// reports itself connected, and carries nothing, with the firewall silently in
// the middle.
//
// Scoped to the tunnel interface rather than the whole program. A rule that
// merely names the binary would also open sing-box's LAN-sharing listeners on
// whatever network the machine is attached to, and those are deliberately
// limited to private and domain profiles. Any profile is right here: a tunnel
// adapter is an unidentified network, which Windows files under Public.
//
// And a group of its own, deliberately: the kill-switch of earlier builds
// cleared its whole group before installing the allow list for a new tunnel,
// so a rule filed under that name was created and swept away in the same
// breath.
//
// sing-box registers a rule for itself as well — "sing-tun (…\sing-box.exe)",
// on the system and mixed stacks — and it is the broader of the two: the whole
// program, any port, any address, every profile, Public included, and it stays
// after the session and after Vair. With the proxy shared over the LAN that
// opened sing-box's listeners to any network the machine joined, past the
// local-subnet, private-network rule that sharing is meant to have. So that
// one is removed as soon as ours is in place, and again when the session ends;
// ours covers the tunnel by itself. Only the rule for Vair's own sing-box: one
// another program's copy registered is not ours to touch.
const (
	EngineRule      = "Vair TUN engine"
	EngineRuleGroup = "Vair tunnel engine"
)

// EnsureEngineRule opens the door for singbox on iface, replacing any earlier
// one, and removes the rule sing-box registered for itself.
func EnsureEngineRule(singbox, iface string) error {
	if singbox == "" || iface == "" {
		return nil
	}
	RemoveEngineRule(singbox)
	script := fmt.Sprintf(
		"New-NetFirewallRule -DisplayName '%s' -Group '%s' -Direction Inbound -Action Allow "+
			"-Program '%s' -InterfaceAlias '%s' -Profile Any | Out-Null",
		EngineRule, EngineRuleGroup, PSQuote(singbox), PSQuote(iface))
	_, err := PS(script)
	RemoveSingTunRule(singbox)
	return err
}

// SingTunRule is the name of the rule sing-box registers for a binary.
func SingTunRule(bin string) string { return "sing-tun (" + bin + ")" }

// RemoveSingTunRule deletes the rule sing-box registered for this binary.
func RemoveSingTunRule(bin string) {
	if bin == "" {
		return
	}
	PS(fmt.Sprintf( //nolint:errcheck
		"Remove-NetFirewallRule -DisplayName '%s' -ErrorAction SilentlyContinue", PSQuote(SingTunRule(bin))))
}

// RemoveEngineRule closes the door again, with sing-box's own rule for singbox
// (when not empty). Idempotent — a rule that is not there is not an error.
func RemoveEngineRule(singbox string) {
	PS(fmt.Sprintf( //nolint:errcheck
		"Remove-NetFirewallRule -DisplayName '%s' -ErrorAction SilentlyContinue", EngineRule))
	RemoveSingTunRule(singbox)
}

// The web interface and the LAN-shared proxy.
//
// Without a rule the default firewall silently drops inbound TCP from OTHER
// hosts (the phone) even though the server is listening — a loopback test to
// the PC's own LAN address still succeeds, which is why "it works from the PC
// but not the phone" is confusing.
//
// Both admit only the local subnet. The listeners take every address of the
// machine, IPv6 included, and the rules used to admit any remote address on a
// private network: with a global IPv6 address and a router that does not
// filter inbound IPv6, the proxy — open by default — answered the internet,
// not only the devices in the room. "localsubnet" is the networks this
// machine's interfaces are on, for IPv4 and IPv6 alike. And only private and
// domain networks — not public Wi-Fi.
const (
	RemoteRule   = "Vair Remote Access"
	LANProxyRule = "Vair LAN Proxy"
	remoteScope  = "remoteip=localsubnet"
	trusted      = "profile=private,domain"
)

// EnsureRemoteRule (re)creates the rule for the web interface on port, for
// program only when it is not empty: the rule used to open the port for
// whatever listened on it.
func EnsureRemoteRule(port int, program string) {
	RemoveRemoteRule()
	args := []string{"advfirewall", "firewall", "add", "rule",
		"name=" + RemoteRule, "dir=in", "action=allow",
		"protocol=TCP", fmt.Sprintf("localport=%d", port), remoteScope, trusted}
	if program != "" {
		args = append(args, "program="+program)
	}
	Netsh(args...)
}

// RemoveRemoteRule deletes it (idempotent).
func RemoveRemoteRule() { Netsh("advfirewall", "firewall", "delete", "rule", "name="+RemoteRule) }

// EnsureLANProxyRule (re)creates the rules for the LAN-shared proxy: one TCP
// rule for both ports, and for SOCKS over UDP one rule per program. Each UDP
// association gets a relay port of its own, so those rules name programs rather
// than ports — Vair, whose relay serves proxy mode, and sing-box, which serves
// the LAN-shared proxy in TUN mode itself. Same rule name, so one delete
// removes them all.
func EnsureLANProxyRule(httpPort, socksPort int, programs ...string) {
	RemoveLANProxyRule()
	Netsh("advfirewall", "firewall", "add", "rule",
		"name="+LANProxyRule, "dir=in", "action=allow",
		"protocol=TCP", fmt.Sprintf("localport=%d,%d", httpPort, socksPort), remoteScope, trusted)
	for _, p := range programs {
		if p == "" {
			continue
		}
		Netsh("advfirewall", "firewall", "add", "rule",
			"name="+LANProxyRule, "dir=in", "action=allow",
			"protocol=UDP", "program="+p, remoteScope, trusted)
	}
}

// RemoveLANProxyRule deletes them (idempotent).
func RemoveLANProxyRule() { Netsh("advfirewall", "firewall", "delete", "rule", "name="+LANProxyRule) }

// Netsh runs a netsh command with no console window, ignoring failures (e.g.
// not elevated). netsh itself is trusted; the arguments are program-controlled.
func Netsh(args ...string) {
	cmd := exec.Command("netsh", args...)
	Hide(cmd)
	_ = cmd.Run()
}

// RemoveProgramRules deletes every firewall rule that names one of these
// programs, whoever made it — Windows makes "allow"/"block" pairs of its own
// the first time a program listens — and says what it did for each.
//
// PowerShell rather than netsh, though netsh can do the deleting: netsh answers
// in the language of the system, and "no rules matched" could only be told from
// "access denied" by reading a sentence that is Russian on a Russian Windows.
func RemoveProgramRules(programs ...string) []string {
	var out []string
	for _, p := range programs {
		res, err := PS(fmt.Sprintf(
			"$r = @(Get-NetFirewallApplicationFilter -Program '%s' -ErrorAction SilentlyContinue | Get-NetFirewallRule -ErrorAction SilentlyContinue)"+NL+
				"if ($r.Count -eq 0) { 'none' } else { $r | Remove-NetFirewallRule -ErrorAction Stop; 'removed ' + $r.Count }",
			PSQuote(p)))
		name := filepath.Base(p)
		res = strings.TrimSpace(res)
		switch {
		case err != nil:
			out = append(out, "firewall rules for "+name+": could not be removed ("+err.Error()+")")
		case res == "none":
			out = append(out, "firewall rules for "+name+": none")
		default:
			out = append(out, "firewall rules for "+name+": "+res)
		}
	}
	return out
}
