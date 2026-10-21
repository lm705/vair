// The explanation under a setting, and the same explanation on demand.
//
// "Show setting descriptions" used to be all or nothing: on, every setting
// carries a paragraph; off, the paragraphs are gone and so is any way to read
// one without turning the whole thing back on. Off now leaves a mark beside the
// setting — one click and the text is there, over the dialog, for that setting
// only.
//
// Two things follow from that, and they are why this is a component rather than
// the stylesheet rule it replaces:
//
//   - a description that can be opened has to exist in both languages at once.
//     The paragraph under a setting is written in the interface language and
//     that is right; the pop-up carries EN/RU, because the English text is the
//     original and someone reading a translated sentence about routing may want
//     to see what it was translated from. Everything here therefore holds the
//     English source string — the i18n key — and translates it when it draws,
//     rather than being handed a finished sentence.
//   - a setting with a list of choices explains the chosen one under the
//     control (the others are noise while you are not on them) and explains all
//     of them in the pop-up, where you have asked. That is `opts`.
//
// The hidden text stays in the DOM: the settings search reads the descriptions,
// and a search that stopped finding things when the descriptions were switched
// off would be a second, unannounced setting.
import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { CSSProperties, ReactNode } from 'react'
import { t10 } from './i18n'

type TT = (s: string) => string

// One choice of a multiple-choice setting: what it is called, and what it does.
// Both are English source strings — the pop-up translates them. For a list of
// examples rather than options (the table filter) the left side is code and is
// left alone; `mono` says which.
export type HintOpt = [string, string]

// opt names one. A function rather than a bare array literal so the translation
// checker can find the two strings: it looks for calls, and a pair inside an
// array is invisible to it — which would mean the choices in these lists were
// the one part of the interface nothing verified had Russian.
export const opt = (name: string, desc: string): HintOpt => [name, desc]

// ex names an example instead. Same pair, different rule for the left half: it
// is what you type, so it stays as written in both languages — and the checker
// knows not to ask for a Russian "germany+ws+tls".
export const ex = (code: string, desc: string): HintOpt => [code, desc]

// What a mark opens: a paragraph, optionally a list under it. `render` is the
// escape hatch for a description built out of several strings and a number —
// it is handed the translator for the language being shown.
export type Desc = {
  en?: string
  opts?: HintOpt[]
  // A list of examples rather than a list of choices: the left half is typed
  // rather than read, so it is code and is never translated. It also names the
  // list — choices are Options, examples are Examples.
  mono?: boolean
  render?: (tt: TT) => ReactNode
}

type Ctx = { lang: string; hide: boolean; open: (d: Desc, at: HTMLElement) => void }

const HintCtx = createContext<Ctx>({ lang: 'en', hide: false, open: () => {} })

// InfoDot is the mark itself. Exported because two of them are not attached to
// a setting: the table filter has no paragraph to hide, so its mark is there
// whatever the setting says.
export function InfoDot({ desc, className }: { desc: Desc; className?: string }) {
  const { lang, open } = useContext(HintCtx)
  const ref = useRef<HTMLButtonElement>(null)
  return (
    <button
      ref={ref}
      type="button"
      className={'hint-i' + (className ? ' ' + className : '')}
      title={t10(lang, 'Show the description')}
      aria-label={t10(lang, 'Show the description')}
      onClick={(e) => {
        e.preventDefault()
        e.stopPropagation()
        if (ref.current) open(desc, ref.current)
      }}
    >
      i
    </button>
  )
}

// Hint is a description under a setting: the paragraph while descriptions are
// on, the mark while they are off.
export function Hint({
  en,
  opts,
  mono,
  className,
  style,
  children,
}: {
  en?: string
  opts?: HintOpt[]
  mono?: boolean
  className?: string
  style?: CSSProperties
  children?: (tt: TT) => ReactNode
}) {
  const { lang, hide } = useContext(HintCtx)
  const tt = (s: string) => t10(lang, s)
  const body = children ? children(tt) : tt(en || '')
  // Given both, they are two different sentences on purpose: `children` is what
  // the setting says under the control — which for a list of choices is the line
  // for the chosen one — and `en` is what the pop-up opens with, above the list
  // of all of them. Given one, it is both.
  const desc: Desc = { en, opts, mono, render: en ? undefined : children }
  if (!hide)
    return (
      <div className={'modal-hint' + (className ? ' ' + className : '')} style={style}>
        {body}
      </div>
    )
  return <Mark desc={desc} body={body} />
}

