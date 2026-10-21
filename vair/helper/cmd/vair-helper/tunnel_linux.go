package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"vair/helper/api"
)

// EnginePath is the only binary this service will ever execute.
//
// A constant, not a request field and not a search path. The unit runs with
// ProtectHome=yes precisely so the helper cannot be asked to run something out
// of the user's home — a sing-box the user can overwrite, executed as root, is
// a one-line path to root. That is also why the packages install the engines
// here rather than extracting them into ~/.local/share the way Windows does.
const EnginePath = "/usr/lib/vair/bin/sing-box"

// configPath is where the validated config is written for the engine to read.
// Under /run/vair, which systemd creates as root with mode 0750, so the caller
// cannot swap the file between validation and exec.
var configPath = filepath.Join(api.DirPath, "tun.json")

// pidPath records the engine's PID so --cleanup can reap a leftover after the
// helper itself was killed.
var pidPath = filepath.Join(api.DirPath, "engine.pid")

// rulesPath holds the policy rules that existed before the engine started, so
// cleanup can tell what the engine added from what the machine already had.
// On disk rather than in memory only, because --cleanup runs in a fresh process
// after the helper was killed, which is exactly when cleanup matters most.
var rulesPath = filepath.Join(api.DirPath, "rules.before")

// tunnel owns at most one running engine.
type tunnel struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	iface     string
	startedAt time.Time
	// exited is closed by the ONE goroutine that reaps the engine. stop() waits
	// on it instead of calling Wait itself: a second Wait on the same Cmd
	// returns "Wait was already called" immediately, which made stop believe the
	// process was gone the moment it asked and skip its own escalation.
	exited chan struct{}
	// onExit is called when the engine dies without being asked to. The server
	// uses it to tell the app, which otherwise goes on showing a connection that
	// no longer exists.
	onExit func()

	// logf receives engine output and the helper's own notices. Set by the
	// connection that owns the tunnel; nil once nobody is listening.
	logf func(api.LogLine)
}

func (t *tunnel) status() api.Status {
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

func (t *tunnel) running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cmd != nil
}

// start writes the config and launches the engine.
//
// The caller has already validated cfg; this function assumes nothing about it
// beyond that, and takes iface only for reporting.
func (t *tunnel) start(cfg []byte, iface string, logf func(api.LogLine), onExit func()) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd != nil {
		return fmt.Errorf("a tunnel is already running on %s", t.iface)
	}
	if _, err := os.Stat(EnginePath); err != nil {
		return fmt.Errorf("engine not found at %s — is the package installed?", EnginePath)
	}

	// 0600 and root-owned: the engine runs as root and nobody else needs to
	// read a file that contains the proxy credentials.
	if err := os.WriteFile(configPath, cfg, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Taken before the engine can add anything, so cleanup has a baseline to
	// subtract. sing-box picks a random table number for the rule auto_redirect
	// installs at priority 1, and a fixed list of table numbers cannot cover it.
	writeRuleSnapshot()

	cmd := exec.Command(EnginePath, "run", "-c", configPath)
	// Its own process group, so teardown can signal the engine AND anything it
	// spawns with one kill. Without this a child outlives the parent and keeps
	// the tunnel device open.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start engine: %w", err)
	}

	exited := make(chan struct{})
	t.cmd = cmd
	t.iface = iface
	t.startedAt = time.Now()
	t.logf = logf
	t.onExit = onExit
	t.exited = exited
	os.WriteFile(pidPath, []byte(fmt.Sprint(cmd.Process.Pid)), 0o600) //nolint:errcheck

	go t.pump(stderr)
	// The only Wait. It closes exited for whoever is waiting, and if nobody
	// asked for this exit, it cleans up after the engine and says so.
	go func() {
		cmd.Wait() //nolint:errcheck
		close(exited)

		t.mu.Lock()
		unexpected := t.cmd == cmd
		if !unexpected {
			t.mu.Unlock()
			return
		}
		t.cmd = nil
		iface := t.iface
		t.iface = ""
		logf := t.logf
		onExit := t.onExit
		t.mu.Unlock()

		os.Remove(pidPath) //nolint:errcheck
		// An engine that died on its own left its routes behind. Nobody else is
		// going to remove them.
		removeRouteLeftovers(iface, logf)
		if logf != nil {
			logf(api.LogLine{Src: "helper", Lvl: "error", Text: "engine exited on its own — tunnel closed"})
		}
		if onExit != nil {
			onExit()
		}
	}()
	return nil
}

// pump forwards engine output to whoever owns the tunnel.
func (t *tunnel) pump(r interface{ Read([]byte) (int, error) }) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 8<<10), 64<<10)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		t.mu.Lock()
		logf := t.logf
		t.mu.Unlock()
		if logf != nil {
			logf(api.LogLine{Src: "singbox", Lvl: engineLevel(line), Text: line})
		}
	}
}

