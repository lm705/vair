package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The kill-switch that outlives the tunnel.
//
// sing-box's own strict_route already drops anything trying to leave outside
// the tunnel — but that filter belongs to the engine process, so it goes when
// the engine goes. A crash, a kill, or this program being closed all end the
// protection at the moment it is most wanted: the tunnel is gone and traffic
// resumes on the physical adapter, unprotected and unannounced.
//
// This holds the line at the firewall instead. Windows Firewall's default
// outbound action becomes Block for the duration, with explicit allow rules for
// the traffic that must keep moving: the tunnel's own interface, the engines
// (so they can reach the VPN server and reconnect), loopback, and — when the
// user has not asked otherwise — the local network.
//
// Why the default action rather than block rules: in Windows Firewall an
// explicit Block beats an explicit Allow, so "block everything, allow these"
// cannot be written as rules. Flipping the default to Block makes it an
// implicit block, which allow rules do override.
//
// The dangerous part is stated plainly, because it is the point: while this is
// engaged the machine has no way out except through the tunnel, and that
// survives this program exiting. Three things undo it — an explicit disconnect,
// this program starting up and finding its own marker, and the command printed
// in the log. The marker on disk is what makes the second one possible: it
// records what the firewall looked like before, so the release puts back
// exactly that.

// killSwitchState is the marker written to disk while the block is engaged.
type killSwitchState struct {
	// Previous is the DefaultOutboundAction each profile had before, so the
	// release restores what was there instead of assuming "Allow".
	Previous map[string]string `json:"previous"`
	Iface    string            `json:"iface"`
	Since    time.Time         `json:"since"`
	PID      int               `json:"pid"`
}

const killSwitchRuleGroup = "Vair kill-switch"

// The firewall operations, behind variables so a test can exercise the marker
// handling without reaching for the machine's actual firewall.
var (
	ksSetOutbound    = setOutboundActionFor
	ksSetAllOutbound = setOutboundAction
	ksRemoveRules    = removeKillSwitchRules
	ksAddRules       = addKillSwitchAllowRules
)

var killSwitchMu sync.Mutex

// killSwitchMarker is where the state lives. In the data directory rather than
// a temp dir: a reboot must not wipe the one file that says how to undo this.
func killSwitchMarker() string { return filepath.Join(DataDir(), "killswitch.json") }

// killSwitchHeld reports whether a block is currently engaged (by us, or by a
// previous run of this program that did not get to release it).
func killSwitchHeld() bool {
	_, err := os.Stat(killSwitchMarker())
	return err == nil
}

// engageKillSwitch blocks everything that is not the tunnel.
//
// iface is the TUN interface's name; traffic on it is allowed through. Returns
// an error without changing anything when the machine is not ours to change
// (no elevation).
func engageKillSwitch(iface string) error {
	killSwitchMu.Lock()
	defer killSwitchMu.Unlock()
	// Already held: the tunnel this was engaged for is gone and a new one has
	// taken its place, with a different name — the allow rule has to follow it,
	// or the block stays and the new tunnel is inside it.
	//
	// This is also what closes the leak between two configurations. Removing a
	// TUN adapter puts the ordinary routes back and takes the engine's own
	// filter with it, so every switch used to have a second of open traffic in
	// the middle of it. The firewall block does not come down for a switch — it
	// only ever comes down for an explicit disconnect — so the gap is covered
	// as long as the rules keep pointing at the right interface.
	if killSwitchHeld() {
		return refreshKillSwitchRules(iface)
	}
	if !isPrivileged() {
		return fmt.Errorf("the persistent kill-switch needs the same privileges as TUN mode")
	}

	if !firewallEnabled() {
		return fmt.Errorf("windows firewall is switched off on this machine, so a firewall block would hold nothing — turn the firewall on to use this")
	}
	prev, err := currentOutboundActions()
	if err != nil {
		return err
	}

	// The allow list first, then the door. In this order a mistake leaves the
	// machine reachable; the other way round it leaves it silent.
	if err := addKillSwitchAllowRules(iface); err != nil {
		removeKillSwitchRules()
		return err
	}
	if err := setOutboundAction("Block"); err != nil {
		removeKillSwitchRules()
		return err
	}

	st := killSwitchState{Previous: prev, Iface: iface, Since: time.Now(), PID: os.Getpid()}
	if data, err := json.MarshalIndent(st, "", "  "); err == nil {
		_ = os.WriteFile(killSwitchMarker(), data, 0o600)
	}
	vlog("warning", "kill-switch: traffic outside the tunnel is blocked at the firewall, and stays blocked if Vair stops. Release: disconnect, or run  Set-NetFirewallProfile -All -DefaultOutboundAction Allow")
	return nil
}

// refreshKillSwitchRules re-points an engaged block at the current tunnel.
//
// The whole allow list is rewritten rather than the one rule edited: it is a
// single call either way, and it also picks up anything else that changed
// between connections (the engines' paths, the LAN setting).
func refreshKillSwitchRules(iface string) error {
	data, err := os.ReadFile(killSwitchMarker())
	if err != nil {
		return nil // nothing engaged after all
	}
	var st killSwitchState
	_ = json.Unmarshal(data, &st)
	if st.Iface == iface {
		return nil // same tunnel, nothing to re-point
	}
	ksRemoveRules()
	if err := ksAddRules(iface); err != nil {
		// The door is still shut and the machine is now inside it. Say so
		// loudly: this is the one failure here that costs the user their
		// connection rather than their protection.
		vlog("error", "kill-switch: could not allow the new tunnel (%v) — traffic stays blocked; disconnect to release it", err)
		return err
	}
	st.Iface = iface
	if out, err := json.MarshalIndent(st, "", "  "); err == nil {
		_ = os.WriteFile(killSwitchMarker(), out, 0o600)
	}
	vlog("info", "kill-switch: block re-pointed at %s (traffic stayed blocked through the switch)", iface)
	return nil
}

