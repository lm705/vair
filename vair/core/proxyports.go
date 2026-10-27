package core

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// The two ports programs reach the local proxy on, and the addresses they
// listen at.

// proxyListenHosts are the addresses the HTTP and SOCKS ports listen on.
//
// A specific address of this machine (Settings → Proxy access → Custom) is for
// the other devices; this machine itself still reaches the proxy on loopback —
// the system proxy, Vair's own checks and requests, and every program set to
// 127.0.0.1. The proxy used to listen on the custom address alone, and a proxy
// session could not come up at all: the check after connecting found nothing
// on 127.0.0.1 and blamed the server ("accepts connections and answers
// nothing").
func proxyListenHosts() []string {
	b := proxyBindHost()
	ip := net.ParseIP(b)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return []string{b}
	}
	return []string{b, "127.0.0.1"}
}

// proxyPortFree reports whether a proxy port can be taken on every address it
// will listen at — loopback always among them, by name: Windows lets a
// listener on 0.0.0.0 share a port with another program's on 127.0.0.1, and
// connections to 127.0.0.1 then reach the other program.
func proxyPortFree(port int) bool {
	hosts := append([]string{"127.0.0.1"}, proxyListenHosts()...)
	for _, h := range hosts {
		ln, err := net.Listen("tcp", net.JoinHostPort(h, strconv.Itoa(port)))
		if err != nil {
			return false
		}
		ln.Close()
	}
	return true
}

// claimProxyPorts settles the ports a proxy session listens on for programs.
//
// A configured port that another program already held used to be swapped for a
// random free one without a word. Where the system proxy is the only thing
// pointed at the port that is harmless — it follows the change — but a program
// set to the port by hand went on talking to whatever held it, outside the
// tunnel, while Vair said "connected": a phone on the LAN, a client given the
// SOCKS port, everything with "Set system proxy" off.
//
// So the port moves only while the system proxy is set and the proxy is not
// open to other devices, and the log and a notification say so — including
// that a program set to the old port by hand is not going through the VPN.
// Otherwise the connection does not come up, and the error names the program
// holding the port.
func claimProxyPorts() (httpPort, socksPort int, err error) {
	httpPort, socksPort = currentProxyPorts()
	byHand := noSystemProxyEnabled() || ProxyOpenToLAN()
	claim := func(port int, kind string) (int, error) {
		if proxyPortFree(port) {
			return port, nil
		}
		// Our own listener from the session being replaced (a switch to another
		// server) lets go of the port a moment after it is told to: wait for it
		// rather than call it taken.
		name, pid := listeningOwner(port)
		for deadline := time.Now().Add(2 * time.Second); pid == os.Getpid() && time.Now().Before(deadline); {
			time.Sleep(50 * time.Millisecond)
			if proxyPortFree(port) {
				return port, nil
			}
			name, pid = listeningOwner(port)
		}
		owner := "another program"
		if name != "" {
			owner = name
		}
		if byHand {
			return 0, fmt.Errorf("proxy port %d (%s) is in use by %s — choose another port in Settings → Local proxy", port, kind, owner)
		}
		alt, err := findFreePort()
		if err != nil {
			return 0, fmt.Errorf("proxy port %d (%s) is in use by %s, and no other port is free", port, kind, owner)
		}
		vlog("warn", "proxy port %d (%s) is in use by %s — this connection uses port %d; programs set to %d by hand are not going through the VPN", port, kind, owner, alt, port)
		desktopNotify("Vair", notifyPortMoved(kind, port, alt, owner))
		return alt, nil
	}
	if httpPort, err = claim(httpPort, "HTTP"); err != nil {
		return 0, 0, err
	}
	if socksPort, err = claim(socksPort, "SOCKS"); err != nil {
		return 0, 0, err
	}
	return httpPort, socksPort, nil
}

// notifyPortMoved is the notification for a port that was taken and replaced.
func notifyPortMoved(kind string, from, to int, owner string) string {
	settingsMu.RLock()
	lang := appSettings.Language
	settingsMu.RUnlock()
	if lang == "ru" {
		if owner == "another program" {
			owner = "другой программой"
		}
		s := fmt.Sprintf("Порт %s-прокси %d занят (%s). На это подключение прокси работает на порту %d", kind, from, owner, to)
		if kind == "HTTP" {
			s += ", системный прокси указывает на него"
		}
		return s + fmt.Sprintf(". Программы, которым порт %d задан вручную, идут не через VPN.", from)
	}
	s := fmt.Sprintf("The %s proxy port %d is in use by %s. For this connection the proxy is on port %d", kind, from, owner, to)
	if kind == "HTTP" {
		s += ", and the system proxy points there"
	}
	return s + fmt.Sprintf(". Programs set to port %d by hand are not going through the VPN.", from)
}

// ProxyBindCheck is the settings screen's line under a custom address: "ok",
// "invalid" (not an IP address) or "absent" (not an address this machine has
// now — the proxy will not start on it until it does).
func CheckProxyBind(addr string) string {
	addr = strings.TrimSpace(addr)
	ip := net.ParseIP(addr)
	if ip == nil {
		return "invalid"
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return "ok"
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "ok" // cannot tell: the connection itself will say
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return "ok"
		}
	}
	return "absent"
}
