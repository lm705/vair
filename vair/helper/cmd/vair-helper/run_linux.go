package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"vair/helper/api"
	"vair/helper/validate"
)

// allowedPaths are the only directories a config may reference. Both are
// root-owned, which is what lets the validator compare paths lexically instead
// of chasing symlinks — see helper/validate.
var allowedPaths = []string{"/usr/lib/vair/rulesets", api.DirPath}

func run(o options) error {
	if o.install || o.uninstall || o.repair || o.stop {
		return errors.New("the service flags are for Windows; on Linux the package manages the helper")
	}
	if o.cleanup {
		return cleanupLeftovers()
	}
	if os.Geteuid() != 0 {
		// Not a permission check — the kernel makes that for us. This is so the
		// failure reads as "you started it wrong" rather than as a mysterious
		// EPERM from the first netlink call.
		return errors.New("must run as root; it is meant to be started by systemd")
	}
	if err := cleanupLeftovers(); err != nil {
		return err
	}

	ln, err := net.Listen("unix", api.SocketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", api.SocketPath, err)
	}
	defer ln.Close()
	// 0660 AND the directory's group.
	//
	// The group is not automatic: a new file takes the creating process's group,
	// which here is root — so a socket left alone is root:root 0660 and the user
	// the whole scheme exists for cannot open it. Measured, not assumed: the
	// first end-to-end run failed with EACCES on connect.
	//
	// The group is READ from the directory rather than configured here, so
	// whatever group the unit's RuntimeDirectory was given is the one that
	// applies, with nothing duplicated between the unit and the code.
	if err := os.Chmod(api.SocketPath, 0o660); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}
	if gid, err := dirGroup(api.DirPath); err == nil {
		if err := os.Chown(api.SocketPath, 0, gid); err != nil {
			return fmt.Errorf("chown socket to gid %d: %w", gid, err)
		}
	} else {
		return fmt.Errorf("read the group of %s: %w", api.DirPath, err)
	}

	t := &tunnel{}
	srv := &server{tunnel: t}

	// A signal must tear the tunnel down, not just stop the process. systemctl
	// stop, a package upgrade, a reboot — all of them arrive here, and leaving
	// the tunnel up would leave the machine routing through an interface whose
	// owner is gone.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		log.Print("shutting down: tearing the tunnel down first")
		t.stop()
		ln.Close()
		os.Remove(api.SocketPath) //nolint:errcheck
		os.Exit(0)
	}()

	log.Printf("listening on %s (protocol %d)", api.SocketPath, api.ProtocolVersion)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go srv.serve(conn)
	}
}

// server is the shared state behind every connection.
type server struct {
	tunnel *tunnel

	mu sync.Mutex
	// owner is the connection that started the running tunnel. Only it may stop
	// or replace the tunnel, and when it goes away the tunnel goes with it.
	owner *session
	// kept takes down a kill-switch left standing for the next tunnel (a stop
	// with keep_block) if none has come by then. Nil when nothing is waiting.
	kept *time.Timer
	// blockIface and blockNets are the running tunnel's interface and prefixes,
	// which a kill_switch request after start_tun builds the block from.
	blockIface string
	blockNets  []string
}

// dropBlock takes the kill-switch down now and stops waiting for a successor.
func (srv *server) dropBlock() {
	srv.mu.Lock()
	if srv.kept != nil {
		srv.kept.Stop()
		srv.kept = nil
	}
	srv.mu.Unlock()
	removeKillSwitch()
}

// keepBlock leaves the kill-switch standing for the tunnel that comes next.
//
// A switch to another server is a stop and a start. The block used to go with
// the stop and come back after the start, and between the two the machine had
// neither a tunnel nor a block: everything went out with its own address while
// the new engine came up. Kept, the block holds through the gap — the engine is
// let out by its cgroup to reach the next server — and the next start replaces
// it in one transaction. If nothing comes within BlockHandoverSec it goes: a
// block over no tunnel is a machine with no network and no explanation.
func (srv *server) keepBlock() {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.kept != nil {
		srv.kept.Stop()
	}
	var t *time.Timer
	t = time.AfterFunc(time.Duration(api.BlockHandoverSec)*time.Second, func() {
		srv.mu.Lock()
		mine := srv.kept == t
		if mine {
			srv.kept = nil
		}
		srv.mu.Unlock()
		if mine && !srv.tunnel.running() {
			removeKillSwitch()
			log.Printf("no tunnel came back within %ds: kill-switch down", api.BlockHandoverSec)
		}
	})
	srv.kept = t
}

