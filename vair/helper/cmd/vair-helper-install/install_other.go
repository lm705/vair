//go:build !linux

package main

import "errors"

// install refuses everywhere but Linux; the file exists so `go build ./...`
// works on the development machine, where a package with no buildable files is
// a hard error rather than a skip.
func install(bool) error {
	return errors.New("vair-helper-install runs on Linux only")
}
