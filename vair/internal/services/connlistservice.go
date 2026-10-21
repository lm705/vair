package services

import "vair/core"

// ConnectionsService feeds the live connections panel.
//
// The panel only reads: tracking runs for the whole session, because the
// question it answers — what has been using the connection — cannot be answered
// by a table that starts filling when you open it.
type ConnectionsService struct{}

// List returns the current connections, busiest first. Polled while the panel
// is open.
func (c *ConnectionsService) List() []core.ConnRow { return core.LiveConnections() }

// Totals returns what has moved since tracking began, by program and
// destination, with the still-open connections folded in.
func (c *ConnectionsService) Totals() []core.ConnTotalRow { return core.ConnectionTotals() }

// ClearTotals empties the totals table.
func (c *ConnectionsService) ClearTotals() { core.ClearConnectionTotals() }

// Tracking reports whether anything is being collected, and SetTracking turns it
// off and on. Off is a real off: the panel keeps what it already has and nothing
// new is recorded anywhere.
func (c *ConnectionsService) Tracking() bool      { return core.ConnectionTracking() }
func (c *ConnectionsService) SetTracking(on bool) { core.SetConnectionTracking(on) }

// Bypass lists the machine's TCP connections that did NOT go through Vair —
// what a program using raw sockets is talking to, which the live list cannot
// show because it never passes through us.
func (c *ConnectionsService) Bypass() []core.BypassRow { return core.BypassConnections() }