// The mark belongs beside the setting's name, on its line — that is where you
// look when you want to know what a setting does. It is written where the
// paragraph was, which is the line below, so it is put in place by measurement:
// the row above is found, the end of its label's text is measured, and the mark
// is laid over that point. Two layouts have to come out right and this handles
// both — the label beside its control, and the label stacked above it.
//
// Measured rather than written into the label because the label is written out
// at seventy call sites and the description at seventy more; pairing them in the
// markup would be seventy edits to move one button, and the pairing would then
// have to be maintained by hand. Measured rather than moved with the DOM because
// the label's text is React's to own: it rewrites it on a language change, and
// anything of ours sitting inside would go with it.
function Mark({ desc, body }: { desc: Desc; body: ReactNode }) {
  const box = useRef<HTMLDivElement>(null)
  const dot = useRef<HTMLSpanElement>(null)
  const placeRef = useRef<() => void>(() => {})
  useLayoutEffect(() => {
    const place = () => {
      const el = box.current
      const d = dot.current
      if (!el || !d) return
      const label = ownerLabel(el)
      if (!label) {
        // Nothing to sit beside — a description under a list or a section
        // rather than under a setting. It stays where it was written.
        el.classList.remove('anchored')
        d.style.left = ''
        d.style.top = ''
        return
      }
      // The end of the last line of the label's TEXT, not of its box: the box
      // is the full width of the row when the label is stacked above its
      // control, and half a row of empty space is not where the name ends.
      const r = document.createRange()
      r.selectNodeContents(label)
      const rects = r.getClientRects()
      const last = rects.length ? rects[rects.length - 1] : label.getBoundingClientRect()
      el.classList.add('anchored')
      const own = el.getBoundingClientRect()
      d.style.left = Math.round(last.right - own.left + 6) + 'px'
      d.style.top = Math.round(last.top + last.height / 2 - own.top - 8) + 'px'
    }
    // Re-measured after every render: the label above may have changed with the
    // language, and a setting that appears and disappears moves the ones below.
    placeRef.current = place
    place()
  })
  // A measurement is only as good as the moment it was taken in, and the first
  // moment is the wrong one. This runs before the dialog around it has settled:
  // the settings window shows one section at a time, and it hides the others in
  // ITS layout effect — which runs after this one, because a child's runs
  // first. Measured then, every mark below the fold is placed against a page
  // that is about to become much shorter, and lands at the bottom of it.
  //
  // So: again on the next frame, when the layout has settled, and again
  // whenever the dialog changes height (a section shown or hidden, a search
  // narrowing the list) or the label's own size changes (the font arrives, the
  // language changes). The window's own resize is the last of them.
  useEffect(() => {
    const again = () => placeRef.current()
    const frame = requestAnimationFrame(again)
    window.addEventListener('resize', again)
    if (document.fonts && document.fonts.ready) document.fonts.ready.then(again).catch(() => {})
    let ro: ResizeObserver | null = null
    if (typeof ResizeObserver !== 'undefined') {
      ro = new ResizeObserver(again)
      const dialog = box.current?.closest('.modal-box')
      if (dialog) ro.observe(dialog)
      const label = box.current ? ownerLabel(box.current) : null
      if (label) ro.observe(label)
    }
    return () => {
      cancelAnimationFrame(frame)
      window.removeEventListener('resize', again)
      ro?.disconnect()
    }
  }, [])
  return (
    <div className="hint-mark" ref={box}>
      <span className="hint-slot" ref={dot}>
        <InfoDot desc={desc} />
      </span>
      {/* Still readable by the settings search, never by the eye. */}
      <span className="hint-src">{body}</span>
    </div>
  )
}

// ownerLabel is the name the mark belongs to: the nearest thing above it that
// names something. Usually a setting's own label, but a description can belong
// to a whole section (the candidate tabs, which are pills rather than a row) or
// to a fold (the connection log), and those are named by a heading.
//
// The walk stops at another mark — two of them over one label would be drawn on
// top of each other, and neither would say which setting it was for, so the
// second stays in the flow instead. It also stops at the edge of a section
// rather than climbing into the one above: better no anchor than the wrong one.
// A fold's body is not a section, so one step out of it is allowed, and that is
// what reaches the heading the fold is titled with.
function ownerLabel(from: HTMLElement): HTMLElement | null {
  for (let node: HTMLElement | null = from; node; node = node.parentElement) {
    for (let p = node.previousElementSibling; p; p = p.previousElementSibling) {
      if (p.classList.contains('hint-mark')) return null
      if (p.classList.contains('modal-row')) {
        // Two classes name a setting inside a row — the tab dialog uses the
        // second for its section labels — and where neither is present the
        // first child is the label by position. A row that opens with a
        // control instead has nothing to sit beside, and says so by
        // returning nothing.
        const named = p.querySelector<HTMLElement>('.modal-row-label, .modal-label')
        if (named) return named
        const first = p.firstElementChild as HTMLElement | null
        if (first && (first.tagName === 'SPAN' || first.tagName === 'DIV') && (first.textContent || '').trim())
          return first
        return null
      }
      if (p.classList.contains('section-header') || p.classList.contains('auto-collapse-head')) {
        // The heading's last element is its text; a fold also carries an arrow
        // before it, and the mark belongs after the words, not after the arrow.
        return (p.lastElementChild as HTMLElement) || (p as HTMLElement)
      }
    }
    if (node.classList.contains('settings-section') || node.classList.contains('modal-box')) return null
  }
  return null
}

