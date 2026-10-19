package services

import "vair/core"

// LogService exposes the engine/app log buffer. Live lines also arrive via the
// "log_batch" Wails event.
type LogService struct{}

// Get returns a snapshot of the log buffer.
func (l *LogService) Get() []core.LogLine { return core.GetLogs() }

// GetImportant returns the small secondary buffer that keeps only errors,
// warnings and Vair's own lines. Verbose engine chatter never enters it, so an
// error survives there long after a flood has rolled it out of the main ring.
func (l *LogService) GetImportant() []core.LogLine { return core.ImportantLogs() }

// FilePath is where the optional file sink writes (Settings → System).
func (l *LogService) FilePath() string { return core.LogFilePath() }

// Clear empties the log buffer.
func (l *LogService) Clear() { core.ClearLogs() }
