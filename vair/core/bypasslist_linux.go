//go:build linux

package core

import (
	"encoding/binary"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// What is talking to the internet WITHOUT going through us — the Linux half.
//
// The reasoning is the Windows file's; only the source differs. There the
// machine's TCP table comes from GetExtendedTcpTable, here from /proc/net/tcp
// and /proc/net/tcp6, and the owning program is found the way every tool on
// this platform finds it: each socket line carries an inode, and the process
// that holds it has that inode as one of its open file descriptors.
//
// Three honest limits, one more than Windows has:
//
//   - TCP only. UDP has no connection to enumerate; a game's UDP traffic stays
//     invisible here, though the TCP it opens alongside does not.
//   - A process owned by another user shows its address but not its name:
//     /proc/<pid>/fd is readable by its owner and root, and the program is
//     deliberately not root.
//   - In TUN mode this list is usually empty, and correctly so — everything is
//     inside the tunnel by construction. It answers a proxy-mode question.
type BypassRow struct {
	Process string `json:"process"`
	Dest    string `json:"dest"`
	Name    string `json:"name"` // reverse-resolved host, when there is one
	State   string `json:"state"`
	Count   int    `json:"count"` // connections folded into this row
}

// bypassSkip is what never counts as going around us: loopback, which is us,
// and our own engines, whose connection to the VPN server IS the tunnel.
func bypassSkip(remote net.IP, process string) bool {
	if remote == nil || remote.IsLoopback() || remote.IsUnspecified() {
		return true
	}
	switch strings.ToLower(process) {
	case "vair", "xray", "sing-box", "vair-helper":
		return true
	}
	return false
}

// procTCPState maps the hex state in /proc/net/tcp to the two words worth
// showing. The numbers are the kernel's TCP_* enum, not the MIB one Windows
// uses, so the mapping is written out rather than shared.
func procTCPState(hexState string) (name string, listening bool) {
	switch strings.ToUpper(hexState) {
	case "01":
		return "established", false
	case "0A":
		return "listening", true
	case "02", "03":
		return "connecting", false
	case "04", "05", "06", "07", "08", "09", "0B":
		return "closing", false
	}
	return "", false
}

// procAddr parses the "0100007F:1F90" half of a /proc/net/tcp line.
//
// The address is the raw bytes of the socket structure printed as hex, so it is
// little-endian per 32-bit word: 0100007F is 127.0.0.1, and an IPv6 address is
// four such words. The port is big-endian, because that is how it sits in the
// structure. Getting either backwards produces plausible-looking nonsense
// rather than an error, which is why this is one function with one test.
func procAddr(field string) (net.IP, int) {
	parts := strings.Split(field, ":")
	if len(parts) != 2 {
		return nil, 0
	}
	raw, err := hex.DecodeString(parts[0])
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return nil, 0
	}
	for i := 0; i+4 <= len(raw); i += 4 {
		w := binary.BigEndian.Uint32(raw[i : i+4])
		binary.LittleEndian.PutUint32(raw[i:i+4], w)
	}
	port, err := strconv.ParseInt(parts[1], 16, 32)
	if err != nil {
		return nil, 0
	}
	return net.IP(raw), int(port)
}

// procConn is one line of /proc/net/tcp{,6} reduced to what the panel wants.
type procConn struct {
	remote net.IP
	port   int
	state  string
	inode  string
}

// readProcNetTCP parses one of the kernel's socket tables.
func readProcNetTCP(path string) []procConn {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []procConn
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 { // header
			continue
		}
		f := strings.Fields(line)
		// sl local rem st tx:rx retr uid timeout inode
		if len(f) < 10 {
			continue
		}
		state, listening := procTCPState(f[3])
		if listening || state == "" {
			continue
		}
		ip, port := procAddr(f[2])
		if ip == nil {
			continue
		}
		out = append(out, procConn{remote: ip, port: port, state: state, inode: f[9]})
	}
	return out
}

// socketOwners maps socket inode → program name by walking /proc/<pid>/fd.
//
// One pass over every process we are allowed to look at, building the whole map
// at once: the alternative is a walk per connection, and a busy machine has
// hundreds. Unreadable processes are skipped in silence — that is the "another
// user's program shows no name" limit, and it is not an error to report.
func socketOwners() map[string]string {
	owners := map[string]string{}
	pids, err := os.ReadDir("/proc")
	if err != nil {
		return owners
	}
	for _, p := range pids {
		if !p.IsDir() {
			continue
		}
		pid := p.Name()
		if pid[0] < '0' || pid[0] > '9' {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", pid, "fd"))
		if err != nil {
			continue // not ours to read
		}
		name := ""
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join("/proc", pid, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			if name == "" {
				name = processComm(pid)
			}
			owners[inode] = name
		}
	}
	return owners
}

// processComm reads the program's own name. /proc/<pid>/comm is the short name
// the kernel keeps; cmdline would give the full path, and for a list of who is
// talking, "firefox" reads better than "/usr/lib/firefox/firefox".
func processComm(pid string) string {
	b, err := os.ReadFile(filepath.Join("/proc", pid, "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// BypassConnections lists the machine's TCP connections that are not going
// through Vair, folded by program and destination.
func BypassConnections() []BypassRow {
	if !connectionsWatched() {
		// Frozen, like the other two tables: the panel keeps the last picture
		// rather than emptying itself the moment the switch is flipped.
		bypassLast.Lock()
		defer bypassLast.Unlock()
		return bypassLast.rows
	}
	conns := append(readProcNetTCP("/proc/net/tcp"), readProcNetTCP("/proc/net/tcp6")...)
	if len(conns) == 0 {
		return nil
	}
	owners := socketOwners()

	byKey := map[string]*BypassRow{}
	var order []string
	for _, c := range conns {
		name := owners[c.inode]
		if bypassSkip(c.remote, name) {
			continue
		}
		host := c.remote.String()
		if c.remote.To4() == nil {
			host = "[" + host + "]"
		}
		dest := host + ":" + strconv.Itoa(c.port)
		key := name + "\x00" + dest
		if e, ok := byKey[key]; ok {
			e.Count++
			continue
		}
		e := &BypassRow{Process: name, Dest: dest, Name: reverseName(c.remote.String()), State: c.state, Count: 1}
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

// bypassLast keeps the most recent answer, so switching recording off leaves
// the table showing what it showed rather than going blank.
var bypassLast = struct {
	sync.Mutex
	rows []BypassRow
}{}

// clearBypassSnapshot drops it, for the panel's clear button.
func clearBypassSnapshot() {
	bypassLast.Lock()
	bypassLast.rows = nil
	bypassLast.Unlock()
}
