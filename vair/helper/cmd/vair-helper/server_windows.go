package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"vair/helper/api"
	"vair/helper/validate"
	"vair/helper/wfp"
)

// The Windows service's request handling.
//
// The same contract as the Linux helper: one tunnel per machine, owned by the
// connection that started it; the owner gone — its process ended, its pipe
// closed, its lease run out — means the tunnel goes. What Windows adds is the
// block the app used to hold itself: the session block (the tunnel's, or the
// proxy kill-switch's), which a teardown hands to the next session for
// BlockHandoverSec, and the persistent block, which stays until someone with
// the right to says otherwise.
//
// Who has the right: the owner of the tunnel for the tunnel and its block;
// the account that put the persistent block up for that block; an elevated
// administrator for anything — the "take over" a shared machine needs. Every
// other client may look (status, block_status) and open the firewall rules,
// which name only this installation's programs.

// handoverGrace is how long a block handed over by a teardown waits for the
// next session. A variable for the tests.
var handoverGrace = time.Duration(api.BlockHandoverSec) * time.Second

// luidWait bounds the wait for a fresh tunnel's adapter to appear, which the
// block needs to name it.
var luidWait = 10 * time.Second

type session struct {
	c   *api.Conn
	nc  net.Conn
	who client
	mu  sync.Mutex // serialises writes: the encoder is not concurrency-safe

	greeted    bool
	ruleSets   map[string]bool
	leaseUntil time.Time
	leaseMu    sync.Mutex
}

func (s *session) write(r *api.Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.nc.(interface{ SetWriteDeadline(time.Time) error }); ok {
		d.SetWriteDeadline(time.Now().Add(writeTimeout)) //nolint:errcheck
		defer d.SetWriteDeadline(time.Time{})            //nolint:errcheck
	}
	s.c.WriteResponse(r) //nolint:errcheck
}

func (s *session) logf(l api.LogLine) { s.write(&api.Response{Log: &l}) }

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

type server struct {
	p   paths
	eng engine
	sys system

	mu    sync.Mutex
	owner *session // the connection whose tunnel runs; nil when none
	iface string   // the running tunnel's adapter
	stack string   // its TCP/IP stack: gvisor needs no inbound rule

	// The session block: the tunnel's, or the proxy kill-switch's.
	block     func()      // takes it down; nil when none stands
	blockOf   *session    // the connection it belongs to; nil while handed over
	blockKept *time.Timer // the hand-over's deadline

	persist persisted // the persistent block's record
}

func newServer(p paths, eng engine, sys system) *server {
	return &server{p: p, eng: eng, sys: sys, persist: loadPersisted(p.statePath)}
}

// startup clears what a service that never got to stop left behind: an engine
// still running from this installation, the adapters, the engine's inbound
// rule. The persistent block is left exactly as it is — that is the point of
// it.
func (srv *server) startup() {
	srv.sys.ghostSweep([]string{srv.p.engine})
	srv.sys.removeEngineRule(srv.p.engine)
	if srv.persist.Persistent {
		if present, known := srv.sys.persistentPresent(); known && !present {
			log.Print("the persistent block is on record but not in place — something removed it; dropping the record")
			srv.persist = persisted{}
			savePersisted(srv.p.statePath, srv.persist)
		}
	}
}

// shutdown is the service stopping, or Windows going down: the tunnel and the
// session block go; the persistent block stays.
func (srv *server) shutdown() {
	srv.stopTunnel()
	srv.dropBlock()
}

