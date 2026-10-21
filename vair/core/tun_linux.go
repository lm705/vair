package core

// removeTUNAdapter is a no-op on Linux, and deliberately so.
//
// A tun device here belongs to the process that opened it: when sing-box exits
// the interface disappears with it, and it is gone outright after a reboot
// since it never existed in any persistent registry. There is nothing to reap,
// so there is no PowerShell equivalent to write.
//
// The one case that could leave an interface behind is a sing-box still running
// after the GUI died — and that is killTree's job (see process_linux.go), not
// this function's.
func removeTUNAdapter() {}

// sweepGhostTunnelAtStart is likewise nothing to do here. The tun device is
// owned by the process that opened it and the kernel takes it back, with its
// routes, the moment that process ends — there is no equivalent of a registered
// adapter outliving its engine.
func sweepGhostTunnelAtStart() {}
