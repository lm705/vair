//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"vair/helper/api"
)

// The Linux kill-switch: nothing leaves this machine except through the tunnel.
//
// On Windows sing-box does this itself — strict_route installs a WFP filter and
// the guarantee comes with the engine. Here strict_route means something
// different, and sing-box warns against pairing it with auto_redirect, so the
// block is ours: one nftables table, installed with the tunnel and removed with
// it. It lives in the helper because it needs root, and because the thing that
// owns the tunnel should own the block that protects it — a GUI that dies
// cannot leave a machine cut off, since the helper tears both down together.
//
// What is allowed out:
//
//   - loopback, which is the machine talking to itself;
//   - the tunnel interface, which is the point;
//   - loopback and tunnel ADDRESSES, for what the engine's own nftables rules
//     send there before the route is looked up again (see killSwitchScript);
//   - the addresses of the VPN servers themselves, or the engine could not
//     reach the server it is building the tunnel to;
//   - everything the helper's own service produces, matched by cgroup. That is
//     the engine and nothing else, and it covers what the server-address list
//     cannot: the engine's bootstrap DNS, and a chain whose next hop is
//     resolved after the block is up;
//   - private networks, when the routing rules are letting the LAN through —
//     printers, NAS, the router's page. Turned off by "Block LAN traffic",
//     exactly as it is in the routing.
//
// Everything else is dropped, including IPv6, which is the family the old
// Windows-only kill-switch missed for years.

// killSwitchTable is ours alone. Never `nft flush ruleset` — this machine may be
// running a firewall that has nothing to do with us.
const killSwitchTable = "vair-killswitch"

// helperCgroup reports the cgroup this helper is in, in the form nft wants: the
// path without its leading slash, and the number of components as the level.
//
// Read at runtime rather than written down as "system.slice/vair-helper.service"
// — which is what it is under the packaged unit, but not when the helper is
// started by hand, or from a user session, or under a slice someone renamed.
// nft REFUSES a path that does not exist ("cgroupv2 path fails: No such file or
// directory"), so a guess is not a harmless one: it costs the rule.
func helperCgroup() (string, int, bool) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		// cgroup v2: "0::/system.slice/vair-helper.service"
		if !strings.HasPrefix(line, "0::/") {
			continue
		}
		path := strings.TrimPrefix(strings.TrimSpace(line), "0::/")
		if path == "" {
			return "", 0, false // the root cgroup matches everything; no rule
		}
		// Only a service cgroup, which holds this helper and the engine it
		// spawned and nothing else. Measured on the test machine: a helper
		// started by hand from a shell sits in the SESSION's scope, and a rule
		// naming that would allow every program the user is running — a
		// kill-switch that blocks nothing while reporting that it is on. When
		// the path is not a service the block is built without this rule, which
		// is stricter, not looser.
		if !strings.HasSuffix(path, ".service") {
			return "", 0, false
		}
		if _, err := os.Stat("/sys/fs/cgroup/" + path); err != nil {
			return "", 0, false
		}
		return path, len(strings.Split(path, "/")), true
	}
	return "", 0, false
}

