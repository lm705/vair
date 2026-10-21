//go:build !windows

package core

// UninstallCleanup has nothing to do outside Windows. The Linux packages remove
// the helper and its unit in their own postrm, and the helper itself takes the
// interface, the routes and the nftables table down the moment its owner
// disappears — that is the same guarantee, made by a service that cannot be
// uninstalled without stopping first. See helper/cmd/vair-helper.
func UninstallCleanup() []string {
	return []string{"nothing to undo: the package scripts and the helper handle it"}
}
