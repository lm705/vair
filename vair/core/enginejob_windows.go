package core

import (
	"os/exec"

	"vair/helper/winsys"
)

// The engines end with Vair.
//
// Every engine Vair starts goes into one job object that kills its members
// when it is closed — and Windows closes it when this process ends, however it
// ends. Before, a crash of Vair left sing-box and xray running on their own:
// the TUN adapter and its routes stayed, the kill-switch's filters (a session
// of this process) were gone, DNS stopped answering, and the machine sat in a
// half-tunnel nobody was driving — some traffic through it, programs bound to
// the physical adapter around it. Now a crash is the end of the tunnel too, and
// the machine is back on its ordinary connection, which is what the kill-switch
// without "persistent" promises once Vair is gone; holding traffic after that
// is the persistent kill-switch's job, and its filters do not depend on the
// engines. Test engines (PING, SPEED, AUTO) go into the same job.

// tieToVair puts a started engine into the job (winsys.TieToOwner). Called
// right after Start; never fatal — an engine that could not be tied is the
// engine as it was before, and the log says so.
func tieToVair(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := winsys.TieToOwner(cmd.Process.Pid); err != nil {
		vlog("warning", "engine %d: not tied to Vair (%v) — it would outlive a crash", cmd.Process.Pid, err)
	}
}
