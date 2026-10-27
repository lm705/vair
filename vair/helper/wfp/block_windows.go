package wfp

import (
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The kill-switch as WFP filters: what is allowed out, in what order, and the
// operations on it — put the whole set up, take it all down, ask whether it is
// standing, and hold a set for the length of one session.
//
// Everything persistent lives under one provider and one sublayer of our own,
// and every filter has a key fixed in advance. Taking the block down is
// therefore not a matter of remembering what was installed: it is "delete every
// key this program could ever have used", which works the same with a marker on
// disk, without one, from another run of the program, from the helper service,
// or from the uninstaller.

var (
	// ProviderKey and SublayerKey are ours, fixed for good: a block put up by
	// any build is taken down by any other.
	ProviderKey = windows.GUID{Data1: 0x9e4e6c5b, Data2: 0x7a3f, Data3: 0x4d0e, Data4: [8]byte{0x9b, 0x51, 0x2a, 0x6c, 0x87, 0x1d, 0x04, 0xe2}}
	SublayerKey = windows.GUID{Data1: 0x9e4e6c5b, Data2: 0x7a3f, Data3: 0x4d0e, Data4: [8]byte{0x9b, 0x51, 0x2a, 0x6c, 0x87, 0x1d, 0x04, 0xe3}}
)

// MaxFilters bounds the key space. The policy uses well under half of it.
const MaxFilters = 48

// FilterKey is the n-th key: the block-all filters take 0 and 1, so the
// question "is the block standing" never depends on how many exceptions there
// happen to be.
func FilterKey(n int) windows.GUID {
	return windows.GUID{Data1: 0x9e4e6c5c, Data2: 0x7a3f, Data3: 0x4d0e, Data4: [8]byte{0x9b, 0x51, 0x2a, 0x6c, 0x87, 0x1d, 0x05, byte(n)}}
}

// BlockOptions is everything the policy depends on.
type BlockOptions struct {
	// Tunnel is the LUID of the TUN interface, or 0 for the proxy kill-switch,
	// which has no tunnel to let through.
	Tunnel uint64
	// Apps are the programs allowed out on their own: the engines, which have
	// to reach the servers (and test configurations) while the block stands,
	// and the program itself.
	Apps []string
	// LAN lets the local network through.
	LAN bool
	// V6Around leaves IPv6 outside the block, for "IPv6 around the tunnel":
	// the tunnel then carries no v6 at all, and blocking it would take away
	// what the user chose to keep. DNS on port 53 is still held in. Only the
	// tunnel's own block honours it — the block that stays after the tunnel
	// has no tunnel to say "around" of.
	V6Around bool
}

// Weights inside our sublayer: the highest matching filter decides.
const (
	wfpWeightLoopback = 15
	wfpWeightApps     = 14
	wfpWeightTunnel   = 13
	wfpWeightNoDNS    = 12
	wfpWeightDHCP     = 11
	wfpWeightLAN      = 10
	wfpWeightBlockAll = 0
)

// lanPrefixes is the local network: private and link-local ranges, multicast
// and broadcast.
var lanPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("ff00::/8"),
}

