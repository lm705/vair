package core

import (
	"encoding/base64"

	qrcode "github.com/skip2/go-qrcode"
)

// QRMaxBytes is the most a QR code holds: the largest version, the lowest
// error correction, byte mode.
const QRMaxBytes = 2953

// qrPixelsPerModule sizes the image by the code rather than the code by a fixed
// image: a dense code in 512 px had under three pixels a module, and the window
// shows the image at a size worked out from its width (App.tsx, .qr-img).
const qrPixelsPerModule = 8

// QRDataURL renders text as a QR PNG and returns it as a data: URL ready for an
// <img src>. Empty for empty input and for text no QR code can hold.
func QRDataURL(text string) string {
	if text == "" || len(text) > QRMaxBytes {
		return ""
	}
	// Medium error correction where the text fits it, Low beyond: a JSON
	// configuration reaches 2.5 KB, did not fit Medium, and "Show QR" did
	// nothing at all.
	q, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		if q, err = qrcode.New(text, qrcode.Low); err != nil {
			return ""
		}
	}
	png, err := q.PNG(-qrPixelsPerModule)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

// QRForConfig returns the QR data URL for row idx of the table tab.
func QRForConfig(idx int) string {
	e, ok := memEntry(TableTab(), idx)
	if !ok {
		return ""
	}
	return QRDataURL(e.Raw)
}
