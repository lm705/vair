//go:build windows

package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	autostartRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`
	// Same Run-key value name as the 1.10 release ("Vair") now that 2.0 replaces
	// it: enabling 2.0 autostart updates the existing entry to the new exe path
	// instead of leaving a stale 1.10 one behind.
	autostartValueName = "Vair"
	deepLinkClassBase  = `Software\Classes\vair`
)

// ApplyAutostart writes/removes the HKCU Run-key entry that launches Vair at
// logon (with --autostart so it comes up minimised to the tray). Ported from 1.10.
func ApplyAutostart(enabled bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if enabled {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return k.SetStringValue(autostartValueName, `"`+exe+`" --autostart`)
	}
	// Disabled → remove the value; a missing value is not an error.
	if err := k.DeleteValue(autostartValueName); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

// ReconcileAutostart makes the Run key agree with the setting, at every start.
//
// The key used to be touched only when the switch changed: turned on, the entry
// was written; turned off, it was removed. Anything that changed one without
// the other left them disagreeing for good — a settings file reset or restored,
// an older build, a copy of the program elsewhere — and the switch said "off"
// while Windows went on starting Vair at every logon.
//
// Off removes the entry only when it starts this program: this exe, or another
// copy of Vair 2 or later, which keeps its settings in the same place and so
// answers to this same switch. An entry under the name that starts Vair 1.x is
// that program's, with a switch of its own.
func ReconcileAutostart(enabled bool) error {
	if enabled {
		return ApplyAutostart(true)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(autostartValueName)
	if err != nil {
		return nil // no entry, or not one we could have written
	}
	if !startsThisProgram(runEntryExe(v)) {
		return nil
	}
	return k.DeleteValue(autostartValueName)
}

// runEntryExe is the program a Run entry starts: the quoted path, or the
// command line up to its first space.
func runEntryExe(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, `"`) {
		if i := strings.IndexByte(cmd[1:], '"'); i >= 0 {
			return cmd[1 : 1+i]
		}
		return cmd[1:]
	}
	if i := strings.IndexByte(cmd, ' '); i >= 0 {
		return cmd[:i]
	}
	return cmd
}

// startsThisProgram: this exe, or an exe whose version resource names it Vair,
// version 2 or later. A file with no version resource — or no file — is not
// claimed.
func startsThisProgram(exe string) bool {
	if exe == "" {
		return false
	}
	if self, err := os.Executable(); err == nil && strings.EqualFold(filepath.Clean(self), filepath.Clean(exe)) {
		return true
	}
	major, product, ok := fileVersion(exe)
	return ok && major >= 2 && strings.EqualFold(strings.TrimSpace(product), "Vair")
}

// fileVersion reads an exe's major file version and product name from its
// version resource.
func fileVersion(path string) (major uint32, product string, ok bool) {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return 0, "", false
	}
	buf := make([]byte, size)
	block := unsafe.Pointer(&buf[0])
	if err := windows.GetFileVersionInfo(path, 0, size, block); err != nil {
		return 0, "", false
	}
	var fixed *windows.VS_FIXEDFILEINFO
	var n uint32
	if err := windows.VerQueryValue(block, `\`, unsafe.Pointer(&fixed), &n); err != nil || fixed == nil || n == 0 {
		return 0, "", false
	}
	// The strings sit under a language-and-codepage key the translation table
	// names; the usual ones are tried after it.
	var keys []string
	var tr unsafe.Pointer
	if windows.VerQueryValue(block, `\VarFileInfo\Translation`, unsafe.Pointer(&tr), &n) == nil && tr != nil {
		for i := uint32(0); i+4 <= n; i += 4 {
			lang := *(*uint16)(unsafe.Add(tr, i))
			cp := *(*uint16)(unsafe.Add(tr, i+2))
			keys = append(keys, fmt.Sprintf("%04x%04x", lang, cp))
		}
	}
	keys = append(keys, "040904b0", "000004b0", "00000000")
	for _, k := range keys {
		var sp unsafe.Pointer
		var sn uint32
		if windows.VerQueryValue(block, `\StringFileInfo\`+k+`\ProductName`, unsafe.Pointer(&sp), &sn) == nil && sp != nil && sn > 0 {
			return fixed.FileVersionMS >> 16, windows.UTF16PtrToString((*uint16)(sp)), true
		}
	}
	return fixed.FileVersionMS >> 16, "", true
}

// RegisterDeepLink registers (or removes) the per-user vair:// URL scheme so a
// vair://import/<…> link opens this exe with the URL as argv[1]. Ported from 1.10.
func RegisterDeepLink(enabled bool) error {
	if !enabled {
		// Delete deepest keys first (DeleteKey refuses keys with subkeys).
		registry.DeleteKey(registry.CURRENT_USER, deepLinkClassBase+`\shell\open\command`)
		registry.DeleteKey(registry.CURRENT_USER, deepLinkClassBase+`\shell\open`)
		registry.DeleteKey(registry.CURRENT_USER, deepLinkClassBase+`\shell`)
		registry.DeleteKey(registry.CURRENT_USER, deepLinkClassBase)
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, deepLinkClassBase, registry.SET_VALUE)
	if err != nil {
		return err
	}
	_ = k.SetStringValue("", "URL:Vair Protocol")
	_ = k.SetStringValue("URL Protocol", "")
	_ = k.Close()
	ck, _, err := registry.CreateKey(registry.CURRENT_USER, deepLinkClassBase+`\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer ck.Close()
	return ck.SetStringValue("", `"`+exe+`" "%1"`)
}

// OpenPath shows a folder in the file manager, OpenURL hands a link to the
// default browser. The WebView must never navigate away from the app, so every
// outbound link goes through here.
func OpenPath(path string) {
	_ = exec.Command("explorer.exe", path).Start()
}

func OpenURL(url string) {
	// Web addresses only (see webLink): the handler below opens files and
	// programs just as readily as pages.
	link, ok := webLink(url)
	if !ok {
		return
	}
	// rundll32 rather than "start": no shell, so a URL containing & or ^ can't
	// be reinterpreted as command syntax.
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", link).Start()
}
