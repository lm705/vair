package main

import "vair/core"

// QRService renders configs / arbitrary text as QR-code data URLs.
type QRService struct{}

// ForConfig returns a QR data URL for row idx of the table tab.
func (q *QRService) ForConfig(idx int) string { return core.QRForConfig(idx) }

// ForText returns a QR data URL for arbitrary text (e.g. a subscription URL).
func (q *QRService) ForText(text string) string { return core.QRDataURL(text) }

// ScanResult carries a decoded QR payload or a human-readable error.
// Text=="" with Error=="" means the user cancelled the picker.
type ScanResult struct {
	Text  string `json:"text"`
	Error string `json:"error,omitempty"`
}

// ScanFile opens the image picker and decodes a QR from the chosen file.
func (q *QRService) ScanFile() ScanResult {
	text, err := scanQRFromFile()
	if err != nil {
		return ScanResult{Error: err.Error()}
	}
	return ScanResult{Text: text}
}

// ScanScreen captures the virtual desktop and decodes a QR found on it.
func (q *QRService) ScanScreen() ScanResult {
	text, err := scanQRFromScreen()
	if err != nil {
		return ScanResult{Error: err.Error()}
	}
	return ScanResult{Text: text}
}
