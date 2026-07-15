// Package core holds Vair's transport-agnostic domain logic, migrated from the
// 1.10 codebase. It knows nothing about Wails or HTTP: backend→frontend
// notifications go through the Emitter (set by the shell), replacing the 1.10
// localhost SSE stream. This separation keeps the domain testable and ready for
// the Linux port.
package core

// Emitter delivers a backend→frontend event. The Wails shell provides the real
// implementation (app.Event.Emit); tests and the future Linux build can supply
// their own.
type Emitter interface {
	Emit(event string, data any)
}

// Events is the process-wide emitter, set by the shell at startup. It defaults
// to a no-op so domain code is safe to call before the UI is wired (and in unit
// tests that don't care about events).
var Events Emitter = nopEmitter{}

type nopEmitter struct{}

func (nopEmitter) Emit(string, any) {}
