//go:build !windows && !linux

package core

// No tunnel block where there is no TUN mode to hold one for.
func engageTunnelBlock(string) {}
func releaseTunnelBlock()      {}
func handOverTunnelBlock()     {}
func tunnelBlockHeld() bool    { return false }

// The proxy kill-switch is Windows-only (killswitch_other.go): there is no
// session block to put up for it here.
const proxyBlockIsSessions = false

func engageProxySessionBlock() (bool, error) { return false, nil }
