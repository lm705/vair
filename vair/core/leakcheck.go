package core

import (
	"encoding/binary"
	"math/rand/v2"
	"net/netip"
	"os/exec"
	"sync"
	"time"
)

// The leak check: after a connection comes up, try to get a packet out of this
// machine the way a leak would — around the tunnel, from the physical adapter —
// and see whether anything answers.
//
// It audits the protection on the machine it runs on rather than on the one it
// was designed on. Filters that did not take, another product's rules, a driver
// that reorders the stack, a routing table someone changed: none of them shows
// up in the settings, all of them show up here. The idea is Mullvad's, and so
// is the caution that makes it worth trusting — an answer proves a leak; no
// answer proves the probe did not get out, which is the result we want, and is
// said as exactly that.
//
// It is only run where something claims to hold traffic in, and it tests what
// is claimed: every packet under a kill-switch or a block of ours, and DNS
// under leak protection in TUN, which holds lookups in by itself — the engine's
// DNS filter, not the kill-switch. That second half used to go untested: a
// session with protection on and the kill-switch off was "does not apply",
// and the adapter trace that found Windows asking the provider's resolver in
// parallel was the first anyone knew of it. Proxy mode without its
// kill-switch never claimed to stop other programs, so there is nothing there
// to test.

// Verdicts, as the session panel shows them.
const (
	leakNotRun   = "not run yet"
	leakPassed   = "passed"
	leakFound    = "LEAK — traffic gets out"
	leakNotApply = "does not apply"
	leakCannot   = "could not run"
	leakNoProbe  = "not available on this system"
)

type leakCheckResult struct {
	verdict string
	detail  string // for the log: which adapter, which address
	note    string // for the panel, when the verdict needs its own; see leakNote
	at      time.Time
}

// What the session panel says under each verdict: why it came out that way,
// and what would change it. Fixed phrases, so the panel can put them in the
// user's language; adapter names and addresses stay in the log.
var leakNotes = map[string]string{
	leakNotRun:  "The check runs a few seconds after the connection comes up.",
	leakPassed:  "Test packets and DNS queries sent from the physical network adapter, around the tunnel, did not get out: the protection holds traffic in.",
	leakFound:   "A test packet sent from the physical network adapter, around the tunnel, was answered: traffic can leave outside the tunnel. The log names the adapter.",
	leakCannot:  "The check could not run; the log says why.",
	leakNoProbe: "This system has no way to send the test packets.",
}

// The DNS-only halves: a TUN session with leak protection and without the
// kill-switch claims DNS and nothing else, and a DNS query that gets out is a
// leak of its own kind.
const (
	leakPassedDNS = "DNS queries sent from the physical network adapter, around the tunnel, got no answer: names are looked up only through the tunnel. Other traffic is not held in, because the kill-switch is off."
	leakFoundDNS  = "A DNS query sent from the physical network adapter, around the tunnel, was answered: names can be looked up outside the tunnel, where the provider sees them. The log names the adapter and the resolver."
)

// Why the check does not apply, per mode. It tests a promise to hold traffic
// in, and these sessions make none.
const (
	leakWhyProxy = "Proxy mode without its kill-switch does not hold other programs in: those not set to use the proxy go out directly by design, so there is nothing to test. Turn on the proxy kill-switch to have it checked."
	leakWhyTUN   = "Without TUN DNS leak protection the names of the sites behind the tunnel go to the system resolver, outside it, by design, so there is nothing to test. Turn DNS leak protection on to have it checked."
)

// leakClaim is what a session promises to hold in: every packet (a kill-switch,
// or a block of ours standing), or DNS (leak protection in TUN). Every packet
// includes DNS: each block has its own "no DNS outside the tunnel" filter.
type leakClaim struct{ traffic, dns bool }

func (c leakClaim) any() bool { return c.traffic || c.dns }

// leakClaimOf reads the claim of the session cm is running. Leak protection is
// the session's own — a change reaches the next connection, not this one —
// while the kill-switch and a standing block act on the running session at once.
func leakClaimOf(cm *connManager, mode ConnMode) leakClaim {
	if blockStanding() {
		return leakClaim{traffic: true, dns: true}
	}
	if tunnelBlockHeld() {
		// The session's own block: the proxy kill-switch's, or the TUN one —
		// which goes up the moment the switch allows it, also in a session
		// started without leak protection. That session said "Kill-switch: on,
		// blocking everything outside the tunnel" and "leak check: does not
		// apply" side by side.
		return leakClaim{traffic: true, dns: true}
	}
	if mode != ModeTUN {
		return leakClaim{}
	}
	protect := dnsLeakProtectionEnabled()
	if cm != nil {
		cm.mu.Lock()
		if f := cm.startedWith; f != nil {
			protect = f.LeakProtect
		}
		cm.mu.Unlock()
	}
	return leakClaim{traffic: protect && killSwitchEnabled(), dns: protect}
}

// leakNote is the panel's line for a result.
func leakNote(r leakCheckResult) string {
	if r.note != "" {
		return r.note
	}
	return leakNotes[r.verdict]
}

var leakState struct {
	sync.Mutex
	last leakCheckResult
	run  uint64 // the latest check started; see newLeakRun
}

func setLeakResult(r leakCheckResult) {
	r.at = time.Now()
	leakState.Lock()
	leakState.last = r
	leakState.Unlock()
}

