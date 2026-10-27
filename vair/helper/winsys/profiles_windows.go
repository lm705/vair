package winsys

import (
	"fmt"
	"strings"
)

// Windows Firewall profiles: the kill-switch of builds before 2.7.0 was made of
// them — the default outbound action set to Block, and a group of allow rules —
// and a block of that kind can still be standing on a machine that is upgraded.
// What is here is what it takes to read one and to take one down; nothing puts
// one up any more.

// LegacyRuleGroup is the group those builds filed their allow rules under.
const LegacyRuleGroup = "Vair kill-switch"

// SetOutbound sets the default outbound action of every profile.
func SetOutbound(action string) error {
	if !IsOutboundAction(action) {
		return fmt.Errorf("not an outbound action: %q", action)
	}
	_, err := PS(fmt.Sprintf(`Set-NetFirewallProfile -All -DefaultOutboundAction %s`, action))
	return err
}

// SetOutboundFor sets it for one profile.
func SetOutboundFor(profile, action string) error {
	if !IsProfileName(profile) || !IsOutboundAction(action) {
		return fmt.Errorf("not a profile and an outbound action: %q %q", profile, action)
	}
	_, err := PS(fmt.Sprintf(`Set-NetFirewallProfile -Name %s -DefaultOutboundAction %s`, profile, action))
	return err
}

// RemoveLegacyRules removes the allow-rule group.
func RemoveLegacyRules() {
	_, _ = PS(fmt.Sprintf(`Remove-NetFirewallRule -Group '%s' -ErrorAction SilentlyContinue`, LegacyRuleGroup))
}

// LegacyRulesPresent says whether that group is still in the firewall — the
// evidence that a firewall block standing now is ours. known is false when the
// firewall could not be asked.
func LegacyRulesPresent() (present, known bool) {
	out, err := PS(fmt.Sprintf(`(Get-NetFirewallRule -Group '%s' -ErrorAction SilentlyContinue | Measure-Object).Count`, LegacyRuleGroup))
	if err != nil {
		return false, false
	}
	n := strings.TrimSpace(out)
	return n != "" && n != "0", true
}

// IsProfileName says whether a name is one of the three Windows firewall
// profiles. Used to keep everything else out of a marker, and out of the
// commands built from it.
func IsProfileName(s string) bool {
	switch strings.ToLower(s) {
	case "domain", "private", "public":
		return true
	}
	return false
}

// IsOutboundAction says the same for the value beside it.
func IsOutboundAction(s string) bool {
	switch strings.ToLower(s) {
	case "allow", "block", "notconfigured":
		return true
	}
	return false
}

// Profile is one Windows firewall profile as it matters here.
type Profile struct {
	Enabled  bool
	Outbound string
}

// FirewallStateScript prints the two things a check needs, one per line: the
// categories of the networks the machine is on, and every profile's state.
const FirewallStateScript = `"ACTIVE:" + ((Get-NetConnectionProfile | ForEach-Object { $_.NetworkCategory }) -join ',')
Get-NetFirewallProfile -All | ForEach-Object { "PROFILE:" + $_.Name + "=" + $_.Enabled + "/" + $_.DefaultOutboundAction }`

// ParseFirewallState reads the shape FirewallStateScript prints. Separate from
// running it so the decision can be tested without a firewall.
func ParseFirewallState(out string) (active []string, profiles map[string]Profile) {
	profiles = map[string]Profile{}
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
			profiles[strings.TrimSpace(name)] = Profile{
				Enabled:  strings.EqualFold(strings.TrimSpace(en), "true"),
				Outbound: strings.TrimSpace(out),
			}
		}
	}
	return active, profiles
}

// FirewallState runs the script and parses it.
func FirewallState() (active []string, profiles map[string]Profile, err error) {
	out, err := PS(FirewallStateScript)
	if err != nil {
		return nil, nil, err
	}
	active, profiles = ParseFirewallState(out)
	return active, profiles, nil
}

// AnyProfileBlocking says whether any profile refuses outbound traffic by
// default, and whether the firewall could be read at all — "no" and "cannot
// tell" lead to opposite actions when the question is whether to throw away the
// only record of a standing block.
func AnyProfileBlocking() (blocking, known bool) {
	_, profiles, err := FirewallState()
	if err != nil || len(profiles) == 0 {
		return false, false
	}
	for _, p := range profiles {
		if strings.EqualFold(p.Outbound, "Block") {
			return true, true
		}
	}
	return false, true
}
