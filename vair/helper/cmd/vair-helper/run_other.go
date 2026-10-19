//go:build !linux

package main

import "errors"

// run refuses everywhere but Linux. The file exists so that `go build ./...`
// and `go vet ./...` stay green on the development machine — a package with no
// buildable files for the target is a hard error, not a skip.
func run(bool) error {
	return errors.New("vair-helper runs on Linux only")
}
