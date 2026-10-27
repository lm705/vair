package core

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// The leak check on Linux: the questions the Windows check asks, asked the way a
// leak would ask them here.
//
// From a socket bound to the physical interface (SO_BINDTODEVICE — an ordinary
// program may set it since Linux 5.7, and WebRTC and "use this interface"
// downloaders do), sent by this program. On Windows the probes come from
// ping.exe and powershell.exe because the block lets Vair out on purpose; on
// Linux the block lets out the helper's cgroup, where the engine runs, and the
// app is an ordinary program like any other — so its own socket is the honest
// probe, and needs no privilege.
//
//   - every packet (a kill-switch): a UDP packet with a time-to-live of one to
//     three. On a real network the first routers answer "time exceeded"; with
//     IP_RECVERR that answer reaches the socket as an error, and an answer of
//     any kind means the packet left the machine.
//   - DNS (leak protection): a query to the resolvers the interface was given
//     and to a public one. An answer means a name could be looked up outside
//     the tunnel.

func leakProbe(c leakClaim) leakCheckResult {
	srcs, err := physicalSources()
	for try := 0; err != nil && try < 2; try++ {
		time.Sleep(time.Second)
		srcs, err = physicalSources()
	}
	if err != nil {
		return leakCheckResult{verdict: leakCannot, detail: err.Error()}
	}
	if c.traffic {
		for _, s := range srcs {
			for ttl := 1; ttl <= 3; ttl++ {
				left, err := udpProbe(s, netip.MustParseAddr(leakProbeTargets[s.addr.Is6()]), 33434, ttl, leakProbePayload, true)
				if err != nil {
					return leakCheckResult{verdict: leakCannot, detail: err.Error()}
				}
				if left {
					return leakCheckResult{verdict: leakFound,
						detail: fmt.Sprintf("a probe sent from %s (%s) around the tunnel was answered", s.adapter, s.addr)}
				}
			}
		}
	}
	var to, from []string
	for _, s := range srcs {
		from = append(from, fmt.Sprintf("%s (%s)", s.adapter, s.addr))
		targets := []netip.Addr{netip.MustParseAddr(leakProbeTargets[s.addr.Is6()])}
		for _, d := range s.dns {
			if d.Is6() == s.addr.Is6() && !slices.Contains(targets, d) {
				targets = append(targets, d)
			}
		}
		for _, t := range targets {
			answered, err := udpProbe(s, t, 53, 64, dnsProbeQuery(), false)
			if err != nil {
				return leakCheckResult{verdict: leakCannot, detail: "the DNS probe: " + err.Error()}
			}
			if answered {
				return leakCheckResult{verdict: leakFound, note: leakFoundDNS,
					detail: fmt.Sprintf("a DNS query sent from %s (%s) to %s around the tunnel was answered", s.adapter, s.addr, t)}
			}
			to = append(to, t.String())
		}
	}
	r := leakCheckResult{verdict: leakPassed,
		detail: "DNS queries to " + strings.Join(to, ", ") + " sent around the tunnel from " + strings.Join(from, ", ") + " got no answer"}
	if c.traffic {
		r.detail = "probes and " + r.detail
	} else {
		r.note = leakPassedDNS
	}
	return r
}

// leakProbePayload is what a UDP probe carries: sixteen zero bytes, like any
// traceroute. It said "vair leak probe" — which, on a machine where the probe
// does get out, told everyone on the path which program was running here.
var leakProbePayload = make([]byte, 16)

// udpProbe sends payload from a socket bound to s's interface and reports
// whether anything came back within 1.5 s: a reply, or — when icmpCounts — an
// ICMP error, which is a router or the target telling us the packet reached it.
// A send the local filter refuses is no answer.
func udpProbe(s leakSource, dst netip.Addr, port, ttl int, payload []byte, icmpCounts bool) (bool, error) {
	fam := unix.AF_INET
	if dst.Is6() {
		fam = unix.AF_INET6
	}
	fd, err := unix.Socket(fam, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return false, fmt.Errorf("no socket: %v", err)
	}
	defer unix.Close(fd)
	if err := unix.SetsockoptString(fd, unix.SOL_SOCKET, unix.SO_BINDTODEVICE, s.adapter); err != nil {
		return false, fmt.Errorf("a socket could not be bound to %s (Linux 5.7 or newer allows it without privileges): %v", s.adapter, err)
	}
	if fam == unix.AF_INET {
		unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_TTL, ttl)   //nolint:errcheck
		unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_RECVERR, 1) //nolint:errcheck
	} else {
		unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_UNICAST_HOPS, ttl) //nolint:errcheck
		unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_RECVERR, 1)        //nolint:errcheck
	}
	tv := unix.NsecToTimeval((1500 * time.Millisecond).Nanoseconds())
	unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv) //nolint:errcheck
	var sa unix.Sockaddr
	if dst.Is4() {
		sa = &unix.SockaddrInet4{Port: port, Addr: dst.As4()}
	} else {
		sa = &unix.SockaddrInet6{Port: port, Addr: dst.As16()}
	}
	if err := unix.Connect(fd, sa); err != nil {
		return false, nil // no route out of this interface: nothing left
	}
	if _, err := unix.Write(fd, payload); err != nil {
		return false, nil // refused on the way out
	}
	buf := make([]byte, 1500)
	_, err = unix.Read(fd, buf)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.EAGAIN), errors.Is(err, unix.EWOULDBLOCK), errors.Is(err, unix.EINTR):
		return false, nil
	case icmpCounts && (errors.Is(err, unix.EHOSTUNREACH) || errors.Is(err, unix.ECONNREFUSED) || errors.Is(err, unix.ENETUNREACH)):
		return true, nil
	}
	return false, nil
}