func (srv *server) serve(nc net.Conn, who client) {
	s := &session{c: api.NewConn(nc), nc: nc, who: who}
	log.Printf("client connected (%s)", who)
	defer func() {
		nc.Close()
		srv.mu.Lock()
		owned := srv.owner == s
		blockMine := srv.blockOf == s
		srv.mu.Unlock()
		if owned {
			// THE teardown rule: a tunnel goes with the connection that owns it.
			// An app that crashed or was killed does not get to leave one behind.
			log.Printf("owner disconnected (%s): tearing the tunnel down", who)
			srv.stopTunnel()
		}
		if owned || blockMine {
			// The block is handed over rather than dropped: an app that crashed
			// comes back within the grace, and until then nothing leaves outside
			// the tunnel it was protecting.
			srv.keepBlock()
		}
		srv.mu.Lock()
		running := srv.owner != nil
		srv.mu.Unlock()
		if !running {
			for name := range s.ruleSets {
				os.Remove(filepath.Join(srv.p.ruleSetDir, name)) //nolint:errcheck
			}
		}
		log.Printf("client gone (%s)", who)
	}()

	for {
		req, err := s.c.ReadRequest()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("read from %s: %v", who, err)
			}
			return
		}
		if req.ID == 0 {
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
	fail := func(err error) { s.write(&api.Response{ID: req.ID, Error: err.Error()}) }
	switch req.Op {
	case api.OpHello:
		s.greeted = true
		s.write(&api.Response{ID: req.ID, OK: true, Protocol: api.ProtocolVersion, Version: version})

	case api.OpStatus:
		st := srv.eng.status()
		if left := s.leaseLeft(); left > 0 {
			st.LeaseExpiresInSec = int(left.Seconds())
		}
		srv.mu.Lock()
		st.Mine = srv.owner != nil && srv.owner == s
		if srv.owner != nil && srv.owner != s {
			st.Owner = srv.owner.who.user
		}
		srv.mu.Unlock()
		s.write(&api.Response{ID: req.ID, OK: true, Status: &st, Block: srv.blockState(false)})

	case api.OpBlockStatus:
		s.write(&api.Response{ID: req.ID, OK: true, Block: srv.blockState(true)})

	case api.OpPing:
		s.renew(leaseDuration(req.LeaseSec))
		s.write(&api.Response{ID: req.ID, OK: true})

	case api.OpPutRuleSet:
		path, err := srv.putRuleSet(s, req)
		if err != nil {
			fail(err)
			return
		}
		s.write(&api.Response{ID: req.ID, OK: true, Path: path})

	case api.OpStartTUN:
		if err := srv.startTUN(s, req); err != nil {
			fail(err)
			return
		}
		st := srv.eng.status()
		st.Mine = true
		s.write(&api.Response{ID: req.ID, OK: true, Status: &st})

	case api.OpStop:
		if err := srv.stop(s, req); err != nil {
			fail(err)
			return
		}
		s.write(&api.Response{ID: req.ID, OK: true})

	case api.OpKillSwitch:
		if err := srv.killSwitch(s, req); err != nil {
			fail(err)
			return
		}
		s.write(&api.Response{ID: req.ID, OK: true})

	case api.OpSessionBlock:
		if err := srv.sessionBlock(s, req); err != nil {
			fail(err)
			return
		}
		s.write(&api.Response{ID: req.ID, OK: true, Block: srv.blockState(false)})

	case api.OpPersistentBlock:
		if err := srv.persistentBlock(s, req); err != nil {
			fail(err)
			return
		}
		s.write(&api.Response{ID: req.ID, OK: true, Block: srv.blockState(false)})

	case api.OpFirewall:
		if err := srv.firewall(req); err != nil {
			fail(err)
			return
		}
		s.write(&api.Response{ID: req.ID, OK: true})

	default:
		fail(fmt.Errorf("unknown op %q", req.Op))
	}
}

// mayDrive says whether s may start, stop or change the tunnel: nobody holds
// it, the same user in the same logon session holds it (the app keeps more
// than one connection), or s is an elevated administrator.
func (srv *server) mayDrive(s *session) (bool, string) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.owner == nil || sameSeat(srv.owner, s) || s.who.admin {
		return true, ""
	}
	return false, srv.owner.who.user
}

// sameSeat: the same account in the same logon session — the same person at
// the same desktop, whichever connection of theirs it is.
func sameSeat(a, b *session) bool {
	return a == b || a.who.sid != "" && a.who.sid == b.who.sid && a.who.sessionID == b.who.sessionID
}

func (srv *server) putRuleSet(s *session, req *api.Request) (string, error) {
	if ok, holder := srv.mayDrive(s); !ok {
		return "", fmt.Errorf("the tunnel belongs to %s's session", holder)
	}
	if !ruleSetName.MatchString(req.Name) {
		return "", fmt.Errorf("%q is not an acceptable rule-set name", req.Name)
	}
	if len(req.Data) == 0 {
		return "", errors.New("rule-set is empty")
	}
	if len(req.Data) > api.MaxRuleSetBytes {
		return "", fmt.Errorf("rule-set is %d bytes, over the %d limit", len(req.Data), api.MaxRuleSetBytes)
	}
	if _, replacing := s.ruleSets[req.Name]; len(s.ruleSets) >= api.MaxRuleSets && !replacing {
		return "", fmt.Errorf("no more than %d rule-sets per session", api.MaxRuleSets)
	}
	path := filepath.Join(srv.p.ruleSetDir, req.Name)
	if err := os.WriteFile(path, req.Data, 0o600); err != nil {
		return "", fmt.Errorf("write rule-set: %w", err)
	}
	if s.ruleSets == nil {
		s.ruleSets = make(map[string]bool)
	}
	s.ruleSets[req.Name] = true
	return path, nil
}

