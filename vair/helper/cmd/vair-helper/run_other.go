//go:build !linux && !windows

package main

import "errors"

// run refuses everywhere but Linux and Windows. The file exists so that
// `go build ./...` and `go vet ./...` stay green for any target — a package
// with no buildable files for it is a hard error, not a skip.
func run(options) error {
	return errors.New("vair-helper runs on Linux and Windows only")
}
