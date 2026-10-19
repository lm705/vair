package core

import (
	"sync"
	"time"
)

// killTreeGrace is how long killTree waits for a child to exit on its own
// before escalating. The engines close their tunnel on the way out, so give
// them a moment — but not so long that disconnecting feels stuck.
const killTreeGrace = 1500 * time.Millisecond

// tunReapMu serialises the ghost-adapter sweep with the creation of a new TUN
// adapter. The sweep runs in the background so a disconnect does not wait for
// it, and it matches every vair-* adapter — including one a fresh connect has
// just made, if the two were ever allowed to overlap.
var (
	tunReapMu   sync.Mutex
	tunReapDone int // bumped by each sweep; read under the lock so the barrier has work to do
)

// awaitTUNReap blocks until no sweep is running. A barrier rather than a guard:
// what must not overlap is the sweep and the creation of the next adapter, and
// the creation is everything that follows.
func awaitTUNReap() {
	tunReapMu.Lock()
	held := tunReapDone
	tunReapMu.Unlock()
	_ = held
}
