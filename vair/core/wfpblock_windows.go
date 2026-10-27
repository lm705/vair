package core

import (
	"os"

	"vair/helper/wfp"
)

// The kill-switch's filters are put up and taken down by helper/wfp, which the
// Windows helper service runs as well. What stays here is the part that knows
// about this program: which settings and which binaries decide the policy.

// blockOptions is everything the policy depends on (see wfp.BlockOptions).
type blockOptions = wfp.BlockOptions

// The operations, behind variables so the kill-switch's own logic can be
// tested without a filtering engine. With the helper service installed they
// are the service's: the app has no rights to the filtering engine, and asks.
var (
	wfpInstallBlock = installPersistentBlock
	wfpRemoveBlock  = removePersistentBlock
	wfpBlockPresent = persistentBlockPresent
)

func installPersistentBlock(o blockOptions) error {
	if !serviceMode() {
		return wfp.InstallPersistent(o)
	}
	h, err := controlHelper()
	if err != nil {
		return err
	}
	// The service builds the policy itself — the programs it lets out are the
	// ones in its own install directory, the tunnel the one it runs. What it is
	// told is whether the local network may pass.
	_, err = h.SetPersistentBlock(true, o.LAN)
	return err
}

// removePersistentBlock takes our filters down — and, through the service,
// a Windows Firewall block of a build before 2.7.0 with them.
func removePersistentBlock() error {
	if !serviceMode() {
		return wfp.RemovePersistent()
	}
	h, err := controlHelper()
	if err != nil {
		return err
	}
	_, err = h.SetPersistentBlock(false, false)
	return err
}

func persistentBlockPresent() (present, known bool) {
	if !serviceMode() {
		return wfp.PersistentPresent()
	}
	h, err := controlHelper()
	if err != nil {
		return false, false
	}
	st, err := h.QuickBlockState()
	if err != nil {
		return false, false
	}
	return st.Persistent, st.Known
}

// currentBlockOptions gathers what the policy needs from this machine and the
// settings.
func currentBlockOptions(iface string) (blockOptions, error) {
	var o blockOptions
	if iface != "" {
		luid, err := wfp.InterfaceLUID(iface)
		if err != nil {
			return o, err
		}
		o.Tunnel = luid
	}
	exe, _ := os.Executable()
	state.mu.RLock()
	xrayBin, singboxBin := state.xrayBin, state.singboxBin
	state.mu.RUnlock()
	for _, p := range []string{xrayBin, singboxBin, exe} {
		if p != "" {
			o.Apps = append(o.Apps, p)
		}
	}
	settingsMu.RLock()
	o.LAN = !appSettings.BlockLAN
	settingsMu.RUnlock()
	return o, nil
}
