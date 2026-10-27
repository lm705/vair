package core

// LogLine is one log entry sent to the frontend (exported DTO over the unexported
// internal logLine).
type LogLine struct {
	T   int64  `json:"t"`   // unix millis
	Lvl string `json:"lvl"` // error | warn | info | raw
	Src string `json:"src"` // xray | singbox | vair
	Msg string `json:"msg"`
}

// GetLogs returns a snapshot of the in-memory log buffer (newest last).
func GetLogs() []LogLine {
	src := logs.snapshot()
	out := make([]LogLine, len(src))
	for i, l := range src {
		out[i] = LogLine(l)
	}
	return out
}

// ClearLogs empties the log buffer.
func ClearLogs() { logs.clear() }

// LogEvent writes a line to the program's log from outside core — the place
// the user reads, which stderr is not.
func LogEvent(level, format string, args ...interface{}) { vlog(level, format, args...) }