// A description that belongs to the setting above it rather than to itself: the
// line that appears under a list when one particular choice is made. It is part
// of that setting's pop-up (as an entry of `opts`), so it must not carry a mark
// of its own — two marks for one setting, one of which comes and goes with the
// selection, reads as two settings.
export function SubHint({ children, style }: { children: (tt: TT) => ReactNode; style?: CSSProperties }) {
  const { lang } = useContext(HintCtx)
  return (
    <div className="modal-hint" style={style}>
      {children((s: string) => t10(lang, s))}
    </div>
  )
}

// HintHost wraps the app: it hands every mark the language and the setting, and
// owns the single pop-up they all open.
export function HintHost({
  lang,
  hide,
  children,
}: {
  lang: string
  hide: boolean
  children: ReactNode
}) {
  const [open, setOpen] = useState<{ desc: Desc; x: number; y: number } | null>(null)
  // Which language the descriptions are being read in. Kept here rather than in
  // the pop-up so it holds while you go down a dialog reading one setting after
  // another — someone who asked for English asked for English, not for English
  // once. It starts at the interface language and returns to it when that
  // changes.
  const [cur, setCur] = useState(lang === 'ru' ? 'ru' : 'en')
  useEffect(() => setCur(lang === 'ru' ? 'ru' : 'en'), [lang])
  const show = (desc: Desc, at: HTMLElement) => {
    const r = at.getBoundingClientRect()
    setOpen({ desc, x: r.left, y: r.bottom + 6 })
  }
  return (
    <HintCtx.Provider value={{ lang, hide, open: show }}>
      {children}
      {open ? (
        <HintPopover
          desc={open.desc}
          x={open.x}
          y={open.y}
          cur={cur}
          setCur={setCur}
          onClose={() => setOpen(null)}
        />
      ) : null}
    </HintCtx.Provider>
  )
}

// The pop-up. Placed under the mark that opened it and pulled back inside the
// window if that would put it past an edge — measured after mount rather than
// guessed from a fixed size, because these vary from one line to a screenful.
function HintPopover({
  desc,
  x,
  y,
  cur,
  setCur,
  onClose,
}: {
  desc: Desc
  x: number
  y: number
  cur: string
  setCur: (l: string) => void
  onClose: () => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState({ x, y })
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const r = el.getBoundingClientRect()
    let nx = x
    let ny = y
    if (nx + r.width > window.innerWidth - 8) nx = Math.max(8, window.innerWidth - r.width - 8)
    if (ny + r.height > window.innerHeight - 8) {
      // Above the mark instead of below it, and pinned to the top if even that
      // does not fit — a description longer than the window still starts at its
      // first line.
      ny = Math.max(8, y - r.height - 26)
    }
    setPos({ x: nx, y: ny })
  }, [x, y, desc, cur])
  useEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', esc)
    return () => window.removeEventListener('keydown', esc)
  }, [onClose])

  const tt = (s: string) => t10(cur, s)
  const body = desc.render ? desc.render(tt) : tt(desc.en || '')
  return (
    <>
      <div className="hint-veil" onMouseDown={onClose} />
      <div
        className="hint-pop"
        ref={ref}
        style={{ left: pos.x, top: pos.y }}
        onMouseDown={(e) => e.stopPropagation()}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="hint-pop-head">
          <span className="hint-pop-title">{tt('Description')}</span>
          <button
            className={'hint-lang' + (cur === 'en' ? ' on' : '')}
            onClick={() => setCur('en')}
          >
            EN
          </button>
          <button
            className={'hint-lang' + (cur === 'ru' ? ' on' : '')}
            onClick={() => setCur('ru')}
          >
            RU
          </button>
          <button className="hint-pop-x" onClick={onClose} title={tt('Close')}>
            ×
          </button>
        </div>
        {body ? <div className="hint-pop-body">{body}</div> : null}
        {desc.opts && desc.opts.length ? (
          <>
            <div className="hint-pop-cap">{desc.mono ? tt('Examples') : tt('Options')}</div>
            <div className="hint-opts">
              {desc.opts.map(([k, v]) => (
                <div className="hint-opt" key={k}>
                  <span className={'hint-opt-k' + (desc.mono ? ' mono' : '')}>
                    {desc.mono ? k : tt(k)}
                  </span>
                  <span className="hint-opt-v">{tt(v)}</span>
                </div>
              ))}
            </div>
          </>
        ) : null}
      </div>
    </>
  )
}
