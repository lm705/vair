//go:build linux

package main

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// The tunnel guard: for as long as a tunnel is up, nothing addressed to the
// tunnel's own prefixes leaves through another interface.
//
// sing-box's auto_redirect answers every DNS query on the machine itself: its
// nat rule rewrites any port-53 packet to the tunnel's DNS address. A program
// that has bound its socket to a physical interface (SO_BINDTODEVICE, open to
// ordinary users since Linux 5.7) keeps that binding through the rewrite, so
// its query — now addressed to 172.19.0.2 — was sent out of the physical
// interface to that network's gateway, in plain text. Measured on the test
// machine: a bound query for a made-up name showed up on the second
// interface's link with or without the kill-switch, and the kill-switch could
// not stop it, because at its hook the packet is indistinguishable from the
// rewritten queries it must let through (the route is looked up again only
// after the output hook).
//
// Postrouting is where the interface is final. A packet for the tunnel's own
// addresses that is about to leave anywhere but the tunnel is never right —
// whatever the settings — so the guard goes up with every tunnel and comes
// down with it, and costs nothing else.
const guardTable = "vair-guard"

func guardScript(iface string, tunNets []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {\n", guardTable)
	b.WriteString("  chain postrouting {\n")
	b.WriteString("    type filter hook postrouting priority 0; policy accept;\n")
	b.WriteString("    oifname \"lo\" accept\n")
	if iface != "" {
		fmt.Fprintf(&b, "    oifname %q accept\n", iface)
	}
	for _, raw := range tunNets {
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			continue
		}
		if n.IP.To4() != nil {
			fmt.Fprintf(&b, "    ip daddr %s drop\n", n)
		} else {
			fmt.Fprintf(&b, "    ip6 daddr %s drop\n", n)
		}
	}
	b.WriteString("  }\n}\n")
	return b.String()
}

// installGuard puts the guard up, replacing any left from before in one
// transaction.
func installGuard(iface string, tunNets []string) error {
	if len(tunNets) == 0 {
		return nil
	}
	script := fmt.Sprintf("add table inet %s\ndelete table inet %s\n", guardTable, guardTable) + guardScript(iface, tunNets)
	return applyNft(script)
}

// removeGuard takes it down; "no such table" is the normal case.
func removeGuard() {
	exec.Command("nft", "delete", "table", "inet", guardTable).Run() //nolint:errcheck
}
