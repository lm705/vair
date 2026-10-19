package services

import "vair/internal/shell"

// ClipboardService reads the OS clipboard natively (Win32 GetClipboardText via
// Wails), which always returns the current contents — unlike the WebView2
// navigator.clipboard.readText(), whose cache can hand back a previously copied
// value when the text was copied in another application. Used by the "Paste
// configs" action so it never imports a stale clipboard.
type ClipboardService struct{}

// Text returns the current OS clipboard text ("" if empty/unavailable).
func (c *ClipboardService) Text() string {
	if shell.App() == nil {
		return ""
	}
	t, _ := shell.App().Clipboard.Text()
	return t
}
