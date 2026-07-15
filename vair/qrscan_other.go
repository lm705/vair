//go:build !windows

package main

import "fmt"

// QR scanning is Windows-only for now (GDI screen capture).
func scanQRFromFile() (string, error)   { return "", fmt.Errorf("not supported") }
func scanQRFromScreen() (string, error) { return "", fmt.Errorf("not supported") }
