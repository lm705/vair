package core

import (
	"strings"

	"golang.org/x/sys/windows"
)

// Which program opened a connection.
//
// The relay sees a connection from 127.0.0.1:54321 and nothing else. Windows
// knows which process owns that port — GetExtendedTcpTable is the same table
// netstat -b reads — so the live list can say "chrome.exe" instead of a number
// nobody can act on.
//
// The table is a syscall over every TCP connection on the machine; procport.go
// keeps it cached.

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
// Names come from a cache of their own (processNameCached): when a burst of new
// connections makes the table be read several times in a second, the processes
// in it are mostly the same ones, and opening each again is the expensive part.
func tcpPortOwners() map[uint16]string {
	out := map[uint16]string{}
	for _, r := range tableRows[tcpRowOwnerPID](tcpTable(2)) {
		// The port arrives with its bytes the other way round.
		port := uint16(r.localPort<<8) | uint16(r.localPort>>8&0xff)
		if name := processNameCached(r.owningPID); name != "" {
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
