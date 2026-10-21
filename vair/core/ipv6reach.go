package core

import (
	"net"
	"strings"
	"sync"
	"time"
)

// Can this machine reach the IPv6 internet on its own?
//
// The question matters because the tunnel answers it for the applications
// whether it can or not. A TUN interface with an IPv6 address makes auto_route
// claim ::/0, and from that moment every program on the machine believes IPv6
// works: browsers ask for AAAA records, get them, and connect. Traffic the
// tunnel carries is fine — the server does the IPv6 for us. Traffic the routing
// rules send DIRECT is not: it is dialled from this machine, and on a link with
// no IPv6 every one of those dials fails instantly —
//
//	ERROR connection: open connection to [2606:4700::6810:b9f1]:443 using
//	outbound/direct[direct]: dial tcp: An invalid argument was supplied.
//
// — and the application, which has no idea, tries again. And again. The log
// fills with the same address twenty times a second, the engine burns processor
// time on connections that cannot exist, and none of it depends on what the
// user is doing: background programs alone keep it going. Refusing IPv6 for
// that traffic instead is both cheaper and faster, because a refusal sends the
// application to IPv4 immediately.

var ipv6Reach struct {
	sync.Mutex
	when time.Time
	has  bool
}

// ipv6ReachTTL is how long an answer is trusted. Long enough that building a
// configuration never walks the interface list twice in a row, short enough
// that plugging in a cable is noticed by the next connection.
const ipv6ReachTTL = 30 * time.Second

// hostHasGlobalIPv6 reports whether an interface of this machine — other than
// our own tunnel — carries a global IPv6 address.
//
// An address is not proof of a working route, but its ABSENCE is proof there is
// no route: nothing can be dialled from an address that does not exist, which
// is exactly the failure this answers.
func hostHasGlobalIPv6() bool {
	ipv6Reach.Lock()
	defer ipv6Reach.Unlock()
	if time.Since(ipv6Reach.when) < ipv6ReachTTL {
		return ipv6Reach.has
	}
	ipv6Reach.when = time.Now()
	ipv6Reach.has = scanForGlobalIPv6()
	return ipv6Reach.has
}

// ourTunV6 is the address the tunnel takes, without its prefix length — the one
// global-looking IPv6 address on this machine that proves nothing.
var ourTunV6 = strings.SplitN(ipv6TunAddress, "/", 2)[0]

func scanForGlobalIPv6() bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.To4() != nil || !ip.IsGlobalUnicast() {
				continue
			}
			// Unique-local addresses (fc00::/7) are a private network, not the
			// internet — and one of them is the tunnel's own.
			if ip.IsPrivate() || ip.String() == ourTunV6 {
				continue
			}
			return true
		}
	}
	return false
}