func (srv *server) startTUN(s *session, req *api.Request) error {
	if len(req.Config) == 0 {
		return errors.New("start_tun needs a config")
	}
	opt := validate.Options{Windows: true, AllowedPathPrefixes: []string{srv.p.ruleSetDir}}
	if err := validate.Config(req.Config, opt); err != nil {
		log.Printf("rejected config from %s: %v", s.who, err)
		return fmt.Errorf("config rejected: %w", err)
	}

	srv.mu.Lock()
	prev := srv.owner
	other := prev != nil && !sameSeat(prev, s)
	srv.mu.Unlock()
	if other && !s.who.admin {
		return fmt.Errorf("the tunnel belongs to %s's session", prev.who.user)
	}
	if other {
		log.Printf("%s takes the tunnel over from %s", s.who, prev.who)
		prev.write(&api.Response{Log: &api.LogLine{Src: "helper", Lvl: "warn",
			Text: "an administrator took the tunnel over"}})
		prev.write(&api.Response{Status: &api.Status{Running: false}})
	}
	// The same owner asking again is a switch: the old engine goes, its block
	// stands until the new one replaces it — two blocks for a moment is traffic
	// stopped, a gap between them is not.
	if srv.eng.running() {
		srv.stopTunnel()
	}

	iface, stack := ifaceFromConfig(req.Config), stackFromConfig(req.Config)
	err := srv.eng.start(req.Config, iface, s.logf, func() {
		// The engine died without being asked to. Tell the owner in a form it
		// can act on, and hand the block over: a reconnect within the grace
		// keeps traffic in, none lets it go.
		s.write(&api.Response{Status: &api.Status{Running: false}})
		srv.mu.Lock()
		owned := srv.owner == s
		if owned {
			srv.owner, srv.iface, srv.stack = nil, "", ""
		}
		srv.mu.Unlock()
		if owned {
			srv.afterEngine(stack)
			srv.keepBlock()
		}
	})
	if err != nil {
		// A block standing for this start stays for its grace: AUTO goes from
		// candidate to candidate, and a failed one must not let traffic out
		// before the next.
		srv.keepBlock()
		return err
	}

	if stack != "gvisor" {
		if err := srv.sys.engineRule(srv.p.engine, iface); err != nil {
			s.logf(api.LogLine{Src: "helper", Lvl: "warn", Text: "the engine's inbound rule could not be opened: " + err.Error()})
		}
	}

	srv.mu.Lock()
	srv.owner, srv.iface, srv.stack = s, iface, stack
	srv.mu.Unlock()

	if req.KillSwitch {
		// Never fatal, as in the app: a tunnel without its block is the tunnel
		// as it was, and the log and the leak check both say what it lacks.
		if err := srv.tunnelBlock(s, iface, req.AllowLAN, req.IPv6Around); err != nil {
			log.Printf("tunnel block for %s: %v", iface, err)
			s.logf(api.LogLine{Src: "helper", Lvl: "error",
				Text: "kill-switch: the tunnel's block could not be put up — " + err.Error()})
		}
	} else {
		srv.dropBlock()
	}

	lease := leaseDuration(req.LeaseSec)
	s.renew(lease)
	go srv.watchLease(s, lease)
	return nil
}

// stopTunnel ends the engine and clears up after it.
func (srv *server) stopTunnel() {
	srv.mu.Lock()
	stack := srv.stack
	srv.owner, srv.iface, srv.stack = nil, "", ""
	srv.mu.Unlock()
	if !srv.eng.running() {
		return
	}
	srv.eng.stop()
	srv.afterEngine(stack)
}

// afterEngine removes what an engine leaves: its inbound rule, and — since a
// crash leaves the adapter registered and a fresh connect then fails on the
// name — the adapters. Synchronous, so a start that follows cannot race it.
func (srv *server) afterEngine(stack string) {
	if stack != "gvisor" {
		srv.sys.removeEngineRule(srv.p.engine)
	}
	srv.sys.sweepAdapters("")
}

func (srv *server) stop(s *session, req *api.Request) error {
	if ok, holder := srv.mayDrive(s); !ok {
		return fmt.Errorf("the tunnel belongs to %s's session", holder)
	}
	srv.stopTunnel()
	if req.KeepBlock {
		srv.keepBlock()
	} else {
		srv.dropBlock()
	}
	return nil
}

