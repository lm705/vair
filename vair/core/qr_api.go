package core

import (
	"encoding/base64"

	qrcode "github.com/skip2/go-qrcode"
)

// QRDataURL renders text as a 512px QR PNG and returns it as a data: URL ready
// for an <img src>. Empty for empty / over-long input.
func QRDataURL(text string) string {
	if text == "" || len(text) > 3000 {
		return ""
	}
	png, err := qrcode.Encode(text, qrcode.Medium, 512)
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