// killSwitchScript builds the ruleset. Separated from running it so a test can
// read what would be installed without being root.
//
// An empty cgroup is what the caller falls back to: `socket cgroupv2` needs a
// recent nft and a path that exists, and a machine without either should still
// get a block built on the server addresses rather than no block at all.
func killSwitchScript(iface string, tunNets, serverIPs []string, allowLAN bool, cgroup string, cgroupLevel int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {\n", killSwitchTable)
	b.WriteString("  chain output {\n")
	b.WriteString("    type filter hook output priority 0; policy drop;\n")
	b.WriteString("    oifname \"lo\" accept\n")
	if iface != "" {
		fmt.Fprintf(&b, "    oifname %q accept\n", iface)
	}
	// Where the engine's own rules send a packet before its route is looked up
	// again. auto_redirect DNATs every port-53 query on the machine to the
	// tunnel's DNS address and redirects TCP to a port on loopback; at this hook
	// such a packet still names the interface it was first routed to, so the
	// oifname rules above do not match it yet. Traced with `nft monitor trace`:
	// under the kill-switch every program's own DNS query (dig @8.8.8.8, a
	// resolver inside a container) died here with the policy, and TCP got
	// through only because in the hybrid path the "server address" in the list
	// below happens to be 127.0.0.1. Both kinds of destination are reached
	// through the tunnel or stay on this machine, whatever the stale interface
	// says.
	b.WriteString("    ip daddr 127.0.0.0/8 accept\n")
	b.WriteString("    ip6 daddr ::1 accept\n")
	for _, raw := range tunNets {
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			continue // the caller parses these; this is the second pair of eyes
		}
		if n.IP.To4() != nil {
			fmt.Fprintf(&b, "    ip daddr %s accept\n", n)
		} else {
			fmt.Fprintf(&b, "    ip6 daddr %s accept\n", n)
		}
	}
	if cgroup != "" {
		fmt.Fprintf(&b, "    socket cgroupv2 level %d %q accept\n", cgroupLevel, cgroup)
	}
	for _, raw := range serverIPs {
		ip := net.ParseIP(raw)
		if ip == nil {
			continue // the caller validates; this is the second pair of eyes
		}
		if ip.To4() != nil {
			fmt.Fprintf(&b, "    ip daddr %s accept\n", ip)
		} else {
			fmt.Fprintf(&b, "    ip6 daddr %s accept\n", ip)
		}
	}
	if allowLAN {
		b.WriteString("    ip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4 } accept\n")
		b.WriteString("    ip6 daddr { fc00::/7, fe80::/10, ff00::/8 } accept\n")
	}
	b.WriteString("  }\n}\n")
	return b.String()
}

// installKillSwitch puts the table up, replacing whatever was there.
//
// Replaced, never added to, and in one nft transaction: the script creates the
// table if it is missing, deletes it, and defines it again, so there is no
// instant between the old block and the new one — which there was when this
// was a delete and then an add, and which a switch to another server passes
// through with the block kept (see keepBlock).
func installKillSwitch(iface string, tunNets, serverIPs []string, allowLAN bool, logf func(api.LogLine)) error {
	cg, level, ok := helperCgroup()
	if !ok {
		cg, level = "", 0
	}
	err := applyNft(replaceScript(killSwitchScript(iface, tunNets, serverIPs, allowLAN, cg, level)))
	if err != nil && cg != "" {
		// Almost certainly the cgroup match. Fall back rather than leave the
		// machine unprotected, and say which one is in place — the difference
		// matters: without the cgroup rule the engine can reach the servers in
		// the list and nothing else, so a chain that resolves its next hop
		// later will not come up.
		if err2 := applyNft(replaceScript(killSwitchScript(iface, tunNets, serverIPs, allowLAN, "", 0))); err2 != nil {
			return fmt.Errorf("kill-switch: %w", err)
		}
		if logf != nil {
			logf(api.LogLine{Src: "helper", Lvl: "warn",
				Text: "kill-switch installed without the cgroup rule (" + firstLine(err.Error()) + ")"})
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("kill-switch: %w", err)
	}
	if logf != nil {
		msg := "kill-switch on: nothing leaves except through the tunnel"
		if cg == "" {
			// Worth saying: without it the engine may only reach the addresses
			// in the list, so a hop resolved later will not come up.
			msg += " (without the cgroup rule — this helper has no cgroup v2 path)"
		}
		logf(api.LogLine{Src: "helper", Lvl: "info", Text: msg})
	}
	return nil
}

// removeKillSwitch takes it down. Best effort: "no such table" is the normal
// case on a teardown that never installed one.
func removeKillSwitch() {
	exec.Command("nft", "delete", "table", "inet", killSwitchTable).Run() //nolint:errcheck
}

// killSwitchPresent reports whether our table is in the ruleset. Used by the
// startup cleanup, which must not leave a machine cut off by a helper that was
// killed while the block was up.
func killSwitchPresent() bool {
	out, err := exec.Command("nft", "list", "tables", "inet").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), killSwitchTable)
}

// replaceScript wraps a table definition so nft swaps it in atomically.
func replaceScript(def string) string {
	return fmt.Sprintf("add table inet %s\ndelete table inet %s\n", killSwitchTable, killSwitchTable) + def
}

func applyNft(script string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
