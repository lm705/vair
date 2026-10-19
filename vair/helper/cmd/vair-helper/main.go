// Command vair-helper is the privileged half of Vair's TUN mode: a small
// service that owns the tunnel so the GUI never has to run as root.
//
// It speaks the protocol in helper/api over a unix socket that systemd creates
// with the installing user's group, and it runs exactly one binary — the
// packaged sing-box — against a configuration it validates first.
//
// This file is deliberately platform-neutral so `go build ./...` works on the
// Windows machine most of this is written on. The implementation is in
// run_linux.go; everywhere else run() refuses.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	cleanup := flag.Bool("cleanup", false,
		"remove leftovers from a previous run and exit; ExecStartPre uses this")
	flag.Parse()

	if err := run(*cleanup); err != nil {
		fmt.Fprintf(os.Stderr, "vair-helper: %v\n", err)
		os.Exit(1)
	}
}
