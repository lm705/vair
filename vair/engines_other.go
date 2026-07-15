//go:build !windows

package main

// Engine extraction is Windows-only for now; the Linux port (Ф6) ships its own
// engine binaries and extraction.
func extractEngines() error { return nil }
