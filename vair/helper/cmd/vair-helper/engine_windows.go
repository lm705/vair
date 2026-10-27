package main

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"vair/helper/api"
	"vair/helper/winsys"
)

// engine is the tunnel's process, behind an interface so the request handling
// can be tested without starting one.
type engine interface {
	start(cfg []byte, iface string, logf func(api.LogLine), onExit func()) error
	stop()
	running() bool
	status() api.Status
}

// startupWait is how long a fresh engine gets to fail before the start is
// called good — the time the app itself waits (tunStartupTimeout in core). An
// engine that refuses its configuration says so in this window, and the error
// goes back to the app with the engine's own last words.
const startupWait = 3 * time.Second

// engineProc runs sing-box from the install directory.
type engineProc struct {
	path   string // the engine; fixed by the service's own location
	dir    string // its working directory: the protected data directory
	config string // where the validated configuration is written

	mu        sync.Mutex
	cmd       *exec.Cmd
	iface     string
	startedAt time.Time
	exited    chan struct{}
	onExit    func()
	logf      func(api.LogLine)
	tail      []string // the last lines it wrote, for a start that fails
}

func (t *engineProc) status() api.Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd == nil {
		return api.Status{Running: false}
	}
	return api.Status{
		Running:   true,
		Iface:     t.iface,
		PID:       t.cmd.Process.Pid,
		UptimeSec: int64(time.Since(t.startedAt).Seconds()),
	}
}

func (t *engineProc) running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cmd != nil
}

// start writes the configuration — already validated by the caller — and runs
// the engine against it.
func (t *engineProc) start(cfg []byte, iface string, logf func(api.LogLine), onExit func()) error {
	t.mu.Lock()
	if t.cmd != nil {
		t.mu.Unlock()
		return fmt.Errorf("a tunnel is already running on %s", t.iface)
	}
	if st, err := os.Stat(t.path); err != nil || st.IsDir() {
		t.mu.Unlock()
		return fmt.Errorf("the engine is not at %s — reinstall Vair", t.path)
	}
	if err := os.WriteFile(t.config, cfg, 0o600); err != nil {
		t.mu.Unlock()
		return fmt.Errorf("write config: %w", err)
	}
	cmd := exec.Command(t.path, "run", "-c", t.config)
	cmd.Dir = t.dir
	winsys.Hide(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.mu.Unlock()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		t.mu.Unlock()
		return fmt.Errorf("start engine: %w", err)
	}
	// With the service: an engine must not outlive what is driving it — a
	// service that died would leave a tunnel nobody can stop.
	if err := winsys.TieToOwner(cmd.Process.Pid); err != nil {
		log.Printf("engine %d not tied to the service: %v", cmd.Process.Pid, err)
	}
	exited := make(chan struct{})
	t.cmd, t.iface, t.startedAt = cmd, iface, time.Now()
	t.exited, t.onExit, t.logf, t.tail = exited, onExit, logf, nil
	t.mu.Unlock()

	go t.pump(stderr)
	// The only Wait. Whoever stops the engine waits on exited instead.
	go func() {
		cmd.Wait() //nolint:errcheck
		close(exited)
		t.mu.Lock()
		unexpected := t.cmd == cmd
		if !unexpected {
			t.mu.Unlock()
			return
		}
		t.cmd, t.iface = nil, ""
		logf, onExit := t.logf, t.onExit
		t.mu.Unlock()
		os.Remove(t.config) //nolint:errcheck
		if logf != nil {
			logf(api.LogLine{Src: "helper", Lvl: "error", Text: "engine exited on its own — tunnel closed"})
		}
		if onExit != nil {
			onExit()
		}
	}()

	select {
	case <-exited:
		t.mu.Lock()
		why := "sing-box stopped at startup"
		for i := len(t.tail) - 1; i >= 0; i-- {
			if t.tail[i] != "" {
				why = t.tail[i]
				break
			}
		}
		t.mu.Unlock()
		if len(why) > 200 {
			why = "..." + why[len(why)-200:]
		}
		return errors.New(why)
	case <-time.After(startupWait):
		return nil
	}
}

// pump forwards engine output to whoever owns the tunnel, and keeps the last
// few lines for a start that fails.
func (t *engineProc) pump(r interface{ Read([]byte) (int, error) }) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 8<<10), 64<<10)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		t.mu.Lock()
		t.tail = append(t.tail, line)
		if len(t.tail) > 8 {
			t.tail = t.tail[len(t.tail)-8:]
		}
		logf := t.logf
		t.mu.Unlock()
		if logf != nil {
			logf(api.LogLine{Src: "singbox", Lvl: engineLevel(line), Text: line})
		}
	}
}

// stop ends the engine and waits for it to be gone.
//
// Process.Kill is TerminateProcess on Windows: immediate. sing-box gets no
// chance to remove its routes, and does not need one — wintun removes the
// adapter with its last handle, and the routes go with the adapter.
func (t *engineProc) stop() {
	t.mu.Lock()
	cmd, exited := t.cmd, t.exited
	t.cmd, t.iface, t.logf, t.onExit = nil, "", nil, nil
	t.mu.Unlock()
	if cmd == nil {
		return
	}
	cmd.Process.Kill() //nolint:errcheck
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		log.Printf("engine %d did not exit within 10s of being killed", cmd.Process.Pid)
	}
	os.Remove(t.config) //nolint:errcheck
}
