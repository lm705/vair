package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"vair/helper/wfp"
	"vair/helper/winsys"
)

// The kill-switch that outlives the tunnel.
//
// sing-box's own strict_route already drops anything trying to leave outside
// the tunnel — but that filter belongs to the engine process, so it goes when
// the engine goes. A crash, a kill, or this program being closed all end the
// protection at the moment it is most wanted: the tunnel is gone and traffic
// resumes on the physical adapter, unprotected and unannounced.
//
// This holds the line in the Windows Filtering Platform instead — see
// wfp_windows.go for why not Windows Firewall, and wfpblock_windows.go for
// what is let through. The filters are persistent: they stay after this
// program exits and after a reboot, which is the point, and they come down
// when the user says so — a disconnect, the setting turned off, the red button
// on the connection bar, or, without the program's window,
// `Vair.exe --release-block` or the release-block.ps1 script written next to
// the marker.
//
// Builds before 2.7.0 did this with Windows Firewall's default outbound action
// and a group of allow rules. A block of that kind can still be standing when
// this build starts, so the code that takes it down is kept — see
// releaseLegacyBlockLocked — and nothing else of it.

// killSwitchState is the marker written to disk while the block is engaged.
type killSwitchState struct {
	// Engine is "wfp" for a block put up by this build. Empty means Windows
	// Firewall, from before 2.7.0.
	Engine string `json:"engine,omitempty"`
	// Previous is, for a Windows Firewall block, the DefaultOutboundAction each
	// profile had before, so its release restores what was there.
	Previous map[string]string `json:"previous,omitempty"`
	Iface    string            `json:"iface"`
	Since    time.Time         `json:"since"`
	PID      int               `json:"pid"`
	Gen      int               `json:"gen,omitempty"`
}

// legacy says the marker describes a Windows Firewall block.
func (st killSwitchState) legacy() bool { return st.Engine == "" }

// The operations, behind variables so a test can exercise the marker handling
// without reaching for the machine's actual firewall.
var (
	ksSetOutbound    = setOutboundActionFor
	ksSetAllOutbound = setOutboundAction
	ksRemoveRules    = removeKillSwitchRules
	ksBlockOptions   = currentBlockOptions
	ksPrivileged     = blocksAvailable
)

// blocksAvailable says whether the blocks can be put up and taken down: by the
// helper service, or by this program when it runs as administrator.
func blocksAvailable() bool { return serviceMode() || isPrivileged() }

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

// readKillSwitchState reads the marker, and says whether there was one.
func readKillSwitchState() (killSwitchState, bool) {
	var st killSwitchState
	data, err := os.ReadFile(killSwitchMarker())
	if err != nil {
		return st, false
	}
	_ = json.Unmarshal(data, &st)
	return st, true
}

// engageKillSwitch blocks everything that is not the tunnel.
//
// iface is the TUN interface's name, whose traffic is let through; "" for the
// proxy kill-switch. Called again for every new tunnel while a block stands —
// the name is different each time on Windows — and then it replaces the whole
// set in one transaction, so a switch between configurations has no moment
// without a block and no moment with the new tunnel shut out.
func engageKillSwitch(iface string) error {
	killSwitchMu.Lock()
	defer killSwitchMu.Unlock()
	if !ksPrivileged() {
		return fmt.Errorf("the persistent kill-switch needs the same privileges as TUN mode")
	}
	opts, err := ksBlockOptions(iface)
	if err != nil {
		return err
	}
	prev, had := readKillSwitchState()
	if err := wfpInstallBlock(opts); err != nil {
		return fmt.Errorf("the block could not be put in place: %w", err)
	}
	// A Windows Firewall block from an earlier build comes down only now that
	// this one is standing: never a moment with neither.
	if had && prev.legacy() {
		releaseLegacyBlockLocked(prev)
	}
	st := killSwitchState{Engine: "wfp", Iface: iface, Since: time.Now(), PID: os.Getpid()}
	if had && !prev.legacy() && !prev.Since.IsZero() {
		st.Since = prev.Since
	}
	// Durable: this file is how the block gets taken down again. Written in
	// place and cut short by a crash or a restart, it would leave a standing
	// block beside a record nobody can read.
	if data, err := json.MarshalIndent(st, "", "  "); err == nil {
		_ = writeFileDurable(killSwitchMarker(), data, 0o600)
	}
	writeReleaseScript()
	switch {
	case had && !prev.legacy() && prev.Iface != iface:
		vlog("info", "kill-switch: block re-pointed at %s (traffic stayed blocked through the switch)", iface)
	case !had || prev.legacy():
		vlog("warning", "kill-switch: traffic outside the tunnel is blocked, and stays blocked if Vair stops. Release: disconnect, or run  %s", releaseHint())
	}
	return nil
}

