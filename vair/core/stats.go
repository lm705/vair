package core

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ─────────────────────────── traffic counters ────────────────────
//
// We measure VPN traffic by inserting a tiny localhost TCP forwarder
// between the consumer (system proxy / sing-box) and xray. The forwarder
// passes bytes through unchanged, but tallies them per direction. xray
// itself is unaware — we simply tell it to listen on an internal port
// and route the externally visible port through the counter.
//
// Why not xray's StatsService (gRPC)? It would buy nothing here and
// pull in the http2 package as a dependency. The forwarder is two dozen
// lines, has no external deps, and works identically for proxy mode and
// hybrid TUN mode.

// trafficCounter holds the byte counters for one VPN session. Up and
// Down are atomic so the reader/writer goroutines and the SSE
// broadcaster can touch them without locking.
//
// LastPersistedUp/Down record how many bytes from this session were
// already folded into the lifetime total on disk. The stats ticker
// periodically rolls (Up − LastPersistedUp) into the total and updates
// the marker; the final disconnect fold-in does the same for whatever
// is left. Apart from those markers we never decrement Up/Down — the
// session display in the UI must stay monotonically increasing for the
// whole session.
type trafficCounter struct {
	Up   atomic.Int64
	Down atomic.Int64

	LastPersistedUp   atomic.Int64
	LastPersistedDown atomic.Int64

	// SkippedUp/SkippedDown are the bytes that crossed while statistics were
	// switched off. Statistics leave them out of what they show and keep; Up
	// and Down stay whole, because the liveness checks read the raw traffic.
	SkippedUp   atomic.Int64
	SkippedDown atomic.Int64

	// ServerDown is the part of Down that can only have come from the server:
	// a connection's bytes back count here once it has received more than
	// serverReplyMin. Below that it may be nothing but the engine's own reply
	// to the proxy handshake — "200 Connection established", a SOCKS reply —
	// which it sends whether or not the server is there, and which made a dead
	// server look alive to the liveness checks for as long as anything tried
	// to connect.
	ServerDown atomic.Int64
}

// serverReplyMin is more than any proxy handshake reply and less than what a
// server sends back on any real exchange (a TLS handshake alone carries a
// certificate).
const serverReplyMin = 512

// skipUncounted sets aside what crossed since the last fold-in without adding
// it to the lifetime total. Called every tick while statistics are off, so
// switching them back on does not bring the off period into the total.
func skipUncounted(c *trafficCounter) {
	up, down := c.Up.Load(), c.Down.Load()
	c.SkippedUp.Add(up - c.LastPersistedUp.Load())
	c.SkippedDown.Add(down - c.LastPersistedDown.Load())
	c.LastPersistedUp.Store(up)
	c.LastPersistedDown.Store(down)
}

// statsTick is one second of the stats ticker, apart from the broadcast: it
// reports whether statistics are counting now. wasOn and ticks are the
// ticker's own state.
func statsTick(c *trafficCounter, enabled bool, wasOn *bool, ticks *int) bool {
	if !enabled {
		// The part of the session seen while statistics were on is kept first
		// — up to 30 s of it may not have been folded in yet — and from then
		// on nothing is.
		if *wasOn {
			persistCounterDelta(c)
			*wasOn = false
		}
		skipUncounted(c)
		return false
	}
	*wasOn = true
	*ticks++
	if *ticks >= 30 {
		*ticks = 0
		persistCounterDelta(c)
	}
	return true
}

// countingReader wraps an io.Reader and tallies the bytes it produces
// into an atomic counter. Used by relayCountedConn for both directions.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
	// also is this connection's own counter, which becomes its row's.
	also *atomic.Int64
	// spy, on the client's side, reads what the connection is for (relayspy.go).
	spy *destSpy
	// watch, on the client's side of a SOCKS port, reads the command of the
	// request (socksudp.go).
	watch *socksCmdWatch
	// server, on the direction back to the client, is trafficCounter.ServerDown;
	// seen is this connection's bytes so far, to know when it has passed
	// serverReplyMin.
	server *atomic.Int64
	seen   int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 {
		cr.n.Add(int64(n))
		if cr.also != nil {
			cr.also.Add(int64(n))
		}
		if cr.spy != nil {
			cr.spy.feed(p[:n])
		}
		if cr.watch != nil {
			cr.watch.feed(p[:n])
		}
		if cr.server != nil {
			cr.seen += int64(n)
			if cr.seen > serverReplyMin {
				cr.server.Add(int64(n))
			}
		}
	}
	return n, err
}

// relayLabelTUN marks the forwarder that sits between the two engines in hybrid
// TUN — sing-box hands it the connection, xray dials it out. Its clients are
// Vair's own engine rather than the user's programs, which changes what a row
// from it can honestly say; see relayCountedConn.
const relayLabelTUN = "tun-socks"

