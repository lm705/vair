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
	// Gen names the allow-rule set currently standing, so the next switch can
	// install its own beside it and take this one down afterwards. Absent in a
	// marker written by an earlier build, which reads as 0 and simply means the
	// next set is #1.
	Gen int `json:"gen,omitempty"`
}

const killSwitchRuleGroup = "Vair kill-switch"

// The firewall operations, behind variables so a test can exercise the marker
// handling without reaching for the machine's actual firewall.
var (
	ksSetOutbound    = setOutboundActionFor
	ksSetAllOutbound = setOutboundAction
	ksRemoveRules    = removeKillSwitchRules
	ksAddRules       = addKillSwitchAllowRules
	ksRemoveOldRules = removeKillSwitchRulesOtherThan
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
	// machine reachable; the other way round it leaves it silent. And the list
	// goes in as a whole or not at all — the script stops at the first rule it
	// cannot create, and what got in is swept before anything is blocked.
	const gen = 1
	if err := addKillSwitchAllowRules(iface, gen); err != nil {
		removeKillSwitchRules()
		return err
	}
	if err := setOutboundAction("Block"); err != nil {
		removeKillSwitchRules()
		return err
	}

	st := killSwitchState{Previous: prev, Iface: iface, Since: time.Now(), PID: os.Getpid(), Gen: gen}
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
//
// Made before broken. The new set is installed under the next generation while
// the old one is still standing, and the old one comes down only afterwards —
// so at no point is the door shut over an empty allow list. If the new set
// cannot be installed, the old one is left exactly where it was: it names the
// tunnel that has just gone, which is of no use to the new one, but it still
// allows the engine and the loopback, and that is the difference between a
// machine that can rebuild the tunnel and one that cannot.
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
	gen := st.Gen + 1
	if err := ksAddRules(iface, gen); err != nil {
		// The previous set still stands, so the machine is not sealed in — but
		// nothing allows the new tunnel, and it will not carry traffic until
		// this succeeds. Say so loudly: this is the one failure here that costs
		// the user their connection rather than their protection.
		ksRemoveOldRules(st.Gen) // sweep whatever half of the new set got in
		vlog("error", "kill-switch: could not allow the new tunnel (%v) — traffic stays blocked; disconnect to release it", err)
		return err
	}
	ksRemoveOldRules(gen)
	st.Gen = gen
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
	// Every profile, or none of it counts. "At least one worked" was the old
	// test and it let a machine out of here with one profile still blocking —
	// the marker deleted, the chip gone, and the block standing on whichever
	// network the user happened to be on.
	restored := len(st.Previous) > 0
	for profile, action := range st.Previous {
		if action == "" {
			action = "Allow"
		}
		if !isFirewallProfileName(profile) {
			continue // junk from an older marker: not a profile, not restorable
		}
		if err := ksSetOutbound(profile, action); err != nil {
			vlog("warning", "kill-switch: the %s profile would not go back to %q (%v)", profile, action, err)
			restored = false
		}
	}
	if !restored {
		if err := ksSetAllOutbound("Allow"); err != nil {
			// The marker stays. Deleting it here left the one machine that
			// needs it most — blocked, with the restore refused — with nothing
			// on disk that knows what to put back and no button that could do
			// anything, because every path checks the marker first.
			vlog("error", "kill-switch: the firewall could not be put back (%v) — the block is still standing; run  Set-NetFirewallProfile -All -DefaultOutboundAction Allow", err)
			return
		}
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
	// Named profiles and named actions only. Anything else on this stream is
	// noise, and noise written into the marker comes back as a command that
	// cannot run — see the note in psOutput.
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		name, action, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name, action = strings.TrimSpace(name), strings.TrimSpace(action)
		if !isFirewallProfileName(name) || !isOutboundAction(action) {
			continue
		}
		m[name] = action
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

// addKillSwitchAllowRules opens exactly what has to keep moving. gen names the
// set it belongs to — see genRuleName.
func addKillSwitchAllowRules(iface string, gen int) error {
	if _, err := psOutput(killSwitchAllowScript(iface, gen)); err != nil {
		return fmt.Errorf("firewall allow rules: %w", err)
	}
	return nil
}

// genRuleName stamps a rule with the set it belongs to.
//
// Re-pointing the block at a new tunnel used to remove the whole allow list and
// then build it again, which left a moment — a PowerShell round trip — with the
// door shut and nothing allowed through it. Not a leak: a machine that goes
// silent in the middle of a switch, including for the engine that is trying to
// bring the new tunnel up. Windows Firewall offers no transaction to avoid
// that, so the two sets are made able to coexist: the new one is installed
// under its own generation, and the previous one comes down only once the new
// one is standing.
func genRuleName(name string, gen int) string { return fmt.Sprintf("%s #%d", name, gen) }

// removeKillSwitchRulesOtherThan takes down every rule of the group that does
// not belong to this generation. Best-effort, like the rest of the firewall
// calls: a rule left behind is an exception too many, not a block too many.
func removeKillSwitchRulesOtherThan(gen int) {
	_, _ = psOutput(fmt.Sprintf(
		`Get-NetFirewallRule -Group '%s' -ErrorAction SilentlyContinue | `+
			`Where-Object { $_.DisplayName -notlike '*#%d' } | `+
			`Remove-NetFirewallRule -ErrorAction SilentlyContinue`,
		killSwitchRuleGroup, gen))
}

// killSwitchAllowScript builds that list. Separate from running it so the shape
// of the exceptions can be checked without touching a firewall.
//
// All of it or none of it. Windows Firewall has no transaction to put this in,
// and a half-installed allow list is the worst of the three outcomes: the door
// shuts behind it and the machine is inside a block missing the exception that
// would have let it out. PowerShell reports a rule it could not create as a
// non-terminating error and carries on to the next one by default, exiting 0 —
// so the preference is raised to Stop, which makes the first failure end the
// script with a non-zero code. The caller then removes the group and the
// engage path never reaches the block.
func killSwitchAllowScript(iface string, gen int) string {
	exe, _ := os.Executable()
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	rule := func(name, args string) {
		fmt.Fprintf(&b, "New-NetFirewallRule -DisplayName '%s' -Group '%s' -Direction Outbound -Action Allow %s | Out-Null\n",
			genRuleName(name, gen), killSwitchRuleGroup, args)
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
	// No rule for loopback, deliberately.
	//
	// There used to be one — "-RemoteAddress 127.0.0.0/8,::1" — and Windows
	// refused it every single time: the address of a firewall rule must be a
	// real unicast/multicast/broadcast address, and the loopback range is none
	// of those. For most of this program's life that refusal was a warning
	// PowerShell walked past, so the block went up anyway and nobody noticed.
	// Then the script was made to stop at the first failure — so that a
	// half-installed allow list could never be shut behind a block — and this
	// invalid rule started taking the whole thing down with it: no allow list,
	// no block, a kill switch that silently did nothing and a machine that
	// went on using its ordinary connection.
	//
	// Nothing is lost by dropping it. Windows Firewall does not filter loopback
	// traffic at all: what a program sends to 127.0.0.1 or ::1 never reaches
	// the filtering engine, so the local proxy ports, the web interface and
	// everything else on this machine keep working under the block without a
	// rule to permit them.
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
// nlPS is the newline used when prefixing a script — PowerShell reads
// carriage returns fine, but a bare \n keeps the encoded command shorter.
const nlPS = "\n"

func psOutput(script string) (string, error) {
	// The progress stream is silenced for every script, and that is a bug fix
	// rather than tidiness. PowerShell writes progress records — "Preparing
	// modules for first use", the per-command bar — to a stream that a
	// non-interactive host renders as CLIXML on stderr, and CombinedOutput
	// hands them back mixed into the output we parse. One of those records was
	// read as a firewall profile called `<Objs Version` and written into the
	// marker; the release then tried to restore a profile by that name, the
	// command failed, and a real profile was left blocking behind it — a
	// machine whose disconnect left the firewall half shut.
	cmd := runHidden("powershell.exe", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-EncodedCommand",
		encodePS("$ProgressPreference = 'SilentlyContinue'"+nlPS+script))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%v: %s", err, psErrorText(string(out)))
	}
	return string(out), nil
}

// psErrorText pulls the readable part out of what PowerShell writes when its
// error stream is redirected: a CLIXML document, several kilobytes of it, with
// the actual message spread across <S S="Error"> fragments and the newlines
// spelled out as _x000D__x000A_. Logged raw, one failed firewall rule filled the
// screen and said nothing; this is the same failure in one line.
func psErrorText(out string) string {
	out = strings.TrimSpace(out)
	if !strings.Contains(out, "#< CLIXML") {
		return out
	}
	var b strings.Builder
	for _, part := range strings.Split(out, `<S S="Error">`)[1:] {
		frag, _, ok := strings.Cut(part, "</S>")
		if !ok {
			continue
		}
		frag = strings.ReplaceAll(frag, "_x000D__x000A_", " ")
		frag = strings.ReplaceAll(frag, "_x000D_", " ")
		frag = strings.ReplaceAll(frag, "_x000A_", " ")
		if frag = strings.TrimSpace(frag); frag != "" {
			if b.Len() > 0 {
				b.WriteString(" ")
			}
			b.WriteString(frag)
		}
	}
	if b.Len() == 0 {
		return out
	}
	// The tail is PowerShell describing its own stack; the first sentence is
	// what went wrong.
	msg := b.String()
	if i := strings.Index(msg, "+ CategoryInfo"); i > 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	return msg
}

// forceReleaseBlock is the button of last resort: put this machine back on its
// network, whatever the marker says.
//
// Everything else keys off the marker, which is right — it remembers what the
// firewall looked like before, so a release restores exactly that. But it also
// means a marker that is missing, unreadable, or written by a run whose restore
// failed leaves a blocked machine with no way out from inside the program. The
// user pressing the red button is not asking us to be careful; they are asking
// for their network back.
func forceReleaseBlock() {
	// Without administrator rights none of the commands below can run, and the
	// person pressing the button is exactly the person who cannot afford to be
	// told to go and find a terminal: their machine has no network. Ask Windows
	// for the rights instead — one prompt, for one command — and leave the
	// program itself unelevated.
	//
	// It happens whenever we are not elevated, not only on failure: trying
	// first would spend several seconds on commands that are certain to be
	// refused, and put "access is denied" in the log ahead of the prompt.
	if !isPrivileged() {
		releaseBlockElevated()
		return
	}
	releaseKillSwitch()
	if !anyProfileBlocking() {
		// Nothing is blocking, so nothing may go on claiming otherwise. This
		// clear is the whole reason the chip stopped being a lie: a block found
		// at startup without a marker set that flag, an ordinary disconnect then
		// released the firewall without touching it, and the connection bar went
		// on showing "traffic blocked" over a machine that was plainly online —
		// pressing it just brought the chip back.
		blockStandingNoMarker.Store(false)
		return
	}
	// Still blocking after a proper release: the marker was missing or wrong.
	vlog("warning", "kill-switch: the block is still standing after the release — opening the firewall directly")
	killSwitchMu.Lock()
	defer killSwitchMu.Unlock()
	ksRemoveRules()
	if err := ksSetAllOutbound("Allow"); err != nil {
		vlog("error", "kill-switch: the firewall refused to open (%v) — run  Set-NetFirewallProfile -All -DefaultOutboundAction Allow  as administrator", err)
		return
	}
	_ = os.Remove(killSwitchMarker())
	blockStandingNoMarker.Store(false)
	vlog("info", "kill-switch: firewall opened")
}

// anyProfileBlocking reports whether any profile is still refusing outbound
// traffic by default.
func anyProfileBlocking() bool {
	out, err := psOutput(firewallStateScript)
	if err != nil {
		return false
	}
	_, profiles := parseFirewallState(out)
	for _, p := range profiles {
		if strings.EqualFold(p.outbound, "Block") {
			return true
		}
	}
	return false
}

// noteStrayBlockAtStart looks for a block standing without a marker to describe
// it, and remembers what it finds so the interface can offer to lift it.
//
// The question is whose block it is. A machine that blocks outbound traffic by
// its own policy is not ours to open, and the chip must not appear on one. The
// evidence used is the setting: "keep blocking after the tunnel is gone" is on,
// nothing else here asked for a block, and the firewall is shut — that is our
// doing, from a run that ended badly. With the setting off we look at nothing
// and claim nothing.
func noteStrayBlockAtStart() {
	if killSwitchHeld() || !persistentKillSwitchEnabled() {
		return
	}
	if !anyProfileBlocking() {
		return
	}
	blockStandingNoMarker.Store(true)
	vlog("warning", "kill-switch: traffic is blocked at the firewall from an earlier run and there is no record of what to put back — release it from the connection bar, or connect")
	announceBlockState()
}

// isFirewallProfileName says whether a name is one of the three Windows
// firewall profiles. Used to keep everything else out of the marker, and out of
// the commands built from it.
func isFirewallProfileName(s string) bool {
	switch strings.ToLower(s) {
	case "domain", "private", "public":
		return true
	}
	return false
}

// isOutboundAction says the same for the value beside it.
func isOutboundAction(s string) bool {
	switch strings.ToLower(s) {
	case "allow", "block", "notconfigured":
		return true
	}
	return false
}

// releaseBlockElevated opens the firewall through a single UAC prompt.
//
// The alternative was relaunching the whole program as administrator, which is
// what TUN mode does — but that is a heavier thing to ask for, it restarts the
// window, and it is not what the button says. One command, one prompt, the
// program stays as it was.
//
// The marker is removed only if the firewall is actually open afterwards. A
// prompt that was dismissed, or a command that failed inside the elevated
// process, must leave the machine describable: the block is still standing and
// the chip has to stay on the bar.
func releaseBlockElevated() {
	vlog("info", "kill-switch: asking Windows for permission to open the firewall")
	if _, err := psOutput(elevatedOpenScript()); err != nil {
		vlog("error", "kill-switch: the firewall was not opened (%v) — allow the prompt, or run  Set-NetFirewallProfile -All -DefaultOutboundAction Allow  as administrator", err)
		return
	}
	if anyProfileBlocking() {
		vlog("error", "kill-switch: the firewall is still blocking after the elevated attempt — run  Set-NetFirewallProfile -All -DefaultOutboundAction Allow  as administrator")
		return
	}
	_ = os.Remove(killSwitchMarker())
	blockStandingNoMarker.Store(false)
	vlog("info", "kill-switch: firewall opened")
}

// elevatedOpenScript is what that prompt runs. Separate from running it so its
// shape can be tested without a firewall or a UAC dialog.
func elevatedOpenScript() string {
	inner := "Set-NetFirewallProfile -All -DefaultOutboundAction Allow; " +
		"Get-NetFirewallRule -Group '" + killSwitchRuleGroup + "' -ErrorAction SilentlyContinue | Remove-NetFirewallRule"
	// Start-Process is what raises the prompt; -Wait is what lets us check the
	// result afterwards rather than reporting success at the moment we asked.
	// The inner command is encoded, so no quoting of ours can be broken by the
	// argument list in between.
	return "$ErrorActionPreference = 'Stop'; " +
		"Start-Process -FilePath 'powershell.exe' -Verb RunAs -WindowStyle Hidden -Wait " +
		"-ArgumentList '-NoProfile','-NonInteractive','-EncodedCommand','" + encodePS(inner) + "'"
}

// checkBlockAfterRestart reconciles the marker with the firewall at a start,
// and tells the user when the machine has come up with no way out.
//
// The case that brings people here is a reboot for Windows updates with the
// block standing. Everything it needs survives that — the outbound default and
// our allow rules are both persistent policy — so the machine comes back
// exactly as protected as it was, which is the point of the setting and also
// the moment it is most surprising: no browser, no mail, no update service,
// and Vair possibly not even started.
//
// Two things are worth doing about it. Say so where the user is looking, once,
// unless the session is being restored anyway (the tunnel is on its way back
// and a warning about a state that lasts three seconds is noise). And handle
// the opposite mismatch: a firewall that is NOT blocking any more, which
// happens when something resets it — an in-place Windows upgrade, a
// "reset firewall to defaults", another security product. The marker would go
// on claiming a block that is not there, the chip would sit on the connection
// bar over a working connection, and the only way to clear it would be to press
// a button that has nothing to do.
func checkBlockAfterRestart() {
	blocking, known := firewallBlocking()
	if !known {
		return // could not read it; claim nothing
	}
	if !blocking {
		_ = os.Remove(killSwitchMarker())
		blockStandingNoMarker.Store(false)
		vlog("warning", "kill-switch: the block is no longer in the firewall — something else opened it (a Windows upgrade or a firewall reset does this). The record of it has been dropped")
		announceBlockState()
		return
	}
	if restoreWanted() {
		return // the tunnel is coming back on its own; nothing to alarm about
	}
	desktopNotify("Vair", notifyBlockedAfterStart())
}

// firewallBlocking reads the firewall and says whether anything is blocking
// outbound traffic by default — and whether it could be read at all. The
// second half matters: "no" and "cannot tell" lead to opposite actions when
// the question is whether to throw away the only record of a standing block.
func firewallBlocking() (blocking, known bool) {
	out, err := psOutput(firewallStateScript)
	if err != nil {
		return false, false
	}
	_, profiles := parseFirewallState(out)
	if len(profiles) == 0 {
		return false, false
	}
	for _, p := range profiles {
		if strings.EqualFold(p.outbound, "Block") {
			return true, true
		}
	}
	return false, true
}