// killSwitch is kill_switch: the block put up or taken down for the running
// tunnel, mid-session. Taking a block down with no tunnel running is how a
// disconnect ends one kept for a successor that is not coming.
func (srv *server) killSwitch(s *session, req *api.Request) error {
	if ok, holder := srv.mayDrive(s); !ok {
		return fmt.Errorf("the tunnel belongs to %s's session", holder)
	}
	if !req.KillSwitch {
		srv.dropBlock()
		return nil
	}
	srv.mu.Lock()
	iface, owned := srv.iface, srv.owner != nil && sameSeat(srv.owner, s)
	srv.mu.Unlock()
	if !owned || !srv.eng.running() {
		return errors.New("no tunnel of this connection to protect")
	}
	return srv.tunnelBlock(s, iface, req.AllowLAN, req.IPv6Around)
}

// sessionBlock is the proxy kill-switch's block: no tunnel to let through.
func (srv *server) sessionBlock(s *session, req *api.Request) error {
	if ok, holder := srv.mayDrive(s); !ok {
		return fmt.Errorf("the tunnel belongs to %s's session", holder)
	}
	if !req.Enable {
		srv.mu.Lock()
		theirs := srv.blockOf != nil && !sameSeat(srv.blockOf, s)
		srv.mu.Unlock()
		if theirs && !s.who.admin {
			return errors.New("the block belongs to another connection")
		}
		if req.KeepBlock {
			srv.keepBlock()
		} else {
			srv.dropBlock()
		}
		return nil
	}
	o := wfp.BlockOptions{Apps: srv.p.blockApps(), LAN: req.AllowLAN}
	return srv.setBlock(s, o)
}

func (srv *server) tunnelBlock(s *session, iface string, lan, v6Around bool) error {
	luid, err := srv.waitLUID(iface)
	if err != nil {
		return err
	}
	o := wfp.BlockOptions{Tunnel: luid, Apps: srv.p.blockApps(), LAN: lan, V6Around: v6Around}
	return srv.setBlock(s, o)
}

