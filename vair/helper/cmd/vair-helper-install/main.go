// Command vair-helper-install enables the privileged helper for one account.
//
// It is what pkexec runs, so it is the ONE moment the user is asked for a
// password — everything after it happens over the socket with no prompt. That
// is the whole design goal: install once, then never again.
//
// Run by the GUI as:
//
//	pkexec /usr/lib/vair/vair-helper-install
//	pkexec /usr/lib/vair/vair-helper-install --uninstall
//
// Platform-neutral shell; the work is in install_linux.go so `go build ./...`
// stays green on the development machine.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	uninstall := flag.Bool("uninstall", false, "stop and disable the helper instead of installing it")
	flag.Parse()

	if err := install(*uninstall); err != nil {
		fmt.Fprintf(os.Stderr, "vair-helper-install: %v\n", err)
		os.Exit(1)
	}
}
