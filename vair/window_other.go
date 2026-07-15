//go:build !windows

package main

// initialWindowSize is the non-Windows fallback until the Linux shell lands.
func initialWindowSize() (int, int) { return 1100, 720 }

// applyWindowIcon is Windows-only (WM_SETICON); no-op elsewhere.
func applyWindowIcon() {}
