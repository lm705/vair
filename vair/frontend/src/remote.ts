// Remote (phone / browser) mode. When the app is opened over the LAN from a
// browser, it's served by the in-process HTTP server instead of running inside
// the native WebView2 — so the WebView2 bridge is absent, native calls (window
// controls, tray, file dialogs, screen capture, second windows) don't work, and
// Wails events aren't pushed in the usual way. We detect that and stream events
// over Server-Sent Events instead.

// IS_REMOTE is set by the remote server, which stamps every page IT serves with
// window.__VAIR_REMOTE__. Anything without the stamp is the native window.
//
// This used to test for window.chrome.webview — the WebView2 bridge. That object
// only exists on Windows, so the NATIVE Linux app (WebKitGTK) matched the
// "remote browser" branch: it hid its own window controls, opened a redundant
// event stream, and took the browser path everywhere IS_REMOTE is checked.
// Asking the server is platform-neutral and can't drift with the webview.
import { SettingsService } from '../bindings/vair/internal/services'

export const IS_REMOTE = (window as any).__VAIR_REMOTE__ === true

// startRemoteEvents pipes Server-Sent Events from /wails/events into the Wails
// runtime, so every existing Events.On(...) listener fires just as it does in the
// desktop app. The page was loaded with ?key=<token>, which set an auth cookie;
// the EventSource is same-origin so it carries that cookie automatically.
export function startRemoteEvents() {
  if (!IS_REMOTE) return
  const es = new EventSource('/wails/events')
  es.onmessage = (e) => {
    try {
      const ev = JSON.parse(e.data)
      ;(window as any)._wails?.dispatchWailsEvent?.(ev)
    } catch {
      /* ignore malformed frame */
    }
  }
  // EventSource reconnects automatically on transient network errors.
}

// openLink opens a web address where the person is: in the browser they are
// holding when the interface is opened over the network, in the computer's
// default browser from the desktop window. From a phone it used to open on the
// computer, out of sight.
export function openLink(url: string) {
  if (IS_REMOTE) {
    window.open(url, '_blank', 'noopener,noreferrer')
    return
  }
  SettingsService.OpenURL(url)
}
