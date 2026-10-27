package core

// The tunnel's block on Linux is the helper's (tunnelblock_helper.go).

func engageTunnelBlock(string) { helperEngageTunnelBlock() }
func releaseTunnelBlock()      { helperReleaseTunnelBlock() }
func tunnelBlockHeld() bool    { return helperTunnelBlockHeld() }

// handOverTunnelBlock has nothing left to do here: the teardown already asked
// the helper to keep the block (StopForHandover), and the helper runs the grace.
func handOverTunnelBlock() {}

// The proxy kill-switch is Windows-only (killswitch_other.go): there is no
// session block to put up for it here.
const proxyBlockIsSessions = false

func engageProxySessionBlock() (bool, error) { return false, nil }
