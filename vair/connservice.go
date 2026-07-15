package main

import "vair/core"

// ConnService exposes connect/disconnect/state to the frontend. Live state
// changes also arrive via the "conn_update" Wails event.
type ConnService struct{}

// Connect starts a connection to row idx of the table's tab. mode is "proxy"
// (default) or "tun". Returns false if idx is invalid.
func (c *ConnService) Connect(idx int, mode string) bool {
	return core.Connect(idx, mode)
}

// Disconnect tears down the current connection.
func (c *ConnService) Disconnect() {
	core.Disconnect()
}

// State returns the current connection state.
func (c *ConnService) State() core.ConnState {
	return core.ConnSnapshot()
}

// CheckExit fetches the public exit IP/geo through the live tunnel (the
// conn-bar "check IP" chip). Synchronous, ~8s cap.
func (c *ConnService) CheckExit() core.ExitInfo {
	return core.CheckExit()
}

// ConnectChain connects the given entries (top→bottom screen order: entry →
// exit) as a chain. Returns "" on success or a human-readable rejection.
func (c *ConnService) ConnectChain(idxs []int, mode string) string {
	return core.ConnectChain(idxs, mode)
}

// AppInfo reports engine availability + elevation (gates the TUN mode pill).
func (c *ConnService) AppInfo() core.AppInfo { return core.GetAppInfo() }

// RestartAdmin relaunches Vair elevated (the "requires admin ↗" chip).
func (c *ConnService) RestartAdmin() { core.RestartAdmin() }