// blockPolicy decides the filters. It touches nothing, so a test can read it.
//
// In order of weight:
//   - loopback, always: the local proxy ports and the web interface live there;
//   - the engines and the program, anywhere: they build the tunnel, rebuild it,
//     and test configurations while the block stands;
//   - the tunnel interface, when there is one: everything the user's programs
//     send goes out here;
//   - no DNS on port 53 to anything else. The local network is let through
//     below, and the resolver most homes use is the router — so without this a
//     lookup still went out in the clear, only to 192.168.1.1 instead of 8.8.8.8;
//   - DHCP, so the machine keeps its address;
//   - the local network, when the user has not asked to block it;
//   - and then nothing.
func blockPolicy(o BlockOptions) []wfpFilterSpec {
	type layer struct {
		key  windows.GUID
		v6   bool
		name string
	}
	layers := []layer{{fwpmLayerALEAuthConnectV4, false, "IPv4"}, {fwpmLayerALEAuthConnectV6, true, "IPv6"}}

	var out []wfpFilterSpec
	// The block-all pair first, at the fixed keys 0 and 1.
	for i, l := range layers {
		if l.v6 && o.V6Around {
			continue
		}
		out = append(out, wfpFilterSpec{key: FilterKey(i), name: "Vair: block everything else (" + l.name + ")",
			layer: l.key, action: fwpActionBlock, weight: wfpWeightBlockAll})
	}
	n := len(layers)
	add := func(l layer, name string, action uint32, weight uint8, conds ...wfpCond) {
		out = append(out, wfpFilterSpec{key: FilterKey(n), name: "Vair: " + name + " (" + l.name + ")",
			layer: l.key, action: action, weight: weight, conds: conds})
		n++
	}
	for _, l := range layers {
		add(l, "loopback", fwpActionPermit, wfpWeightLoopback, condLoopback())
		for _, app := range o.Apps {
			add(l, "program "+appBaseName(app), fwpActionPermit, wfpWeightApps, condApp(app))
		}
		if o.Tunnel != 0 {
			add(l, "the tunnel", fwpActionPermit, wfpWeightTunnel, condInterface(o.Tunnel))
		}
		add(l, "no DNS outside the tunnel", fwpActionBlock, wfpWeightNoDNS, condRemotePort(53))
		if l.v6 {
			add(l, "DHCPv6", fwpActionPermit, wfpWeightDHCP, condProtocol(17), condLocalPort(546), condRemotePort(547))
		} else {
			add(l, "DHCP", fwpActionPermit, wfpWeightDHCP, condProtocol(17), condLocalPort(68), condRemotePort(67))
		}
		if o.LAN {
			var conds []wfpCond
			for _, p := range lanPrefixes {
				if p.Addr().Is6() == l.v6 {
					conds = append(conds, condRemotePrefix(p))
				}
			}
			// Conditions on the same field are alternatives in WFP: one filter
			// with every range is "any of these".
			add(l, "local network", fwpActionPermit, wfpWeightLAN, conds...)
		}
	}
	// And the same doors the other way. The block was one-way: a connection
	// opened from outside to the physical adapter — a peer, a scanner, anyone
	// who has the address — was accepted, and what answered it went out with
	// the machine's real address. From outside, only what may go out may be
	// reached: loopback, the engines and the program, the tunnel, DHCP and,
	// unless blocked, the local network. No DNS rule: nothing is asked here.
	inbound := []layer{{fwpmLayerALEAuthRecvAcceptV4, false, "IPv4"}, {fwpmLayerALEAuthRecvAcceptV6, true, "IPv6"}}
	for _, l := range inbound {
		if l.v6 && o.V6Around {
			continue
		}
		add(l, "inbound: block everything else", fwpActionBlock, wfpWeightBlockAll)
		add(l, "inbound: loopback", fwpActionPermit, wfpWeightLoopback, condLoopback())
		for _, app := range o.Apps {
			add(l, "inbound: program "+appBaseName(app), fwpActionPermit, wfpWeightApps, condApp(app))
		}
		if o.Tunnel != 0 {
			add(l, "inbound: the tunnel", fwpActionPermit, wfpWeightTunnel, condInterface(o.Tunnel))
		}
		if l.v6 {
			add(l, "inbound: DHCPv6", fwpActionPermit, wfpWeightDHCP, condProtocol(17), condLocalPort(546), condRemotePort(547))
		} else {
			add(l, "inbound: DHCP", fwpActionPermit, wfpWeightDHCP, condProtocol(17), condLocalPort(68), condRemotePort(67))
		}
		if o.LAN {
			var conds []wfpCond
			for _, p := range lanPrefixes {
				if p.Addr().Is6() == l.v6 {
					conds = append(conds, condRemotePrefix(p))
				}
			}
			add(l, "inbound: local network", fwpActionPermit, wfpWeightLAN, conds...)
		}
	}
	return out
}

