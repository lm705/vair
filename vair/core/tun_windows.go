//go:build windows

package core

// removeTUNAdapter reaps leftover TUN adapters by name so they don't linger in
// the system and cause "file already exists" on the next connect.
//
// Windows keeps the adapter registered after the engine that created it dies,
// so a crash leaves one behind and the reconnect fails on a name clash. The
// interface name carries a timestamp, hence the wildcards; xc-tun-* is from the
// 1.10 releases and is swept too, for users upgrading.
func removeTUNAdapter() {
	runHidden("powershell", "-NonInteractive", "-Command",
		`Get-NetAdapter -Name 'vair-*','xc-tun-*' -ErrorAction SilentlyContinue | Remove-NetAdapter -Confirm:$false`,
	).Run() //nolint:errcheck
}