// heldBlockMode is the kind of session the standing block was put up for:
// TUN when its record names a tunnel, proxy when it names none.
func heldBlockMode() (ConnMode, bool) {
	st, had := readKillSwitchState()
	if !had || st.legacy() {
		return "", false
	}
	if st.Iface == "" {
		return ModeProxy, true
	}
	return ModeTUN, true
}

// releaseHint is the way out that works without the program's window: the
// program itself with --release-block, or the script beside the marker when
// the program is gone.
func releaseHint() string {
	exe, _ := os.Executable()
	if serviceMode() {
		// The service takes it down for whoever asks; the script stays the
		// way out for when the program is gone, and that one does need rights.
		return fmt.Sprintf("\"%s\" --release-block  (or, as administrator, powershell -ExecutionPolicy Bypass -File \"%s\")", exe, releaseScriptPath())
	}
	return fmt.Sprintf("\"%s\" --release-block  as administrator (or powershell -ExecutionPolicy Bypass -File \"%s\")", exe, releaseScriptPath())
}

// releaseKillSwitch takes the block down. Safe to call when nothing is
// engaged, and safe to call from a different run of the program than the one
// that engaged it — which is exactly the case it exists for.
//
// The marker is removed only when the block is really gone: a release that was
// refused must leave the machine describable, with the chip still on the bar.
func releaseKillSwitch() {
	killSwitchMu.Lock()
	defer killSwitchMu.Unlock()
	st, had := readKillSwitchState()
	present, known := wfpBlockPresent()
	if !had && !(known && present) {
		return // nothing of ours to take down
	}
	if err := wfpRemoveBlock(); err != nil {
		vlog("error", "kill-switch: the block could not be taken down (%v) — it is still standing; run  %s", err, releaseHint())
		return
	}
	if had && st.legacy() && !releaseLegacyBlockLocked(st) {
		return
	}
	_ = os.Remove(killSwitchMarker())
	removeReleaseScript()
	vlog("info", "kill-switch: block released")
}

// releaseLegacyBlockLocked takes down a Windows Firewall block of the kind
// builds before 2.7.0 put up: the default outbound action back to what the
// marker recorded, the allow-rule group removed. Reports whether it managed.
func releaseLegacyBlockLocked(st killSwitchState) bool {
	ksRemoveRules()
	// Restore per profile, falling back to Allow for anything the marker does
	// not name: a machine with no way out is a worse failure than a firewall
	// that is one setting more permissive than it was.
	// Every profile, or none of it counts. "At least one worked" was the old
	// test and it let a machine out of here with one profile still blocking.
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
			vlog("error", "kill-switch: the firewall could not be put back (%v) — the block is still standing; run  %s", err, releaseHint())
			return false
		}
	}
	return true
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

// forceReleaseBlock is the button of last resort: put this machine back on its
// network, whatever the marker says.
//
// Without administrator rights nothing below can run, and the person pressing
// the button is exactly the person who cannot afford to be told to go and find
// a terminal: their machine has no network. So Windows is asked for the rights
// instead — one prompt, for one run of this program with --release-block — and
// the program itself stays unelevated. It happens whenever we are not elevated,
// not only on failure: trying first would spend seconds on calls certain to be
// refused, and put "access is denied" in the log ahead of the prompt.
func forceReleaseBlock() {
	if !ksPrivileged() {
		if unattendedExit.Load() {
			// Windows is ending the session: a prompt now would hold the
			// shutdown up with nobody there to answer it. The next start takes
			// the block down — it is not the persistent one, or this would not
			// be running.
			vlog("warning", "kill-switch: the block needs administrator rights to come down and Windows is ending the session — the next start of Vair releases it")
			return
		}
		ksElevate()
		return
	}
	releaseKillSwitch()
	if standing, _ := blockStandsNow(); !standing {
		// Nothing is blocking, so nothing may go on claiming otherwise: a flag
		// left set here put "traffic blocked" on the bar over a machine that
		// was plainly online.
		blockStandingNoMarker.Store(false)
		legacyStray.Store(false)
		return
	}
	// Still standing after a proper release: the marker was missing or wrong.
	vlog("warning", "kill-switch: the block is still standing after the release — taking it down directly")
	if err := openEverythingOfOurs(); err != nil {
		vlog("error", "kill-switch: the block would not come down (%v) — run  %s", err, releaseHint())
		return
	}
	_ = os.Remove(killSwitchMarker())
	removeReleaseScript()
	blockStandingNoMarker.Store(false)
	legacyStray.Store(false)
	vlog("info", "kill-switch: block released")
}

