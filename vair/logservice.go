package main

import "vair/core"

// LogService exposes the engine/app log buffer. Live lines also arrive via the
// "log_batch" Wails event.
type LogService struct{}

// Get returns a snapshot of the log buffer.
func (l *LogService) Get() []core.LogLine { return core.GetLogs() }

// Clear empties the log buffer.
func (l *LogService) Clear() { core.ClearLogs() }
