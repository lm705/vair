package core

import (
	"net"
	"os"
	"strconv"
)

// PortCheck is what a proxy port field shows under it: whether the port can be
// used, and if not, what has it.
type PortCheck struct {
	// State is one of: free, ours (Vair's own current connection), busy
	// (another program — Owner/PID when known), reserved (the system will not
	// let it be taken), same (equal to the other proxy port), remote (the
	// remote-access port), invalid (outside 1–65535).
	State string `json:"state"`
	Owner string `json:"owner,omitempty"`
	PID   int    `json:"pid,omitempty"`
}

// CheckProxyPort answers for one of the two proxy ports as typed, before it is
// saved; other is the other proxy port as typed. A port that can be bound now
// is free; the check is repeated by the connection itself, which is what it
// ultimately answers to.
func CheckProxyPort(port, other int) PortCheck {
	switch {
	case port < 1 || port > 65535:
		return PortCheck{State: "invalid"}
	case port == other:
		return PortCheck{State: "same"}
	case port == webPort || port == RemotePortSetting():
		return PortCheck{State: "remote"}
	}
	if portHeldByUs(port) {
		return PortCheck{State: "ours"}
	}
	if name, pid := listeningOwner(port); name != "" {
		if pid == os.Getpid() || isTrackedPID(pid) {
			return PortCheck{State: "ours"}
		}
		return PortCheck{State: "busy", Owner: name, PID: pid}
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(proxyBindHost(), strconv.Itoa(port)))
	if err == nil {
		ln.Close()
		return PortCheck{State: "free"}
	}
	if bindRefusedBySystem(err) {
		return PortCheck{State: "reserved"}
	}
	return PortCheck{State: "busy"}
}

// portHeldByUs reports whether the connection that is up listens on port.
func portHeldByUs(port int) bool {
	state.conn.mu.Lock()
	st := state.conn.state
	state.conn.mu.Unlock()
	if st.Status != ConnConnected && st.Status != ConnConnecting {
		return false
	}
	return st.HTTPPort == port || st.SOCKSPort == port
}

// isTrackedPID reports whether pid is an engine this program started.
func isTrackedPID(pid int) bool {
	spawnedPIDsMu.Lock()
	defer spawnedPIDsMu.Unlock()
	for _, p := range spawnedPIDs {
		if p == pid {
			return true
		}
	}
	return false
}
