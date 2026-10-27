//go:build !windows

// Command helper-probe drives the Windows helper service; there is nothing to
// drive elsewhere.
package main

import "fmt"

func main() { fmt.Println("helper-probe is for the Windows helper service") }