// openEverythingOfOurs takes down every block this program could have put up,
// with or without a record of it: all our WFP filters, and — where there is
// evidence it was ours — a Windows Firewall block from an earlier build.
func openEverythingOfOurs() error {
	killSwitchMu.Lock()
	defer killSwitchMu.Unlock()
	if err := wfpRemoveBlock(); err != nil {
		return err
	}
	if legacyEvidence() {
		ksRemoveRules()
		if err := ksSetAllOutbound("Allow"); err != nil {
			return err
		}
	}
	return nil
}

// blockInstalled reports whether any block of ours is in place: a marker, or
// our filters with or without one.
func blockInstalled() bool {
	if killSwitchHeld() {
		return true
	}
	standing, _ := blockStandsNow()
	return standing
}

// legacyStray remembers that the block found at start was a Windows Firewall
// one with no marker: the only case where opening the firewall's default
// action is ours to do without a record saying so.
var legacyStray atomic.Bool

// legacyEvidence says whether a Windows Firewall block standing now is ours:
// a marker from an earlier build, our allow-rule group still in the
// firewall, or the stray block found at start.
func legacyEvidence() bool {
	if st, had := readKillSwitchState(); had && st.legacy() {
		return true
	}
	if legacyStray.Load() {
		return true
	}
	// Not while Windows is ending the session: this is a PowerShell launch,
	// and the seconds it takes are the ones the rest of the teardown needs.
	if unattendedExit.Load() {
		return false
	}
	present, _ := winsys.LegacyRulesPresent()
	return present
}

// blockStandsNow says whether a block of ours is in force right now, and
// whether that could be told at all. Our WFP filters are asked directly; a
// Windows Firewall block counts only with evidence that it is ours.
func blockStandsNow() (standing, known bool) {
	present, known := wfpBlockPresent()
	if known && present {
		return true, true
	}
	if legacyEvidence() {
		return firewallBlocking()
	}
	return false, known
}

// anyProfileBlocking reports whether any profile is still refusing outbound
// traffic by default.
func anyProfileBlocking() bool {
	blocking, _ := winsys.AnyProfileBlocking()
	return blocking
}

// noteStrayBlockAtStart looks for a block standing without a marker to describe
// it, and remembers what it finds so the interface can offer to lift it.
//
// Our WFP filters answer the question of whose block it is by themselves: they
// carry our provider and our keys, so if they are there, they are ours. For a
// Windows Firewall block from an earlier build the evidence is weaker, and the
// old rule stands: "keep blocking after the tunnel is gone" is on, nothing
// else here asked for a block, and the firewall is shut.
func noteStrayBlockAtStart() {
	if killSwitchHeld() {
		return
	}
	if present, known := wfpBlockPresent(); known && present {
		blockStandingNoMarker.Store(true)
		vlog("warning", "kill-switch: traffic is blocked from an earlier run and there is no record of it — release it from the connection bar, or connect")
		announceBlockState()
		return
	}
	if !persistentKillSwitchEnabled() || !anyProfileBlocking() {
		return
	}
	legacyStray.Store(true)
	blockStandingNoMarker.Store(true)
	vlog("warning", "kill-switch: traffic is blocked at the firewall from an earlier run and there is no record of what to put back — release it from the connection bar, or connect")
	announceBlockState()
}

// releaseBlockElevated takes the block down through a single UAC prompt: this
// program again, elevated, with --release-block, which does the release and
// exits. The alternative, relaunching the whole program as administrator, is
// heavier than what the button says and restarts the window.
//
// The marker is removed only if the block is actually gone afterwards. A prompt
// that was dismissed, or a release that failed inside the elevated process,
// must leave the machine describable: the chip has to stay on the bar.
// ksElevate is releaseBlockElevated, behind a variable for the tests.
var ksElevate = releaseBlockElevated

// ourFiltersPresent reports whether our block's filters are in the filtering
// engine — answered only where it can be asked; without the rights to ask,
// "no" is the answer, and the marker is what the caller goes by.
func ourFiltersPresent() bool {
	present, known := wfpBlockPresent()
	return known && present
}

func releaseBlockElevated() {
	vlog("info", "kill-switch: asking Windows for permission to take the block down")
	if _, err := psOutputFor(elevatedOpenScript(), 10*time.Minute); err != nil {
		vlog("error", "kill-switch: the block was not taken down (%v) — allow the prompt, or run  %s", err, releaseHint())
		return
	}
	if standing, known := blockStandsNow(); standing || !known && killSwitchHeld() {
		// !known here is an unelevated process unable to read the filters. The
		// elevated run removes the marker when it succeeds, so the marker is
		// the answer: still there means still standing.
		if standing || killSwitchHeld() {
			vlog("error", "kill-switch: the block is still standing after the elevated attempt — run  %s", releaseHint())
			return
		}
	}
	_ = os.Remove(killSwitchMarker())
	blockStandingNoMarker.Store(false)
	legacyStray.Store(false)
	vlog("info", "kill-switch: block released")
}

