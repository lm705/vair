package core

import (
	"fmt"
	"time"
)

// tunIfaceName is unique per session on Windows.
//
// Adapters can survive a crash, and a fresh connect must not collide with a
// ghost left by the last one; the name carries a timestamp so it never does.
// Linux answers differently on purpose — there the helper owns the interface
// and removes it, and its validator only accepts a name it recognises.
func tunIfaceName() string { return fmt.Sprintf("vair-%d", time.Now().Unix()%10000) }

// adaptTUNConfig is a no-op here: the configuration is already shaped for the
// platform that builds it.
func adaptTUNConfig(map[string]interface{}) {}
