//go:build !windows

package core

import "errors"

// The persistent kill-switch is Windows-only for now.
//
// On Linux the block belongs in nftables, added and removed by the privileged
// helper — the same place the TUN device and its routes already live. That work
// is not written yet, and a safety switch that silently does nothing is worse
// than one that is honestly absent, so these are no-ops and the capability says
// so.

func engageKillSwitch(string) error { return errKillSwitchUnavailable }
func releaseKillSwitch()            {}
func releaseStaleKillSwitch()       {}
func forceReleaseBlock()            {}
func killSwitchHeld() bool          { return false }

// The tunnel's block: tunnelblock_windows.go, tunnelblock_linux.go.

// ourFiltersPresent: the block is the helper's on Linux, gone with the tunnel.
func ourFiltersPresent() bool { return false }

// heldBlockMode: there is no block of ours to describe.
func heldBlockMode() (ConnMode, bool) { return "", false }

// releaseHint names the way out without the window; Windows only.
func releaseHint() string { return "" }

// ReleaseBlockFromCommandLine is `--release-block`; the Linux block is the
// helper's and goes with it.
func ReleaseBlockFromCommandLine() int { return 0 }

// blockInstalled reports a block of ours; Windows only.
func blockInstalled() bool { return false }

var errKillSwitchUnavailable = errors.New("the persistent kill-switch is Windows-only for now")

// noteStrayBlockAtStart has nothing to look at where there is no firewall block
// to begin with.
func noteStrayBlockAtStart() {}

// checkBlockAfterRestart has no firewall to reconcile with here.
func checkBlockAfterRestart() {}

// blocksAvailable: there are no blocks of the app's own here.
func blocksAvailable() bool { return isPrivileged() }
