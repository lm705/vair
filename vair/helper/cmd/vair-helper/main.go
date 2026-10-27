// Command vair-helper is the privileged half of Vair's TUN mode: a small
// service that owns the tunnel so the GUI never has to run as root or as
// administrator.
//
// It speaks the protocol in helper/api — over a unix socket that systemd
// creates with the installing user's group on Linux, over a named pipe on
// Windows — and it runs exactly one binary, the sing-box installed beside it,
// against a configuration it validates first.
//
// This file is platform-neutral. The Linux implementation is in run_linux.go,
// the Windows service in run_windows.go; everywhere else run() refuses.
package main

import (
	"flag"
	"fmt"
	"os"
)

// options are the command line. Most of it is for the setup program, which
// is the only thing that installs, repairs or removes the Windows service.
type options struct {
	cleanup   bool // Linux: remove leftovers of a previous run and exit (ExecStartPre)
	install   bool // Windows: register the service and start it
	uninstall bool // Windows: stop it, take every block and rule of ours down, remove it
	repair    bool // Windows: register it again if it is missing, and start it
	stop      bool // Windows: stop it, before the setup program replaces its files
}

func main() {
	var o options
	flag.BoolVar(&o.cleanup, "cleanup", false,
		"remove leftovers from a previous run and exit; ExecStartPre uses this (Linux)")
	flag.BoolVar(&o.install, "install-service", false,
		"register the Windows service and start it; the setup program runs this")
	flag.BoolVar(&o.uninstall, "uninstall-service", false,
		"stop the Windows service, take down every block and rule of Vair's and remove the service")
	flag.BoolVar(&o.repair, "repair", false,
		"register the Windows service again if it is missing, and start it")
	flag.BoolVar(&o.stop, "stop-service", false,
		"stop the Windows service; the setup program runs this before replacing files")
	flag.Parse()

	if err := run(o); err != nil {
		fmt.Fprintf(os.Stderr, "vair-helper: %v\n", err)
		os.Exit(1)
	}
}
