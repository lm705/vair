package core

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Which program opened a connection, on Linux.
//
// /proc holds the same answer Windows keeps in a table: the socket's inode is
// in /proc/net/tcp, and the process that owns that inode has it as a symlink in
// its fd directory. Walking every process for every connection would be absurd,
// so the map is built once and cached for a couple of seconds, exactly as on
// the other platform.

var procPortCache = struct {
	sync.Mutex
	m    map[uint16]string
	when time.Time
}{m: map[uint16]string{}}

const procPortTTL = 2 * time.Second

func processForLocalPort(localAddr string) string {
	_, portStr, err := net.SplitHostPort(localAddr)
	if err != nil {
		return ""
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 || p > 65535 {
		return ""
	}
	procPortCache.Lock()
	if time.Since(procPortCache.when) > procPortTTL {
		procPortCache.m = tcpPortOwners()
		procPortCache.when = time.Now()
	}
	name := procPortCache.m[uint16(p)]
	procPortCache.Unlock()
	return name
}

// tcpPortOwners maps a local port to the program holding it.
func tcpPortOwners() map[uint16]string {
	inodes := map[string]uint16{} // socket inode → local port
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			// local_address is "0100007F:D431" — the port is hex, after the colon.
			addr := fields[1]
			i := strings.LastIndex(addr, ":")
			if i < 0 {
				continue
			}
			port, err := strconv.ParseUint(addr[i+1:], 16, 16)
			if err != nil {
				continue
			}
			inodes[fields[9]] = uint16(port)
		}
	}
	if len(inodes) == 0 {
		return map[uint16]string{}
	}

	out := map[uint16]string{}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, d := range procs {
		if !d.IsDir() {
			continue
		}
		pid := d.Name()
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		fds, err := os.ReadDir("/proc/" + pid + "/fd")
		if err != nil {
			continue // someone else's process, or it exited
		}
		var name string
		for _, fd := range fds {
			link, err := os.Readlink("/proc/" + pid + "/fd/" + fd.Name())
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			port, ok := inodes[inode]
			if !ok {
				continue
			}
			if name == "" {
				name = readComm(pid)
			}
			if name != "" {
				out[port] = name
			}
		}
	}
	return out
}

// readComm is the process's own name, which is what a person recognises.
func readComm(pid string) string {
	b, err := os.ReadFile(filepath.Join("/proc", pid, "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