// session is one client connection.
type session struct {
	c  *api.Conn
	nc net.Conn
	mu sync.Mutex // serialises writes: the encoder is not concurrency-safe

	greeted bool
	// ruleSets names the files this session has staged. Touched only by the
	// serve goroutine, which handles one request at a time, so it needs no lock
	// of its own — unlike the write path above, which other goroutines share.
	ruleSets map[string]bool
	// leaseUntil is when this session's tunnel dies without another ping.
	leaseUntil time.Time
	leaseMu    sync.Mutex
}

func (s *session) write(r *api.Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nc.SetWriteDeadline(time.Now().Add(writeTimeout)) //nolint:errcheck
	s.c.WriteResponse(r)                                //nolint:errcheck
	s.nc.SetWriteDeadline(time.Time{})                  //nolint:errcheck
}

func (s *session) renew(d time.Duration) {
	s.leaseMu.Lock()
	s.leaseUntil = time.Now().Add(d)
	s.leaseMu.Unlock()
}

func (s *session) leaseLeft() time.Duration {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	return time.Until(s.leaseUntil)
}

func (srv *server) serve(nc net.Conn) {
	peer := peerDescription(nc)
	log.Printf("client connected (%s)", peer)

	s := &session{c: api.NewConn(nc), nc: nc}
	defer func() {
		nc.Close()
		// THE teardown rule: if this connection owned the tunnel, it goes now.
		// A GUI that crashed, was killed, or lost its socket does not get to
		// leave a tunnel behind — the user would be left with a machine routing
		// through an interface no running program admits to owning.
		srv.mu.Lock()
		owned := srv.owner == s
		if owned {
			srv.owner = nil
		}
		srv.mu.Unlock()
		if owned {
			log.Printf("owner disconnected (%s): tearing the tunnel down", peer)
			srv.tunnel.stop()
			srv.dropBlock() // the tunnel is gone; a block over nothing is a machine offline
		}
		// Staged rule-sets belong to the session that sent them, and this is
		// where they go — NOT in stop(). Switching servers is stop-then-start on
		// one connection, so a stop that deleted them would pull the files out
		// from under the config that was staged moments earlier and hand the
		// engine a dangling path, which sing-box treats as fatal.
		for name := range s.ruleSets {
			os.Remove(api.RuleSetPath(name)) //nolint:errcheck
		}
		os.Remove(api.RuleSetDir) //nolint:errcheck — only succeeds once empty
		log.Printf("client gone (%s)", peer)
	}()

	for {
		req, err := s.c.ReadRequest()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("read from %s: %v", peer, err)
			}
			return
		}
		if req.ID == 0 {
			// id 0 is the helper's own channel for unsolicited frames; a request
			// using it could never be answered unambiguously.
			s.write(&api.Response{Error: "request id must be non-zero"})
			continue
		}
		if !s.greeted && req.Op != api.OpHello {
			s.write(&api.Response{ID: req.ID, Error: "hello must be the first request"})
			return
		}
		srv.handle(s, req)
	}
}

func (srv *server) handle(s *session, req *api.Request) {
	switch req.Op {
	case api.OpHello:
		s.greeted = true
		// Both sides report their version and each decides for itself; the
		// helper does not refuse an older GUI here, because the GUI's message
		// ("reinstall the helper") is the one the user can act on.
		s.write(&api.Response{ID: req.ID, OK: true, Protocol: api.ProtocolVersion, Version: version})

	case api.OpStatus:
		st := srv.tunnel.status()
		if left := s.leaseLeft(); left > 0 {
			st.LeaseExpiresInSec = int(left.Seconds())
		}
		s.write(&api.Response{ID: req.ID, OK: true, Status: &st})

	case api.OpPing:
		lease := leaseDuration(req.LeaseSec)
		s.renew(lease)
		s.write(&api.Response{ID: req.ID, OK: true})

	case api.OpPutRuleSet:
		if err := srv.putRuleSet(s, req); err != nil {
			s.write(&api.Response{ID: req.ID, Error: err.Error()})
			return
		}
		// Protocol 5 says where the set went, so a client does not have to
		// know the helper's directories; here it is the fixed one.
		s.write(&api.Response{ID: req.ID, OK: true, Path: api.RuleSetPath(req.Name)})

	case api.OpStartTUN:
		if err := srv.startTUN(s, req); err != nil {
			s.write(&api.Response{ID: req.ID, Error: err.Error()})
			return
		}
		st := srv.tunnel.status()
		s.write(&api.Response{ID: req.ID, OK: true, Status: &st})

	case api.OpStop:
		srv.mu.Lock()
		owned := srv.owner == s
		srv.mu.Unlock()
		if !owned && srv.tunnel.running() {
			s.write(&api.Response{ID: req.ID, Error: "another client owns the tunnel"})
			return
		}
		srv.tunnel.stop()
		if req.KeepBlock && killSwitchPresent() {
			srv.keepBlock()
		} else {
			srv.dropBlock()
		}
		srv.mu.Lock()
		if srv.owner == s {
			srv.owner = nil
		}
		srv.mu.Unlock()
		s.write(&api.Response{ID: req.ID, OK: true})

	case api.OpKillSwitch:
		if err := srv.setKillSwitch(s, req); err != nil {
			s.write(&api.Response{ID: req.ID, Error: err.Error()})
			return
		}
		s.write(&api.Response{ID: req.ID, OK: true})

	default:
		s.write(&api.Response{ID: req.ID, Error: fmt.Sprintf("unknown op %q", req.Op)})
	}
}