func appBaseName(path string) string {
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// InstallPersistent puts the whole persistent set up in one transaction,
// replacing whatever set of ours was there. Either the new set is standing
// when this returns nil, or nothing changed at all — including the previous
// set, which stays exactly as it was. So re-pointing the block at a new tunnel
// has no moment with the door shut and nothing allowed, and no moment with it
// open.
func InstallPersistent(o BlockOptions) error {
	specs := blockPolicy(o)
	if len(specs) > MaxFilters {
		return fmt.Errorf("kill-switch policy has %d filters, more than the %d keys reserved", len(specs), MaxFilters)
	}
	e, err := wfpOpen()
	if err != nil {
		return err
	}
	defer e.close()
	return e.inTransaction(func() error {
		if err := e.addProvider(ProviderKey, "Vair"); err != nil {
			return err
		}
		provider := ProviderKey
		if err := e.addSublayer(SublayerKey, &provider, "Vair kill-switch", fwpmSublayerFlagPersistent); err != nil {
			return err
		}
		for i := 0; i < MaxFilters; i++ {
			if err := e.deleteFilter(FilterKey(i)); err != nil {
				return err
			}
		}
		for _, s := range specs {
			if err := e.addFilter(s, &provider, SublayerKey, fwpmFilterFlagPersistent); err != nil {
				return err
			}
		}
		return nil
	})
}

// RemovePersistent takes every filter of ours down, and the sublayer and the
// provider with them, in one transaction.
func RemovePersistent() error {
	e, err := wfpOpen()
	if err != nil {
		return err
	}
	defer e.close()
	return e.inTransaction(func() error {
		for i := 0; i < MaxFilters; i++ {
			if err := e.deleteFilter(FilterKey(i)); err != nil {
				return err
			}
		}
		if err := e.deleteSublayer(SublayerKey); err != nil {
			return err
		}
		return e.deleteProvider(ProviderKey)
	})
}

// PersistentPresent reports whether our block-all filters are in place. known
// is false when the question could not be asked — no rights, or the filtering
// engine not answering — which is not the same as "no".
func PersistentPresent() (present, known bool) {
	e, err := wfpOpen()
	if err != nil {
		return false, false
	}
	defer e.close()
	for i := 0; i < 2; i++ {
		ok, err := e.filterExists(FilterKey(i))
		if err != nil {
			return false, false
		}
		if ok {
			return true, true
		}
	}
	return false, true
}

// OpenSession puts the same policy up in a dynamic session of its own and
// returns what takes it down.
//
// Dynamic hands the clean-up to the filtering engine: the filters go when the
// session is closed, or, if the process holding it dies, when the process
// does. These filters take whatever keys the engine gives them, so they never
// meet the persistent set at its fixed keys — the two can stand at once.
func OpenSession(o BlockOptions) (func(), error) {
	sublayer, err := windows.GenerateGUID()
	if err != nil {
		return nil, err
	}
	e, err := wfpOpenSession(true)
	if err != nil {
		return nil, err
	}
	err = e.inTransaction(func() error {
		if err := e.addSublayer(sublayer, nil, "Vair kill-switch (tunnel)", 0); err != nil {
			return err
		}
		for _, s := range blockPolicy(o) {
			s.key = windows.GUID{}
			s.name = strings.Replace(s.name, "Vair: ", "Vair tunnel: ", 1)
			if err := e.addFilter(s, nil, sublayer, 0); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		e.close()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(e.close) }, nil
}

// InterfaceLUID finds the LUID of the adapter Windows calls alias — the TUN
// interface's name is all the engine reports.
func InterfaceLUID(alias string) (uint64, error) {
	size := uint32(15 * 1024)
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, size)
		aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_INCLUDE_ALL_INTERFACES, 0, aa, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return 0, err
		}
		for a := aa; a != nil; a = a.Next {
			if strings.EqualFold(windows.UTF16PtrToString(a.FriendlyName), alias) {
				return a.Luid, nil
			}
		}
		return 0, fmt.Errorf("no network interface named %q", alias)
	}
	return 0, fmt.Errorf("the adapter list kept growing while it was read")
}

