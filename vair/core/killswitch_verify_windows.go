//go:build windows

package core

import (
	"fmt"
	"strings"
)

// Whether the block that was just installed is actually blocking anything.
//
// Setting DefaultOutboundAction to Block is not the same as traffic being
// blocked. Windows applies that default only on profiles whose firewall is
// switched ON, and only the profile of the network the machine is currently
// attached to decides what happens to its traffic. A machine whose active
// profile has the firewall off — an antivirus took it over, or somebody turned
// it off years ago — accepted every command we sent, reported no error, and
// went on passing traffic with the user's own address on it.
//
// That is the worst shape a safety feature can take: a switch that says on, a
// program that says nothing, and no protection at all. So the block is read
// back after it is installed, and what is found is what the interface shows.

// fwProfile is one Windows firewall profile as it matters here.
type fwProfile struct {
	enabled  bool
	outbound string
}

// parseFirewallState reads the shape firewallStateScript prints. Separate from
// running it so the decision can be tested without a firewall.
func parseFirewallState(out string) (active []string, profiles map[string]fwProfile) {
	profiles = map[string]fwProfile{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "ACTIVE:"):
			for _, c := range strings.Split(strings.TrimPrefix(line, "ACTIVE:"), ",") {
				if c = strings.TrimSpace(c); c != "" {
					// The connection profile calls it DomainAuthenticated; the
					// firewall profile is called Domain.
					if strings.EqualFold(c, "DomainAuthenticated") {
						c = "Domain"
					}
					active = append(active, c)
				}
			}
		case strings.HasPrefix(line, "PROFILE:"):
			body := strings.TrimPrefix(line, "PROFILE:")
			name, rest, ok := strings.Cut(body, "=")
			if !ok {
				continue
			}
			en, out, _ := strings.Cut(rest, "/")
			profiles[strings.TrimSpace(name)] = fwProfile{
				enabled:  strings.EqualFold(strings.TrimSpace(en), "true"),
				outbound: strings.TrimSpace(out),
			}
		}
	}
	return active, profiles
}

// blockGapReason names what is wrong with a block that is supposedly standing,
// or returns "" when it is real. Written for a person: it ends up under the
// setting that asked for it and in the session panel.
func blockGapReason(active []string, profiles map[string]fwProfile) string {
	if len(active) == 0 {
		return "" // no network to judge: nothing to warn about
	}
	for _, name := range active {
		p, ok := profiles[name]
		if !ok {
			continue
		}
		if !p.enabled {
			return fmt.Sprintf("the Windows firewall is switched off for this network (the %s profile), so the block does not apply to it", name)
		}
		if !strings.EqualFold(p.outbound, "Block") {
			return fmt.Sprintf("the %s profile did not take the block (it reports %q)", name, p.outbound)
		}
	}
	return ""
}

// firewallStateScript prints the two things the check needs, one per line.
const firewallStateScript = `"ACTIVE:" + ((Get-NetConnectionProfile | ForEach-Object { $_.NetworkCategory }) -join ',')
Get-NetFirewallProfile -All | ForEach-Object { "PROFILE:" + $_.Name + "=" + $_.Enabled + "/" + $_.DefaultOutboundAction }`

// verifyBlockStanding reads the firewall back and returns what is wrong with
// the block, or "" when there is nothing wrong. An unreadable firewall is not
// an accusation: it answers "".
func verifyBlockStanding() string {
	out, err := psOutput(firewallStateScript)
	if err != nil {
		return ""
	}
	return blockGapReason(parseFirewallState(out))
}
