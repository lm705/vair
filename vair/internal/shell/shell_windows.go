//go:build windows

package shell

import (
	"os"
	"os/exec"

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
	// rundll32 rather than "start": no shell, so a URL containing & or ^ can't
	// be reinterpreted as command syntax.
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