// engineLevel maps sing-box's own prefix onto the app's log levels so the panel
// filters behave the same as they do when the GUI spawns the engine itself.
func engineLevel(line string) string {
	switch {
	case strings.Contains(line, "FATAL") || strings.Contains(line, "ERROR"):
		return "error"
	case strings.Contains(line, "WARN"):
		return "warn"
	default:
		return "info"
	}
}

// stop tears the tunnel down and waits for the engine to be gone.
//
// SIGTERM first so sing-box can remove its routes and interface; SIGKILL if it
// will not go. Both to the process GROUP: signalling only the leader leaves
// grandchildren holding the device, which is exactly the state that looks to a
// user like "the VPN disconnected but my internet is still broken".
func (t *tunnel) stop() {
	t.mu.Lock()
	cmd := t.cmd
	t.cmd = nil
	iface := t.iface
	t.iface = ""
	logf := t.logf
	t.logf = nil
	exited := t.exited
	t.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid // Setpgid made the child its own group leader

	syscall.Kill(-pgid, syscall.SIGTERM) //nolint:errcheck

	// Wait on the channel the reaper closes, NOT on a second Wait() — Go returns
	// "Wait was already called" from that immediately, which made this function
	// believe the engine was gone the moment it asked.
	killed := false
	select {
	case <-exited:
	case <-time.After(sigtermGrace):
		// sing-box removes its own routes on SIGTERM, and that takes longer than
		// a process usually needs to die. Killing it early is how the machine
		// ends up with rules pointing at an interface that no longer exists —
		// which breaks ALL networking, not just the tunnel, and looks to the
		// user like the VPN broke their internet.
		if logf != nil {
			logf(api.LogLine{Src: "helper", Lvl: "warn",
				Text: fmt.Sprintf("engine did not exit within %s — killing it", sigtermGrace)})
		}
		syscall.Kill(-pgid, syscall.SIGKILL) //nolint:errcheck
		killed = true
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			if logf != nil {
				logf(api.LogLine{Src: "helper", Lvl: "error", Text: "engine did not exit after SIGKILL"})
			}
		}
	}

	if killed {
		// It never got to clean up after itself, so we do — best effort, because
		// leaving the machine unable to reach the network is the worse outcome by
		// a wide margin.
		removeRouteLeftovers(iface, logf)
	}

	os.Remove(pidPath)    //nolint:errcheck
	os.Remove(configPath) //nolint:errcheck
	os.Remove(rulesPath)  //nolint:errcheck
	if logf != nil {
		logf(api.LogLine{Src: "helper", Lvl: "info", Text: "tunnel stopped"})
	}
}

// sigtermGrace is how long the engine gets to tear its own routing down.
//
// Generous on purpose. The cost of waiting too long is a slow disconnect; the
// cost of waiting too little is a machine whose networking is broken until
// somebody deletes ip rules by hand.
const sigtermGrace = 10 * time.Second

// removeRouteLeftovers undoes what an engine that died without cleaning up left
// behind: the policy-routing rules, the route table, the interface, and the
// nftables table auto_redirect installs.
//
// Best effort by design — every step may legitimately fail because the engine
// already did it, and none of the failures are worth reporting. What is NOT
// acceptable is leaving them: rules pointing at a vanished interface silently
// blackhole everything the machine sends.
func removeRouteLeftovers(iface string, logf func(api.LogLine)) {
	if iface == "" {
		iface = "vair0"
	}
	// Anything that appeared while the engine was running. This is what catches
	// the rule at priority 1 pointing at a randomly numbered table — the one that
	// silently blackholes the machine, because it sits above main and its table
	// is empty once the interface is gone.
	removeAddedRules(logf)

	// sing-box's defaults: table 2022 and rule priorities in the 9000s. Kept as a
	// backstop for the case where the snapshot is missing (a helper killed
	// between start and the write, a /run wiped underneath us).
	for i := 0; i < 8; i++ { // rules repeat, so delete until the calls stop working
		if err := exec.Command("ip", "rule", "del", "table", routeTable).Run(); err != nil {
			break
		}
	}
	exec.Command("ip", "route", "flush", "table", routeTable).Run() //nolint:errcheck
	exec.Command("ip", "link", "del", iface).Run()                  //nolint:errcheck
	// Only our own table, never a flush of the whole ruleset: this machine may
	// be running a firewall that has nothing to do with us.
	exec.Command("nft", "delete", "table", "inet", "sing-box").Run() //nolint:errcheck
	if logf != nil {
		logf(api.LogLine{Src: "helper", Lvl: "warn", Text: "cleaned up routing left by the engine"})
	}
}

