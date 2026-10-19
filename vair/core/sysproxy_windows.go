//go:build windows

package core

import (
	"fmt"
	"strings"

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

// wininetKeyCmd is the same key in the form reg.exe wants.
const wininetKeyCmd = `HKCU\` + wininetKey

func (wininetProxy) set(host string, port int) (proxySnapshot, error) {
	snap := readWininet()
	addr := fmt.Sprintf("%s:%d", host, port)
	// WinINET wants one semicolon-separated string, and <local> — the token for
	// "any name without a dot" — has no equivalent in the neutral list.
	over := strings.Join(bypassWin(), ";") + ";<local>"
	for _, c := range [][]string{
		{"reg", "add", wininetKeyCmd, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "1", "/f"},
		{"reg", "add", wininetKeyCmd, "/v", "ProxyServer", "/t", "REG_SZ", "/d", addr, "/f"},
		{"reg", "add", wininetKeyCmd, "/v", "ProxyOverride", "/t", "REG_SZ", "/d", over, "/f"},
	} {
		if err := runHidden(c[0], c[1:]...).Run(); err != nil {
			return snap, fmt.Errorf("reg: %w", err)
		}
	}
	flushWininet()
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
	// No snapshot — an old lock file, from before this field existed. Off is the
	// only safe reading: we know we enabled it, and nothing else.
	if len(snap) == 0 {
		runHidden("reg", "add", wininetKeyCmd, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "0", "/f").Run() //nolint:errcheck
		flushWininet()
		return nil
	}
	enable := "0"
	if snap["enable"] == "1" {
		enable = "1"
	}
	runHidden("reg", "add", wininetKeyCmd, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", enable, "/f").Run() //nolint:errcheck
	// An empty entry means the value did not exist before, so delete it instead
	// of writing "": a ProxyServer of "" is not the same state as no ProxyServer.
	for name, v := range map[string]string{"ProxyServer": snap["server"], "ProxyOverride": snap["override"]} {
		if v == "" {
			runHidden("reg", "delete", wininetKeyCmd, "/v", name, "/f").Run() //nolint:errcheck
			continue
		}
		runHidden("reg", "add", wininetKeyCmd, "/v", name, "/t", "REG_SZ", "/d", v, "/f").Run() //nolint:errcheck
	}
	flushWininet()
	return nil
}

// readWininet captures the three values set is about to overwrite. Read through
// the registry API rather than by parsing `reg query` output, which is localised
// and would have to be scraped.
// pointsAt reads the two values that decide it: the switch and the address.
func (wininetProxy) pointsAt(host string, port int) (bool, error) {
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

// flushWininet clears the WinINET cache so running processes pick the change up
// now; this broadcasts WM_SETTINGCHANGE to every top-level window.
func flushWininet() {
	runHidden("rundll32.exe", "inetcpl.cpl,ClearMyTracksByProcess", "8").Run() //nolint:errcheck
}
