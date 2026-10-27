//go:build !windows && !linux

package core

// leakProbe: Windows and Linux have one (leakcheck_windows.go,
// leakcheck_linux.go); elsewhere there is no TUN mode to check.
func leakProbe(leakClaim) leakCheckResult {
	return leakCheckResult{verdict: leakNoProbe}
}
