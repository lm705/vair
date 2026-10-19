// Package services holds the ten objects the frontend calls.
//
// They are deliberately thin: each method translates a request into one call
// into core (or into internal/shell for anything that touches the system) and
// hands back the result. Logic that belongs to the program rather than to the
// interface lives in core — a service that starts making decisions is a service
// in the wrong package.
//
// Wails derives a binding id from the package path, so moving a service between
// packages changes every id and requires regenerating frontend/bindings.
package services

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

// ConnectChain connects the given entries as a chain, in the order given:
// idxs[0] is the entry hop, idxs[last] the exit. Returns "" on success or a human-readable rejection.
func (c *ConnService) ConnectChain(idxs []int, mode string) string {
	return core.ConnectChain(idxs, mode)
}

// AppInfo reports engine availability + elevation (gates the TUN mode pill).
func (c *ConnService) AppInfo() core.AppInfo { return core.GetAppInfo() }

// RestartAdmin acquires the privileges TUN needs (the "requires admin ↗" chip).
// The METHOD name stays as it is: renaming it would rename the generated
// binding and force a frontend change for nothing. What it calls is now
// platform-neutral — see core.EnsurePrivilege.
func (c *ConnService) RestartAdmin() string { return core.EnsurePrivilege() }