// releaseKillSwitch puts the firewall back. Safe to call when nothing is
// engaged, and safe to call from a different run of the program than the one
// that engaged it — which is exactly the case it exists for.
func releaseKillSwitch() {
	killSwitchMu.Lock()
	defer killSwitchMu.Unlock()
	data, err := os.ReadFile(killSwitchMarker())
	if err != nil {
		return
	}
	var st killSwitchState
	_ = json.Unmarshal(data, &st)

	ksRemoveRules()
	// Restore per profile, falling back to Allow for anything the marker does
	// not name: a machine with no way out is a worse failure than a firewall
	// that is one setting more permissive than it was.
	restored := false
	for profile, action := range st.Previous {
		if action == "" {
			action = "Allow"
		}
		if err := ksSetOutbound(profile, action); err == nil {
			restored = true
		}
	}
	if !restored {
		_ = ksSetAllOutbound("Allow")
	}
	_ = os.Remove(killSwitchMarker())
	vlog("info", "kill-switch: firewall restored")
}

// releaseStaleKillSwitch is called once at startup: a marker still on disk means
// the last run ended without releasing — a crash, a kill, or a machine switched
// off mid-session. Releasing it here is what keeps "no internet until Vair runs
// again" from becoming "no internet, and no idea why".
func releaseStaleKillSwitch() {
	if !killSwitchHeld() {
		return
	}
	vlog("warning", "kill-switch: a block left over from the previous run — releasing it")
	releaseKillSwitch()
}

// ─────────────────────────── the firewall itself ───────────────────────────

// currentOutboundActions reads each profile's DefaultOutboundAction so the
// release can put back what was there.
func currentOutboundActions() (map[string]string, error) {
	out, err := psOutput(`Get-NetFirewallProfile -All | ForEach-Object { $_.Name + "=" + $_.DefaultOutboundAction }`)
	if err != nil {
		return nil, fmt.Errorf("read firewall profiles: %w", err)
	}
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if name, action, ok := strings.Cut(line, "="); ok && name != "" {
			m[name] = strings.TrimSpace(action)
		}
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("read firewall profiles: nothing to read")
	}
	return m, nil
}

// firewallEnabled reports whether Windows Firewall is on for any profile.
// With it off, DefaultOutboundAction is a value nobody reads.
func firewallEnabled() bool {
	out, err := psOutput(`(Get-NetFirewallProfile -All | Where-Object { $_.Enabled -eq 'True' }).Count`)
	if err != nil {
		return true // cannot tell; do not stand in the way
	}
	return strings.TrimSpace(out) != "0" && strings.TrimSpace(out) != ""
}

func setOutboundAction(action string) error {
	_, err := psOutput(fmt.Sprintf(`Set-NetFirewallProfile -All -DefaultOutboundAction %s`, action))
	return err
}

func setOutboundActionFor(profile, action string) error {
	_, err := psOutput(fmt.Sprintf(`Set-NetFirewallProfile -Name %s -DefaultOutboundAction %s`, profile, action))
	return err
}

// addKillSwitchAllowRules opens exactly what has to keep moving.
func addKillSwitchAllowRules(iface string) error {
	if _, err := psOutput(killSwitchAllowScript(iface)); err != nil {
		return fmt.Errorf("firewall allow rules: %w", err)
	}
	return nil
}

// killSwitchAllowScript builds that list. Separate from running it so the shape
// of the exceptions can be checked without touching a firewall.
func killSwitchAllowScript(iface string) string {
	exe, _ := os.Executable()
	var b strings.Builder
	rule := func(name, args string) {
		fmt.Fprintf(&b, "New-NetFirewallRule -DisplayName '%s' -Group '%s' -Direction Outbound -Action Allow %s | Out-Null\n",
			name, killSwitchRuleGroup, args)
	}
	// The tunnel itself. Everything the user's applications send goes out here.
	if iface != "" {
		rule("Vair tunnel", fmt.Sprintf("-InterfaceAlias '%s'", psQuote(iface)))
	}
	// The engines, so the tunnel can be built and rebuilt while the block holds.
	state.mu.RLock()
	xrayBin, singboxBin := state.xrayBin, state.singboxBin
	state.mu.RUnlock()
	for i, p := range []string{xrayBin, singboxBin, exe} {
		if p == "" {
			continue
		}
		rule(fmt.Sprintf("Vair program %d", i+1), fmt.Sprintf("-Program '%s'", psQuote(p)))
	}
	// Loopback: the proxy ports, the web interface, everything local.
	rule("Vair loopback", "-RemoteAddress 127.0.0.0/8,::1")
	// The local network, unless the user asked for it to be blocked too.
	settingsMu.RLock()
	blockLAN := appSettings.BlockLAN
	settingsMu.RUnlock()
	if !blockLAN {
		rule("Vair local network", "-RemoteAddress LocalSubnet")
		// DHCP and DNS to the router, or the address itself goes stale.
		rule("Vair dhcp", "-Protocol UDP -RemotePort 67,68")
	}
	return b.String()
}

func removeKillSwitchRules() {
	_, _ = psOutput(fmt.Sprintf(
		`Remove-NetFirewallRule -Group '%s' -ErrorAction SilentlyContinue`, killSwitchRuleGroup))
}

// psQuote escapes a value for a single-quoted PowerShell string.
func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

// psOutput runs a PowerShell snippet and returns its output. Errors carry the
// output, because "the command failed" without what it said is not a report.
func psOutput(script string) (string, error) {
	cmd := runHidden("powershell.exe", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-EncodedCommand", encodePS(script))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
