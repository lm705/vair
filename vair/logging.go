package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────── log store ───────────────────────────
//
// In-memory ring buffer of recent log lines, shown in the UI's Logs panel.
// Sources: xray/sing-box stderr from the active connection ("xray"/"singbox")
// and Vair's own diagnostics ("vair", via vlog). Each new line is also pushed
// to clients as a lossy SSE "log" event so the panel updates live without
// blocking the broadcast path. The buffer is session-only (not persisted).

type logLine struct {
	T   int64  `json:"t"`   // unix millis
	Lvl string `json:"lvl"` // error | warn | info | raw
	Src string `json:"src"` // xray | singbox | vair
	Msg string `json:"msg"`
}

type logStore struct {
	mu      sync.Mutex
	lines   []logLine // ring buffer for the /api/logs snapshot
	pending []logLine // lines added since the last SSE flush
	cap     int
	// Token-bucket rate limit for high-volume core output (see gate). A
	// verbose TUN connection can emit tens of thousands of "creating
	// connection" lines per second; without a cap they pile up in the ring,
	// the SSE batches, and the client's 1024-slot channel, spiking CPU and
	// RAM into the gigabytes. error/warn lines bypass the limit so failures
	// are never dropped.
	rlStart   time.Time
	rlCount   int
	rlDropped int
}

const (
	logRateInterval = 250 * time.Millisecond
	logRateLimit    = 200 // info/raw lines per interval (~800/s) before dropping
)

var logs = &logStore{cap: 2000}

// ansiRe matches ANSI SGR colour escapes (e.g. sing-box on Windows still
// emits them even when stderr is piped). We strip them before storing so the
// Logs panel shows clean text instead of "[31mERROR[0m".
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

func stripANSI(s string) string {
	// Fast path: most lines (all of xray's) carry no escapes, so skip the
	// regexp entirely unless an ESC byte is actually present. Matters under a
	// verbose flood where this runs tens of thousands of times per second.
	if strings.IndexByte(s, 0x1b) < 0 {
		return s
	}
	return ansiRe.ReplaceAllString(s, "")
}

// add records a line. It does NOT broadcast directly: under verbose logging a
// busy core (especially TUN, which routes all system traffic) can emit
// thousands of lines per second, and one SSE event per line froze the UI
// (every event = a JSON marshal on the server + a parse and DOM write on the
// client). Instead lines accumulate in `pending` and a ticker flushes them as
// a single batched event a few times per second — bounded work regardless of
// log volume. See logFlushLoop.
// push appends a line to both the ring buffer and the pending SSE batch.
// Caller must hold l.mu.
func (l *logStore) push(ln logLine) {
	l.lines = append(l.lines, ln)
	if len(l.lines) > 2*l.cap {
		// Amortised trim: let the slice grow to 2× cap, then drop the oldest
		// half in one copy. This keeps the per-add cost O(1) under a flood
		// instead of copying `cap` elements on every single line.
		l.lines = append(l.lines[:0], l.lines[len(l.lines)-l.cap:]...)
	}
	l.pending = append(l.pending, ln)
	if len(l.pending) > l.cap {
		// A flood between flushes: keep only the newest cap lines for the live
		// stream (the panel re-fetches the full snapshot on open anyway).
		l.pending = append(l.pending[:0], l.pending[len(l.pending)-l.cap:]...)
	}
}

// add stores a pre-classified line (vlog/tlog and result summaries). Not
// rate-limited — these are low-volume and always worth keeping.
func (l *logStore) add(src, lvl, msg string) {
	msg = stripANSI(strings.TrimRight(msg, "\r\n"))
	if msg == "" {
		return
	}
	ln := logLine{T: time.Now().UnixMilli(), Lvl: lvl, Src: src, Msg: msg}
	l.mu.Lock()
	l.push(ln)
	l.mu.Unlock()
}

// gate advances the rate-limit window and reports whether a high-volume core
// line should be kept. error/warn lines (isErrWarn) always pass; info/raw
// lines are capped at logRateLimit per window. Callers MUST evaluate this on
// the raw bytes BEFORE allocating a string (scanner.Text()), so each dropped
// line under a verbose flood costs almost nothing — no string, no GC churn.
// When a window rolls over with drops, one summary line is pushed so the user
// knows output was suppressed.
func (l *logStore) gate(isErrWarn bool) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.rlStart) >= logRateInterval {
		if l.rlDropped > 0 {
			l.push(logLine{T: now.UnixMilli(), Lvl: "warn", Src: "vair",
				Msg: fmt.Sprintf("… %d log lines suppressed (too fast — lower verbosity to see all)", l.rlDropped)})
		}
		l.rlStart = now
		l.rlCount = 0
		l.rlDropped = 0
	}
	if isErrWarn {
		return true
	}
	if l.rlCount >= logRateLimit {
		l.rlDropped++
		return false
	}
	l.rlCount++
	return true
}

// flush emits the lines accumulated since the last flush as one batched,
// lossy SSE "log" event (payload is an array). Returns quickly when idle.
func (l *logStore) flush() {
	l.mu.Lock()
	if len(l.pending) == 0 {
		l.mu.Unlock()
		return
	}
	batch := make([]logLine, len(l.pending))
	copy(batch, l.pending)
	l.pending = l.pending[:0]
	l.mu.Unlock()
	state.broadcast(SSEEvent{Type: "log", Payload: batch, Lossy: true})
}

