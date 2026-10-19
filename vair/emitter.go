package main

import (
	"vair/core"
	"vair/internal/remote"
	"vair/internal/shell"
)

// coreEmitter bridges the domain layer's events to Wails. Replaces the 1.10 SSE
// broadcast: core.Events.Emit(...) → app.Event.Emit(...). Safe before the app
// exists (emits are dropped until the app is set).
type coreEmitter struct{}

func (coreEmitter) Emit(event string, data any) {
	if app := shell.App(); app != nil {
		app.Event.Emit(event, data)
	}
	// Also fan out to any LAN remote-control clients (browser/phone) over SSE.
	remote.Emit(event, data)
	// Keep the tray menu in step with the connection / auto state it shows.
	if event == "conn_update" || event == "auto_update" {
		shell.RefreshTrayMenu()
	}
}

func init() { core.Events = coreEmitter{} }
