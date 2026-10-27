package core

import "sync"

// The inbound rules for the web interface and the LAN-shared proxy, through
// the helper service: the service takes the whole set at once (the remote
// port and the two proxy ports), so the app keeps what it last asked for and
// sends all of it with every change.
var svcFirewall struct {
	sync.Mutex
	remote, lanHTTP, lanSocks int
}

// ServiceFirewallRemote opens the web interface's port, or closes it (0).
func ServiceFirewallRemote(port int) {
	svcFirewall.Lock()
	svcFirewall.remote = port
	svcFirewall.Unlock()
	sendServiceFirewall()
}

// ServiceFirewallLAN opens the LAN proxy's ports, or closes them (0, 0).
func ServiceFirewallLAN(httpPort, socksPort int) {
	svcFirewall.Lock()
	svcFirewall.lanHTTP, svcFirewall.lanSocks = httpPort, socksPort
	svcFirewall.Unlock()
	sendServiceFirewall()
}

func sendServiceFirewall() {
	svcFirewall.Lock()
	r, h, s := svcFirewall.remote, svcFirewall.lanHTTP, svcFirewall.lanSocks
	svcFirewall.Unlock()
	c, err := controlHelper()
	if err == nil {
		err = c.Firewall(r, h, s)
	}
	if err != nil {
		vlog("warning", "firewall: the Vair helper service could not set the inbound rules: %v", err)
	}
}
