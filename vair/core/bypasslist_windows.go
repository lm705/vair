//go:build windows

package core

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// What is talking to the internet WITHOUT going through us.
//
// The live list next door can only show connections that came through our own
// listener, and in proxy mode that is a choice each program makes: anything
// using raw sockets — a game, an updater, a client with its own network stack —
// never touches the proxy and so never appears. Which makes the panel useless
// for precisely the question people open it with: this program behaves oddly
// with the VPN on, what is it actually talking to?
//
// So this reads the machine's own TCP table instead, the same call the process
// lookup already uses, and reports the connections that are NOT ours. It is an
// observation of the operating system, not of our traffic: nothing is
// intercepted, nothing is redirected, and turning the whole thing off (the
// panel's recording switch) turns this off with it.
//
// Two honest limits. A process belonging to another user or to the system will
// show its address but not its name unless Vair is running elevated — Windows
// does not hand out the image path of a process you cannot open. And this is
// TCP, over IPv4 and IPv6: UDP has no connection to enumerate, so a game's UDP
// traffic — and QUIC, which is UDP — is still invisible here, though the TCP it
// opens alongside is not.

// BypassRow is one connection that did not go through the proxy.
type BypassRow struct {
	Process string `json:"process"`
	Dest    string `json:"dest"`
	Name    string `json:"name"` // reverse-resolved host, when there is one
	State   string `json:"state"`
	Count   int    `json:"count"` // connections folded into this row
}

// bypassSkip is what never counts as "going around us": the loopback traffic
// that IS us, and the connections our own engines make.
func bypassSkip(remote net.IP, process string) bool {
	if remote == nil || remote.IsLoopback() || remote.IsUnspecified() {
		return true
	}
	switch strings.ToLower(process) {
	case "vair.exe", "xray.exe", "sing-box.exe":
		// The engine's connection to the VPN server is the tunnel itself, not a
		// leak around it.
		return true
	}
	return false
}

// tcpStateName turns the MIB state into the two words worth showing. Anything
// not established is on its way in or out and says so.
func tcpStateName(s uint32) string {
	switch s {
	case 2:
		return "listening"
	case 3:
		return "connecting"
	case 5:
		return "established"
	case 4, 6, 7, 8, 9, 10, 11:
		return "closing"
	}
	return ""
}

// BypassConnections lists the machine's TCP connections that are not going
// through Vair, newest state first, folded by program and destination.
func BypassConnections() []BypassRow {
	if !connectionsWatched() {
		// Frozen, like the other two: the panel keeps the last picture instead of
		// emptying itself the moment the switch is flipped.
		bypassLast.Lock()
		defer bypassLast.Unlock()
		return bypassLast.rows
	}
	byKey := map[string]*BypassRow{}
	var order []string
	for _, r := range machineTCP() {
		if r.state == 2 { // a listening socket is not a conversation
			continue
		}
		name := processNameCached(r.pid)
		if bypassSkip(r.remote, name) {
			continue
		}
		dest := net.JoinHostPort(r.remote.String(), strconv.Itoa(r.port))
		key := name + "\x00" + dest
		if e, ok := byKey[key]; ok {
			e.Count++
			continue
		}
		e := &BypassRow{Process: name, Dest: dest, Name: reverseName(r.remote.String()), State: tcpStateName(r.state), Count: 1}
		byKey[key] = e
		order = append(order, key)
	}
	out := make([]BypassRow, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Process != out[j].Process {
			return out[i].Process < out[j].Process
		}
		return out[i].Dest < out[j].Dest
	})
	bypassLast.Lock()
	bypassLast.rows = out
	bypassLast.Unlock()
	return out
}

// bypassLast keeps the most recent answer, so switching recording off leaves the
// table showing what it showed rather than going blank.
var bypassLast = struct {
	sync.Mutex
	rows []BypassRow
}{}

// tcpConn is one row of the machine's TCP table, IPv4 or IPv6.
type tcpConn struct {
	remote net.IP
	port   int
	state  uint32
	pid    uint32
}