// elevatedOpenScript is what that prompt runs. Separate from running it so its
// shape can be tested without a UAC dialog.
func elevatedOpenScript() string {
	exe, _ := os.Executable()
	// Start-Process is what raises the prompt; -Wait is what lets us check the
	// result afterwards rather than reporting success at the moment we asked.
	return "$ErrorActionPreference = 'Stop'; " +
		"Start-Process -FilePath '" + psQuote(exe) + "' -ArgumentList '--release-block' -Verb RunAs -WindowStyle Hidden -Wait"
}

// ReleaseBlockFromCommandLine is `Vair.exe --release-block`: take down every
// block of ours and report. Run elevated — by the red button through a UAC
// prompt, by the uninstaller, or by hand. Returns the process exit code.
func ReleaseBlockFromCommandLine() int {
	if !ksPrivileged() {
		fmt.Fprintln(os.Stderr, "Vair --release-block: needs to run as administrator")
		return 2
	}
	releaseKillSwitch()
	if standing, _ := blockStandsNow(); standing {
		if err := openEverythingOfOurs(); err != nil {
			fmt.Fprintln(os.Stderr, "Vair --release-block:", err)
			return 1
		}
	}
	if standing, _ := blockStandsNow(); standing {
		fmt.Fprintln(os.Stderr, "Vair --release-block: the block is still standing")
		return 1
	}
	_ = os.Remove(killSwitchMarker())
	removeReleaseScript()
	return 0
}

// checkBlockAfterRestart reconciles the marker with what is actually in force
// at a start, and tells the user when the machine has come up with no way out.
//
// The case that brings people here is a reboot for Windows updates with the
// block standing. The filters are persistent, so the machine comes back exactly
// as protected as it was, which is the point of the setting and also the moment
// it is most surprising: no browser, no mail, no update service, and Vair
// possibly not even started.
//
// Two things are worth doing about it. Say so where the user is looking, once,
// unless the session is being restored anyway (the tunnel is on its way back
// and a warning about a state that lasts three seconds is noise). And handle
// the opposite mismatch: a block that is NOT there any more — something removed
// it — while the marker goes on claiming it, the chip sitting on the bar over a
// working connection with a button that has nothing to do.
func checkBlockAfterRestart() {
	blocking, known := blockStandsNow()
	if !known {
		return // could not read it; claim nothing
	}
	if !blocking {
		_ = os.Remove(killSwitchMarker())
		removeReleaseScript()
		blockStandingNoMarker.Store(false)
		vlog("warning", "kill-switch: the block is no longer in place — something else removed it. The record of it has been dropped")
		announceBlockState()
		return
	}
	if restoreWanted() {
		return // the tunnel is coming back on its own; nothing to alarm about
	}
	desktopNotify("Vair", notifyBlockedAfterStart())
}

// firewallBlocking reads Windows Firewall and says whether anything is blocking
// outbound traffic by default — and whether it could be read at all. Only a
// block from a build before 2.7.0 lives there; the question matters because
// "no" and "cannot tell" lead to opposite actions when it is whether to throw
// away the only record of a standing block.
func firewallBlocking() (blocking, known bool) {
	return winsys.AnyProfileBlocking()
}

// ── the way out without the program ───────────────────────────────────────────

// releaseScriptPath is where release-block.ps1 lives: beside the marker, in the
// data folder, which outlives both an uninstall of the program and a deleted
// Vair.exe.
func releaseScriptPath() string { return filepath.Join(DataDir(), "release-block.ps1") }

// writeReleaseScript leaves a way out that needs nothing of this program.
//
// WFP has no command of its own to remove a filter — no netsh verb, no
// PowerShell cmdlet — so a block whose program has been deleted would have no
// undo, on a machine that cannot download it again because the block is
// standing. This script calls the filtering engine directly with the same keys
// the program uses, generated from the same constants, and takes everything of
// ours down in one transaction.
func writeReleaseScript() {
	_ = writeFileDurable(releaseScriptPath(), []byte(releaseScriptText()), 0o644)
}

func removeReleaseScript() { _ = os.Remove(releaseScriptPath()) }

func releaseScriptText() string {
	return wfp.ReleaseScript(releaseScriptPath(), killSwitchMarker())
}
