package shell

// Scanning a QR from an image FILE is the same work on every platform: a native
// open dialog, a file read, and core's decoder. It used to live in
// qrscan_windows.go, which meant Linux answered "not supported" for a feature
// with nothing platform-specific in it — and Capabilities said QR-from-file was
// available, because that is what the code should have been doing.
//
// Only the SCREEN capture is per-platform (GDI on Windows; on Wayland it needs
// an xdg-desktop-portal handshake this build does not do).

import (
	"os"

	"vair/core"
)

func ScanQRFromFile() (string, error) {
	paths, err := theApp.Dialog.OpenFile().
		SetTitle("Select a QR image").
		CanChooseFiles(true).
		AddFilter("Images (*.png;*.jpg;*.jpeg;*.gif;*.bmp)", "*.png;*.jpg;*.jpeg;*.gif;*.bmp").
		AddFilter("All files (*.*)", "*.*").
		PromptForMultipleSelection()
	if err != nil || len(paths) == 0 {
		return "", nil // cancelled — not an error the UI should report
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		return "", err
	}
	return core.DecodeQRBytes(data)
}