type leakSource struct {
	adapter string
	addr    netip.Addr
	// dns is the resolvers this interface was given — where a program that
	// ignores the system's resolver, or systemd-resolved for this link's own
	// domains, would send a query.
	dns []netip.Addr
}

// physicalSources finds the interfaces a leak would leave from: up, not
// loopback, not this program's tunnel, and holding a default route — one IPv4
// and, where there is one, one global IPv6 address each.
func physicalSources() ([]leakSource, error) {
	withRoute := defaultRouteIfaces()
	dns := linkResolvers()
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("the network interfaces could not be read: %v", err)
	}
	var out []leakSource
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 ||
			strings.HasPrefix(strings.ToLower(ifc.Name), "vair") || !withRoute[ifc.Name] {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		var got4, got6 bool
		for _, a := range addrs {
			pfx, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			ip := pfx.Addr().Unmap()
			switch {
			case ip.Is4() && !got4 && !ip.IsLinkLocalUnicast():
				out = append(out, leakSource{ifc.Name, ip, dns[ifc.Name]})
				got4 = true
			case ip.Is6() && !got6 && ip.IsGlobalUnicast() && !isULA(ip):
				out = append(out, leakSource{ifc.Name, ip, dns[ifc.Name]})
				got6 = true
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no network interface with a default route besides the tunnel")
	}
	return out, nil
}

// defaultRouteIfaces names the interfaces holding a default route, from the
// kernel's own tables.
func defaultRouteIfaces() map[string]bool {
	out := map[string]bool{}
	if f, err := os.Open("/proc/net/route"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			// Iface Destination Gateway Flags … Mask …
			fs := strings.Fields(sc.Text())
			if len(fs) >= 8 && fs[1] == "00000000" && fs[7] == "00000000" {
				out[fs[0]] = true
			}
		}
		f.Close()
	}
	if f, err := os.Open("/proc/net/ipv6_route"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			// dest prefixlen src prefixlen nexthop metric refcnt use flags iface
			fs := strings.Fields(sc.Text())
			if len(fs) >= 10 && strings.Trim(fs[0], "0") == "" && fs[1] == "00" {
				out[fs[9]] = true
			}
		}
		f.Close()
	}
	return out
}

// linkResolvers is each interface's resolvers, as systemd-resolved has them;
// without resolved, the system-wide list for every interface.
func linkResolvers() map[string][]netip.Addr {
	if out, err := exec.Command("resolvectl", "dns").Output(); err == nil {
		if m := parseResolvectlDNS(string(out)); len(m) > 0 {
			return m
		}
	}
	var all []netip.Addr
	for _, f := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			fs := strings.Fields(line)
			if len(fs) >= 2 && fs[0] == "nameserver" {
				if ip, err := netip.ParseAddr(fs[1]); err == nil && !ip.IsLoopback() && !slices.Contains(all, ip) {
					all = append(all, ip)
				}
			}
		}
	}
	out := map[string][]netip.Addr{}
	for name := range defaultRouteIfaces() {
		out[name] = all
	}
	return out
}

// parseResolvectlDNS reads `resolvectl dns`: "Link 3 (enp0s8): 192.168.0.1 …".
func parseResolvectlDNS(out string) map[string][]netip.Addr {
	m := map[string][]netip.Addr{}
	for _, line := range strings.Split(out, "\n") {
		lp, rp := strings.Index(line, "("), strings.Index(line, "):")
		if !strings.HasPrefix(line, "Link ") || lp < 0 || rp < lp {
			continue
		}
		name := line[lp+1 : rp]
		for _, f := range strings.Fields(line[rp+2:]) {
			// "192.168.0.1#dns.example" names a DoT server; the address is first
			if i := strings.IndexByte(f, '#'); i >= 0 {
				f = f[:i]
			}
			if ip, err := netip.ParseAddr(f); err == nil {
				m[name] = append(m[name], ip.Unmap())
			}
		}
	}
	return m
}