// ReleaseScript is a PowerShell script that takes the persistent set down
// without Vair.
//
// WFP has no command of its own to remove a filter — no netsh verb, no
// PowerShell cmdlet — so a block whose program has been deleted would have no
// undo, on a machine that cannot download it again because the block is
// standing. The script calls the filtering engine directly with the same keys
// this package uses, generated from the same constants, and takes everything
// of ours down in one transaction. scriptPath is where it will be saved (for
// the line that says how to run it); markerPath, when not empty, is the file it
// removes once the block is down.
func ReleaseScript(scriptPath, markerPath string) string {
	var keys strings.Builder
	for i := 0; i < MaxFilters; i++ {
		if i > 0 {
			keys.WriteString(",")
		}
		fmt.Fprintf(&keys, "'%s'", FilterKey(i).String())
	}
	removeMarker := ""
	if markerPath != "" {
		removeMarker = "Remove-Item -LiteralPath '" + strings.ReplaceAll(markerPath, "'", "''") + "' -ErrorAction SilentlyContinue\n"
	}
	return `# Takes Vair's kill-switch block down without Vair. Run as administrator:
#   powershell -ExecutionPolicy Bypass -File "` + scriptPath + `"
$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class VairWfp {
  [DllImport("fwpuclnt.dll")] public static extern uint FwpmEngineOpen0(IntPtr server, uint authn, IntPtr auth, IntPtr session, out IntPtr engine);
  [DllImport("fwpuclnt.dll")] public static extern uint FwpmEngineClose0(IntPtr engine);
  [DllImport("fwpuclnt.dll")] public static extern uint FwpmTransactionBegin0(IntPtr engine, uint flags);
  [DllImport("fwpuclnt.dll")] public static extern uint FwpmTransactionCommit0(IntPtr engine);
  [DllImport("fwpuclnt.dll")] public static extern uint FwpmTransactionAbort0(IntPtr engine);
  [DllImport("fwpuclnt.dll")] public static extern uint FwpmFilterDeleteByKey0(IntPtr engine, ref Guid key);
  [DllImport("fwpuclnt.dll")] public static extern uint FwpmSubLayerDeleteByKey0(IntPtr engine, ref Guid key);
  [DllImport("fwpuclnt.dll")] public static extern uint FwpmProviderDeleteByKey0(IntPtr engine, ref Guid key);
}
"@
$notFound = @(` + fmt.Sprint(fwpEFilterNotFound) + `, ` + fmt.Sprint(fwpENotFound) + `, ` + fmt.Sprint(fwpESublayerNotFound) + `, ` + fmt.Sprint(fwpEProviderNotFound) + `)
$e = [IntPtr]::Zero
$r = [VairWfp]::FwpmEngineOpen0([IntPtr]::Zero, 10, [IntPtr]::Zero, [IntPtr]::Zero, [ref]$e)
if ($r -ne 0) { throw ("cannot open the filtering engine (0x{0:X8}) - run this as administrator" -f $r) }
[void][VairWfp]::FwpmTransactionBegin0($e, 0)
try {
  foreach ($k in @(` + keys.String() + `)) {
    $g = [Guid]$k
    $r = [VairWfp]::FwpmFilterDeleteByKey0($e, [ref]$g)
    if ($r -ne 0 -and $notFound -notcontains $r) { throw ("filter {0}: 0x{1:X8}" -f $k, $r) }
  }
  $g = [Guid]'` + SublayerKey.String() + `'
  $r = [VairWfp]::FwpmSubLayerDeleteByKey0($e, [ref]$g)
  if ($r -ne 0 -and $notFound -notcontains $r) { throw ("sublayer: 0x{0:X8}" -f $r) }
  $g = [Guid]'` + ProviderKey.String() + `'
  $r = [VairWfp]::FwpmProviderDeleteByKey0($e, [ref]$g)
  if ($r -ne 0 -and $notFound -notcontains $r) { throw ("provider: 0x{0:X8}" -f $r) }
  $r = [VairWfp]::FwpmTransactionCommit0($e)
  if ($r -ne 0) { throw ("commit: 0x{0:X8}" -f $r) }
} catch {
  [void][VairWfp]::FwpmTransactionAbort0($e)
  throw
} finally {
  [void][VairWfp]::FwpmEngineClose0($e)
}
` + removeMarker + `'Vair kill-switch block removed.'
`
}
