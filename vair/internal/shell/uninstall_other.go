//go:build !windows

package shell

// The uninstall path is Windows-only: on Linux the package scripts remove the
// helper and its unit, and the helper undoes the network state itself. These two
// exist so main.go can be written once.

// RunUninstallCleanup is never reached outside Windows.
func RunUninstallCleanup() {}

// HasUninstallCleanupFlag always reports false: nothing passes the flag here.
func HasUninstallCleanupFlag([]string) bool { return false }

// HasEnableAutostartFlag always reports false: the flag is the Windows
// installer's, and there is no Windows installer here.
func HasEnableAutostartFlag([]string) bool { return false }

// RunEnableAutostart is never reached outside Windows.
func RunEnableAutostart() {}
