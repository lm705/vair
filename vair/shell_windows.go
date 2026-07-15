//go:build windows

package main

import (
	"os"

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

// applyAutostart writes/removes the HKCU Run-key entry that launches Vair at
// logon (with --autostart so it comes up minimised to the tray). Ported from 1.10.
func applyAutostart(enabled bool) error {
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

// registerDeepLink registers (or removes) the per-user vair:// URL scheme so a
// vair://import/<…> link opens this exe with the URL as argv[1]. Ported from 1.10.
func registerDeepLink(enabled bool) error {
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
