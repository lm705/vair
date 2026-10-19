package core

// updateApplyBlocked returns why an update cannot be installed here.
//
// No Linux install kind can be updated from inside the app yet, and each is
// blocked for its own reason, so the message names the actual next step rather
// than a generic failure:
//
//   - package: /usr/bin/vair is root-owned and belongs to dpkg or rpm. Handing
//     a downloaded package to the package manager needs a separate privileged
//     step (pkexec), which is stage-2 work.
//   - appimage: replacing $APPIMAGE is straightforward, but the manifest
//     publishes no AppImage asset to replace it with.
//   - none: a tarball or a build tree, with no bare-binary asset published.
func updateApplyBlocked() string {
	switch installKind() {
	case "package":
		return "installed from a package — update it with your package manager (apt/dnf/pacman)"
	case "appimage":
		return "running as an AppImage — download the new AppImage and replace this one"
	default:
		return "in-app updates are not available on this build — download the new release manually"
	}
}
