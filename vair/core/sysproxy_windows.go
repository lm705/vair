//go:build windows

package core

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// wininetProxy drives the WinINET setting under
// HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings, which is
// what Windows itself, Edge, Chrome and most applications read.
type wininetProxy struct{}

func sysProxy() sysProxyBackend { return wininetProxy{} }

func (wininetProxy) name() string { return "wininet" }

// available — the registry is always there.
func (wininetProxy) available() bool { return true }

const wininetKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func (wininetProxy) set(host string, port int) (proxySnapshot, error) {
	snap := readWininet()
	addr := fmt.Sprintf("%s:%d", host, port)
	// WinINET wants one semicolon-separated string, and <local> — the token for
	// "any name without a dot" — has no equivalent in the neutral list.
	over := strings.Join(bypassWin(), ";") + ";<local>"

	// Through the per-connection options, not only the three registry values:
	// automatic configuration comes before a manual proxy in WinINET. A PAC
	// script (AutoConfigURL) or "Automatically detect settings" (WPAD — on by
	// default in Windows) that is in force overrides the address written here,
	// and programs that follow the system setting went around Vair while it
	// said "connected": a PAC answering DIRECT sent a request straight out in a
	// test, and a network that serves WPAD — a company's, a public Wi-Fi, or
	// someone on it — does the same with a proxy of its choosing. For the
	// session both are switched off; what they were is in the snapshot, and
	// revert puts them back.
	if cur, err := queryPerConn(); err == nil {
		snap["flags"] = strconv.FormatUint(uint64(cur.flags), 10)
		snap["pac"] = cur.pac
		snap["pcserver"] = cur.server
		snap["pcbypass"] = cur.bypass
		if err := setPerConn(perConnSettings{flags: proxyTypeDirect | proxyTypeProxy, server: addr, bypass: over}); err == nil {
			notifyWininet()
			return snap, nil
		}
		// The API refused: the registry values below are what is left.
		delete(snap, "flags")
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, wininetKey, registry.SET_VALUE)
	if err != nil {
		return snap, fmt.Errorf("open Internet Settings: %w", err)
	}
	defer k.Close()
	if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
		return snap, fmt.Errorf("ProxyEnable: %w", err)
	}
	if err := k.SetStringValue("ProxyServer", addr); err != nil {
		return snap, fmt.Errorf("ProxyServer: %w", err)
	}
	if err := k.SetStringValue("ProxyOverride", over); err != nil {
		return snap, fmt.Errorf("ProxyOverride: %w", err)
	}
	notifyWininet()
	return snap, nil
}

// revert restores what set found, rather than merely switching the proxy off.
//
// Switching it off was the old behaviour and it quietly cost users their own
// configuration: someone on a corporate proxy who ran Vair once came back to a
// machine with the proxy disabled and no reason to connect that to a VPN client
// they had disconnected. The snapshot comes from the lock file, so this holds
// after a crash too, when nothing is left in memory.
func (wininetProxy) revert(snap proxySnapshot) error {
	// A snapshot taken through the per-connection options is put back the same
	// way: automatic detection and the PAC script included.
	if f, ok := snap["flags"]; ok {
		if flags, err := strconv.ParseUint(f, 10, 32); err == nil {
			err := setPerConn(perConnSettings{flags: uint32(flags), server: snap["pcserver"], bypass: snap["pcbypass"], pac: snap["pac"]})
			if err == nil {
				notifyWininet()
				return nil
			}
		}
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, wininetKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Internet Settings: %w", err)
	}
	defer k.Close()
	defer notifyWininet()

	// No snapshot — an old lock file, from before this field existed. Off is the
	// only safe reading: we know we enabled it, and nothing else.
	if len(snap) == 0 {
		return k.SetDWordValue("ProxyEnable", 0)
	}
	enable := uint32(0)
	if snap["enable"] == "1" {
		enable = 1
	}
	k.SetDWordValue("ProxyEnable", enable) //nolint:errcheck
	// An empty entry means the value did not exist before, so delete it instead
	// of writing "": a ProxyServer of "" is not the same state as no ProxyServer.
	for name, v := range map[string]string{"ProxyServer": snap["server"], "ProxyOverride": snap["override"]} {
		if v == "" {
			k.DeleteValue(name) //nolint:errcheck
			continue
		}
		k.SetStringValue(name, v) //nolint:errcheck
	}
	return nil
}

// readWininet captures the three values set is about to overwrite. Read through
// the registry API rather than by parsing `reg query` output, which is localised
// and would have to be scraped.
// pointsAt reads the two values that decide it: the switch and the address.
func (wininetProxy) pointsAt(host string, port int) (bool, error) {
	// The per-connection view first: it is what WinINET acts on, and it says
	// whether a PAC script or automatic detection has been switched back on —
	// either takes the traffic away from the address while the registry values
	// alone still read as ours.
	if cur, err := queryPerConn(); err == nil {
		auto := cur.flags&(proxyTypeAutoProxyURL|proxyTypeAutoDetect) != 0
		return cur.flags&proxyTypeProxy != 0 && !auto && strings.Contains(cur.server, fmt.Sprintf("%s:%d", host, port)), nil
	}
	snap := readWininet()
	if snap["enable"] != "1" {
		return false, nil
	}
	// ProxyServer is either "host:port" or a per-protocol list
	// ("http=h:p;https=h:p"); ours is always the plain form, so a substring
	// match is both sufficient and tolerant of a list somebody else wrote.
	return strings.Contains(snap["server"], fmt.Sprintf("%s:%d", host, port)), nil
}

func readWininet() proxySnapshot {
	snap := proxySnapshot{"enable": "0"}
	k, err := registry.OpenKey(registry.CURRENT_USER, wininetKey, registry.QUERY_VALUE)
	if err != nil {
		return snap
	}
	defer k.Close()
	if v, _, err := k.GetIntegerValue("ProxyEnable"); err == nil && v != 0 {
		snap["enable"] = "1"
	}
	if v, _, err := k.GetStringValue("ProxyServer"); err == nil {
		snap["server"] = v
	}
	if v, _, err := k.GetStringValue("ProxyOverride"); err == nil {
		snap["override"] = v
	}
	return snap
}

// notifyWininet tells the machine that the proxy settings changed.
//
// This used to run "rundll32 inetcpl.cpl,ClearMyTracksByProcess 8" under a
// comment claiming it broadcast WM_SETTINGCHANGE. It does not: that command
// deletes the user's temporary internet files — neither a notification nor
// ours to do. Programs picked the change up whenever they next happened to
// re-read the registry.
//
// The documented mechanism is two option calls with a null handle: the first
// says the settings changed, the second makes WinINET re-read them. Chromium
// watches the registry keys itself and was already covered by the write; this
// is for everything that goes through WinINET, our own WebView2 window
// included.
func notifyWininet() {
	const (
		optionSettingsChanged = 39
		optionRefresh         = 37
	)
	for _, opt := range []uintptr{optionSettingsChanged, optionRefresh} {
		procInternetSetOptionW.Call(0, opt, 0, 0) //nolint:errcheck
	}
}

var procInternetSetOptionW = windows.NewLazySystemDLL("wininet.dll").NewProc("InternetSetOptionW")
