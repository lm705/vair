//go:build windows

package core

import (
	"fmt"
	"strings"

	"vair/helper/winsys"
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
		if !p.Enabled {
			return fmt.Sprintf("the Windows firewall is switched off for this network (the %s profile), so the block does not apply to it", name)
		}
		if !strings.EqualFold(p.Outbound, "Block") {
			return fmt.Sprintf("the %s profile did not take the block (it reports %q)", name, p.Outbound)
		}
	}
	return ""
}

// verifyBlockStanding reads the firewall back and returns what is wrong with
// the block, or "" when there is nothing wrong. An unreadable firewall is not
// an accusation: it answers "".
//
// For a block put up by this build that means asking the filtering engine
// whether our filters are in place. Windows Firewall's profiles no longer say
// anything about it: the filters apply whether the firewall is on or off, on
// every network, which is exactly what the profile checks below used to catch
// the absence of.
func verifyBlockStanding() string {
	if st, had := readKillSwitchState(); had && st.legacy() {
		active, profiles, err := winsys.FirewallState()
		if err != nil {
			return ""
		}
		return blockGapReason(active, profiles)
	}
	present, known := wfpBlockPresent()
	if !known || present {
		return ""
	}
	return "the block's filters are not in place in the Windows Filtering Platform"
}
