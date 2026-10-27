package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// leakProbe sends the probes and reads the answer.
//
// From ping.exe, and that is the point rather than a convenience: this program
// and its engines are allowed out by the block on purpose, so a probe of ours
// would get through by design and prove nothing. ping.exe is allowed nothing,
// so it is the ordinary program the protection is there to hold. It is bound to
// the physical adapter's address, which is what a leak looks like: traffic
// that did not follow the tunnel's routes.
//
// ping.exe's exit status is read, never its text, which is in the system's
// language: 0 means something answered, and an answer — a reply, or "time
// exceeded" from a router on the way — means the packet left the machine.
func leakProbe(c leakClaim) leakCheckResult {
	srcs, err := physicalSources()
	// An adapter that is reconnecting (Wi-Fi roaming, a cable, a virtual NIC
	// being reconfigured) has no gateway for a moment; a moment later it does.
	for try := 0; err != nil && try < 2; try++ {
		time.Sleep(time.Second)
		srcs, err = physicalSources()
	}
	if err != nil {
		return leakCheckResult{verdict: leakCannot, detail: err.Error()}
	}
	if c.traffic {
		ping := filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE")
		for _, s := range srcs {
			for ttl := 1; ttl <= 3; ttl++ {
				args := []string{"-n", "1", "-w", "1500", "-i", strconv.Itoa(ttl), "-S", s.addr.String()}
				if s.addr.Is6() {
					args = append([]string{"-6"}, args...)
				}
				args = append(args, leakProbeTargets[s.addr.Is6()])
				if runHidden(ping, args...).Run() == nil {
					return leakCheckResult{verdict: leakFound,
						detail: fmt.Sprintf("a probe sent from %s (%s) around the tunnel was answered", s.adapter, s.addr)}
				}
			}
		}
	}
	asked, err := dnsLeakProbe(srcs)
	if err != nil {
		return leakCheckResult{verdict: leakCannot, detail: "the DNS probe: " + err.Error()}
	}
	for _, a := range asked {
		if a.answered {
			return leakCheckResult{verdict: leakFound, note: leakFoundDNS,
				detail: fmt.Sprintf("a DNS query sent from %s (%s) to %s around the tunnel was answered", a.src.adapter, a.src.addr, a.resolver)}
		}
	}
	var from, to []string
	for _, s := range srcs {
		from = append(from, fmt.Sprintf("%s (%s)", s.adapter, s.addr))
	}
	for _, a := range asked {
		to = append(to, a.resolver.String())
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

// dnsLeakAsk is one question the DNS probe put: from where, to whom, and
// whether anyone answered.
type dnsLeakAsk struct {
	src      leakSource
	resolver netip.Addr
	answered bool
}

// dnsLeakProbe asks the way a leak would: from each physical adapter's address,
// the resolvers that adapter was given and a public one — what Windows does by
// itself with every lookup when nothing stops it, the same question to every
// adapter's resolver at once. From powershell.exe, for the reason the other
// probe uses ping.exe: a program no filter lets out on purpose. The query is
// for a name anyone can answer, so silence means the question did not leave.
func dnsLeakProbe(srcs []leakSource) ([]dnsLeakAsk, error) {
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	query := base64.StdEncoding.EncodeToString(dnsProbeQuery())
	var out []dnsLeakAsk
	setupFailed := 0
	for _, s := range srcs {
		targets := []netip.Addr{netip.MustParseAddr(leakProbeTargets[s.addr.Is6()])}
		for _, d := range s.dns {
			if d.Is6() == s.addr.Is6() && !slices.Contains(targets, d) {
				targets = append(targets, d)
			}
		}
		for _, t := range targets {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
				"-Command", dnsProbeScript(s.addr, t, query))
			prepareProc(cmd)
			err := cmd.Run()
			cancel()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				return nil, fmt.Errorf("powershell could not run: %v", err)
			}
			if code == dnsProbeSetupFailed {
				setupFailed++
				continue
			}
			out = append(out, dnsLeakAsk{src: s, resolver: t, answered: code == 0})
		}
	}
	if len(out) == 0 && setupFailed > 0 {
		return nil, fmt.Errorf("no socket could be bound to a physical adapter's address")
	}
	return out, nil
}

// dnsProbeSetupFailed is the probe script's exit code for "could not even
// bind", which says nothing about a leak either way.
const dnsProbeSetupFailed = 3

// dnsProbeScript sends one query from src to resolver:53 and exits 0 when an
// answer comes back, 1 when none does within 1.5 s (or the send is refused),
// dnsProbeSetupFailed when the socket cannot be bound to src.
func dnsProbeScript(src, resolver netip.Addr, queryB64 string) string {
	return "$ErrorActionPreference='Stop';" +
		"try{$u=New-Object System.Net.Sockets.UdpClient((New-Object System.Net.IPEndPoint([System.Net.IPAddress]::Parse('" + src.String() + "'),0)))}catch{exit " + strconv.Itoa(dnsProbeSetupFailed) + "};" +
		"$u.Client.ReceiveTimeout=1500;" +
		"$q=[Convert]::FromBase64String('" + queryB64 + "');" +
		"try{[void]$u.Send($q,$q.Length,(New-Object System.Net.IPEndPoint([System.Net.IPAddress]::Parse('" + resolver.String() + "'),53)))}catch{exit 1};" +
		"$ep=New-Object System.Net.IPEndPoint([System.Net.IPAddress]::Any,0);" +
		"try{[void]$u.Receive([ref]$ep);exit 0}catch{exit 1}"
}

type leakSource struct {
	adapter string
	addr    netip.Addr
	// dns is the resolvers this adapter was given — where Windows sends its
	// parallel lookups.
	dns []netip.Addr
}

// physicalSources finds the addresses a leak would leave from: every adapter
// that is up, has a gateway, and is not this program's tunnel — one IPv4 and,
// where there is one, one global IPv6 address each.
func physicalSources() ([]leakSource, error) {
	size := uint32(15 * 1024)
	var buf []byte
	for attempt := 0; attempt < 3; attempt++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC,
			windows.GAA_FLAG_INCLUDE_GATEWAYS|windows.GAA_FLAG_SKIP_MULTICAST,
			0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("the network adapters could not be read: %v", err)
		}
		break
	}
	var out []leakSource
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		name := windows.UTF16PtrToString(a.FriendlyName)
		if a.OperStatus != windows.IfOperStatusUp || a.FirstGatewayAddress == nil ||
			a.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK || strings.HasPrefix(strings.ToLower(name), "vair") {
			continue
		}
		var dns []netip.Addr
		for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
			if ip, ok := netip.AddrFromSlice(d.Address.IP()); ok && !ip.Unmap().IsLinkLocalUnicast() {
				dns = append(dns, ip.Unmap())
			}
		}
		var got4, got6 bool
		for u := a.FirstUnicastAddress; u != nil; u = u.Next {
			ip, ok := netip.AddrFromSlice(u.Address.IP())
			if !ok {
				continue
			}
			ip = ip.Unmap()
			switch {
			case ip.Is4() && !got4 && !ip.IsLinkLocalUnicast():
				out = append(out, leakSource{name, ip, dns})
				got4 = true
			case ip.Is6() && !got6 && ip.IsGlobalUnicast() && !isULA(ip):
				out = append(out, leakSource{name, ip, dns})
				got6 = true
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no network adapter with a gateway besides the tunnel")
	}
	return out, nil
}
