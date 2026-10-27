// A drop-in replacement for <select>, because the native one cannot be themed.
//
// On Linux the popup a <select> drops down is drawn by the toolkit, not by the
// page: it ignores our CSS, it ignores `color-scheme: dark`, and it ignores
// GTK_THEME (all three measured on GNOME 46 / WebKitGTK 2.52 — the popup stayed
// white over the dark app, only its selection colour tracked the GTK theme).
// The only lever left is to stop asking for a native control.
//
// The API mirrors <select> deliberately — same className/style/value/onChange,
// same <option> children — so switching a call site is a two-character edit and
// its existing handler keeps working unchanged.
import { useEffect, useLayoutEffect, useRef, useState, Children, isValidElement } from 'react'
import type { CSSProperties, ReactNode } from 'react'
import { createPortal } from 'react-dom'

type Opt = { value: string; label: string; disabled: boolean }

// text flattens an option's children to a plain label. Not every option is one
// string: `{tt('Allow LAN access')} (0.0.0.0)` is an ARRAY of two, and
// String()-ing that would join them with a comma.
function text(node: ReactNode): string {
  if (node === null || node === undefined || typeof node === 'boolean') return ''
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(text).join('')
  if (isValidElement(node)) return text((node as any).props?.children)
  return ''
}

// flatten walks <option> children, including the arrays a .map() produces and
// any fragments around them.
function flatten(children: ReactNode, out: Opt[] = []): Opt[] {
  Children.forEach(children, (child: any) => {
    if (!isValidElement(child)) return
    const props: any = (child as any).props
    if ((child as any).type === 'option') {
      out.push({
        value: String(props.value ?? ''),
        label: text(props.children),
        disabled: !!props.disabled,
      })
    } else if (props?.children) {
      flatten(props.children, out) // fragment or wrapper
    }
  })
  return out
}

export default function Select({
  className,
  style,
  value,
  onChange,
  children,
  id,
  title,
  disabled,
}: {
  className?: string
  style?: CSSProperties
  value: string
  // Shaped like a DOM change event so existing handlers need no edit.
  onChange: (e: { target: { value: string } }) => void
  children: ReactNode
  id?: string
  title?: string
  disabled?: boolean
}) {
  const opts = flatten(children)
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const btnRef = useRef<HTMLButtonElement | null>(null)
  const listRef = useRef<HTMLDivElement | null>(null)
  // fs travels with the position: the list is portalled to <body>, so it
  // inherits the page's size rather than the trigger's, and a dialog scaled up
  // to 18px dropped a 13px list. Taken from the trigger itself, so it is right
  // for the settings dialog and for the small selects in the log bar alike.
  const [pos, setPos] = useState<
    { left: number; right: number; top: number; width: number; drop: 'down' | 'up'; fs: string } | null
  >(null)

  const current = opts.find((o) => o.value === value)

  // The list is portalled to <body> and positioned in viewport coordinates.
  // Rendering it inside the trigger would put it inside the settings modal's
  // scrolling box, where a list opened near the bottom is simply clipped.
  useLayoutEffect(() => {
    if (!open || !btnRef.current) return
    const r = btnRef.current.getBoundingClientRect()
    const fs = getComputedStyle(btnRef.current).fontSize
    // Row height tracks the font: padding 8px plus a line and a half.
    const rowH = (parseFloat(fs) || 12) * 1.5 + 8
    const wanted = Math.min(opts.length * rowH + 8, 260)
    const below = window.innerHeight - r.bottom
    const drop = below < wanted && r.top > below ? 'up' : 'down'
    setPos({
      left: r.left,
      right: r.right,
      top: drop === 'down' ? r.bottom + 2 : Math.max(4, r.top - wanted - 2),
      width: r.width,
      drop,
      fs,
    })
  }, [open, opts.length])

  // A list wider than its trigger, hung from the trigger's left edge, ran off
  // the right of the window when the trigger sat at the right of a row — the
  // usual place for a short list — and its longest entries were cut. It is
  // measured once it is drawn and, when it does not fit, lines up with the
  // trigger's right edge instead, as far left as it has to go. Before paint,
  // so it is never seen in the wrong place.
  useLayoutEffect(() => {
    const el = listRef.current
    if (!open || !pos || !el) return
    const w = el.offsetWidth
    const maxLeft = window.innerWidth - w - 4
    if (pos.left <= maxLeft) return
    const left = Math.max(4, Math.min(pos.right - w, maxLeft))
    if (left !== pos.left) setPos({ ...pos, left })
  }, [open, pos])

  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => {
      if (btnRef.current?.contains(e.target as Node)) return
      if (listRef.current?.contains(e.target as Node)) return
      setOpen(false)
    }
    // Scroll and resize move the trigger out from under a fixed-position list,
    // so close rather than let it float somewhere wrong. Capture phase: the
    // scroll happens on the modal body, not on window.
    //
    // Only a scroll that actually moves the trigger counts. The log panel
    // auto-scrolls itself on every new line, and closing on that made the log's
    // own dropdowns unusable — they shut the instant a line arrived, which with
    // an engine running is immediately.
    const onScroll = (e: Event) => {
      const t = e.target as Node | null
      if (t && t.nodeType === 1 && btnRef.current && !t.contains(btnRef.current)) return
      setOpen(false)
    }
    document.addEventListener('mousedown', onDoc, true)
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onScroll)
    return () => {
      document.removeEventListener('mousedown', onDoc, true)
      window.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', onScroll)
    }
  }, [open])

  const pick = (o: Opt) => {
    setOpen(false)
    if (o.disabled || o.value === value) return
    onChange({ target: { value: o.value } })
  }

  const onKey = (e: React.KeyboardEvent) => {
    if (!open) {
      if (e.key === 'Enter' || e.key === ' ' || e.key === 'ArrowDown') {
        e.preventDefault()
        setActive(Math.max(0, opts.findIndex((o) => o.value === value)))
        setOpen(true)
      }
      return
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      setOpen(false)
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActive((i) => Math.min(opts.length - 1, i + 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive((i) => Math.max(0, i - 1))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (opts[active]) pick(opts[active])
    }
  }

  return (
    <>
      <button
        type="button"
        id={id}
        title={title}
        disabled={disabled}
        ref={btnRef}
        className={(className ? className + ' ' : '') + 'vsel' + (open ? ' vsel-open' : '')}
        style={style}
        onClick={() => {
          if (disabled) return
          setActive(Math.max(0, opts.findIndex((o) => o.value === value)))
          setOpen((v) => !v)
        }}
        onKeyDown={onKey}
      >
        <span className="vsel-label">{current?.label ?? ''}</span>
        <span className="vsel-caret">▾</span>
      </button>
      {open &&
        pos &&
        createPortal(
          <div
            ref={listRef}
            className="vsel-list"
            style={{ left: pos.left, top: pos.top, minWidth: pos.width, fontSize: pos.fs }}
            role="listbox"
          >
            {opts.map((o, i) => (
              <div
                key={o.value + i}
                role="option"
                aria-selected={o.value === value}
                className={
                  'vsel-opt' +
                  (o.value === value ? ' vsel-cur' : '') +
                  (i === active ? ' vsel-act' : '') +
                  (o.disabled ? ' vsel-dis' : '')
                }
                onMouseEnter={() => setActive(i)}
                onClick={() => pick(o)}
              >
                {o.label}
              </div>
            ))}
          </div>,
          document.body,
        )}
    </>
  )
}