// putRuleSet stores one rule-set for this session's next tunnel.
func (srv *server) putRuleSet(s *session, req *api.Request) error {
	if !ruleSetName.MatchString(req.Name) {
		return fmt.Errorf("%q is not an acceptable rule-set name", req.Name)
	}
	if len(req.Data) == 0 {
		return errors.New("rule-set is empty")
	}
	if len(req.Data) > api.MaxRuleSetBytes {
		return fmt.Errorf("rule-set is %d bytes, over the %d limit", len(req.Data), api.MaxRuleSetBytes)
	}

	if _, replacing := s.ruleSets[req.Name]; len(s.ruleSets) >= api.MaxRuleSets && !replacing {
		return fmt.Errorf("no more than %d rule-sets per session", api.MaxRuleSets)
	}

	// 0700: nothing but root reads these, and the caller must not be able to
	// swap a file between staging it and the engine opening it.
	if err := os.MkdirAll(api.RuleSetDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", api.RuleSetDir, err)
	}
	if err := os.WriteFile(api.RuleSetPath(req.Name), req.Data, 0o600); err != nil {
		return fmt.Errorf("write rule-set: %w", err)
	}

	if s.ruleSets == nil {
		s.ruleSets = make(map[string]bool)
	}
	s.ruleSets[req.Name] = true
	return nil
}

// setKillSwitch is kill_switch: the block put up or taken down for the running
// tunnel, so a switch turned on mid-session protects that session rather than
// the next one. Taking a block down with no tunnel running is allowed too: it
// is how a disconnect ends a block kept for a successor that is not coming.
func (srv *server) setKillSwitch(s *session, req *api.Request) error {
	srv.mu.Lock()
	owned, other := srv.owner == s, srv.owner != nil && srv.owner != s
	iface, nets := srv.blockIface, srv.blockNets
	srv.mu.Unlock()
	if other {
		return errors.New("another client owns the tunnel")
	}
	if !req.KillSwitch {
		srv.dropBlock()
		return nil
	}
	if !owned || !srv.tunnel.running() {
		return errors.New("no tunnel of this connection to protect")
	}
	if err := checkServerIPs(req.ServerIPs); err != nil {
		return err
	}
	return installKillSwitch(iface, nets, req.ServerIPs, req.AllowLAN,
		func(l api.LogLine) { s.write(&api.Response{Log: &l}) })
}

// checkServerIPs is the gate before an address list reaches an nftables script.
func checkServerIPs(ips []string) error {
	if len(ips) > api.MaxServerIPs {
		return fmt.Errorf("at most %d server addresses", api.MaxServerIPs)
	}
	for _, ip := range ips {
		if net.ParseIP(ip) == nil {
			// Straight into an nftables script if it were let through, so it is
			// checked here rather than escaped there.
			return fmt.Errorf("%q is not an address", ip)
		}
	}
	return nil
}