// newLeakRun numbers a check. A later one — a kill-switch switched again, a new
// session — supersedes a check still probing, whose answer is then dropped: the
// switch turned on started a probe, the switch turned off a few seconds later
// said "does not apply", and the first probe came back and wrote "passed" over
// it about a block that was gone.
func newLeakRun() uint64 {
	leakState.Lock()
	defer leakState.Unlock()
	leakState.run++
	return leakState.run
}

// setLeakResultOf records a result only while its check is still the latest.
func setLeakResultOf(run uint64, r leakCheckResult) bool {
	r.at = time.Now()
	leakState.Lock()
	defer leakState.Unlock()
	if leakState.run != run {
		return false
	}
	leakState.last = r
	return true
}

func lastLeakResult() leakCheckResult {
	leakState.Lock()
	defer leakState.Unlock()
	if leakState.last.verdict == "" {
		return leakCheckResult{verdict: leakNotRun}
	}
	return leakState.last
}

// leakCheckApplies says whether this session claims to hold anything in.
func leakCheckApplies(mode ConnMode) bool {
	return leakClaimOf(state.conn, mode).any()
}

// leakCheckDelay is how long after "connected" the probe goes out: long enough
// for the routes and the filters of a fresh tunnel to settle, short enough to
// tell the user while they are still looking.
const leakCheckDelay = 5 * time.Second

// runLeakCheckAfterConnect runs one check for the session that just came up,
// and drops the answer if that session is gone by the time it arrives.
func runLeakCheckAfterConnect(cm *connManager, cmd *exec.Cmd, mode ConnMode) {
	run := newLeakRun()
	notApply := func() {
		// The reason depends on the mode: this used to give the proxy-mode one
		// for a TUN session without its kill-switch too.
		why := leakWhyProxy
		if mode == ModeTUN {
			why = leakWhyTUN
		}
		setLeakResultOf(run, leakCheckResult{verdict: leakNotApply, detail: why, note: why})
	}
	if !leakClaimOf(cm, mode).any() {
		notApply()
		return
	}
	setLeakResultOf(run, leakCheckResult{verdict: leakNotRun})
	time.Sleep(leakCheckDelay)
	if !sessionStillOurs(cm, cmd) {
		return
	}
	// Decided again after the wait. A check started by switching the proxy
	// kill-switch off still saw its block; the block was gone by the time the
	// probe went out, and the panel said "LEAK" about a session that claims
	// nothing — the same session reads "does not apply" when it starts that way.
	claim := leakClaimOf(cm, mode)
	if !claim.any() {
		notApply()
		return
	}
	r := leakProbe(claim)
	if !sessionStillOurs(cm, cmd) || !setLeakResultOf(run, r) {
		return
	}
	switch r.verdict {
	case leakFound:
		vlog("error", "leak check: %s — the protection is not holding traffic in", r.detail)
		desktopNotify("Vair", notifyLeakFound(r.note == leakFoundDNS))
	case leakPassed:
		vlog("info", "leak check: %s", r.detail)
	case leakCannot:
		vlog("warning", "leak check: could not run — %s", r.detail)
	}
}

// leakRowIfLive is the check as the session panel shows it: the verdict for
// the session that is running. With nothing connected there is no session to
// have checked, and the row says so rather than repeating an old answer.
func leakRowIfLive(live bool) SessionFactRow {
	if !live {
		return SessionFactRow{Label: "Leak check", Value: "not connected"}
	}
	r := lastLeakResult()
	return SessionFactRow{Label: "Leak check", Value: r.verdict, Note: leakNote(r)}
}

// recheckLeak brings the verdict in line with a kill-switch turned on or off
// during the session. On, the check runs for this session now rather than
// staying "does not apply" until the next connection; off, a "passed" about a
// protection that is gone is replaced.
func recheckLeak() {
	cm := state.conn
	cm.mu.Lock()
	cmd, mode, up := cm.cmd, cm.state.Mode, cm.state.Status == ConnConnected
	cm.mu.Unlock()
	if up {
		go runLeakCheckAfterConnect(cm, cmd, mode)
	}
}

func notifyLeakFound(dns bool) string {
	settingsMu.RLock()
	lang := appSettings.Language
	settingsMu.RUnlock()
	switch {
	case dns && lang == "ru":
		return "Проверка утечек: DNS-запрос получил ответ мимо туннеля. Имена сайтов могут уходить провайдеру — подробности в журнале"
	case dns:
		return "Leak check: a DNS query was answered outside the tunnel. Site names can reach the provider — see the log"
	case lang == "ru":
		return "Проверка утечек: пакет вышел с этой машины мимо туннеля. Защита не удерживает трафик — подробности в журнале"
	}
	return "Leak check: a packet left this machine outside the tunnel. The protection is not holding traffic in — see the log"
}

// leakProbeTargets are where the probes are aimed. The address hardly matters:
// with a TTL of one to three a probe never gets there — on an ordinary network
// the first routers answer "time exceeded" instead, and that answer is the
// leak. They are public anycast resolvers because every network routes to them.
var leakProbeTargets = map[bool]string{false: "1.1.1.1", true: "2606:4700:4700::1111"}

// dnsProbeQuery is a standard query for the A record of example.com — a name
// every resolver answers, so no answer means the question never left.
func dnsProbeQuery() []byte {
	q := []byte{0, 0, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(q, uint16(rand.Uint32()))
	for _, label := range []string{"example", "com"} {
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	return append(q, 0, 0, 1, 0, 1)
}

func isULA(ip netip.Addr) bool {
	b := ip.As16()
	return b[0]&0xfe == 0xfc
}
