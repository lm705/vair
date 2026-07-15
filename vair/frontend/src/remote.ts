// Remote (phone / browser) mode. When the app is opened over the LAN from a
// browser, it's served by the in-process HTTP server instead of running inside
// the native WebView2 — so the WebView2 bridge is absent, native calls (window
// controls, tray, file dialogs, screen capture, second windows) don't work, and
// Wails events aren't pushed in the usual way. We detect that and stream events
// over Server-Sent Events instead.

// IS_REMOTE is true in a plain browser (no WebView2 host bridge), false in the
// desktop app's WebView2.
export const IS_REMOTE = !((window as any).chrome && (window as any).chrome.webview)

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