func (srv *server) startTUN(s *session, req *api.Request) error {
	if len(req.Config) == 0 {
		return errors.New("start_tun needs a config")
	}
	if err := validate.Config(req.Config, validate.Options{AllowedPathPrefixes: allowedPaths}); err != nil {
		// Logged as well as returned: a rejected config is either a bug in the
		// app or someone probing the socket, and both are worth a line in the
		// journal.
		log.Printf("rejected config: %v", err)
		return fmt.Errorf("config rejected: %w", err)
	}

	if err := checkServerIPs(req.ServerIPs); err != nil {
		return err
	}

	srv.mu.Lock()
	other := srv.owner != nil && srv.owner != s
	if !other && srv.kept != nil {
		// A block kept for this start: it stands until the new one replaces it.
		srv.kept.Stop()
		srv.kept = nil
	}
	srv.mu.Unlock()
	if other {
		return errors.New("another client owns the tunnel")
	}
	// Same owner asking again means "switch to this config", which is what the
	// app does when the user picks a different server.
	if srv.tunnel.running() {
		srv.tunnel.stop()
	}

	iface := ifaceFromConfig(req.Config)
	if err := srv.tunnel.start(req.Config, iface,
		func(l api.LogLine) { s.write(&api.Response{Log: &l}) },
		func() {
			// The engine died without being asked to. Tell the owner in a form
			// it can act on — an unsolicited status frame — rather than only as
			// a log line it would have to parse. Without this the app goes on
			// showing a connection that no longer exists.
			s.write(&api.Response{Status: &api.Status{Running: false}})
			srv.mu.Lock()
			if srv.owner == s {
				srv.owner = nil
			}
			srv.mu.Unlock()
		}); err != nil {
		srv.dropBlock() // never leave a block over a tunnel that did not start
		return err
	}

	nets := tunNetsFromConfig(req.Config)
	srv.mu.Lock()
	srv.blockIface, srv.blockNets = iface, nets
	srv.mu.Unlock()

	// With every tunnel, whatever the settings: see guard_linux.go.
	if err := installGuard(iface, nets); err != nil {
		srv.tunnel.stop()
		srv.dropBlock()
		return fmt.Errorf("tunnel guard: %w", err)
	}

	// After the engine, not before: the block names the tunnel interface, and
	// until the engine has created it there is nothing to allow.
	if req.KillSwitch {
		if err := installKillSwitch(iface, nets, req.ServerIPs, req.AllowLAN,
			func(l api.LogLine) { s.write(&api.Response{Log: &l}) }); err != nil {
			srv.tunnel.stop()
			srv.dropBlock() // a kept block included: nothing is coming to take it over
			return err
		}
	} else {
		removeKillSwitch()
	}

	srv.mu.Lock()
	srv.owner = s
	srv.mu.Unlock()

	lease := leaseDuration(req.LeaseSec)
	s.renew(lease)
	go srv.watchLease(s, lease)
	return nil
}

// watchLease tears the tunnel down when the pings stop.
//
// The disconnect path above covers a GUI that died. This covers one that is
// still connected but wedged — a hung process holds its socket open forever,
// and without a lease the tunnel would outlive it indefinitely.
func (srv *server) watchLease(s *session, lease time.Duration) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for range tick.C {
		srv.mu.Lock()
		still := srv.owner == s
		srv.mu.Unlock()
		if !still {
			return
		}
		if s.leaseLeft() <= 0 {
			log.Print("lease expired: tearing the tunnel down")
			// Tear down BEFORE telling anyone. The client that let its lease
			// lapse is quite likely the one that stopped reading, and notifying
			// it first put a blocking write in front of the teardown — which is
			// how a 3-second lease survived twenty seconds in testing.
			srv.tunnel.stop()
			srv.dropBlock()
			s.write(&api.Response{Log: &api.LogLine{
				Src: "helper", Lvl: "warn",
				Text: "lease expired — the app stopped pinging, tunnel closed",
			}})
			srv.mu.Lock()
			if srv.owner == s {
				srv.owner = nil
			}
			srv.mu.Unlock()
			return
		}
	}
}

// tunNetsFromConfig returns the tunnel's own prefixes — the addresses of the
// TUN inbound, masked — which the kill-switch lets through by destination (see
// killSwitchScript). Only prefixes that parse: this names what the block
// allows, so anything else is left out rather than guessed at.
func tunNetsFromConfig(raw []byte) []string {
	var doc struct {
		Inbounds []struct {
			Type    string   `json:"type"`
			Address []string `json:"address"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	var out []string
	for _, in := range doc.Inbounds {
		if in.Type != "tun" {
			continue
		}
		for _, a := range in.Address {
			if _, n, err := net.ParseCIDR(a); err == nil {
				out = append(out, n.String())
			}
		}
	}
	return out
}

// peerDescription names the process on the other end for the journal. Purely
// informational — the access decision was made by the socket's permissions
// before this connection existed.
func peerDescription(nc net.Conn) string {
	uc, ok := nc.(*net.UnixConn)
	if !ok {
		return "unknown"
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return "unknown"
	}
	var cred *syscall.Ucred
	var credErr error
	raw.Control(func(fd uintptr) { //nolint:errcheck
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if credErr != nil || cred == nil {
		return "unknown"
	}
	return fmt.Sprintf("pid=%d uid=%d", cred.Pid, cred.Uid)
}

// dirGroup returns the group owning a directory — the group systemd was told to
// give the runtime directory, and therefore the group allowed to talk to us.
func dirGroup(path string) (int, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("stat returned no unix metadata")
	}
	return int(st.Gid), nil
}
