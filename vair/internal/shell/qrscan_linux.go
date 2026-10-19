package shell

import "fmt"

// Scanning from the SCREEN is Windows-only. Wayland has no equivalent of a
// blind desktop grab by design: a client must go through xdg-desktop-portal's
// ScreenCast, which shows a picker and returns a PipeWire stream — a different
// mechanism, not a port of this one. Capabilities.QRScreen is false here, so the
// menu item is not offered and this is only a backstop.
//
// Scanning from a FILE works and lives in qrscan.go.
func ScanQRFromScreen() (string, error) { return "", fmt.Errorf("not supported") }
