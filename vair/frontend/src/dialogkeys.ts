// The keyboard in dialogs, in one place.
//
// Every dialog is a .modal-overlay with its box inside, and none of them did
// anything about the keyboard: Settings opened from Enter left the focus on the
// button behind it, Tab walked on into the window underneath, and Esc closed
// nothing — so a keyboard could open a dialog and not get back out. Rather than
// teach each of a dozen dialogs the same three things, this watches for them:
//
//   - a dialog that opens takes the focus (its box, which says it is a modal
//     dialog), unless something inside it already took it;
//   - Tab and Shift+Tab go round inside the top dialog and never behind it;
//   - Esc closes the top dialog the way its ✕ does — so a dialog that asks
//     before throwing away an edit still asks — or, with no ✕, the way a click
//     beside it does;
//   - when it closes, the focus goes back to where it was before it opened.
//
// A dialog with an Esc of its own (the discard question) stops the key before
// it gets here; one that wants to keep Esc away from itself does the same.

const OVERLAY = '.modal-overlay'
const FOCUSABLE =
  'button:not([disabled]), a[href], input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

const overlays = () => [...document.querySelectorAll<HTMLElement>(OVERLAY)]

/** The dialog on top, or null when none is open. */
export const topDialog = (): HTMLElement | null => {
  const all = overlays()
  return all.length ? all[all.length - 1] : null
}

/** Whether a dialog is open: keys meant for the table must leave it alone. */
export const dialogOpen = () => topDialog() !== null || document.querySelector('.kb-veil') !== null

const focusables = (root: HTMLElement) =>
  [...root.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((el) => el.getClientRects().length > 0)

// A description popup opened from inside a dialog lives outside it and has
// the keyboard first.
const inHint = () => !!document.querySelector('.hint-pop')

let installed = false

export function installDialogKeys() {
  if (installed) return
  installed = true

  // Where the focus was before each open dialog appeared.
  const back = new Map<HTMLElement, HTMLElement | null>()
  let queued = false
  const settle = () => {
    queued = false
    const list = overlays()
    let opened: HTMLElement | null = null
    for (const o of list) {
      if (back.has(o)) continue
      back.set(o, document.activeElement as HTMLElement | null)
      const box = o.firstElementChild as HTMLElement | null
      if (box) {
        if (!box.hasAttribute('role')) box.setAttribute('role', 'dialog')
        box.setAttribute('aria-modal', 'true')
        if (!box.hasAttribute('tabindex')) box.tabIndex = -1
      }
      opened = o
    }
    for (const [o, prev] of back) {
      if (o.isConnected) continue
      back.delete(o)
      const now = document.activeElement
      if ((!now || now === document.body || !now.isConnected) && prev?.isConnected)
        prev.focus({ preventScroll: true })
    }
    if (opened && opened === list[list.length - 1] && !opened.contains(document.activeElement)) {
      const box = opened.firstElementChild as HTMLElement | null
      ;(box ?? opened).focus({ preventScroll: true })
    }
  }
  // Once a frame at most: the table below re-renders on every scroll.
  new MutationObserver(() => {
    if (queued) return
    queued = true
    requestAnimationFrame(settle)
  }).observe(document.body, { childList: true, subtree: true })

  document.addEventListener('keydown', (e) => {
    const top = topDialog()
    if (!top || inHint()) return
    if (e.key === 'Tab') {
      const list = focusables(top)
      if (!list.length) {
        e.preventDefault()
        return
      }
      const i = list.indexOf(document.activeElement as HTMLElement)
      const first = list[0]
      const last = list[list.length - 1]
      if (i < 0) {
        e.preventDefault()
        ;(e.shiftKey ? last : first).focus()
      } else if (e.shiftKey && i === 0) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && i === list.length - 1) {
        e.preventDefault()
        first.focus()
      }
      return
    }
    if (e.key === 'Escape') {
      // A list or a menu inside the dialog closed itself with this key.
      if (e.defaultPrevented) return
      e.preventDefault()
      const x = top.querySelector<HTMLElement>('.xbtn')
      if (x) x.click()
      else top.click()
    }
  })
}
