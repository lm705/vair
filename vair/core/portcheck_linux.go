package core

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// listeningOwner names the program listening on port, and its PID.
//
// Only sockets in LISTEN state count. The table it used to read held every TCP
// socket, so a program's outgoing connection that happened to use the port as
// its local end was named as the owner; and it never said which process, so a
// port Vair itself listened on was reported as held by "vair", a stranger —
// the check compares the PID with its own.
//
// A process of another user keeps its fd directory closed to us: its port is
// then busy with no name, which is what the caller falls back to.
func listeningOwner(port int) (string, int) {
	inodes := listeningInodes(uint16(port))
	if len(inodes) == 0 {
		return "", 0
	}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return "", 0
	}
	for _, d := range procs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || !d.IsDir() {
			continue
		}
		fds, err := os.ReadDir("/proc/" + d.Name() + "/fd")
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink("/proc/" + d.Name() + "/fd/" + fd.Name())
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			if inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] {
				return readComm(d.Name()), pid
			}
		}
	}
	return "", 0
}

// listeningInodes are the socket inodes listening on port, over IPv4 and IPv6.
func listeningInodes(port uint16) map[string]bool {
	out := map[string]bool{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			fields := strings.Fields(line)
			// local_address "0100007F:D431", state "0A" is LISTEN, inode field 9
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			addr := fields[1]
			i := strings.LastIndex(addr, ":")
			if i < 0 {
				continue
			}
			if p, err := strconv.ParseUint(addr[i+1:], 16, 16); err == nil && uint16(p) == port {
				out[fields[9]] = true
			}
		}
	}
	return out
}

// bindRefusedBySystem is EACCES: a port below 1024, which needs root.
func bindRefusedBySystem(err error) bool {
	return errors.Is(err, syscall.EACCES)
}
