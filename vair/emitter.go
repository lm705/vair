package main

import "vair/core"

// coreEmitter bridges the domain layer's events to Wails. Replaces the 1.10 SSE
// broadcast: core.Events.Emit(...) → app.Event.Emit(...). Safe before the app
// exists (emits are dropped until theApp is set).
type coreEmitter struct{}

func (coreEmitter) Emit(event string, data any) {
	if theApp != nil {
		theApp.Event.Emit(event, data)
	}
	// Also fan out to any LAN remote-control clients (browser/phone) over SSE.
	remoteEmit(event, data)
	// Keep the tray menu in step with the connection / auto state it shows.
	if event == "conn_update" || event == "auto_update" {
		refreshTrayMenu()
	}
}

func init() { core.Events = coreEmitter{} }