// logFlushLoop drives periodic batched delivery of buffered log lines.
func logFlushLoop() {
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		logs.flush()
	}
}

func (l *logStore) snapshot() []logLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.lines)
	if n > l.cap {
		n = l.cap
	}
	out := make([]logLine, n)
	copy(out, l.lines[len(l.lines)-n:])
	return out
}

func (l *logStore) clear() {
	l.mu.Lock()
	// Drop the backing arrays (not just reslice) so a buffer that grew under a
	// flood is actually reclaimed by the GC.
	l.lines = nil
	l.pending = nil
	l.mu.Unlock()
}

// parseLevel classifies a raw stderr line by source so the UI can colour it.
// xray uses bracketed tags ([Warning]/[Info]/[Error]); sing-box prints the
// level word (WARN/INFO/ERROR/FATAL). Unrecognised lines are "raw".
func parseLevel(src, line string) string {
	// Match xray's capitalised tags ([Error]/[Warning]/[Info]) and sing-box's
	// uppercase words (ERROR/WARN/INFO/FATAL) WITHOUT allocating an uppercased
	// copy of every line — this runs per line under a verbose flood, so the
	// old strings.ToUpper was a real GC/CPU cost.
	switch {
	case strings.Contains(line, "[Error]") || strings.Contains(line, "[Fatal]") || strings.Contains(line, "ERROR") || strings.Contains(line, "FATAL") || strings.Contains(line, "PANIC") || strings.Contains(line, "panic"):
		return "error"
	case strings.Contains(line, "[Warning]") || strings.Contains(line, "WARN"):
		return "warn"
	case strings.Contains(line, "[Info]") || strings.Contains(line, "INFO"):
		return "info"
	}
	return "raw"
}

// errWarnTokens mirrors parseLevel's error/warn cases, pre-converted to bytes
// so the rate-limit gate can classify scanner.Bytes() WITHOUT allocating a
// string. Under a verbose flood the gate runs on every line; only the lines we
// actually keep get a string (scanner.Text()) allocated.
var errWarnTokens = [][]byte{
	[]byte("[Error]"), []byte("[Fatal]"), []byte("ERROR"), []byte("FATAL"),
	[]byte("PANIC"), []byte("panic"), []byte("[Warning]"), []byte("WARN"),
}

func levelErrWarnBytes(b []byte) bool {
	for _, t := range errWarnTokens {
		if bytes.Contains(b, t) {
			return true
		}
	}
	return false
}

// vlog records a Vair diagnostic: still printed to stderr (for console /
// LEGACY runs) and also pushed into the log store / SSE stream so it shows
// in the UI Logs panel. level is "error" | "warn" | "info".
func vlog(level, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, msg)
	logs.add("vair", level, msg)
}

// logTestsEnabled reports whether the "Log speed/ping tests" setting is on.
func logTestsEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return appSettings.LogTests
}

// nodeLogLabel is a short human label for a node used to prefix its test-core
// log lines, so interleaved output from concurrent probes stays attributable.
func nodeLogLabel(n *Node) string {
	if n == nil {
		return "?"
	}
	if s := strings.TrimSpace(n.Name); s != "" {
		return s
	}
	if n.Host != "" {
		return fmt.Sprintf("%s:%d", n.Host, n.Port)
	}
	return string(n.Kind)
}

// tlog records a ping/speed test result into the Logs panel, but only when
// the "Log speed/ping tests" setting is on (off by default — bulk tests can
// produce hundreds of lines). Tagged with the "test" source so it can be
// filtered separately from connection/core output.
func tlog(format string, args ...interface{}) {
	if !logTestsEnabled() {
		return
	}
	logs.add("test", "info", fmt.Sprintf(format, args...))
}

// lineSink is an io.Writer that splits a test core's output stream into lines
// and pushes each into the Logs panel under the "test" source, prefixed with
// the config name (bulk tests run many cores concurrently, so the name keeps
// interleaved lines attributable). engine is "xray"/"singbox" for level
// classification. Used only while the LogTests setting is on.
type lineSink struct {
	engine, name string
	buf          []byte
}

func (w *lineSink) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		raw := w.buf[:i]
		keep := logs.gate(levelErrWarnBytes(raw))
		var line string
		if keep {
			line = strings.TrimRight(string(raw), "\r")
		}
		w.buf = w.buf[i+1:]
		if keep && strings.TrimSpace(line) != "" {
			logs.add("test", parseLevel(w.engine, line), "["+w.name+"] "+line)
		}
	}
	return len(p), nil
}

// pumpProcLog reads a child process's stderr pipe line-by-line into the log
// store, tagged by source ("xray"/"singbox"). It also feeds each line to the
// optional sink (used by the connection paths to keep a rolling tail for the
// crash-error message). Returns when the pipe closes (process exit).
func pumpProcLog(src string, pipe io.Reader, sink func(string)) {
	if pipe == nil {
		return
	}
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		// Drop/keep decision on the raw bytes first — no string allocation for
		// the lines we drop under a verbose flood (the bulk of them).
		if !logs.gate(levelErrWarnBytes(scanner.Bytes())) {
			continue
		}
		line := scanner.Text()
		if sink != nil {
			sink(line)
		}
		logs.add(src, parseLevel(src, line), line)
	}
}
