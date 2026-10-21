package core

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Which program opened a connection.
//
// The relay sees a connection from 127.0.0.1:54321 and nothing else. Windows
// knows which process owns that port — GetExtendedTcpTable is the same table
// netstat -b reads — so the live list can say "chrome.exe" instead of a number
// nobody can act on.
//
// The table is a syscall over every TCP connection on the machine, so it is
// read at most a few times a second and cached; a browser opening forty
// connections at once must not mean forty scans.

var procPortCache = struct {
	sync.Mutex
	m    map[uint16]string
	when time.Time
}{m: map[uint16]string{}}

const procPortTTL = 2 * time.Second

// processForLocalPort maps "127.0.0.1:54321" to the name of the program that
// owns that port, or "" when it cannot be told.
func processForLocalPort(localAddr string) string {
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
	if time.Since(procPortCache.when) > procPortTTL {
		procPortCache.m = tcpPortOwners()
		procPortCache.when = time.Now()
	}
	name := procPortCache.m[port]
	procPortCache.Unlock()
	return name
}

// MIB_TCPROW_OWNER_PID, in the order the API returns it.
type tcpRowOwnerPID struct {
	state      uint32
	localAddr  uint32
	localPort  uint32
	remoteAddr uint32
	remotePort uint32
	owningPID  uint32
}

// tcpPortOwners reads the whole table once and returns local port → program.
func tcpPortOwners() map[uint16]string {
	const (
		afINet                   = 2
		tcpTableOwnerPIDAll      = 5
		errInsufficientBuffer    = 122
		processQueryLimitedInfo  = 0x1000
		maxProcessNameBufferSize = 260
	)
	out := map[uint16]string{}

	mod := windows.NewLazySystemDLL("iphlpapi.dll")
	proc := mod.NewProc("GetExtendedTcpTable")

	var size uint32
	r, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, afINet, tcpTableOwnerPIDAll, 0)
	if r != errInsufficientBuffer || size == 0 {
		return out
	}
	buf := make([]byte, size)
	r, _, _ = proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0,
		afINet, tcpTableOwnerPIDAll, 0)
	if r != 0 {
		return out
	}

	n := *(*uint32)(unsafe.Pointer(&buf[0]))
	rowSize := unsafe.Sizeof(tcpRowOwnerPID{})
	names := map[uint32]string{}
	for i := uint32(0); i < n; i++ {
		off := unsafe.Sizeof(uint32(0)) + uintptr(i)*rowSize
		if off+rowSize > uintptr(len(buf)) {
			break
		}
		row := (*tcpRowOwnerPID)(unsafe.Pointer(&buf[off]))
		// The port arrives with its bytes the other way round.
		port := uint16(row.localPort<<8) | uint16(row.localPort>>8&0xff)
		pid := row.owningPID
		name, seen := names[pid]
		if !seen {
			name = processName(pid)
			names[pid] = name
		}
		if name != "" {
			out[port] = name
		}
	}
	return out
}

// processName is the executable's file name for a pid, without its path.
func processName(pid uint32) string {
	if pid == 0 {
		return ""
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h) //nolint:errcheck
	buf := make([]uint16, 260)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	full := windows.UTF16ToString(buf[:size])
	if i := strings.LastIndexAny(full, `\/`); i >= 0 {
		return full[i+1:]
	}
	return full
}
