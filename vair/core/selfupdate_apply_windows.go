//go:build windows

package core

// updateApplyBlocked returns why an update cannot be installed, or "" when it
// can. Windows replaces the running exe in place, which is what the manifest's
// asset is — except for a copy the setup program installed, which the setup
// program updates (see installKind). The window offers no button there, but the
// call is open to the web interface too, and from an elevated Vair it would
// have put the portable build in the installer's place.
func updateApplyBlocked() string {
	if installKind() == "installer" {
		return "installed by the setup program — download the new setup program and run it"
	}
	return ""
}