// MIB_TCP6ROW_OWNER_PID, in the order the API returns it.
type tcp6RowOwnerPID struct {
	localAddr     [16]byte
	localScopeID  uint32
	localPort     uint32
	remoteAddr    [16]byte
	remoteScopeID uint32
	remotePort    uint32
	state         uint32
	owningPID     uint32
}

// machineTCP reads both tables once. Same call as tcpPortOwners, kept separate
// because that one answers "who owns this local port" and throws the rest away,
// and this one needs the rest — and the IPv6 half, which a program that goes
// around the proxy uses as readily as IPv4.
func machineTCP() []tcpConn {
	const afINet, afINet6 = 2, 23
	// The port arrives with its bytes the other way round.
	port := func(p uint32) int { return int(uint16(p<<8) | uint16(p>>8&0xff)) }
	var out []tcpConn
	if buf := tcpTable(afINet); buf != nil {
		for _, r := range tableRows[tcpRowOwnerPID](buf) {
			a := r.remoteAddr
			out = append(out, tcpConn{
				remote: net.IPv4(byte(a), byte(a>>8), byte(a>>16), byte(a>>24)),
				port:   port(r.remotePort),
				state:  r.state,
				pid:    r.owningPID,
			})
		}
	}
	if buf := tcpTable(afINet6); buf != nil {
		for _, r := range tableRows[tcp6RowOwnerPID](buf) {
			ip := make(net.IP, 16)
			copy(ip, r.remoteAddr[:])
			out = append(out, tcpConn{remote: ip, port: port(r.remotePort), state: r.state, pid: r.owningPID})
		}
	}
	return out
}

// tcpTable returns GetExtendedTcpTable's answer for one address family, or nil.
// The table can grow between asking its size and reading it, so a buffer that
// turns out too small is tried again at the new size.
func tcpTable(af uintptr) []byte {
	const (
		tcpTableOwnerPIDAll   = 5
		errInsufficientBuffer = 122
	)
	proc := windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")
	var size uint32
	r, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, af, tcpTableOwnerPIDAll, 0)
	for try := 0; try < 3 && r == errInsufficientBuffer && size > 0; try++ {
		size += 4 << 10
		buf := make([]byte, size)
		r, _, _ = proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, af, tcpTableOwnerPIDAll, 0)
		if r == 0 {
			return buf
		}
	}
	return nil
}

// tableRows reads the rows of a MIB_*TABLE_OWNER_PID: a count, then the rows.
func tableRows[T any](buf []byte) []T {
	if len(buf) < 4 {
		return nil
	}
	n := *(*uint32)(unsafe.Pointer(&buf[0]))
	var zero T
	rowSize := unsafe.Sizeof(zero)
	out := make([]T, 0, n)
	for i := uint32(0); i < n; i++ {
		off := unsafe.Sizeof(uint32(0)) + uintptr(i)*rowSize
		if off+rowSize > uintptr(len(buf)) {
			break
		}
		out = append(out, *(*T)(unsafe.Pointer(&buf[off])))
	}
	return out
}

// processNameCached keeps pid → name for a few seconds. The table is read once
// a second and most of the pids in it are the same ones as last time; opening
// every process again each round is the expensive half of this.
var procNameCache = struct {
	sync.Mutex
	m    map[uint32]string
	when time.Time
}{m: map[uint32]string{}}

func processNameCached(pid uint32) string {
	procNameCache.Lock()
	defer procNameCache.Unlock()
	if time.Since(procNameCache.when) > 5*time.Second {
		procNameCache.m = map[uint32]string{}
		procNameCache.when = time.Now()
	}
	if n, ok := procNameCache.m[pid]; ok {
		return n
	}
	n := processName(pid)
	procNameCache.m[pid] = n
	return n
}

// clearBypassSnapshot drops the frozen picture, so the panel's clear button
// empties this table too rather than leaving one of three behind.
func clearBypassSnapshot() {
	bypassLast.Lock()
	bypassLast.rows = nil
	bypassLast.Unlock()
}
