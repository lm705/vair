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
		out[i] = LogLine{T: l.T, Lvl: l.Lvl, Src: l.Src, Msg: l.Msg}
	}
	return out
}

// ClearLogs empties the log buffer.
func ClearLogs() { logs.clear() }
