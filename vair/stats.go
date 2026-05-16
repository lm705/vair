package main

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
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 {
		cr.n.Add(int64(n))
	}
	return n, err
}

// startCountingForwarder accepts connections on listenPort and forwards
// them to 127.0.0.1:targetPort, counting bytes in both directions into
// the counter. listenPort==0 lets the OS pick a free port.
//
// Returns the actual listen port (useful when listenPort was 0) and an
// error. The returned cancel context, when cancelled, closes the listener
// and stops accepting new connections; existing relays drain naturally.
func startCountingForwarder(ctx context.Context, listenPort, targetPort int, counter *trafficCounter, label string) (int, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort))
	if err != nil {
		return 0, fmt.Errorf("listen on %d: %w", listenPort, err)
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
			go relayCountedConn(conn, targetPort, counter)
		}
	}()
	_ = label // reserved for future per-direction logging
	return port, nil
}

// relayCountedConn shuttles bytes between a client and xray, counting
// uplink (client→xray) and downlink (xray→client) into the shared
// counter. Closes both connections when either direction ends.
func relayCountedConn(client net.Conn, targetPort int, counter *trafficCounter) {
	defer client.Close()
	target, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", targetPort), 5*time.Second)
	if err != nil {
		return
	}
	defer target.Close()

	// Two goroutines, one per direction. Each finishes when its source
	// hits EOF or its destination closes. We wait for both with a
	// WaitGroup so we don't tear down the connections mid-flush.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(target, &countingReader{r: client, n: &counter.Up}) //nolint:errcheck
		// Signal write half-close so xray sees the end of the upstream;
		// it can finish flushing its response before we tear everything
		// down. Errors here are benign.
		if t, ok := target.(*net.TCPConn); ok {
			t.CloseWrite() //nolint:errcheck
		}
	}()
	go func() {
		defer wg.Done()
		io.Copy(client, &countingReader{r: target, n: &counter.Down}) //nolint:errcheck
		if c, ok := client.(*net.TCPConn); ok {
			c.CloseWrite() //nolint:errcheck
		}
	}()
	wg.Wait()
}
