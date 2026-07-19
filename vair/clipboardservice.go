package main

// ClipboardService reads the OS clipboard natively (Win32 GetClipboardText via
// Wails), which always returns the current contents — unlike the WebView2
// navigator.clipboard.readText(), whose cache can hand back a previously copied
// value when the text was copied in another application. Used by the "Paste
// configs" action so it never imports a stale clipboard.
type ClipboardService struct{}

// Text returns the current OS clipboard text ("" if empty/unavailable).
func (c *ClipboardService) Text() string {
	if theApp == nil {
		return ""
	}
	t, _ := theApp.Clipboard.Text()
	return t
}
