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
}

// countingReader wraps an io.Reader and tallies the bytes it produces
// into an atomic counter. Used by relayCountedConn for both directions.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
	// also is the live list's per-connection counter, when the panel is open.
	// Nil the rest of the time, which is the ordinary case.
	also *atomic.Int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 {
		cr.n.Add(int64(n))
		if cr.also != nil {
			cr.also.Add(int64(n))
		}
	}
	return n, err
}

// liveUp / liveDown give the row's counters, or nil when nothing is tracked.
func liveUp(e *connEntry) *atomic.Int64 {
	if e == nil {
		return nil
	}
	return e.Up
}

func liveDown(e *connEntry) *atomic.Int64 {
	if e == nil {
		return nil
	}
	return e.Down
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
func relayCountedConn(client net.Conn, targetPort int, counter *trafficCounter, label string) {
	defer client.Close()
	target, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", targetPort), 5*time.Second)
	if err != nil {
		return
	}
	defer target.Close()

	// The live list, when someone is looking at it. The first bytes of the
	// conversation name the destination the client asked for; they are read into
	// a buffer and handed on unchanged, so a client whose greeting we do not
	// recognise is not affected in any way.
	var live *connEntry
	var prefix []byte
	if connectionsWatched() {
		buf := make([]byte, 1024)
		client.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
		n, rerr := client.Read(buf)
		client.SetReadDeadline(time.Time{}) //nolint:errcheck
		if n > 0 {
			prefix = buf[:n]
			dest := peekDestination(prefix)
			if dest != "" {
				// Who asked for it — but only where the answer means anything.
				// In proxy mode the client of this relay is the user's own
				// program; in TUN it is sing-box, one step further along the
				// same chain, and naming it would put "sing-box" in the program
				// column of every row on the machine. Unknown is the truth
				// there, and the panel already has a way to say it.
				//
				// The verdict is left empty for the opposite reason in proxy
				// mode: which way a connection goes is the engine's decision and
				// it says so in its own log a moment later, so claiming "proxy"
				// here would label a direct-routed connection as tunnelled. In
				// TUN this relay only ever carries what the engine has already
				// decided to tunnel, so it can say so itself.
				process, via := processForLocalPort(client.RemoteAddr().String()), ""
				if label == relayLabelTUN {
					// The engine is one step ahead of us on the same
					// connection: take the row it has already opened rather
					// than list the connection a second time.
					live = adoptEngineRow(dest)
					// And when there is none — the engine's log can be quiet,
					// or a line can be lost to the rate gate — open one that
					// does not claim to know who asked. The client of this
					// relay is sing-box, one step along the same chain, and
					// naming it would put the engine in the program column of
					// every row on the machine. Where it went we do know:
					// everything this relay carries is already bound for the
					// tunnel.
					process, via = "", "proxy"
				}
				if live == nil {
					live = connBegin(dest, process, via, "", true)
				}
				defer connEnd(live)
			}
		}
		// A read that timed out or failed with nothing to show for it is left
		// alone: the relay below behaves exactly as it did before this feature
		// existed. Describing a connection must never be able to end one.
		_ = rerr
		if len(prefix) > 0 {
			if _, werr := target.Write(prefix); werr != nil {
				return
			}
			if live != nil {
				live.Up.Add(int64(len(prefix)))
			}
			counter.Up.Add(int64(len(prefix)))
		}
	}

	// Two goroutines, one per direction. Each finishes when its source
	// hits EOF or its destination closes. We wait for both with a
	// WaitGroup so we don't tear down the connections mid-flush.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(target, &countingReader{r: client, n: &counter.Up, also: liveUp(live)}) //nolint:errcheck
		// Signal write half-close so xray sees the end of the upstream;
		// it can finish flushing its response before we tear everything
		// down. Errors here are benign.
		if t, ok := target.(*net.TCPConn); ok {
			t.CloseWrite() //nolint:errcheck
		}
	}()
	go func() {
		defer wg.Done()
		io.Copy(client, &countingReader{r: target, n: &counter.Down, also: liveDown(live)}) //nolint:errcheck
		if c, ok := client.(*net.TCPConn); ok {
			c.CloseWrite() //nolint:errcheck
		}
	}()
	wg.Wait()
}