// waitLUID finds the tunnel's adapter, which the engine creates a moment after
// it starts.
func (srv *server) waitLUID(iface string) (uint64, error) {
	deadline := time.Now().Add(luidWait)
	for {
		luid, err := srv.sys.luid(iface)
		if err == nil {
			return luid, nil
		}
		if time.Now().After(deadline) {
			return 0, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// setBlock replaces the session block with one built from o, belonging to s.
// The new one is up before the old one comes down.
func (srv *server) setBlock(s *session, o wfp.BlockOptions) error {
	closeFn, err := srv.sys.openSessionBlock(o)
	if err != nil {
		return err
	}
	srv.mu.Lock()
	prev := srv.block
	srv.block, srv.blockOf = closeFn, s
	if srv.blockKept != nil {
		srv.blockKept.Stop()
		srv.blockKept = nil
	}
	srv.mu.Unlock()
	if prev != nil {
		prev()
	}
	return nil
}

// dropBlock takes the session block down now.
func (srv *server) dropBlock() {
	srv.mu.Lock()
	c := srv.block
	srv.block, srv.blockOf = nil, nil
	if srv.blockKept != nil {
		srv.blockKept.Stop()
		srv.blockKept = nil
	}
	srv.mu.Unlock()
	if c != nil {
		c()
	}
}

// keepBlock hands the session block over: it stands for handoverGrace, for the
// next session to take over, and comes down if none has. Every hand-over
// renews the grace, so a string of candidates keeps it up for as long as they
// keep coming.
func (srv *server) keepBlock() {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.block == nil {
		return
	}
	srv.blockOf = nil
	if srv.blockKept != nil {
		srv.blockKept.Stop()
	}
	var t *time.Timer
	t = time.AfterFunc(handoverGrace, func() {
		srv.mu.Lock()
		var c func()
		if srv.blockKept == t {
			c = srv.block
			srv.block, srv.blockKept = nil, nil
		}
		srv.mu.Unlock()
		if c != nil {
			c()
			log.Printf("no session came back within %s: the session block is down", handoverGrace)
		}
	})
	srv.blockKept = t
}

// persistentBlock puts the block that outlives everything up, or takes every
// block of that kind down — ours, and a Windows Firewall one of a build before
// 2.7.0.
func (srv *server) persistentBlock(s *session, req *api.Request) error {
	if req.Enable {
		if ok, holder := srv.mayDrive(s); !ok {
			return fmt.Errorf("the tunnel belongs to %s's session", holder)
		}
		srv.mu.Lock()
		iface := ""
		if srv.owner != nil {
			iface = srv.iface
		}
		srv.mu.Unlock()
		o := wfp.BlockOptions{Apps: srv.p.blockApps(), LAN: req.AllowLAN}
		if iface != "" {
			luid, err := srv.waitLUID(iface)
			if err != nil {
				return err
			}
			o.Tunnel = luid
		}
		if err := srv.sys.installPersistent(o); err != nil {
			return fmt.Errorf("the block could not be put in place: %w", err)
		}
		// A Windows Firewall block from an earlier build comes down only now
		// that this one stands: never a moment with neither.
		if srv.sys.legacyPresent() {
			if err := srv.sys.releaseLegacy(); err != nil {
				log.Printf("the old firewall block would not come down: %v", err)
			}
		}
		since := time.Now()
		if srv.persist.Persistent && !srv.persist.Since.IsZero() {
			since = srv.persist.Since
		}
		srv.persist = persisted{Persistent: true, OwnerSID: s.who.sid, Owner: s.who.user, LAN: req.AllowLAN, Since: since}
		savePersisted(srv.p.statePath, srv.persist)
		log.Printf("persistent block up for %s (tunnel %q)", s.who.user, iface)
		return nil
	}

	if sid := srv.persist.OwnerSID; srv.persist.Persistent && sid != "" && sid != s.who.sid && !s.who.admin {
		return fmt.Errorf("the block was put up by %s; only that account or an administrator can take it down", srv.persist.Owner)
	}
	if err := srv.sys.removePersistent(); err != nil {
		return fmt.Errorf("the block could not be taken down: %w", err)
	}
	if srv.sys.legacyPresent() {
		if err := srv.sys.releaseLegacy(); err != nil {
			return fmt.Errorf("the firewall could not be put back: %w", err)
		}
	}
	srv.persist = persisted{}
	savePersisted(srv.p.statePath, srv.persist)
	log.Printf("persistent block taken down by %s", s.who.user)
	return nil
}

// firewall opens or closes the rules for the web interface and the LAN-shared
// proxy. The client says ports; the rules name this installation's programs.
func (srv *server) firewall(req *api.Request) error {
	for _, p := range []int{req.RemotePort, req.LANHTTPPort, req.LANSocksPort} {
		if p != 0 && (p < 1024 || p > 65535) {
			return fmt.Errorf("port %d is outside 1024–65535", p)
		}
	}
	if req.RemotePort > 0 {
		srv.sys.remoteRule(req.RemotePort, srv.p.app)
	} else {
		srv.sys.removeRemoteRule()
	}
	if req.LANHTTPPort > 0 && req.LANSocksPort > 0 {
		srv.sys.lanRule(req.LANHTTPPort, req.LANSocksPort, srv.p.engine, srv.p.app)
	} else {
		srv.sys.removeLANRule()
	}
	return nil
}

// blockState says which blocks stand. withLegacy also asks Windows Firewall,
// which takes a PowerShell launch — only block_status pays for it.
func (srv *server) blockState(withLegacy bool) *api.BlockState {
	present, known := srv.sys.persistentPresent()
	srv.mu.Lock()
	session := srv.block != nil
	srv.mu.Unlock()
	bs := &api.BlockState{Persistent: present, Known: known, Session: session}
	if withLegacy {
		bs.Legacy = srv.sys.legacyPresent()
	}
	return bs
}

// watchLease tears the tunnel down when the pings stop: an app still connected
// but wedged holds its pipe open for ever.
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
			log.Printf("lease expired (%s): tearing the tunnel down", s.who)
			// Tear down BEFORE telling anyone: the client that let its lease
			// lapse is quite likely the one that stopped reading.
			srv.stopTunnel()
			srv.keepBlock()
			s.write(&api.Response{Log: &api.LogLine{Src: "helper", Lvl: "warn",
				Text: "lease expired — the app stopped pinging, tunnel closed"}})
			s.write(&api.Response{Status: &api.Status{Running: false}})
			return
		}
	}
}

// stackFromConfig is the tun inbound's stack. sing-box's default is not
// gvisor, so a missing one counts as needing the inbound rule.
func stackFromConfig(raw []byte) string {
	var doc struct {
		Inbounds []struct {
			Type  string `json:"type"`
			Stack string `json:"stack"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	for _, in := range doc.Inbounds {
		if in.Type == "tun" {
			return in.Stack
		}
	}
	return ""
}
