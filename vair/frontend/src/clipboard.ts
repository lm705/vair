// Copying to the OS clipboard, from anywhere in the app.
//
// The async navigator.clipboard API is often blocked in the embedded webview
// (the page is served from a custom scheme, which is not a secure context), so
// every copy falls back to a hidden textarea and execCommand. Kept here rather
// than in App so the dialogs can copy without importing the whole application.
export function copyText(text: string) {
  try {
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(text).catch(() => execCopy(text))
      return
    }
  } catch {
    /* fall through */
  }
  execCopy(text)
}

function execCopy(text: string) {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.top = '-1000px'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  try {
    document.execCommand('copy')
  } catch {
    /* ignore */
  }
  document.body.removeChild(ta)
}
