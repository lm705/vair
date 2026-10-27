//go:build windows || linux

package core

import (
	"net"
	"strconv"
	"sync"
	"time"
)

// The cache in front of the system's table of who owns which port. Reading the
// table walks every TCP connection on the machine (and on Linux every process's
// open files), so the answer is kept for procPortTTL; tcpPortOwners is the
// platform's own half.

var procPortCache = struct {
	sync.Mutex
	m    map[uint16]string
	when time.Time
}{m: map[uint16]string{}}

const procPortTTL = 2 * time.Second

// processForLocalPort maps "127.0.0.1:54321" — the client's end of a connection
// the relay accepted at opened — to the name of the program that owns that
// port, or "" when it cannot be told.
//
// A port missing from a snapshot taken before the connection existed says
// nothing about it, so such a miss reads the table again. A browser opens a
// page's connections in a burst: the rule used to be "read again at most every
// 100 ms", and every connection after the first in a burst came back without a
// program — 19 of 20 in TestABurstOfConnectionsIsNamed, and a dash in the
// program column for most of what Chrome opened. A port missing from a
// snapshot taken after the connection opened really is unknown: a process the
// program may not inspect, or a connection already closed. Reads stay bounded:
// one read answers every connection that opened before it.
func processForLocalPort(localAddr string, opened time.Time) string {
	_, portStr, err := net.SplitHostPort(localAddr)
	if err != nil {
		return ""
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 || p > 65535 {
		return ""
	}
	port := uint16(p)

	procPortCache.Lock()
	defer procPortCache.Unlock()
	read := func() {
		procPortCache.m = tcpPortOwners()
		procPortCache.when = time.Now()
	}
	if time.Since(procPortCache.when) > procPortTTL {
		read()
	}
	name, ok := procPortCache.m[port]
	// Not After rather than Before: on Windows the clock moves in timer ticks,
	// and a read and a connection inside the same tick carry the same time.
	if !ok && !procPortCache.when.After(opened) {
		read()
		name = procPortCache.m[port]
	}
	return name
}