// startCountingForwarder accepts connections on bindHost:listenPort and forwards
// them to 127.0.0.1:targetPort, counting bytes in both directions into
// the counter. listenPort==0 lets the OS pick a free port. bindHost is
// "127.0.0.1" for local-only listeners or "0.0.0.0" to expose on the LAN.
//
// Returns the actual listen port (useful when listenPort was 0) and an
// error. The returned cancel context, when cancelled, closes the listener
// and stops accepting new connections; existing relays drain naturally.
func startCountingForwarder(ctx context.Context, bindHost string, listenPort, targetPort int, counter *trafficCounter, label string) (int, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bindHost, listenPort))
	if err != nil {
		return 0, fmt.Errorf("listen on %s:%d: %w", bindHost, listenPort, err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				// Listener closed (normal during shutdown) or transient.
				// Returning ends the accept loop; ctx.Done() above is the
				// reliable signal that we're winding down.
				return
			}
			go relayCountedConn(conn, targetPort, counter, label)
		}
	}()
	_ = label // reserved for future per-direction logging
	return port, nil
}

// relayCountedConn shuttles bytes between a client and xray, counting
// uplink (client→xray) and downlink (xray→client) into the shared
// counter. Closes both connections when either direction ends.
//
// Everything is forwarded from the first byte. Which connection it is — the
// row in the connections panel — is read off the client's bytes on the way
// through (relayspy.go) and opened the moment the request has been seen; the
// connection's own counters become the row's, so the bytes before that moment
// are on it too.
func relayCountedConn(client net.Conn, targetPort int, counter *trafficCounter, label string) {
	defer client.Close()
	target, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", targetPort), 5*time.Second)
	if err != nil {
		return
	}
	defer target.Close()

	opened := time.Now()
	var up, down atomic.Int64 // this connection's counts — the row's, once there is one
	var row atomic.Pointer[connEntry]
	defer func() {
		if e := row.Load(); e != nil {
			connEnd(e)
		}
	}()
	var spy *destSpy
	if connectionsWatched() {
		spy = newDestSpy(
			func(dest string) {
				if e := openRelayRow(dest, client, opened, label, &up, &down); e != nil {
					row.Store(e)
				}
			},
			func(name string) {
				if e := row.Load(); e != nil {
					nameRelayRow(e, name)
				}
			})
	}

	// A SOCKS client asking for UDP — a device on the network, a program here,
	// sing-box in TUN mode — gets Vair's relay, which it can reach and which
	// counts (socksudp.go); everything else passes as it is.
	var watch *socksCmdWatch
	var fromEngine io.Reader = target
	if label == "proxy-socks" || label == relayLabelTUN {
		w, wrap, cleanup := socksUDPRelay(client, counter)
		defer cleanup()
		if w != nil {
			watch, fromEngine = w, wrap(target)
		}
	}

	// Two goroutines, one per direction. Each finishes when its source
	// hits EOF or its destination closes. We wait for both with a
	// WaitGroup so we don't tear down the connections mid-flush.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(target, &countingReader{r: client, n: &counter.Up, also: &up, spy: spy, watch: watch}) //nolint:errcheck
		// Signal write half-close so xray sees the end of the upstream;
		// it can finish flushing its response before we tear everything
		// down. Errors here are benign.
		if t, ok := target.(*net.TCPConn); ok {
			t.CloseWrite() //nolint:errcheck
		}
	}()
	go func() {
		defer wg.Done()
		io.Copy(client, &countingReader{r: fromEngine, n: &counter.Down, also: &down, server: &counter.ServerDown}) //nolint:errcheck
		if c, ok := client.(*net.TCPConn); ok {
			c.CloseWrite() //nolint:errcheck
		}
	}()
	wg.Wait()
}

// openRelayRow opens the row for a relayed connection whose destination has just
// been read, and points it at the relay's counters.
//
// Who asked for it — but only where the answer means anything. In proxy mode
// the client of this relay is the user's own program; in TUN it is sing-box,
// one step further along the same chain, and naming it would put "sing-box" in
// the program column of every row on the machine. Unknown is the truth there.
//
// The verdict is left empty in proxy mode: which way a connection goes is the
// engine's decision and it says so in its own log a moment later, so claiming
// "proxy" here would label a direct-routed connection as tunnelled. In TUN this
// relay only ever carries what the engine has already decided to tunnel.
func openRelayRow(dest string, client net.Conn, opened time.Time, label string, up, down *atomic.Int64) *connEntry {
	var live *connEntry
	process, via := "", ""
	if label == relayLabelTUN {
		// The engine is one step ahead of us on the same connection: take the
		// row it has already opened rather than list the connection twice.
		live = adoptEngineRow(dest)
		via = "proxy"
	} else {
		process = processForLocalPort(client.RemoteAddr().String(), opened)
	}
	if live == nil {
		live = connBegin(dest, process, via, "", true)
	}
	if live == nil {
		return nil
	}
	conns.Lock()
	live.Up, live.Down = up, down
	conns.Unlock()
	return live
}

// nameRelayRow shows the name the client meant in place of a bare address.
func nameRelayRow(e *connEntry, name string) {
	conns.Lock()
	defer conns.Unlock()
	host, port, err := net.SplitHostPort(e.Dest)
	if err != nil || net.ParseIP(host) == nil {
		return
	}
	e.Dest = net.JoinHostPort(name, port)
}