// routeTable is the policy-routing table sing-box uses for auto_route. Its
// default, restated here because the cleanup has to name it and the config does
// not set it.
const routeTable = "2022"

// ruleLines reads the current policy rules, one normalised line each.
func ruleLines() []string {
	out, err := exec.Command("ip", "rule", "show").Output()
	if err != nil {
		return nil
	}
	var rules []string
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			rules = append(rules, strings.Join(f, " "))
		}
	}
	return rules
}

// writeRuleSnapshot records the rules in place right now.
func writeRuleSnapshot() {
	lines := ruleLines()
	if len(lines) == 0 {
		os.Remove(rulesPath) //nolint:errcheck
		return               // no snapshot is better than an empty one that claims everything is ours
	}
	os.WriteFile(rulesPath, []byte(strings.Join(lines, "\n")), 0o600) //nolint:errcheck
}

// removeAddedRules deletes every policy rule that was not there before the
// engine started, and empties the tables those rules pointed at.
//
// By difference rather than by a list of known numbers, because sing-box chooses
// the table id at random. The cost of the approach is that a rule the user added
// by hand DURING a session is removed with the rest; the alternative is leaving a
// rule that sends everything to an empty table, which takes the machine off the
// network entirely. Without a snapshot this does nothing at all — guessing which
// of a stranger's routing rules are ours is not a thing to do as root.
func removeAddedRules(logf func(api.LogLine)) {
	data, err := os.ReadFile(rulesPath)
	if err != nil {
		return
	}
	had := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		had[line] = true
	}

	for _, line := range ruleLines() {
		if had[line] {
			continue
		}
		pref, selector, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(selector)
		// Delete by the full selector, not by priority alone: two rules can share
		// a priority, and the other one may be somebody else's.
		args := append([]string{"rule", "del", "pref", strings.TrimSpace(pref)}, fields...)
		if err := exec.Command("ip", args...).Run(); err != nil {
			continue
		}
		// The table it pointed at is ours too, and an orphaned table keeps
		// answering lookups if a rule is ever recreated with the same number.
		for i, f := range fields {
			if f == "lookup" && i+1 < len(fields) && isNumeric(fields[i+1]) {
				exec.Command("ip", "route", "flush", "table", fields[i+1]).Run() //nolint:errcheck
			}
		}
		if logf != nil {
			logf(api.LogLine{Src: "helper", Lvl: "warn", Text: "removed leftover routing rule: " + line})
		}
	}
	os.Remove(rulesPath) //nolint:errcheck
}

// isNumeric reports whether s is a table number rather than a name like "main".
// Named tables come from /etc/iproute2/rt_tables and belong to the system.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cleanupLeftovers is what ExecStartPre runs: make the state on disk match
// reality before the service starts.
//
// It matters because the helper can be killed without ever running its own
// teardown — `systemctl kill`, an OOM, a crash. /run is a tmpfs so a reboot
// clears it, but a restart does not.
func cleanupLeftovers() error {
	if data, err := os.ReadFile(pidPath); err == nil {
		var pid int
		if _, err := fmt.Sscanf(string(data), "%d", &pid); err == nil && pid > 1 {
			// Kill the group, not the PID: same reason as stop(). A PID that has
			// been recycled is a real risk here, which is why the pidfile lives
			// in /run — it is gone after a reboot, and within one boot PID reuse
			// on Linux is sequential enough that a stale entry belongs to a
			// process that has not existed for microseconds, not days.
			syscall.Kill(-pid, syscall.SIGKILL) //nolint:errcheck
		}
		os.Remove(pidPath) //nolint:errcheck
		// That engine was killed outright, so the routing it installed is still
		// in place. This is the only code that will ever remove it: the helper
		// that started it is gone.
		removeRouteLeftovers("", nil)
	}
	// A block installed by a helper that was killed outlives it, and there is no
	// tunnel left for it to allow — the machine would come back with no network
	// and nothing on screen to explain it. This is the only code that will ever
	// remove it.
	if killSwitchPresent() {
		log.Printf("removing a kill-switch left by a previous run")
		removeKillSwitch()
	}
	os.Remove(rulesPath)  //nolint:errcheck
	os.Remove(configPath) //nolint:errcheck
	// Rule-sets staged by a session that no longer exists. /run is a tmpfs, so
	// these are megabytes of RAM held by nothing.
	os.RemoveAll(api.RuleSetDir) //nolint:errcheck
	// A socket file left behind by a killed process makes bind() fail with
	// "address already in use" even though nothing is listening.
	if err := os.Remove(api.SocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}
