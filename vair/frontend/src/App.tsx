// Vair 2.0 main window — an exact port of the 1.10 web/index.html DOM (same
// ids/classes, so style.css is the verbatim 1.10 stylesheet). The main window
// stays English on purpose (as in 1.10); context menus/chips translate via t10.
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import Check from './Check'
import { Events, Window } from '@wailsio/runtime'
import { useVirtualizer } from '@tanstack/react-virtual'
import { IS_REMOTE } from './remote'
import {
  ConfigService,
  ConnService,
  TestService,
  AutoService,
  TabService,
  LogService,
  QRService,
  SettingsService,
  UpdateService,
  ClipboardService,
} from '../bindings/vair/internal/services'
import { t10 } from './i18n'
import { copyText } from './clipboard'
import { checkExit, exitChecking } from './exitcheck'
import SettingsModal from './SettingsModal'
import TabModal from './TabModal'
import SourcesModal from './SourcesModal'
import AutoModal from './AutoModal'
import ConfigModal from './ConfigModal'
import ConnectionsModal from './ConnectionsModal'
import HistoryModal from './HistoryModal'
import SessionModal from './SessionModal'
import UnlockModal from './UnlockModal'
import Select from './Select'
import { HintHost, InfoDot, ex } from './hints'
import type { Desc } from './hints'

type Row = NonNullable<Awaited<ReturnType<typeof ConfigService.Window>>>[number]
// Sort is "idx" (original order) or "<column>" / "<column>:desc" — the table
// header cycles a column through natural → reversed → off. SORT_COLS lists the
// sortable columns; the backend (core/memstore.go lessKey) knows the same names.
type SortCol = 'type' | 'name' | 'host' | 'transport' | 'security' | 'ping' | 'stab' | 'speed'
type Sort = 'idx' | SortCol | `${SortCol}:desc`
type Menu =
  | { kind: 'row'; x: number; y: number; idx: number }
  | { kind: 'empty'; x: number; y: number }
  | { kind: 'tab'; x: number; y: number; tab: any }

const ROW_H = 33 // measured 1.10 row: act-cell button 22px + td padding 10 + border 1
// Tab bar → dropdown thresholds: more than this many rows, or a row that can no
// longer fit this many tabs.
const TAB_MAX_ROWS = 5
const TAB_MIN_PER_ROW = 3
const WINDOW = 200 // rows fetched per Go call (windowed read from the memstore)
// TYPE pills: [label, protocol value] — exact 1.10 set/order.
const PROTOS: [string, string][] = [
  ['vless', 'vless'],
  ['vmess', 'vmess'],
  ['trojan', 'trojan'],
  ['ss', 'ss'],
  ['ss2022', 'ss2022'],
  ['hy2', 'hysteria2'],
  ['tuic', 'tuic'],
  ['wg', 'wireguard'],
  ['socks', 'socks'],
  ['http', 'http'],
]
// The detached AUTO window loads the same app at /?view=auto; the 1.10
// body.auto-standalone CSS then hides everything except the titlebar + panel.
const STANDALONE_AUTO = new URLSearchParams(window.location.search).get('view') === 'auto'

const FILTER_TITLE =
  'Filter by name, host, type, transport or security. + combines conditions: germany+ws+tls needs all three (name, transport, security); germany+poland matches either (same column). Use field: to target a column, e.g. name:russia. unlock:chatgpt keeps the servers the last service check found it open on.'
// The same thing said properly, for the mark beside the box. The tooltip above
// is one line by necessity; a filter with three ways to combine words earns
// worked examples, and those are the part people copy. The examples themselves
// are left in English — they are what you type.
const FILTER_HELP: Desc = {
  en: 'Hides every row that does not match what you type. One word is searched for everywhere at once: the name, the address, the type, the transport and the security. To search one column only, put its name and a colon in front — name:, host:, type:, transport:, security:. Conditions are joined with a plus. Conditions about different columns must all match; two conditions about the same column mean either of them. Case does not matter and part of a word is enough. The figure beside the box counts the rows left against the tab total.',
  mono: true,
  opts: [
    ex('germany', 'Anywhere: the name, the address or the type.'),
    ex('name:germany', 'The name only — an address that happens to contain it is left out.'),
    ex('germany+ws+tls', 'All three at once: the name, the ws transport and TLS security.'),
    ex('name:germany+host:185.', 'Two columns at once: the name contains germany AND the address starts with 185. Spaces around the plus are allowed.'),
    ex('germany+poland', 'Either of them — two words for the same column widen the search instead of narrowing it.'),
    ex('host:185.', 'Every server whose address starts with that.'),
    ex('type:vless', 'One protocol. Bare vless, ws, reality and the like are recognised without the prefix.'),
    ex('unlock:chatgpt', 'The servers the last service check found ChatGPT open on. Servers nobody checked are left out — the honest answer for them is unknown.'),
  ],
}

// FloatingMenu positions a fixed pop-up at (x, y) but never outside the window.
//
// A menu opened by right-clicking near the right or bottom edge used to be drawn
// past the frame, and the further into the corner the click was, the more of the
// menu went with it — the last items, which are the destructive ones, ended up
// unreachable. Measured after mount rather than guessed from a fixed width,
// because the menus differ in size and the row menu grows with the selection.
//
// useLayoutEffect, not useEffect: it runs before the browser paints, so the
// corrected position is the first one drawn and there is no visible jump.
function FloatingMenu({
  x,
  y,
  className,
  children,
}: {
  x: number
  y: number
  className: string
  children: React.ReactNode
}) {
  const ref = useRef<HTMLDivElement | null>(null)
  const [pos, setPos] = useState({ left: x, top: y })
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const gap = 6 // never flush against the frame
    const { width, height } = el.getBoundingClientRect()
    setPos({
      left: Math.max(gap, Math.min(x, window.innerWidth - width - gap)),
      top: Math.max(gap, Math.min(y, window.innerHeight - height - gap)),
    })
  }, [x, y])
  return (
    <div ref={ref} className={className} style={{ left: pos.left, top: pos.top }}>
      {children}
    </div>
  )
}

// ── exact 1.10 helpers ──────────────────────────────────────────────
function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  if (n < 1024) return n + ' B'
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  const s = v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2)
  return s + ' ' + units[i]
}
function fmtUptime(s: number): string {
  if (!s || s < 0) return ''
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const ss = s % 60
  return h > 0 ? h + 'h ' + m + 'm' : m > 0 ? m + 'm ' + ss + 's' : ss + 's'
}
function rawBody(raw: string): string {
  const i = (raw || '').lastIndexOf('#')
  return i >= 0 ? raw.slice(0, i) : raw || ''
}
// rawKey is what "the same configuration" means when two rows are compared.
//
// The name is already out of it (rawBody drops the #fragment), which is why a
// server renamed by its subscription still lights up. What was left in was the
// spelling of the query: the same configuration pasted twice can arrive as
// `path=%2F` and as `path=/`, or with its parameters in another order, and the
// row on the second tab then stayed unlit while the first one was connected.
//
// Percent-decoded and sorted, both spellings are one key. Nothing else is
// touched — no lowercasing, because a UUID is case-insensitive but a
// Shadowsocks key is not.
function rawKey(raw: string): string {
  const body = rawBody(raw)
  const q = body.indexOf('?')
  const dec = (v: string) => {
    try {
      return decodeURIComponent(v)
    } catch {
      return v // a stray % is not ours to fix — compare it as written
    }
  }
  if (q < 0) return dec(body)
  const head = dec(body.slice(0, q))
  const params = body
    .slice(q + 1)
    .split('&')
    .filter(Boolean)
    .map((p) => {
      const e = p.indexOf('=')
      return e < 0 ? dec(p) : dec(p.slice(0, e)) + '=' + dec(p.slice(e + 1))
    })
    .sort()
  return head + '?' + params.join('&')
}
// stabTitle is the same history in words, for the hover.
function stabTitle(tt: (s: string) => string, st: any): string {
  const parts = [
    `${tt('Checks')}: ${st.samples}`,
    `${tt('Average')}: ${st.avg} ms`,
    `${tt('Jitter')}: ±${st.jitter} ms`,
  ]
  if (st.percent >= 0) parts.push(`${tt('Answered')}: ${st.percent}%`)
  else parts.push(tt('Too few checks to judge'))
  return parts.join('\n')
}

function protoLabel(pr: string): string {
  if (pr === 'hysteria2') return 'hy2'
  if (pr === 'wireguard') return 'wg'
  return pr
}
function fmtLogTime(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => (n < 10 ? '0' + n : '' + n)
  return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds())
}
// isImp mirrors core.isImportant — everything except the engines' [Info]
// narration. Both sides must agree, or the live stream and the buffer fetched
// on open would disagree about what belongs in the "important" view.
function isImp(l: any): boolean {
  return l.lvl !== 'info' || l.src === 'vair' || l.src === 'test'
}
function containsNodeURL(text: string): boolean {
  if (!text) return false
  // socks5 before socks so the alternation cannot match the shorter one first.
  // http(s) is absent on purpose: a pasted subscription link is a source, not a
  // node, and subURLsIn below is what handles those.
  return /(vless|vmess|trojan|ss|hysteria2|hy2|tuic|wireguard|wg|socks5|socks):\/\//i.test(text)
}
// subURLsIn returns every http(s) link in the text, one per line — so pasting a
// whole list of subscriptions adds them all. Every non-blank line has to BEGIN
// with a link, and the answer is [] otherwise: mixed content is configs, not a
// source list, so a config body that merely contains a URL never lands here.
//
// What follows the link on its line is ignored rather than fatal. Lists are
// copied out of chats and READMEs with a comment, a count or a stray word after
// the address, and refusing the whole paste over one of them threw away
// nineteen good links to punish the twentieth — silently, because a paste that
// yields neither sources nor configs does nothing at all.
function subURLsIn(text: string): string[] {
  const lines = sourceLines(text)
  if (lines.length === 0) return []
  const urls: string[] = []
  for (const l of lines) {
    const link = l.split(/\s+/)[0]
    if (!/^https?:\/\/\S+$/i.test(link)) return []
    if (!urls.includes(link)) urls.push(link)
  }
  return urls
}
function sourceLines(text: string): string[] {
  return (text || '')
    .split(/[\r\n]+/)
    .map((l) => l.trim())
    .filter(Boolean)
}
// Whether any of those lines carried something after the link, so a paste can
// say what it left out instead of quietly keeping half of what was written.
function subURLsHadExtra(text: string): boolean {
  return sourceLines(text).some((l) => /\s/.test(l))
}
function pasteworthy(text: string): boolean {
  if (!text) return false
  if (containsNodeURL(text)) return true
  if (subURLsIn(text).length > 0) return true
  // base64-encoded subscription body
  const t = text.trim()
  return t.length > 50 && /^[A-Za-z0-9+/=\r\n]+$/.test(t)
}

// A speed reading in at most ten characters: two decimals under 100 MB/s,
// one above it, none above a thousand. The speed chip is sized to the longest
// of these, so the cap is what keeps the column from growing for a number
// nobody has.
function fmtSpeed(v: number) {
  const d = v >= 1000 ? 0 : v >= 100 ? 1 : 2
  return v.toFixed(d) + ' MB/s'
}

// setFontVar writes one of the two text-size variables. The tab buttons carry
// `transition:all`, so their text size arrives 150ms after the others — that is
// the transition doing its job, not a missed repaint.
function setFontVar(name: string, px: number) {
  document.documentElement.style.setProperty(name, px + 'px')
}

// The red chip that stands in for a disconnect when there is nothing to
// disconnect from.
//
// It says "releasing" while it works, and that is not decoration: opening the
// firewall is a PowerShell run of a second or more, and with the chip sitting
// there unchanged the first click read as a click that did nothing. The second
// one then arrived while the first was still working.
function BlockedChip({ tt }: { tt: (s: string) => string }) {
  const [busy, setBusy] = useState(false)
  return (
    <button
      className="pchip blocked-chip"
      disabled={busy}
      title={tt('Traffic is blocked by the kill-switch. Click to release it.')}
      onClick={() => {
        setBusy(true)
        Promise.resolve(ConnService.ReleaseBlock()).finally(() => setBusy(false))
      }}
    >
      {busy ? tt('releasing…') : tt('traffic blocked ✕')}
    </button>
  )
}

export default function App() {
  // table / view state
  const [total, setTotal] = useState(0)
  const [tabTotal, setTabTotal] = useState(0)
  const [stats, setStats] = useState<any>(null)
  const [connsOpen, setConnsOpen] = useState(false)
  const [unlockOpen, setUnlockOpen] = useState(false)
  // The row whose measurement history is open, or -1 for none. The AUTO panel
  // opens the same dialog for its own candidates, addressing them by the
  // configuration rather than by a row of this table — it keeps that state
  // itself, so the detached window has it too.
  const [historyIdx, setHistoryIdx] = useState(-1)
  // "Show setting descriptions" as the dialogs see it. The body class below
  // still drives the stylesheet; this drives the marks that replace the
  // paragraphs, which are React's to draw.
  const [hideHints, setHideHints] = useState(false)
  const [traffic, setTraffic] = useState<any>(null) // live stats_update payload
  const [sort, setSort] = useState<Sort>('idx')
  const [filter, setFilter] = useState('')
  const [protos, setProtos] = useState<string[]>([])
  const [tabs, setTabs] = useState<any[]>([])
  const [activeTab, setActiveTab] = useState('')
  const [reloadKey, setReloadKey] = useState(0)
  const [loading, setLoading] = useState<{ op?: string } | null>(null)
  // connection
  const [conn, setConn] = useState<any>(null)
  const [connPing, setConnPing] = useState<any>(null) // {st, delay} for the cping chip
  const [exit, setExit] = useState<any>(null) // {cls, label, title} for the cexit chip
  const [selectedMode, setSelectedMode] = useState<'proxy' | 'tun'>('proxy')
  const [lastRaw, setLastRaw] = useState('') // most recently connected raw → "last" badge
  const [auto, setAuto] = useState(false)
  const [autoLast, setAutoLast] = useState<any>({}) // latest auto_update payload
  // Browser (phone) mode can't open the native AUTO window — the panel renders
  // as an in-page modal there instead.
  const [remoteAutoOpen, setRemoteAutoOpen] = useState(false)
  // selection / menus / rename
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [menu, setMenu] = useState<Menu | null>(null)
  const [editingIdx, setEditingIdx] = useState<number | null>(null)
  const [editName, setEditName] = useState('')
  const [copiedSet, setCopiedSet] = useState<Set<number>>(new Set()) // row ⎘ "done" (✓) flash
  const flashIdxRef = useRef<Set<number>>(new Set()) // entry indices to flash green (newly added)
  const flashTabRef = useRef('') // tab those indices belong to — the flash must not leak to other tabs
  const flashCopied = (idxs: number[]) => {
    if (!idxs.length) return
    setCopiedSet(new Set(idxs))
    setTimeout(() => setCopiedSet(new Set()), 1000)
  }
  const [tabModalId, setTabModalId] = useState<string | null>(null)
  // Per-tab "source unreachable" reason, set from the source_error event and
  // cleared once that tab loads configs again.
  const [srcErr, setSrcErr] = useState<Record<string, string>>({})
  const [sourcesOpen, setSourcesOpen] = useState(false)
  // Add / edit / view a config manually (the ConfigModal form). null = closed.
  const [configModal, setConfigModal] = useState<
    { mode: 'add' | 'edit' | 'view'; tabId: string; idx?: number; raw?: string } | null
  >(null)
  const [toast, setToast] = useState<{ text: string; fade: boolean } | null>(null)
  const [updBanner, setUpdBanner] = useState<{ latest: string; notes?: string } | null>(null)
  const [appInfo, setAppInfo] = useState<any>({ singbox_available: true, is_admin: true })
  const suppressDeltaRef = useRef(false) // one reload_delta toast is skipped after adding a sub
  const [activeSubs, setActiveSubs] = useState<any[]>([]) // sub-bar (main/active tab subs)
  const [logH, setLogH] = useState(() => Math.max(120, Math.round(window.innerHeight * 0.3)))
  const toastTimer = useRef<number>(0)
  // Compact tab strip (1.10 updateTabLayout): when every tab button would sit
  // on its own wrapped row, collapse to a dropdown trigger. The 'compact'
  // classes are managed directly on the DOM nodes (their React classNames are
  // static, so React never overwrites them).
  const tabBarRef = useRef<HTMLDivElement>(null)
  const toolbarRef = useRef<HTMLDivElement>(null)
  const [tabDD, setTabDD] = useState<{ x: number; y: number } | null>(null)
  // Type-filter dropdown anchor (null = closed).
  const [protoDD, setProtoDD] = useState<{ x: number; y: number } | null>(null)
  // Brief ✓ after copying the connected config from the conn-bar.
  const [connCopied, setConnCopied] = useState(false)
  // The session panel behind the mark on the connection bar.
  const [sessionOpen, setSessionOpen] = useState(false)
  const [winW, setWinW] = useState(0)
  // logs
  const [logsOpen, setLogsOpen] = useState(false)
  const [logLines, setLogLines] = useState<any[]>([])
  // Second, much smaller buffer holding ONLY the important lines (mirrors the
  // backend's own important ring). The main buffer is capped, so a verbose
  // engine flood pushes an error out of it within seconds; this one only ever
  // receives important lines, so the error is still there minutes later.
  const [impLines, setImpLines] = useState<any[]>([])
  const [logFind, setLogFind] = useState('') // free-text search in the Logs panel
  const [logSrc, setLogSrc] = useState('')
  const [logLvl, setLogLvl] = useState('')
  const [logAuto, setLogAuto] = useState(true)
  // Auto-scroll off means "hold still". The panel used to keep taking lines
  // while the user was reading one: the ring trims from the top, every row
  // shifts up, and the line being read walks off the screen — the whole reason
  // to switch auto-scroll off in the first place. Held lines wait here and go
  // in when the reader asks for them.
  const logHeld = useRef<any[]>([])
  const [logHeldN, setLogHeldN] = useState(0)
  const logAutoRef = useRef(true)
  // A monotonic id per line, so React can tell the rows apart. With an index
  // for a key every row's text changes when the window scrolls, and the whole
  // list is rewritten several times a second under a flood.
  const logSeq = useRef(0)
  // modals
  const [qrSrc, setQrSrc] = useState<string | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [stg, setStg] = useState<any>(null)
  const [version, setVersion] = useState('')
  const [lang, setLang] = useState('en') // 1.10 default; settings.language overrides
  // bulk test progress
  const [barPct, setBarPct] = useState(0)
  const [pingRunning, setPingRunning] = useState(false)
  const [speedRunning, setSpeedRunning] = useState(false)

  const tt = (en: string) => t10(lang, en) // exact 1.10 dict (EN keys)

  const twRef = useRef<HTMLDivElement>(null)
  const logViewRef = useRef<HTMLDivElement>(null)
  const cacheRef = useRef<Map<number, Row>>(new Map()) // position → Row (current tab)
  const pendingRef = useRef<Set<number>>(new Set()) // window starts in flight
  // No-blank windowing. Two rules kill the tab-switch flash without ever showing
  // another tab's rows:
  //  1. Rows on screen are NEVER discarded mid-view. Any same-tab change (sort,
  //     filter, reload, paste, delete, the loaded→reloadKey bump that follows a
  //     switch) keeps cacheRef as-is, marks every window stale (winFreshRef reset)
  //     and refetches over the visible rows in place.
  //  2. A real TAB switch stashes the outgoing tab's rows (+total) and restores
  //     the incoming tab's stash if present — instant paint — then refetches all
  //     of it (rule 1) so restored rows are corrected within one round-trip.
  // viewSeq bumps on every view-effect run; a window response is applied only if
  // its run is still current (stale in-flight responses are dropped, and their
  // pending mark is left alone — it belongs to the newer run by then).
  const winFreshRef = useRef<Set<number>>(new Set())
  const viewSeqRef = useRef(0)
  const stashRef = useRef<Map<string, { rows: Map<number, Row>; total: number }>>(new Map())
  const prevTabRef = useRef('')
  const totalRef = useRef(0)
  const [, bump] = useState(0)
  const connRef = useRef<any>(null)
  const activeTabRef = useRef('')
  const logsOpenRef = useRef(false)
  const selRef = useRef<Set<number>>(new Set())
  const selRawsRef = useRef<Map<number, string>>(new Map())
  const anchorRef = useRef<number | null>(null)
  const exitIdRef = useRef('')
  const bulkTabRef = useRef('')
  const selectAllRef = useRef<() => void>(() => {}) // keyboard Ctrl+A → current view
  const importRef = useRef<(text: string, toastAdded?: string) => void>(() => {}) // paste/deeplink → current closures
  const langRef = useRef('en')
  const filterRef = useRef('')
  const protosRef = useRef<string[]>([])
  const statsTimer = useRef<number>(0)
  const resortTimer = useRef<number>(0)
  const sortRef = useRef<Sort>('idx')
  connRef.current = conn
  activeTabRef.current = activeTab
  selRef.current = selected
  langRef.current = lang
  filterRef.current = filter
  protosRef.current = protos
  sortRef.current = sort
  totalRef.current = total

  // Live header counters during a test: refetch Stats on a 1s debounce so
  // ping ok / failed / best climb as results arrive. Stats is a full scan of
  // the tab (with filter), so on 100k+ tabs anything more frequent burns CPU.
  const scheduleStatsRefresh = () => {
    if (statsTimer.current) return
    statsTimer.current = window.setTimeout(() => {
      statsTimer.current = 0
      ConfigService.Stats(activeTabRef.current, filterRef.current, protosRef.current).then(setStats)
    }, 1000)
  }
  // Live RE-SORT during a ping/speed test: the order depends on results the test
  // is producing, but entry_update only patches each row in place (position
  // unchanged). When sorted by ping/speed, refetch the visible windows on a 1s
  // debounce so rows actually re-order as results arrive (the no-blank window
  // refetch keeps scroll + rows). idx sort never reorders on results → skipped.
  const scheduleResort = () => {
    if (resortTimer.current || (sortRef.current !== 'ping' && sortRef.current !== 'speed')) return
    resortTimer.current = window.setTimeout(() => {
      resortTimer.current = 0
      setReloadKey((k) => k + 1)
    }, 200) // 1.10 used 250ms; rows' values update instantly, only order catches up
  }

  // ── data loading ──────────────────────────────────────────────────
  // The window range last painted, so a re-sort knows which cached rows are
  // still worth keeping (see below).
  const visibleWinRef = useRef<[number, number] | null>(null)
  // Which view the rows in flight belong to. A re-sort is NOT a new view, and
  // treating it as one is what kept a speed-sorted table wrong until RELOAD:
  // every landing result bumped the order, every bump invalidated the window
  // fetch that was on its way back, and under a running AUTO the answers never
  // stopped being superseded. Only a real change of view — tab, sort, filter,
  // protocol — may discard rows that are already on their way.
  const viewKeyRef = useRef('')
  useEffect(() => {
    const viewKey = [activeTab, sort, filter, protos.join(',')].join('\u0000')
    if (viewKeyRef.current !== viewKey) {
      viewKeyRef.current = viewKey
      viewSeqRef.current++
    }
    if (prevTabRef.current !== activeTab) {
      // Real TAB switch: stash the outgoing tab's rows, restore the incoming
      // tab's if we have them (instant paint, corrected by the refetch below).
      if (prevTabRef.current)
        stashRef.current.set(prevTabRef.current, { rows: cacheRef.current, total: totalRef.current })
      const hit = stashRef.current.get(activeTab)
      stashRef.current.delete(activeTab)
      cacheRef.current = hit ? hit.rows : new Map()
      if (hit) setTotal(hit.total) // stashed count until the fresh Count lands
      while (stashRef.current.size > 8) {
        // Evict the least-recently-used tab (Map preserves insertion order).
        stashRef.current.delete(stashRef.current.keys().next().value as string)
      }
      prevTabRef.current = activeTab
    }
    // Same tab (sort / filter / reload / paste / delete / the loaded bump that
    // follows a switch): keep the rows on screen — mark every window stale and
    // refetch over them in place. Never blank mid-view.
    //
    // Rows OUTSIDE the viewport are dropped, though. The order is recomputed on
    // the backend for every result that lands, and only the visible windows are
    // refetched; keeping the rest meant the table was assembled from several
    // different orders at once. Sorted by speed under a running AUTO that shows
    // as a list sorted in pieces, with dead configs sitting in the middle of the
    // fast ones — each piece correct for the order it was fetched under. A row
    // that briefly is not there yet is better than a row that is wrong.
    if (prevTabRef.current === activeTab && visibleWinRef.current) {
      const [vs, ve] = visibleWinRef.current
      for (const pos of [...cacheRef.current.keys()]) {
        const w = Math.floor(pos / WINDOW) * WINDOW
        if (w < vs || w > ve) cacheRef.current.delete(pos)
      }
    }
    winFreshRef.current = new Set()
    pendingRef.current.clear()
    allIdxRef.current = null // full-order cache follows the view
    allPosRef.current = null
    ConfigService.Count(activeTab, filter, protos).then(setTotal)
    ConfigService.Count(activeTab, '', []).then(setTabTotal)
    ConfigService.Stats(activeTab, filter, protos).then(setStats)
    if (activeTab) refreshSubBar(activeTab)
    bump((v) => v + 1)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sort, filter, protos, activeTab, reloadKey])

  const virtualizer = useVirtualizer({
    count: total,
    getScrollElement: () => twRef.current,
    estimateSize: () => ROW_H,
    overscan: 14,
  })
  const items = virtualizer.getVirtualItems()

  // ── columns ───────────────────────────────────────────────────────
  // One list, and the browser does the arithmetic.
  //
  // Each column is a grid track. `minmax(a, b)` is the floor-plus-share we used
  // to compute by hand, `max-content` is "exactly what is inside" — neither can
  // be said to a <col>, which is why the table version needed a measuring pass
  // on every frame and shimmered while the window moved. Here the whole layout
  // is one string, read by the header and by every row, so they cannot drift
  // apart and there is nothing to keep in step.
  //
  // Floors are in ch — the width of a "0" in the current font — so they follow
  // the interface font-size setting instead of going stale with it. ACTIONS is
  // max-content: exactly its buttons, in either language, in either form.
  // The two flexible columns carry an fr and a floor in characters; the floor
  // is written in the stylesheet, where the narrow layout can lower it, and is
  // read back here so the track and the fit check never disagree.
  const COLSPEC: { cls: string; track: string; floor: number; fr?: number; floorVar?: string }[] = [
    { cls: 'ci', track: 'max-content', floor: 4 },
    { cls: 'cpr', track: 'max-content', floor: 8 },
    { cls: 'cn', track: '', floor: 15, fr: 1.6, floorVar: '--cn-floor' },
    { cls: 'ch', track: '', floor: 13, fr: 1.1, floorVar: '--ch-floor' },
    { cls: 'ct', track: 'max-content', floor: 10.5 },
    { cls: 'cs', track: 'max-content', floor: 10 },
    { cls: 'cp2', track: 'max-content', floor: 9.5 },
    { cls: 'cst', track: 'max-content', floor: 10.5 },
    { cls: 'csp', track: 'max-content', floor: 11.5 },
    { cls: 'ca', track: 'max-content', floor: 18 },
  ]
  // What is given up when the row stops fitting, least useful first. The
  // decision is taken from the row itself — does it overflow its box — not from
  // a window width: the same question the layout is already answering.
  // TYPE is last: on a phone it is the only thing left to give, and the old
  // stylesheet hid it there for the same reason.
  const DROP_ORDER = ['ci', 'ct', 'cs', 'ch', 'cpr']
  const [dropped, setDropped] = useState<Set<string>>(new Set())
  const actWordsRef = useRef(0)
  const natRef = useRef<Record<string, number>>({})
  const wRef = useRef<Record<string, number>>({})
  const sigRef = useRef('')
  const droppedRef = useRef(dropped)
  droppedRef.current = dropped

  useLayoutEffect(() => {
    // One pass changes one thing — a label form, or one column — and then asks
    // to be run again. A window dragged from wide to narrow in a single motion
    // therefore steps through the same sequence it would step through slowly,
    // instead of stopping at the first change and leaving the row overflowing.
    let pending = 0
    const schedule = () => {
      if (pending) return
      pending = window.setTimeout(() => {
        pending = 0
        write()
        fit()
      }, 16)
    }
    const cssNum = (name: string, dflt: number) => {
      const v = parseFloat(getComputedStyle(document.documentElement).getPropertyValue(name))
      return Number.isFinite(v) && v > 0 ? v : dflt
    }
    const write = () => {
      const el = document.querySelector('.tw') as HTMLElement | null
      if (!el) return
      const drop = droppedRef.current
      // What the stylesheet says the flexible columns may shrink to, and how
      // much room the action buttons keep. Both change with the layout.
      for (const c of COLSPEC) {
        if (c.floorVar) c.floor = cssNum(c.floorVar, c.floor)
      }
      // A fade only ever belongs to a column on its way out. If one is still
      // marked while its column is gone, clear it: a column that is present and
      // blank reads as a fault, and it is the one state this must never show.
      for (const cls of DROP_ORDER) {
        if (document.body.classList.contains('fade-' + cls) && drop.has(cls)) {
          document.body.classList.remove('fade-' + cls)
        }
      }
      // Each chip is one size for every row, and the size is the widest reading
      // it can hold — for speed the ten characters fmtSpeed can produce, for
      // ping four digits, and for both the word shown while the test is still
      // running. Measured in a chip of its own so the sizes follow the
      // font-size setting instead of numbers written here.
      {
        let sp = document.getElementById('sp-probe') as HTMLElement | null
        if (!sp) {
          sp = document.createElement('span')
          sp.id = 'sp-probe'
          sp.className = 'pill'
          sp.style.cssText =
            'position:absolute;visibility:hidden;pointer-events:none;top:0;left:0;' +
            'width:auto;min-width:0;max-width:none;white-space:nowrap'
          el.appendChild(sp)
        }
        const widest = (readings: string[]) => {
          let w = 0
          for (const reading of readings) {
            sp!.textContent = reading
            w = Math.max(w, sp!.getBoundingClientRect().width)
          }
          return Math.ceil(w) + 'px'
        }
        const root = document.documentElement.style
        root.setProperty('--speed-w', widest(['99.99 MB/s', 'connecting…']))
        root.setProperty('--ping-w', widest(['9999ms', 'pinging…', 'timeout']))
      }
      // Every column but NAME and HOST is as wide as the widest thing in it,
      // measured here and written to every row as a pixel count. It cannot be
      // left to max-content: each row is its own grid, so the rows would each
      // answer the question for themselves and stop lining up the moment one of
      // them held a longer word than the rest.
      // What is measured is the ink — the boxes the content actually draws —
      // and never the cell, which is as wide as the column already is and would
      // make each pass record what the pass before it had given away.
      const INK: Record<string, string> = {
        cpr: '.pb',
        ct: '.nb',
        cs: '.sb',
        cp2: '.vc > *',
        cst: '.vc > *',
        csp: '.vc > *',
        ca: '.act-cell > *',
      }
      const ink = (cell: HTMLElement, sel?: string) => {
        let l = Infinity
        let r = -Infinity
        if (sel) {
          const parts = cell.querySelectorAll(sel)
          // A cell holding nothing — no ping taken yet, no history — asks for
          // nothing. Measuring the cell instead is exactly what would feed a
          // column's own width back into itself.
          if (!parts.length) return 0
          parts.forEach((p) => {
            const e2 = p as HTMLElement
            const b = e2.getBoundingClientRect()
            if (!b.width) return
            l = Math.min(l, b.left)
            // What the thing is showing is not always what it has: a badge that
            // is already cut short would otherwise ask for exactly the width
            // that is cutting it, and the ellipsis would never come off. Unless
            // the size was fixed on purpose — the speed chip is one width and
            // an error inside it is meant to end in an ellipsis.
            // A ceiling in pixels, or --fixed, says the stylesheet chose this
            // size on purpose. A ceiling in per cent says only "do not spill
            // out of the cell", which is not a decision about how wide the
            // content is and must not stop the column from growing.
            const cs2 = getComputedStyle(e2)
            const cap = cs2.maxWidth
            const capped =
              (cap !== 'none' && !cap.endsWith('%')) ||
              cs2.getPropertyValue('--fixed').trim() === '1'
            r = Math.max(r, b.right + (capped ? 0 : Math.max(0, e2.scrollWidth - e2.clientWidth)))
          })
        } else {
          const range = document.createRange()
          range.selectNodeContents(cell)
          const b = range.getBoundingClientRect()
          range.detach()
          l = b.left
          r = b.right
        }
        if (!(r > l)) return 0
        const cs = getComputedStyle(cell)
        return Math.ceil(r - l + parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight)) + 2
      }
      // The widths are kept while the view is the same, so a column does not
      // change width under a scroll that brings a longer value into the window.
      // They are taken again when the view really changes: another tab, another
      // language, the buttons' labels, the row that is connected.
      // A row that is fully painted — the placeholder rows a tab switch puts up
      // for a moment have a number in them and nothing else. Measuring those
      // and publishing the answer is what made the table blink when a tab was
      // opened: every column briefly took the width of nothing.
      const real = !!document.querySelector('.gtable .gc.ca')
      const sig = [
        activeTab,
        lang,
        getComputedStyle(document.documentElement).getPropertyValue('--app-fs-base'),
        Array.from(drop).sort().join(','),
        document.body.classList.contains('acts-tight'),
        conn?.status ?? '',
        conn?.conn_raw ?? '',
      ].join('|')
      if (sig !== sigRef.current && real) {
        // The old widths stand until there is something to replace them with:
        // they are the last honest answer, and one view's columns are close
        // enough to the next one's that holding them for a frame is invisible.
        sigRef.current = sig
        wRef.current = {}
      }
      const want = (cls: string, px: number) => {
        if (px > (wRef.current[cls] ?? 0)) wRef.current[cls] = px
      }
      // A column has to hold its heading as well as its values: TRANSPORT,
      // SECURITY and STABILITY are all longer words than anything under them.
      // A heading is measured along its own flow — each part's width plus its
      // margins — rather than edge to edge. Both matter: the slot kept for the
      // sort arrow is empty, so a range over the text walks straight past it
      // and STABILITY came out nine pixels short; and on the centred columns
      // that same slot is pulled back out with a negative margin so the label
      // sits over its values, which an edge-to-edge reading would charge for
      // twice and push the label off centre again.
      const flowInk = (e: HTMLElement) => {
        if (!e.children.length) return ink(e)
        let w = 0
        for (const c of Array.from(e.children)) {
          const k = c as HTMLElement
          const cs = getComputedStyle(k)
          w += k.getBoundingClientRect().width + parseFloat(cs.marginLeft) + parseFloat(cs.marginRight)
        }
        const cs = getComputedStyle(e)
        return Math.ceil(w + parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight)) + 2
      }
      for (const th of Array.from(document.querySelectorAll('.thw .gh'))) {
        const e = th as HTMLElement
        want(e.className.replace('gh ', '').split(' ')[0], flowInk(e))
      }
      // The button says connect until it says disconnect, and the difference
      // between the two words is a column that changes width the moment a
      // connection opens — under the pointer that opened it. The room for the
      // longer word is kept from the start, and it is kept by measuring: the
      // word is swapped into a real button, the cell is read, and the word is
      // put back, all before anything is painted. Guessing the difference from
      // a text probe was two pixels out, which is a column that still moves.
      let caWidest = 0
      const caInk = (cell: HTMLElement) => {
        const own = ink(cell, INK.ca)
        if (!caWidest) {
          const lbl = Array.from(cell.querySelectorAll('.lbl-full, .lbl-short')).find(
            (e2) => (e2 as HTMLElement).getBoundingClientRect().width > 0,
          ) as HTMLElement | undefined
          if (!lbl) return own
          const btn = lbl.closest('button')
          if (!btn) return own
          // Both forms of the button, not just both words: the disconnect
          // button is a pixel wider on each side and does not letter-space its
          // label, so the word alone answers a slightly different question.
          const short = lbl.classList.contains('lbl-short')
          const forms = [
            { cls: 'btn sm ghost', word: short ? 'conn' : 'connect' },
            { cls: 'btn sm-disc', word: short ? 'disc' : 'disconnect' },
          ]
          const wore = btn.className
          const said = lbl.textContent
          const seen: number[] = []
          for (const form of forms) {
            btn.className = form.cls
            lbl.textContent = form.word
            seen.push(ink(cell, INK.ca))
          }
          btn.className = wore
          lbl.textContent = said
          // The buttons hang from the right-hand end of the column, so PING
          // and SPEED keep their place and the first button grows leftwards
          // into the room kept for the longer word. The gap on the right is
          // that same difference, which puts the shorter form — the one on
          // screen nearly all the time — squarely in the middle of the column.
          const gap = Math.max(seen[1] - seen[0], cssNum('--act-gap-min', 10))
          document.documentElement.style.setProperty('--act-gap', gap + 'px')
          caWidest = seen[0] + 2 * gap
        }
        return Math.max(own, caWidest)
      }
      if (real) {
        for (const cell of Array.from(document.querySelectorAll('.gtable .gc'))) {
          const e = cell as HTMLElement
          const cls = e.className.replace('gc ', '').split(' ')[0]
          if (cls === 'cn' || cls === 'ch') continue
          want(cls, cls === 'ca' ? caInk(e) : ink(e, INK[cls]))
        }
      }
      const cols = COLSPEC.filter((c) => !drop.has(c.cls))
        .map((c) =>
          c.fr
            ? 'minmax(' + c.floor + 'ch,' + c.fr + 'fr)'
            : (wRef.current[c.cls] ?? Math.ceil(c.floor * 7)) + 'px',
        )
        .join(' ')
      document.documentElement.style.setProperty('--cols', cols)
      // The heading row is a second grid in a second box. The two boxes are the
      // same width — the heading row is padded by the scrollbar's gutter — and
      // every track but NAME and HOST is now a pixel count, so the same list
      // resolves to the same numbers in both.
      document.documentElement.style.setProperty('--cols-head', cols)
      const row = document.querySelector('.gtable .grow') as HTMLElement | null
      if (real && row && row.children.length) {
        const widths = Array.from(row.children).map(
          (c) => (c as HTMLElement).getBoundingClientRect().width,
        )
        // What each column takes at its narrowest, remembered for the two
        // decisions below: whether the row still fits, and whether a column
        // that was given up can come back.
        const vis = COLSPEC.filter((c) => !drop.has(c.cls))
        vis.forEach((c, i) => {
          if (c.cls === 'cn' || c.cls === 'ch') return
          natRef.current[c.cls] = widths[i]
          // While the buttons wear their words, remember what that costs: it is
          // the threshold the check below uses to put the words back.
          if (c.cls === 'ca' && !document.body.classList.contains('acts-tight')) {
            actWordsRef.current = widths[i]
          }
        })
      }
      // The gutter the vertical scrollbar occupies, mirrored to the heading row
      // so the two grids resolve against the same width. Measured, because it
      // is a system metric — 8px at 100% scaling, more at 125%, zero with
      // overlay scrollbars.
      const gutter = Math.max(0, el.offsetWidth - el.clientWidth)
      document.documentElement.style.setProperty('--sbw', gutter + 'px')
    }

    // What one "0" is wide in the table's own font. The floors are written in
    // ch so they follow the font-size setting; this is what turns them into the
    // pixels the fit check adds up.
    const chPx = () => {
      let probe = document.getElementById('ch-probe') as HTMLElement | null
      const host = document.querySelector('.tw')
      if (!probe && host) {
        probe = document.createElement('div')
        probe.id = 'ch-probe'
        probe.style.cssText = 'position:absolute;visibility:hidden;pointer-events:none;width:10ch'
        host.appendChild(probe)
      }
      const w = probe ? probe.getBoundingClientRect().width / 10 : 7
      return w > 0 ? w : 7
    }
    const fit = () => {
      const el = document.querySelector('.tw') as HTMLElement | null
      const row = document.querySelector('.gtable .grow') as HTMLElement | null
      if (!el || !row) return
      const drop = new Set(droppedRef.current)
      const tight = document.body.classList.contains('acts-tight')
      const ch = chPx()
      // What the row needs at its narrowest: each content-sized column at the
      // width the browser just gave it — which for a max-content track IS its
      // content — plus the floors of the two that hold text. Read from the last
      // layout, never from the current one, so the answer cannot feed itself.
      const need =
        COLSPEC.filter((c) => !drop.has(c.cls)).reduce(
          (a, c) => a + (c.fr ? c.floor * ch : natRef.current[c.cls] ?? c.floor * ch),
          0,
        ) + 2
      const box = el.clientWidth
      if (need > box) {
        if (!tight) {
          document.body.classList.add('acts-tight')
          schedule()
          return
        }
        for (const cls of DROP_ORDER) {
          if (!drop.has(cls)) {
            // Fade first, remove a tenth of a second later. The column cannot
            // be animated away — its cells stop being rendered — but the moment
            // can be given an edge to be seen leaving by.
            document.body.classList.add('fade-' + cls)
            window.setTimeout(() => {
              const next = new Set(droppedRef.current)
              next.add(cls)
              setDropped(next)
              // The class comes off with the column, not a moment later: while
              // both are on, the column is present and blank.
              document.body.classList.remove('fade-' + cls)
              schedule()
            }, 110)
            return
          }
        }
        return
      }
      // Room to spare. Give back what was taken, in reverse order, and only
      // when what comes back still leaves the row inside its box.
      const spare = box - need
      for (const cls of [...DROP_ORDER].reverse()) {
        if (!drop.has(cls)) continue
        // What it took when it was last on screen, or its written floor if it
        // has not been seen yet.
        const back = natRef.current[cls] ?? (COLSPEC.find((c) => c.cls === cls)?.floor ?? 10) * ch
        if (spare < back + 24) return
        // Straight back, at full strength. Fading it up meant one frame with
        // the column there and nothing in it — which reads as a bug, not as a
        // transition, and is exactly what it looked like.
        document.body.classList.remove('fade-' + cls)
        drop.delete(cls)
        setDropped(drop)
        schedule()
        return
      }
      // Last to go is first to return: the words on the buttons. The width they
      // want is the one they had when they were words — remembered, so this is
      // not a guess either.
      const actPx = natRef.current['ca'] ?? 18 * ch
      if (tight && spare > (actWordsRef.current || 200) - actPx + 16) {
        document.body.classList.remove('acts-tight')
        schedule()
      }
    }
    write()
    fit()
    // Once more when the first rows have landed: the effect runs as soon as the
    // count is known, and at that moment the window may still hold placeholder
    // rows — which have no buttons to measure.
    const settle = window.setTimeout(() => {
      write()
      fit()
    }, 300)
    const ro = new ResizeObserver(schedule)
    ro.observe(document.documentElement)
    window.addEventListener('resize', schedule)
    return () => {
      ro.disconnect()
      window.removeEventListener('resize', schedule)
      window.clearTimeout(settle)
      if (pending) window.clearTimeout(pending)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  // conn is in the list because the connected row's button says "disconnect",
  // which is wider than "connect": the column resizes itself, but the heading
  // row is mirrored from the rows and has to be told to look again.
  // The font size is in the list because nothing else notices it: the window
  // has not changed size, so no resize arrives, and every width here is
  // measured in the font the setting just changed.
  }, [total, loading, activeTab, dropped, conn?.status, conn?.conn_raw, lang, stg?.app_font_size])

  useEffect(() => {
    if (items.length === 0) return
    const startW = Math.floor(items[0].index / WINDOW) * WINDOW
    const endW = Math.floor(items[items.length - 1].index / WINDOW) * WINDOW
    // Bind responses to THIS run: if the view moved on (another effect run bumped
    // viewSeq), the response is dropped — its rows may be from another tab or a
    // pre-reload data set. Its pending mark is left alone: after the newer run
    // cleared pendingRef, that mark belongs to the newer run's own fetch.
    const seq = viewSeqRef.current
    const targetCache = cacheRef.current
    visibleWinRef.current = [startW, endW]
    for (let w = startW; w <= endW; w += WINDOW) {
      // Fresh for this run? skip. Otherwise (re)fetch — stale rows stay rendered
      // until the fresh window overwrites them in place.
      if (winFreshRef.current.has(w) || pendingRef.current.has(w)) continue
      pendingRef.current.add(w)
      ConfigService.Window(activeTab, sort, filter, protos, w, WINDOW).then((rows) => {
        if (seq !== viewSeqRef.current) return // view moved on — drop stale response
        pendingRef.current.delete(w)
        const got = rows ?? []
        got.forEach((r, i) => targetCache.set(w + i, Object.assign(r as any, { _seq: seq })))
        // A window that comes back short is the end of the list — or a list that
        // has just become shorter. The positions it did not fill do not exist in
        // this view any more, and leaving the previous view's rows there is what
        // put phantom rows on the table: a configuration from a filtered list
        // sitting above the real ones, numbered as if it belonged, doing nothing
        // when clicked because its index was no longer in the tab.
        for (let i = got.length; i < WINDOW; i++) targetCache.delete(w + i)
        winFreshRef.current.add(w)
        bump((v) => v + 1)
      })
    }
  }, [items, total, sort, filter, protos, activeTab, reloadKey])

  // showToast — the 1.10 reload-toast: visible ~1.4s, then a .45s fade.
  const showToast = (text: string) => {
    if (toastTimer.current) window.clearTimeout(toastTimer.current)
    setToast({ text, fade: false })
    toastTimer.current = window.setTimeout(() => {
      setToast((t) => (t ? { ...t, fade: true } : t))
      toastTimer.current = window.setTimeout(() => setToast(null), 460)
    }, 1400)
  }

  const tabsRef = useRef<any[]>([])
  tabsRef.current = tabs
  // dedupKeyRef tracks "<tabId>:<dedup_mode>" of the active tab; flipping the
  // dedup seg in Tab Settings changes the visible set server-side, so the
  // table must re-query (1.10 onTabsUpdate dedupFlipped).
  const dedupKeyRef = useRef('')
  const refreshTabs = () =>
    TabService.List().then((ts) => {
      const list = ts ?? []
      setTabs(list)
      const act = list.find((t) => t.id === activeTabRef.current)
      if (act) {
        const key = act.id + ':' + (act.dedup_mode || '')
        if (dedupKeyRef.current.startsWith(act.id + ':') && dedupKeyRef.current !== key)
          setReloadKey((k) => k + 1)
        dedupKeyRef.current = key
      }
    })
  // Sub-bar above the table: the active tab's subscription info (1.10
  // updateSubBar). TabDTO doesn't carry subs — pull the full tab on change.
  const refreshSubBar = (id: string) => {
    if (!id) return
    TabService.Detail(id).then((t: any) =>
      setActiveSubs(((t?.subs || []) as any[]).filter((s) => s.title || s.total > 0 || s.expire > 0)),
    )
  }
  // startTabDrag — the 1.10 mouse-based tab reorder (HTML5 DnD is unreliable
  // inside the webview). A >5px move enters drag mode; drop over another tab
  // reorders; a plain click still switches (the click event fires untouched).
  // suppressTabClickRef: after a real drag, swallow the click that mouseup fires
  // so releasing on the source tab doesn't also "switch" (harmless) — mainly it
  // stops a drag-that-ended-on-the-same-tab from feeling like a mis-click.
  const suppressTabClickRef = useRef(false)

  // ── long-press: the touch stand-in for right-click ──────────────────
  // A phone has no context menu gesture of its own, so the tab menu (Settings,
  // Delete tab) was unreachable there. Holding a tab for half a second opens
  // the same menu at the finger. Only touch pointers arm it — a mouse still
  // uses the real right-click, and holding the button down to drag a tab must
  // not pop a menu. Any real movement cancels it so a drag stays a drag.
  const lpTimer = useRef<number | null>(null)
  const lpFrom = useRef<{ x: number; y: number } | null>(null)
  // Set when a long press opened a menu. The click that arrives on release must
  // be swallowed, or the same gesture would also switch tab / select the row
  // behind the menu it just opened.
  const lpFired = useRef(false)
  const consumeLongPress = () => {
    if (!lpFired.current) return false
    lpFired.current = false
    return true
  }
  const cancelLongPress = () => {
    if (lpTimer.current !== null) {
      clearTimeout(lpTimer.current)
      lpTimer.current = null
    }
    lpFrom.current = null
  }
  const beginLongPress = (e: React.PointerEvent, fire: (x: number, y: number) => void) => {
    if (e.pointerType !== 'touch') return
    cancelLongPress()
    lpFired.current = false // a fresh gesture never inherits the last one's flag
    const x = e.clientX
    const y = e.clientY
    lpFrom.current = { x, y }
    lpTimer.current = window.setTimeout(() => {
      lpTimer.current = null
      lpFrom.current = null
      lpFired.current = true
      fire(x, y)
    }, 500)
  }
  const moveLongPress = (e: React.PointerEvent) => {
    const from = lpFrom.current
    if (!from) return
    if (Math.abs(e.clientX - from.x) > 8 || Math.abs(e.clientY - from.y) > 8) cancelLongPress()
  }
  // Attached to anything that arms a long press, so the timer can never outlive
  // the gesture that started it.
  const longPressCancelProps = {
    onPointerMove: moveLongPress,
    onPointerUp: cancelLongPress,
    onPointerCancel: cancelLongPress,
    onPointerLeave: cancelLongPress,
  }

  const startTabDrag = (e: React.PointerEvent, dragId: string) => {
    // Pointer capture routes every move/up to this element even when the cursor
    // leaves it — far more reliable than document mouse listeners inside WebView2
    // (where the Wails drag runtime and lost mouse-capture can eat the events).
    const el = e.currentTarget as HTMLElement
    const startX = e.clientX
    const startY = e.clientY
    let moved = false
    try {
      el.setPointerCapture(e.pointerId)
    } catch {
      /* ignore */
    }
    const clearMarks = () =>
      document.querySelectorAll('.tab-btn').forEach((tb) => tb.classList.remove('dragging', 'drag-over'))
    const tabUnder = (x: number, y: number): HTMLElement | null =>
      (document.elementFromPoint(x, y) as HTMLElement | null)?.closest('.tab-btn') as HTMLElement | null
    const onMove = (me: PointerEvent) => {
      if (!moved && (Math.abs(me.clientX - startX) > 4 || Math.abs(me.clientY - startY) > 4)) {
        moved = true
        el.classList.add('dragging')
      }
      if (!moved) return
      document.querySelectorAll('.tab-btn').forEach((tb) => tb.classList.remove('drag-over'))
      const tbtn = tabUnder(me.clientX, me.clientY)
      if (tbtn && tbtn.dataset.id !== dragId) tbtn.classList.add('drag-over')
    }
    const onUp = (ue: PointerEvent) => {
      el.removeEventListener('pointermove', onMove)
      el.removeEventListener('pointerup', onUp)
      el.removeEventListener('pointercancel', onUp)
      try {
        el.releasePointerCapture(ue.pointerId)
      } catch {
        /* ignore */
      }
      clearMarks()
      if (!moved) return
      suppressTabClickRef.current = true
      const tbtn = tabUnder(ue.clientX, ue.clientY)
      const toId = tbtn?.dataset.id
      if (!toId || toId === dragId) return
      const ids = tabsRef.current.map((t) => t.id)
      const fi = ids.indexOf(dragId)
      const ti = ids.indexOf(toId)
      if (fi < 0 || ti < 0) return
      ids.splice(fi, 1)
      ids.splice(ti, 0, dragId)
      setTabs((prev) => ids.map((id) => prev.find((t) => t.id === id)!).filter(Boolean))
      TabService.Reorder(ids)
    }
    el.addEventListener('pointermove', onMove)
    el.addEventListener('pointerup', onUp)
    el.addEventListener('pointercancel', onUp)
  }

  const switchTab = (id: string) => {
    // Update the ref IMMEDIATELY (not on next render) — event handlers
    // (loading/loaded/entry_update) tab-filter against it and must never see
    // the previous tab after the user clicked.
    activeTabRef.current = id
    setActiveTab(id)
    setLoading(null)
    // The backend answers whether a fetch is in flight for the target tab
    // RIGHT NOW (read under its lock) — the spinner follows that truth, not a
    // possibly-stale cached flag.
    TabService.Switch(id).then((fetching) => {
      if (fetching && activeTabRef.current === id) setLoading({})
    })
  }
  const createTab = async () => {
    const nt = await TabService.Create()
    // The list, read back rather than assumed: the tab to switch to is the one
    // that was just made, and if the call's answer cannot be read for any
    // reason it is still the last one in the list. Without an id nothing
    // switched, and the click looked like it had done nothing — a new tab
    // appeared in the bar (that arrives on its own event) while the table went
    // on showing the tab being left.
    const list = (await TabService.List()) ?? []
    setTabs(list)
    const id = (nt as any)?.id || list[list.length - 1]?.id
    if (!id) return
    // A tab that has just been made holds nothing, and saying so here is what
    // makes it look made: the counts still describe the tab being left, and
    // until the new ones come back the view falls through to a table with no
    // rows in it — an empty rectangle where the four ways to fill the tab
    // should be.
    setTotal(0)
    setTabTotal(0)
    switchTab(id)
  }

  // ── row helpers (exact buildRow logic) ──────────────────────────────
  const connLit = (r: Row): boolean => {
    const cs = conn
    if (!cs || (cs.status !== 'connected' && cs.status !== 'connecting')) return false
    const chainRaws: string[] = cs.chain_raws || []
    if (chainRaws.length && r.raw && chainRaws.some((h) => rawKey(h) === rawKey(r.raw))) return true
    if (cs.conn_raw) return rawKey(cs.conn_raw) === rawKey(r.raw)
    return cs.conn_tab === activeTab && cs.entry_index === r.index
  }
  const findRowByIndex = (idx: number): Row | null => {
    for (const row of cacheRef.current.values()) if (row.index === idx) return row
    return null
  }
  // Cloudflare WARP: enrol and drop the result into the tab as a WireGuard
  // config. A network round trip, so the button says it is working — enrolment
  // is normally under a second but goes through the tunnel when one is up.
  const [warpBusy, setWarpBusy] = useState(false)
  const addWARP = () => {
    if (warpBusy) return
    setWarpBusy(true)
    ConfigService.AddWARP(activeTabRef.current)
      .then((err: string) => showToast(err || tt('WARP added')))
      .catch((e: any) => showToast(String(e)))
      .finally(() => setWarpBusy(false))
  }

  // Manual add / edit / view config (ConfigModal). Add + edit are user-tab only;
  // Sources rows open read-only View (upstream configs shouldn't be edited).
  const openConfigAdd = () => {
    if (activeTab === 'main') return
    setMenu(null)
    setConfigModal({ mode: 'add', tabId: activeTab })
  }
  const openConfigEditView = (idx: number, view: boolean) => {
    const row = findRowByIndex(idx)
    if (!row) return
    setMenu(null)
    setConfigModal({ mode: view ? 'view' : 'edit', tabId: activeTab, idx, raw: row.raw })
  }
  // dropStashForTab evicts a tab's stashed rows so a background data change
  // can't resurface stale rows when the user switches back to that tab.
  const dropStashForTab = (tab: string) => {
    stashRef.current.delete(tab)
  }
  const findConnIdx = (): number => {
    const cs = connRef.current
    if (!cs || !cs.conn_raw) return -1
    for (const row of cacheRef.current.values())
      if (rawKey(row.raw) === rawKey(cs.conn_raw)) return row.index
    return -1
  }

  // allIdx/allPos cache the FULL ordered index list of the current view (every
  // matching row, not just loaded windows) — shift-range / select-all / chain
  // ordering use it. Dropped whenever the view changes (see the count effect).
  const allIdxRef = useRef<number[] | null>(null)
  const allPosRef = useRef<Map<number, number> | null>(null)
  const ensureAllIndices = (cb: (idxList: number[]) => void) => {
    if (allIdxRef.current) {
      cb(allIdxRef.current)
      return
    }
    ConfigService.Indices(activeTab, sort, filter, protos)
      .then((idx) => {
        const list = idx ?? []
        allIdxRef.current = list
        allPosRef.current = new Map(list.map((ix, i) => [ix, i]))
        cb(list)
      })
      .catch(() => cb(allIdxRef.current || []))
  }

  const toggleRowSelect = (idx: number, e: React.MouseEvent) => {
    const r = findRowByIndex(idx)
    if (e.shiftKey && anchorRef.current !== null && selRef.current.size > 0) {
      // Range select over the FULL ordered set (1.10 semantics): a span
      // crossing windows the client never loaded still selects every row in
      // between; their raws are fetched so copy is complete.
      const anchor = anchorRef.current
      ensureAllIndices((idxList) => {
        const pos = allPosRef.current
        const from = pos?.get(anchor)
        const to = pos?.get(idx)
        if (from === undefined || to === undefined) {
          setSelected((prev) => {
            const n = new Set(prev)
            n.add(idx)
            if (r) selRawsRef.current.set(idx, r.raw)
            return n
          })
          return
        }
        const lo = Math.min(from, to)
        const hi = Math.max(from, to)
        const range = idxList.slice(lo, hi + 1)
        setSelected((prev) => {
          const n = new Set(prev)
          for (const ix of range) n.add(ix)
          return n
        })
        // Pull raws for copy in the background (only the ones we don't hold).
        const need = range.filter((ix) => !selRawsRef.current.has(ix))
        if (need.length)
          ConfigService.RawsFor(activeTab, need).then((raws) => {
            ;(raws ?? []).forEach((raw, i) => {
              if (raw) selRawsRef.current.set(need[i], raw)
            })
          })
      })
      anchorRef.current = idx
      return
    }
    setSelected((prev) => {
      const n = new Set(prev)
      if (e.ctrlKey || e.metaKey) {
        if (n.has(idx)) {
          n.delete(idx)
          selRawsRef.current.delete(idx)
        } else {
          n.add(idx)
          if (r) selRawsRef.current.set(idx, r.raw)
        }
      } else {
        if (n.has(idx) && n.size === 1) {
          n.clear()
          selRawsRef.current.clear()
        } else {
          n.clear()
          selRawsRef.current.clear()
          n.add(idx)
          if (r) selRawsRef.current.set(idx, r.raw)
        }
      }
      anchorRef.current = idx
      return n
    })
  }
  const clearSelection = () => {
    setSelected(new Set())
    selRawsRef.current.clear()
  }

  // ── context-menu actions ──────────────────────────────────────────
  // copySelected copies every selected config's raw, in on-screen (view) order
  // so the clipboard matches what the user sees.
  const copySelected = () => {
    const pos = allPosRef.current
    const idxs = [...selRef.current]
    if (pos) idxs.sort((a, b) => (pos.get(a) ?? 0) - (pos.get(b) ?? 0))
    const raws = idxs.map((i) => selRawsRef.current.get(i)).filter(Boolean)
    if (raws.length) {
      copyText(raws.join('\n'))
      flashCopied(idxs)
      showToast(idxs.length > 1 ? tt('Copied') + ' ' + idxs.length : tt('Copied'))
    }
  }
  // selectAllInView selects every row of the current filtered/sorted view
  // (the whole set, not just loaded windows) with raws for copy.
  const selectAllInView = () => {
    ConfigService.RawsAll(activeTab, sort, filter, protos).then((d: any) => {
      const idx: number[] = d?.idx ?? []
      const raw: string[] = d?.raw ?? []
      selRawsRef.current.clear()
      idx.forEach((ix, i) => {
        if (raw[i]) selRawsRef.current.set(ix, raw[i])
      })
      setSelected(new Set(idx))
    })
  }
  selectAllRef.current = selectAllInView
  // connectChain connects the selection as a chain, in the order it was PICKED:
  // the first config clicked is the entry hop, the last is the exit whose
  // address sites see. Screen order was wrong for this — the table re-sorts
  // itself as results arrive, so the same two clicks built a different chain
  // depending on what the pings did in between.
  const connectChain = () => {
    const ordered = [...selRef.current]
    if (ordered.length < 2) return
    ConnService.ConnectChain(ordered, selectedMode).then((err) => {
      if (err) showToast(err)
    })
  }
  const testSelected = (idx: number, withSpeed: boolean) => {
    const idxs = selRef.current.size > 0 ? [...selRef.current] : [idx]
    if (withSpeed) TestService.SpeedSelected(idxs)
    else TestService.PingSelected(idxs)
  }
  const deleteSelectedRows = (idx?: number) => {
    const idxs = selRef.current.size > 0 ? [...selRef.current] : idx !== undefined ? [idx] : []
    if (idxs.length === 0) return
    ConfigService.DeleteEntries(idxs)
    clearSelection()
  }
  const showQR = async (idx: number) => setQrSrc((await QRService.ForConfig(idx)) || null)
  const showQRText = async (text: string) => setQrSrc((await QRService.ForText(text)) || null)
  const startRename = (idx: number) => {
    const r = findRowByIndex(idx)
    setEditingIdx(idx)
    setEditName(r?.name || '')
  }
  const commitRename = () => {
    if (editingIdx !== null && editName.trim()) ConfigService.RenameEntry(editingIdx, editName.trim())
    setEditingIdx(null)
  }
  // addSubsToTabSource appends one or more subscription URLs to a tab's
  // persistent sources (the server re-fetches each), sequentially, and reports a
  // combined toast — 1.10 addSubToTabSource, generalised to a list.
  const addSubsToTabSource = async (tabId: string, urls: string[]) => {
    if (urls.length === 0) return
    // The adds trigger re-fetches whose "+N" delta toasts would stack on ours.
    suppressDeltaRef.current = true
    setTimeout(() => (suppressDeltaRef.current = false), 8000)
    let added = 0
    for (const u of urls) {
      try {
        if (await TabService.AddSourceURL(tabId, u)) added++
      } catch {
        /* keep going — one bad link shouldn't abort the rest */
      }
    }
    if (urls.length === 1) {
      showToast(added ? tt('Subscription added to tab sources') : tt('Subscription already in tab sources'))
    } else {
      showToast(`${tt('Sources added')}: ${added} / ${urls.length}`)
    }
  }
  // pasteConfigs streams the raw text to Go in ≤40MB line-aligned chunks (Wails
  // caps a binding call at 64MB). BeginPaste pins the target tab up front (tab
  // switches mid-paste can't reroute the data) and shows the spinner; EndPaste
  // does ONE SQLite write for the whole paste.
  const PASTE_CHUNK = 40 * 1024 * 1024
  const pasteConfigs = async (text: string) => {
    const tabId = await ConfigService.BeginPaste()
    try {
      let start = 0
      while (start < text.length) {
        let end = Math.min(start + PASTE_CHUNK, text.length)
        if (end < text.length) {
          const nl = text.lastIndexOf('\n', end)
          if (nl > start) end = nl + 1 // don't split a config line
        }
        await ConfigService.PasteChunk(tabId, text.slice(start, end))
        start = end
      }
    } finally {
      await ConfigService.EndPaste(tabId)
    }
  }
  // importPayload routes a paste / QR / deep-link payload: a subscription URL
  // becomes a tab source; configs are pasted (a new tab is created on Sources).
  const importPayload = (text: string, toastAdded?: string) => {
    const trimmed = (text || '').trim()
    if (!trimmed) return
    // One OR MANY bare links → tab sources; anything else is config text.
    const subs = subURLsIn(trimmed)
    const isSub = subs.length > 0
    const run = (tabId: string) => {
      if (isSub) {
        addSubsToTabSource(tabId, subs)
        if (subURLsHadExtra(trimmed))
          showToast(tt('Added the links; anything written after them on the line was ignored.'))
        return
      }
      pasteConfigs(text).then(() => {
        refreshTabs()
        TabService.Active().then(setActiveTab)
        setReloadKey((k) => k + 1)
        if (toastAdded) showToast(toastAdded)
      })
    }
    if (isSub && activeTabRef.current === 'main') {
      // Subscriptions can't live on Sources — create a user tab first.
      TabService.Create().then((nt) => {
        TabService.Switch(nt.id)
        setActiveTab(nt.id)
        run(nt.id)
      })
      return
    }
    run(activeTabRef.current)
  }
  importRef.current = importPayload
  const pasteFromClipboard = () => {
    // Read the clipboard NATIVELY (Win32 via Wails) — navigator.clipboard.readText()
    // reads WebView2's cache, which can hand back a value copied earlier instead of
    // what was just copied in another app. Fall back to the async API only if the
    // native read comes back empty.
    ClipboardService.Text()
      .then((text) => {
        if (pasteworthy(text)) {
          importPayload(text)
          return
        }
        navigator.clipboard
          ?.readText?.()
          .then((t) => {
            if (pasteworthy(t)) importPayload(t)
          })
          .catch(() => {})
      })
      .catch(() => {})
  }
  // qrToActiveTab imports a scanned QR payload (1.10 port).
  const qrToActiveTab = (text: string) => {
    text = (text || '').trim()
    if (!text) {
      showToast(tt('No QR code found'))
      return
    }
    if (!pasteworthy(text)) {
      showToast(tt('QR is not a config or subscription'))
      return
    }
    importPayload(text, tt('Added from QR'))
  }
  const scanQRFile = () => {
    QRService.ScanFile().then((r: any) => {
      if (r?.error) showToast(tt('Could not read a QR from that image'))
      else qrToActiveTab(r?.text || '')
    })
  }
  const scanQRScreen = () => {
    showToast(tt('Scanning screen…'))
    QRService.ScanScreen().then((r: any) => {
      if (r?.error) showToast(tt('No QR code found on screen'))
      else qrToActiveTab(r?.text || '')
    })
  }

  // acquirePrivilege gets whatever TUN mode is missing: elevation on Windows,
  // the helper service on Linux. Windows never comes back from a successful
  // call — the elevated copy takes over — while Linux does, so capabilities are
  // re-read afterwards or the chip would keep offering an install that has
  // already happened.
  const acquirePrivilege = () => {
    // From a phone there is nothing to acquire: the elevation prompt, and the
    // relaunched copy behind it, are on the computer. Doing it anyway would
    // restart the program under the user's nose and drop this browser session
    // with no explanation, so it says where the click has to happen instead.
    if (IS_REMOTE) {
      showToast(tt('This has to be done on the computer itself — it asks for permission there.'))
      return
    }
    ConnService.RestartAdmin().then((why: string) => {
      if (why) {
        showToast(why)
        return
      }
      ConnService.AppInfo().then(setAppInfo)
    })
  }

  // ── conn-bar chips ────────────────────────────────────────────────
  const rePingConn = () => {
    const idx = findConnIdx()
    if (idx >= 0) TestService.PingOne(idx)
    else TestService.PingConnected() // connected config lives on another tab
  }
  const doCheckExit = () => {
    if (!connRef.current || connRef.current.status !== 'connected') return
    if (exit?.cls === 'testing') return
    setExit(exitChecking(tt))
    checkExit(tt).then(setExit)
  }

  // ── settings modal (instant apply, as in 1.10 — no Save button) ────
  const applyTheme = (th?: string) => document.body.classList.toggle('theme-light', th === 'light')
  // One class on <body> rather than a prop threaded through four dialogs: every
  // hint is already a .modal-hint, so hiding them is a stylesheet's job and the
  // setting reaches the tab, settings and auto dialogs — including the
  // standalone auto window — without any of them knowing about it. The state
  // beside it is for the descriptions that can be opened again: those are drawn
  // by <Hint>, and a stylesheet cannot put a button where a paragraph was.
  const applyHints = (hide?: boolean) => {
    setHideHints(!!hide)
    document.body.classList.toggle('no-hints', !!hide)
  }
  const openSettings = () => {
    SettingsService.Get().then(setStg)
    setSettingsOpen(true)
  }
  const refreshStg = () => SettingsService.Get().then(setStg)
  const applySettings = (patch: Record<string, any>) => {
    if (!stg) return
    const merged = { ...stg, ...patch }
    setStg(merged)
    if ('theme' in patch) applyTheme(patch.theme)
    if ('language' in patch) setLang(patch.language || 'en')
    if ('modal_font_size' in patch)
      setFontVar('--modal-fs-base', patch.modal_font_size || 12)
    if ('app_font_size' in patch) setFontVar('--app-fs-base', patch.app_font_size || 13)
    if ('hide_hints' in patch) applyHints(patch.hide_hints)
    SettingsService.Set(merged).then((applied: any) => {
      setStg(applied)
      setAuto(!!applied?.auto_connect)
    })
  }
  // Choosing a mode is a decision, so it is written down. AUTO's "remember
  // last" reads it: without that, the pill said proxy and AUTO connected in TUN
  // because that was the mode of some earlier session.
  //
  // Through a setter that touches ONLY this field. Sending the whole settings
  // object — as this did — writes every other field as well, and a click before
  // the window had finished loading them sent an object full of zeroes: every
  // setting reset, and the firewall block released because the switch holding
  // it had apparently just been turned off.
  const pickMode = (m: 'proxy' | 'tun') => {
    setSelectedMode(m)
    SettingsService.SetSelectedMode(m)
    setStg((p: any) => (p ? { ...p, selected_mode: m } : p))
  }
  const setAutoEnabled = (on: boolean) => {
    setAuto(on)
    AutoService.SetEnabled(on)
  }

  // ── logs ──────────────────────────────────────────────────────────
  // tagLines stamps each line with an id of its own; see logSeq.
  const tagLines = (ls: any[]) => ls.map((l) => ({ ...l, _id: logSeq.current++ }))
  const openLogs = () => {
    logsOpenRef.current = true
    setLogsOpen(true)
    logHeld.current = []
    setLogHeldN(0)
    LogService.Get().then((ls) => setLogLines(tagLines(ls ?? [])))
    LogService.GetImportant().then((ls) => setImpLines(tagLines(ls ?? [])))
  }
  // releaseHeld puts the lines that arrived while the view was held into the
  // panel — on demand, or when auto-scroll comes back on.
  const releaseHeld = () => {
    const held = logHeld.current
    logHeld.current = []
    setLogHeldN(0)
    if (!held.length) return
    setLogLines((prev) => [...prev, ...held].slice(-2000))
    const imp = held.filter(isImp)
    if (imp.length) setImpLines((prev) => [...prev, ...imp].slice(-500))
  }
  const setAutoScroll = (on: boolean) => {
    logAutoRef.current = on
    setLogAuto(on)
    if (on) releaseHeld()
  }
  const closeLogs = () => {
    logsOpenRef.current = false
    setLogsOpen(false)
  }
  const logPass = (l: any): boolean => {
    if (logSrc && l.src !== logSrc) return false
    if (logFind && !(l.msg || '').toLowerCase().includes(logFind.toLowerCase())) return false
    if (!logLvl) return true // "all"
    // "important" = everything that is NOT an engine [Info] line. That keeps the
    // warnings and errors, Vair's own messages (connect, refresh, AUTO) AND the
    // engines' access lines ("… accepted … [http -> proxy]"), which say which
    // destination was requested and which outbound took it — the genuinely
    // useful part. Only xray/sing-box's [Info] narration is dropped.
    if (logLvl === 'important') return isImp(l)
    // Untagged engine lines ("… accepted … [http -> proxy]") rank BELOW info:
    // they used to share info's rank, which made "info+" identical to "all".
    const rank: any = { raw: 0, info: 1, warn: 2, error: 3 }
    const need: any = { info: 1, warn: 2, error: 3 }
    return (rank[l.lvl] ?? 0) >= (need[logLvl] ?? 0)
  }
  // In "important" mode the panel reads the dedicated buffer instead of the
  // main one, so the lines survive an engine flood. Every other mode filters
  // the main buffer as before.
  const logBase = logLvl === 'important' ? impLines : logLines
  const logShown = logBase.filter(logPass)
  const copyLogs = () => {
    const text = logShown
      .map((l) => fmtLogTime(l.t) + ' ' + (l.src || '') + ' ' + l.msg)
      .join('\n')
    navigator.clipboard.writeText(text).catch(() => {})
  }
  useEffect(() => {
    if (logAuto && logViewRef.current) logViewRef.current.scrollTop = logViewRef.current.scrollHeight
  }, [logLines, impLines, logAuto])

  // ── mount: initial state + events ─────────────────────────────────
  useEffect(() => {
    ConnService.State().then(setConn)
    ConnService.AppInfo().then(setAppInfo)
    AutoService.Enabled().then(setAuto)
    SettingsService.Version().then(setVersion)
    SettingsService.Get().then((s: any) => {
      setStg(s)
      if (STANDALONE_AUTO) document.body.classList.add('auto-standalone')
      applyTheme(s?.theme)
      applyHints(s?.hide_hints)
      if (s?.language) setLang(s.language)
      if (s?.last_connected_raw) setLastRaw(s.last_connected_raw)
      // The pill the user last chose, falling back to the mode of the last
      // session for anyone upgrading into this.
      const startMode = s?.selected_mode || s?.last_connected_mode
      if (startMode === 'tun') setSelectedMode('tun')
      setFontVar('--modal-fs-base', s?.modal_font_size || 12)
      setFontVar('--app-fs-base', s?.app_font_size || 13)
    })
    refreshTabs()
    TabService.Active().then(setActiveTab)
    // vair:// link the app was LAUNCHED with (pull-based — no startup race).
    if (!STANDALONE_AUTO)
      SettingsService.TakePendingDeepLink().then((dl) => {
        if (dl && pasteworthy(dl)) importRef.current(dl, t10(langRef.current, 'Added from link'))
      })

    const offs = [
      Events.On('conn_update', (e: any) => {
        const cs = e?.data?.payload
        const prev = connRef.current
        if (!cs || cs.status !== 'connected' || cs.conn_raw !== prev?.conn_raw) setConnPing(null)
        setConn(cs)
        if (cs?.status === 'connected') {
          if (cs.chain_raws?.length) setLastRaw(cs.chain_raws[cs.chain_raws.length - 1])
          else if (cs.conn_raw) setLastRaw(cs.conn_raw)
          // reset the exit chip when the connection identity changes
          const idNow =
            (cs.chain_raws?.length ? cs.chain_raws.join(',') : cs.conn_raw || '') + '|' + cs.mode
          if (exitIdRef.current !== idNow) {
            exitIdRef.current = idNow
            setExit(null)
          }
        } else {
          exitIdRef.current = ''
          setExit(null)
        }
      }),
      Events.On('auto_update', (e: any) => {
        const p = e?.data?.payload
        if (!p) return
        setAutoLast(p)
        setAuto(!!p.enabled)
      }),
      Events.On('tabs_update', () => {
        refreshTabs()
        refreshSubBar(activeTabRef.current)
      }),
      // vair:// deep link → same routing as a paste (1.10 handleDeepLinkPayload):
      // a subscription becomes a tab source, configs are pasted; on Sources a
      // new user tab is created first (importPayload handles both).
      Events.On('deeplink', (e: any) => {
        const payload = ((e?.data ?? '') + '').trim()
        if (!payload) return
        if (!pasteworthy(payload)) {
          showToast(t10(langRef.current, 'Link has no config or subscription'))
          return
        }
        importRef.current(payload, t10(langRef.current, 'Added from link'))
      }),
      // Settings changed — possibly in ANOTHER window. The detached AUTO panel
      // is a second copy of this frontend and used to read the settings once,
      // at startup, so switching the language in the main window left it in the
      // old one. Presentation only: the language, theme, hint density and font
      // size, plus the settings object the dialogs read.
      Events.On('settings_update', (e: any) => {
        const s = e?.data?.payload
        if (!s) return
        setStg(s)
        setLang(s.language || 'en')
        applyTheme(s.theme)
        applyHints(s.hide_hints)
        setFontVar('--modal-fs-base', s.modal_font_size || 12)
        setFontVar('--app-fs-base', s.app_font_size || 13)
      }),
      Events.On('active_tab', (e: any) => {
        const id = e?.data?.payload
        if (!id) return
        activeTabRef.current = id // immediate — see switchTab
        setActiveTab(id)
        clearSelection()
        if (bulkTabRef.current !== id) setBarPct(0)
        // Clear any stale spinner; if the tab IS fetching, SwitchTab broadcasts
        // a tab-tagged 'loading' right after this event which re-shows it.
        setLoading(null)
        setReloadKey((k) => k + 1)
      }),
      Events.On('loading', (e: any) => {
        if (e?.data?.tab && e.data.tab !== activeTabRef.current) return
        setLoading({ op: e?.data?.payload?.op })
      }),
      Events.On('loaded', (e: any) => {
        // A tab that just loaded configs is no longer in the "source unreachable"
        // state (a failed refresh re-raises source_error right after).
        const lt = e?.data?.tab
        if (lt)
          setSrcErr((m) => {
            if (m[lt] === undefined) return m
            const next = { ...m }
            delete next[lt]
            return next
          })
        if (e?.data?.tab && e.data.tab !== activeTabRef.current) {
          // A background refresh changed a NON-active tab (e.g. its sources): drop
          // that tab's stashed windows so switching to it refetches, never paints
          // stale cached rows.
          dropStashForTab(e.data.tab)
          refreshTabs()
          return
        }
        setLoading(null)
        setReloadKey((k) => k + 1)
        refreshTabs()
      }),
      Events.On('stats_update', (e: any) => setTraffic(e?.data?.payload)),
      // "+N −M" toast after a reload recomputes the tab's config set. Tab-scoped:
      // background refreshes of OTHER tabs must not toast over the current one.
      // a file was dropped onto the window → refresh the view + summary toast
      Events.On('dropped', (e: any) => {
        const p = e?.data?.payload
        refreshTabs()
        TabService.Active().then(setActiveTab)
        setReloadKey((k) => k + 1)
        if (p?.message) showToast(t10(langRef.current, 'Imported') + ' — ' + p.message)
      }),
      // a tab's source could not be fetched → tell the user right away (until
      // now this was only visible inside the tab-settings modal)
      Events.On('source_error', (e: any) => {
        const p = e?.data?.payload
        const first = (p?.errors || [])[0]
        showToast(t10(langRef.current, 'Source unavailable') + (first ? ' — ' + first : ''))
        // Remember it per tab: an EMPTY tab whose source is unreachable must say
        // so instead of offering the "add a config" hints (nothing is missing —
        // the source just didn't answer).
        if (p?.tab) setSrcErr((m) => ({ ...m, [p.tab]: first || '' }))
      }),
      Events.On('reload_delta', (e: any) => {
        if (e?.data?.tab && e.data.tab !== activeTabRef.current) return
        if (suppressDeltaRef.current) {
          suppressDeltaRef.current = false
          return
        }
        const p = e?.data?.payload
        const parts: string[] = []
        if (p?.added > 0) parts.push('+' + p.added)
        if (p?.removed > 0) parts.push('−' + p.removed)
        if (parts.length) showToast(parts.join('   '))
        // Flash newly-added rows green, fading out (1.10 row-new).
        if (Array.isArray(p?.idx) && p.idx.length) {
          flashIdxRef.current = new Set(p.idx)
          flashTabRef.current = e?.data?.tab || activeTabRef.current
          bump((v) => v + 1)
          setTimeout(() => {
            flashIdxRef.current = new Set()
            bump((v) => v + 1)
          }, 2600)
        }
      }),
      // live ping/speed result for one config → patch its cached row + the
      // conn-bar ping chip (noteConnPing)
      Events.On('entry_update', (e: any) => {
        const p = e?.data?.payload
        if (!p || typeof p.index !== 'number') return
        // Row patches are STRICTLY tab-scoped: a test running on tab A must
        // never rewrite tab B's rows (indices collide across tabs!). The
        // conn-bar ping chip below matches by raw, so it stays global.
        if (e?.data?.tab && e.data.tab !== activeTabRef.current) {
          const ccs = connRef.current
          if (ccs && ccs.status === 'connected' && ccs.conn_raw && p.raw && rawKey(p.raw) === rawKey(ccs.conn_raw))
            setConnPing({ st: p.ping_status, delay: p.delay })
          return
        }
        // Match by index; also accept a raw change (rename mutates raw) as long
        // as the index matches. Patch every display field so rename/host edits
        // reflect too — not just ping/speed.
        for (const [pos, row] of cacheRef.current) {
          if (row.index === p.index) {
            cacheRef.current.set(pos, {
              ...row,
              raw: p.raw ?? row.raw,
              name: p.name ?? row.name,
              host: p.host ?? row.host,
              port: typeof p.port === 'number' ? p.port : row.port,
              net: p.network ?? row.net,
              sec: p.security ?? row.sec,
              proto: p.protocol ?? row.proto,
              ping: p.delay,
              speed: p.speed_mbps || 0,
              speed_live: p.speed_live || 0,
              ping_st: p.ping_status,
              speed_st: p.speed_status,
              ping_err: p.ping_err || '',
              speed_err: p.speed_err || '',
              // Absent when the config has no measurement history yet; keeping
              // the old value then is right, because nothing replaced it.
              stab: p.stab ?? row.stab,
            })
            bump((v) => v + 1)
            break
          }
        }
        scheduleStatsRefresh() // live ping ok / failed / best counters
        scheduleResort() // live re-order when sorted by ping/speed
        const cs = connRef.current
        if (cs && cs.status === 'connected' && cs.conn_raw && p.raw && rawKey(p.raw) === rawKey(cs.conn_raw))
          setConnPing({ st: p.ping_status, delay: p.delay })
      }),
      // bulk test progress → thin amber bar under the header (1.10 pb-main)
      Events.On('bulk_ping_start', (e: any) => {
        bulkTabRef.current = e?.data?.tab || ''
        setPingRunning(true)
        setBarPct(0)
      }),
      Events.On('bulk_ping_progress', (e: any) => {
        const p = e?.data?.payload
        if (p?.total > 0 && e?.data?.tab === activeTabRef.current) {
          setBarPct(Math.round((p.done / p.total) * 100))
          scheduleStatsRefresh()
        }
      }),
      Events.On('bulk_ping_done', () => {
        setPingRunning(false)
        setBarPct(100)
        setTimeout(() => setBarPct(0), 400)
        setReloadKey((k) => k + 1)
      }),
      Events.On('bulk_speed_start', (e: any) => {
        bulkTabRef.current = e?.data?.tab || ''
        setSpeedRunning(true)
        setBarPct(0)
      }),
      Events.On('bulk_speed_progress', (e: any) => {
        const p = e?.data?.payload
        if (p?.total > 0 && e?.data?.tab === activeTabRef.current) {
          setBarPct(Math.round((p.done / p.total) * 100))
          scheduleStatsRefresh()
        }
      }),
      Events.On('bulk_speed_done', () => {
        setSpeedRunning(false)
        setBarPct(100)
        setTimeout(() => setBarPct(0), 400)
        setReloadKey((k) => k + 1)
      }),
      Events.On('log', (e: any) => {
        if (!logsOpenRef.current) return
        const raw = e?.data?.payload
        if (!Array.isArray(raw)) return
        const batch = raw.map((l: any) => ({ ...l, _id: logSeq.current++ }))
        if (!logAutoRef.current) {
          // Held, not dropped: the same cap as the panel, so a long read cannot
          // grow this without bound either.
          logHeld.current = [...logHeld.current, ...batch].slice(-2000)
          setLogHeldN(logHeld.current.length)
          return
        }
        setLogLines((prev) => [...prev, ...batch].slice(-2000))
        const imp = batch.filter(isImp)
        if (imp.length) setImpLines((prev) => [...prev, ...imp].slice(-500))
      }),
    ]

    // keyboard: Ctrl+A select-all, Delete/Backspace delete, Escape clear
    const onKey = (e: KeyboardEvent) => {
      const el = document.activeElement as HTMLElement | null
      const inInput = el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')
      // e.code is layout-independent — Ctrl+A must work on a RU keyboard too
      // (there e.key is 'ф').
      if (e.ctrlKey && e.code === 'KeyA') {
        if (inInput) return
        e.preventDefault()
        selectAllRef.current()
        return
      }
      if (
        (e.key === 'Delete' || e.key === 'Backspace') &&
        selRef.current.size > 0 &&
        activeTabRef.current !== 'main'
      ) {
        if (inInput) return
        e.preventDefault()
        deleteSelectedRows()
      }
      if (e.key === 'Escape') {
        setMenu(null)
        if (selRef.current.size > 0) clearSelection()
      }
    }
    document.addEventListener('keydown', onKey, true)
    // Ctrl+V anywhere pastes configs / subscription URLs (1.10; no paste box)
    const onPaste = (e: ClipboardEvent) => {
      const el = document.activeElement as HTMLElement | null
      if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')) return
      const text = e.clipboardData?.getData('text') || ''
      if (text) {
        if (pasteworthy(text)) importRef.current(text)
        return
      }
      // Some webviews hand an empty clipboardData on the synthetic event —
      // fall back to the async clipboard API.
      navigator.clipboard
        ?.readText?.()
        .then((t) => {
          if (pasteworthy(t)) importRef.current(t)
        })
        .catch(() => {})
    }
    document.addEventListener('paste', onPaste)
    // Ctrl+C: the 'copy' event is the reliable path in a webview (the async
    // clipboard API is often blocked). Write the selected raws in view order.
    const onCopy = (e: ClipboardEvent) => {
      const el = document.activeElement as HTMLElement | null
      if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')) return
      // Someone who has highlighted text — in the log panel, in a hint, anywhere
      // — is copying that text. Rows can stay selected for a long time, and this
      // handler used to overwrite the clipboard with them, which made copying a
      // log line by hand impossible.
      const textSel = window.getSelection()
      if (textSel && !textSel.isCollapsed && String(textSel).trim() !== '') return
      if (selRef.current.size === 0) return
      const pos = allPosRef.current
      const idxs = [...selRef.current]
      if (pos) idxs.sort((a, b) => (pos.get(a) ?? 0) - (pos.get(b) ?? 0))
      const raws = idxs.map((i) => selRawsRef.current.get(i)).filter(Boolean) as string[]
      if (!raws.length) return
      e.preventDefault()
      e.clipboardData?.setData('text/plain', raws.join('\n'))
      flashCopied(idxs)
      showToast(t10(langRef.current, 'Copied') + (raws.length > 1 ? ' ' + raws.length : ''))
    }
    document.addEventListener('copy', onCopy)
    // Disable the native (browser) context menu everywhere except inputs — the
    // app draws its own row/tab/empty-area menus.
    const onCtx = (e: MouseEvent) => {
      const el = e.target as HTMLElement | null
      if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')) return
      e.preventDefault()
    }
    document.addEventListener('contextmenu', onCtx)
    // window resizes re-run the compact-tab measurement
    const onResize = () => setWinW(window.innerWidth)
    window.addEventListener('resize', onResize)
    // Startup "new version" banner (1.10 checkUpdateStartup) — once per launch.
    const updT = setTimeout(() => {
      UpdateService.Check()
        .then((j: any) => {
          if (j?.notify) setUpdBanner({ latest: j.latest, notes: j.notes })
        })
        .catch(() => {})
    }, 3000)
    // uptime ticks locally between conn_update pushes
    const tick = setInterval(() => {
      setConn((c: any) =>
        c && c.status === 'connected' ? { ...c, uptime_sec: (c.uptime_sec || 0) + 1 } : c,
      )
    }, 1000)
    return () => {
      offs.forEach((f) => typeof f === 'function' && f())
      document.removeEventListener('keydown', onKey, true)
      document.removeEventListener('paste', onPaste)
      document.removeEventListener('copy', onCopy)
      document.removeEventListener('contextmenu', onCtx)
      window.removeEventListener('resize', onResize)
      clearTimeout(updT)
      clearInterval(tick)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // close context menus on any click
  useEffect(() => {
    if (!menu) return
    const close = () => setMenu(null)
    const id = setTimeout(() => document.addEventListener('click', close, { once: true }), 10)
    return () => {
      clearTimeout(id)
      document.removeEventListener('click', close)
    }
  }, [menu])
  // close the compact-tab dropdown on any click
  useEffect(() => {
    if (!tabDD) return
    const close = () => setTabDD(null)
    const id = setTimeout(() => document.addEventListener('click', close, { once: true }), 10)
    return () => {
      clearTimeout(id)
      document.removeEventListener('click', close)
    }
  }, [tabDD])
  // File drop is a USER-TAB feature (a dropped file becomes one of that tab's
  // sources), and SOURCES is the built-in read-only list — so the drop target is
  // attached only while a user tab is active. Without the attribute Wails raises
  // no event and shows no highlight, which is exactly the "not allowed here"
  // behaviour we want on SOURCES.
  useEffect(() => {
    const root = document.getElementById('root')
    if (!root) return
    if (activeTab && activeTab !== 'main') root.setAttribute('data-file-drop-target', '')
    else root.removeAttribute('data-file-drop-target')
  }, [activeTab])
  // Close the type dropdown on a click OUTSIDE it (clicks inside must survive,
  // so ctrl+click can pick several types in a row).
  useEffect(() => {
    if (!protoDD) return
    const close = (e: MouseEvent) => {
      const t = e.target as HTMLElement | null
      if (t?.closest('.proto-dd-menu') || t?.closest('#proto-dd-trigger')) return
      setProtoDD(null)
    }
    const id = setTimeout(() => document.addEventListener('click', close), 10)
    return () => {
      clearTimeout(id)
      document.removeEventListener('click', close)
    }
  }, [protoDD])
  // compact-mode detection (1.10 updateTabLayout): measure with compact OFF,
  // then re-apply synchronously before paint — no flicker, no oscillation.
  useLayoutEffect(() => {
    const bar = tabBarRef.current
    const tbar = toolbarRef.current
    if (!bar || !tbar) return
    bar.classList.remove('compact')
    tbar.classList.remove('compact')
    const btns = bar.querySelectorAll('.tab-btn')
    let compact = false
    if (btns.length >= 2) {
      // Group the buttons by their row (offsetTop) to get both the row count and
      // how many tabs each row holds.
      const perRow = new Map<number, number>()
      btns.forEach((b) => {
        const top = (b as HTMLElement).offsetTop
        perRow.set(top, (perRow.get(top) ?? 0) + 1)
      })
      const rows = perRow.size
      const widestRow = Math.max(...perRow.values())
      // Collapse into the dropdown when the tabs would need MORE than
      // TAB_MAX_ROWS rows, or when a row can no longer hold TAB_MIN_PER_ROW
      // tabs — at that point the bar is a tall stack of near-single tabs, which
      // the dropdown does better. (The second rule is what actually fires on a
      // narrow window: the layout floor stops the bar from ever growing to six
      // rows.) Only meaningful once there are enough tabs to fill a row.
      if (rows > TAB_MAX_ROWS || (btns.length >= TAB_MIN_PER_ROW && widestRow < TAB_MIN_PER_ROW))
        compact = true
    }
    if (compact) {
      bar.classList.add('compact')
      tbar.classList.add('compact')
    } else if (tabDD) {
      setTabDD(null)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tabs, activeTab, winW, stg?.sources_enabled])

  // The active tab is remembered by the backend and defaults to SOURCES, which
  // the Sources setting can hide. Hidden and still active, it left the window
  // showing a table nobody could navigate away from — no tab looked selected
  // and the list was empty. Whenever the active tab is not one of the visible
  // ones, move to the first that is.
  useEffect(() => {
    if (!tabs.length) return
    const visible = tabs.filter((t) => !(t.id === 'main' && stg?.sources_enabled === false))
    if (!visible.length) return
    if (activeTab && visible.some((t) => t.id === activeTab)) return
    switchTab(visible[0].id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tabs, activeTab, stg?.sources_enabled])

  // ── derived ───────────────────────────────────────────────────────
  const connected = !!conn && conn.status === 'connected'
  const connecting = conn?.status === 'connecting'
  const isError = conn?.status === 'error'
  const isActive = connected || connecting || isError
  const isProxy = conn?.mode !== 'tun'
  const stateCls = connected ? (isProxy ? 'cp' : 'conn-tun') : connecting ? 'cc' : isError ? 'ce' : ''
  // "SYSTEM PROXY" only when the OS setting really points at us. It does not
  // when the user turned that off in settings, and it cannot on a desktop with
  // no backend to drive (see Capabilities.SystemProxyBackend) — in both cases
  // only apps explicitly aimed at the local port are tunnelled, and claiming
  // otherwise is how "the VPN doesn't work" reports get written.
  const clabel = connected
    ? isProxy
      ? conn?.system_proxy
        ? 'SYSTEM PROXY'
        : 'LOCAL PROXY'
      : 'TUN MODE'
    : connecting
      ? conn?.mode === 'tun'
        ? 'STARTING TUN…'
        : 'CONNECTING…'
      : conn?.status === 'disconnecting'
        ? 'DISCONNECTING…'
        : isError
          ? 'ERROR'
          : 'DISCONNECTED'
  const filtering = filter !== '' || protos.length > 0
  const trafficEnabled = !traffic || traffic.enabled !== false
  const totalUp = traffic ? traffic.total_up : (stats?.total_up ?? 0)
  const totalDown = traffic ? traffic.total_down : (stats?.total_down ?? 0)

  // copyConnected puts the ACTIVE connection's config URL on the clipboard —
  // every hop (one per line) for a chain. Works even when the config's row is no
  // longer in the tab.
  const copyConnected = () => {
    const cs = connRef.current
    if (!cs) return
    const raws: string[] =
      cs.chain_raws && cs.chain_raws.length > 0 ? cs.chain_raws : cs.conn_raw ? [cs.conn_raw] : []
    if (raws.length === 0) return
    copyText(raws.join('\n'))
    setConnCopied(true)
    setTimeout(() => setConnCopied(false), 1200)
    showToast(tt('Copied') + (raws.length > 1 ? ' ' + raws.length : ''))
  }

  const onProtoClick = (e: React.MouseEvent, val: string) => {
    if (e.ctrlKey || e.metaKey)
      setProtos((p) => (p.includes(val) ? p.filter((x) => x !== val) : [...p, val]))
    else setProtos([val])
  }

  // ── column sorting ────────────────────────────────────────────────
  // Left click cycles a column: natural → reversed → off (original order).
  // Natural means A→Z for the text columns, fastest-first for ping and speed.
  // Right click clears the sort from any state.
  const cycleSort = (col: SortCol) => {
    setSort((s) => (s === col ? (`${col}:desc` as Sort) : s === `${col}:desc` ? 'idx' : (col as Sort)))
  }
  // sortableTH renders one clickable header cell with its state arrow.
  const sortableTH = (cls: string, col: SortCol, label: string) => {
    const active = sort === col || sort === `${col}:desc`
    const desc = sort === `${col}:desc`
    if (dropped.has(cls)) return null
    return (
      <div
        className={'gh ' + cls + ' sortable' + (active ? ' sorted' : '')}
        onClick={() => cycleSort(col)}
        onContextMenu={(e) => {
          e.preventDefault()
          e.stopPropagation()
          setSort('idx')
        }}
        title={
          active
            ? (desc ? 'Sorted — click to clear' : 'Sorted — click to reverse') + ' · right-click to clear'
            : 'Click to sort · right-click to clear'
        }
      >
        <span className="th-label">{label}</span>
        <span className="th-arrow">{active ? (desc ? '▾' : '▴') : ''}</span>
      </div>
    )
  }

  const padTop = items.length > 0 ? items[0].start : 0
  const padBottom = items.length > 0 ? virtualizer.getTotalSize() - items[items.length - 1].end : 0

  // ── row painter (exact buildRow port) ───────────────────────────────
  const renderRow = (vi: { index: number }) => {
    const r = cacheRef.current.get(vi.index)
    const pos = vi.index + 1
    if (!r)
      return (
        <div className="grow" key={'p' + vi.index} style={{ height: ROW_H }} role="row">
          {!dropped.has('ci') && <div className="gc ci">{pos}</div>}
        </div>
      )

    let pp = 'pill pending'
    let pt = '—'
    if (r.ping_st === 'testing_ping') {
      pp = 'pill tp'
      pt = 'pinging…'
    } else if (r.ping_st === 'ok') {
      pp = r.ping < 150 ? 'pill ok-fast' : 'pill ok-ping'
      pt = r.ping + 'ms'
    } else if (r.ping_st === 'failed') {
      pp = 'pill failed'
      pt = r.ping_err || 'timeout'
    }

    let sp = 'pill pending'
    let st = '—'
    if (r.speed_st === 'testing_speed') {
      sp = 'pill ts'
      // The chip is one width, and these are the readings it has to hold:
      // ten characters at most, so a fast link loses a decimal rather than
      // the chip growing a column for everyone.
      st = r.speed_live > 0 ? fmtSpeed(r.speed_live) : 'connecting…'
    } else if (r.speed_st === 'ok') {
      sp = 'pill ok-speed'
      st = fmtSpeed(r.speed)
    } else if (r.speed_st === 'failed') {
      sp = 'pill failed'
      st = r.speed_err || 'failed'
    } else if (r.speed_st === 'skipped') {
      sp = 'pill skipped'
      st = '—'
    }

    const ncls = (r.net || 'tcp').toLowerCase().replace(/[^a-z]/g, '')
    const chainRaws: string[] = conn?.chain_raws || []
    const inChain = !!(chainRaws.length && r.raw && chainRaws.indexOf(r.raw) >= 0)
    const isChainExit = !!(chainRaws.length > 0 && r.raw === chainRaws[chainRaws.length - 1])
    const rowLit = connLit(r)
    const isConn = rowLit && connected
    // Painted, because blanking the table on every sort is worse — but not
    // clickable: this row was fetched under a view that no longer exists, and
    // its index may belong to a different configuration now. The refetch
    // replaces it within a round trip.
    const stale = (r as any)._seq !== undefined && (r as any)._seq !== viewSeqRef.current
    const isLast = !!(r.raw && lastRaw && rawKey(r.raw) === rawKey(lastRaw))
    const hostport = (r.host || '') + (r.port ? ':' + r.port : '')
    const cls =
      (selected.has(r.index) ? 'selected ' : '') +
      (flashTabRef.current === activeTab && flashIdxRef.current.has(r.index) ? 'row-new ' : '') +
      (stale ? 'row-stale ' : '') +
      (rowLit ? (conn?.mode === 'tun' ? 'row-ct' : 'row-cp') : '')

    return (
      <div
        role="row"
        key={r.index}
        className={'grow' + (cls.trim() ? ' ' + cls.trim() : '')}
        style={{ height: ROW_H }}
        onClick={(e) => {
          if (stale) return
          if ((e.target as HTMLElement).closest('.act-cell')) return
          if (consumeLongPress()) return
          toggleRowSelect(r.index, e)
        }}
        onPointerDown={(e) => {
          if (stale) return
          // Not over the action buttons — a hold there is aimed at the button.
          if ((e.target as HTMLElement).closest('.act-cell')) return
          beginLongPress(e, (x, y) => {
            if (!selRef.current.has(r.index)) {
              selRawsRef.current.clear()
              selRawsRef.current.set(r.index, r.raw)
              setSelected(new Set([r.index]))
            }
            setMenu({ kind: 'row', x, y, idx: r.index })
          })
        }}
        {...longPressCancelProps}
        onDoubleClick={(e) => {
          if ((e.target as HTMLElement).closest('.act-cell')) return
          e.preventDefault()
          if (connected && conn?.conn_raw === r.raw) return
          ConnService.Connect(r.index, selectedMode)
        }}
        onContextMenu={(e) => {
          e.preventDefault()
          if (!selRef.current.has(r.index)) {
            selRawsRef.current.clear()
            selRawsRef.current.set(r.index, r.raw)
            setSelected(new Set([r.index]))
          }
          setMenu({ kind: 'row', x: e.clientX, y: e.clientY, idx: r.index })
        }}
      >
        {!dropped.has('ci') && <div className="gc ci">{pos}</div>}
        {!dropped.has('cpr') && (
          <div className="gc cpr">
            <span className={'pb ' + (r.proto || '')} title={r.proto}>
              {protoLabel(r.proto || '?')}
            </span>
          </div>
        )}
        <div className="gc cn">
          <div className="nc">
            <div className="nm-row">
              <span
                className={'fav' + (r.fav ? ' on' : '')}
                title="Favorite"
                onClick={(e) => {
                  e.stopPropagation()
                  ConfigService.ToggleFavorite(r.index)
                }}
              >
                {r.fav ? '★' : '☆'}
              </span>
              {editingIdx === r.index ? (
                <input
                  className="finput"
                  style={{ height: 20, flex: 1, minWidth: 0 }}
                  value={editName}
                  autoFocus
                  onClick={(e) => e.stopPropagation()}
                  onChange={(e) => setEditName(e.target.value)}
                  onBlur={commitRename}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') commitRename()
                    if (e.key === 'Escape') setEditingIdx(null)
                  }}
                />
              ) : (
                <span className="nm" title={r.name}>
                  {r.name}
                </span>
              )}
              {isLast && (
                <span className="last-badge" title="Last connected config">
                  last
                </span>
              )}
            </div>
          </div>
        </div>
        {!dropped.has('ch') && (
        <div className="gc ch">
          <span className="nh" title={hostport}>
            {hostport}
          </span>
        </div>
        )}
        {!dropped.has('ct') && (
        <div className="gc ct">
          <span className={'nb ' + ncls}>{r.net || 'tcp'}</span>
        </div>
        )}
        {!dropped.has('cs') && (
        <div className="gc cs">
          <span className={'sb ' + (r.sec || 'none')} title={r.sec || 'none'}>
            {r.sec || 'none'}
          </span>
        </div>
        )}
        <div className="gc cp2">
          <div className="vc">
            <span className={pp} title={pt}>
              {pt}
            </span>
          </div>
        </div>
        <div className="gc cst">
          <div className="vc">
            {r.stab ? (
              // The cell is the way into the full record: it already shows the
              // summary, so the detail belongs behind it rather than behind a
              // menu item nobody would look for.
              <span
                className={
                  'stab-pct clickable' + (r.stab.percent >= 0 && r.stab.percent < 80 ? ' poor' : '')
                }
                title={stabTitle(tt, r.stab)}
                onClick={(e) => {
                  e.stopPropagation()
                  setHistoryIdx(r.index)
                }}
              >
                {r.stab.percent >= 0 ? r.stab.percent + '%' : '—'}
              </span>
            ) : null}
          </div>
        </div>
        <div className="gc csp">
          <div className="vc">
            <span className={sp} title={st}>
              {st}
            </span>
          </div>
        </div>
        <div className="gc ca">
          <div className="act-cell">
            {isConn && inChain && !isChainExit ? (
              <span className="chain-hop-tag" title={tt('chain hop')}>
                ⛓
              </span>
            ) : isConn ? (
              <button className="btn sm-disc" title="Disconnect" onClick={() => ConnService.Disconnect()}>
                <span className="lbl-full">disconnect</span>
                <span className="lbl-short">disc</span>
              </button>
            ) : (
              <button
                className="btn sm ghost"
                title={selectedMode === 'tun' ? 'TUN mode (all traffic)' : 'System Proxy (HTTP/SOCKS)'}
                onClick={() => ConnService.Connect(r.index, selectedMode)}
              >
                <span className="lbl-full">connect</span>
                <span className="lbl-short">conn</span>
              </button>
            )}
            {/* Every action carries both a word and a compact form; the phone
                media query swaps which one is displayed. Doing it in CSS rather
                than JS keeps the labels correct during a resize with no
                re-render and no width tracking. The stopwatch/bolt use U+FE0E so
                they render as monochrome glyphs instead of colour emoji. */}
            <button className="btn sm ghost act-ping" title="Ping" onClick={() => TestService.PingOne(r.index)}>
              <span className="lbl-full">ping</span>
              <span className="lbl-short">⏱︎</span>
            </button>
            <button className="btn sm ghost act-speed" title="Speed" onClick={() => TestService.SpeedOne(r.index)}>
              <span className="lbl-full">speed</span>
              <span className="lbl-short">⚡︎</span>
            </button>
            <button
              className={'cpb' + (copiedSet.has(r.index) ? ' done' : '')}
              title="Copy URL"
              onClick={() => {
                copyText(r.raw)
                flashCopied([r.index])
              }}
            >
              {copiedSet.has(r.index) ? '✓' : '⎘'}
            </button>
          </div>
        </div>
      </div>
    )
  }

  const selCount = selected.size
  const selLabel = selCount > 1 ? selCount + ' ' + tt('configs') : tt('config')

  return (
    <HintHost lang={lang} hide={hideHints}>

      {/* ── Custom title bar ──
          Hidden when the compositor draws its own decorations instead, which
          would stack two titlebars. The remote UI runs in a browser with no
          native decoration, so it keeps this one regardless.

          onDoubleClick is the maximise gesture every desktop expects from a
          titlebar. Windows gives it to us for free — the drag region is a
          caption hit-test there, and the WM handles the double-click — so we
          must NOT add our own on top or the two toggles cancel each other out.
          GTK's move-drag has no such gesture, so on Linux this handler IS the
          gesture. Hence native_titlebar_gestures rather than an OS check. */}
      {(IS_REMOTE || appInfo?.caps?.custom_titlebar !== false) && (
      <div
        id="titlebar"
        className="active"
        onDoubleClick={
          !IS_REMOTE && appInfo?.caps?.native_titlebar_gestures === false
            ? (e) => {
                // Only the drag area — double-clicking a window button must not
                // also maximise.
                if ((e.target as HTMLElement).closest('.tb-btns')) return
                Window.ToggleMaximise()
              }
            : undefined
        }
      >
        <div className="tb-drag" id="tb-drag">
          <div className="tb-logo">
            <img id="tb-logo-img" src="/icon.png" alt="" />
          </div>
          <span className="tb-appname">Vair</span>
        </div>
        {/* Native window controls are meaningless in browser (phone) mode — the
            page isn't a native window there. Hidden when remote. */}
        <div className="tb-btns" style={IS_REMOTE ? { display: 'none' } : undefined}>
          <button className="tb-btn" id="tb-min" title="Minimize" onClick={() => Window.Minimise()}>
            <svg width="10" height="1" viewBox="0 0 10 1">
              <rect width="10" height="1" fill="currentColor" />
            </svg>
          </button>
          <button className="tb-btn" id="tb-max" title="Maximize" onClick={() => Window.ToggleMaximise()}>
            <svg width="9" height="9" viewBox="0 0 9 9">
              <rect x="0.5" y="0.5" width="8" height="8" fill="none" stroke="currentColor" strokeWidth="1" />
            </svg>
          </button>
          <button
            className="tb-btn tb-close"
            id="tb-close"
            title="Close"
            onClick={() => {
              if (STANDALONE_AUTO) AutoService.CloseAuto()
              // The capability check is not redundant with the setting: a
              // profile carried over from a machine that HAD a tray still says
              // tray_enabled, and honouring it here would hide the window into
              // nothing.
              else if (stg?.tray_enabled && appInfo?.caps?.tray !== false) Window.Hide()
              else SettingsService.Quit() // 1.10 default: ✕ really quits
            }}
          >
            <svg width="10" height="10" viewBox="0 0 10 10">
              <line x1="0" y1="0" x2="10" y2="10" stroke="currentColor" strokeWidth="1.2" />
              <line x1="10" y1="0" x2="0" y2="10" stroke="currentColor" strokeWidth="1.2" />
            </svg>
          </button>
        </div>
        </div>
      )}

      {/* ── Header ── */}
      <header>
        <div className="stats">
          <div className="stat">
            <span className="sv" id="s-tot">
              {stats?.configs ?? 0}
            </span>
            <span className="sl">configs</span>
          </div>
          <div className="stat">
            <span className="sv ok" id="s-ok">
              {stats?.ping_ok ?? 0}
            </span>
            <span className="sl">ping ok</span>
          </div>
          <div className="stat">
            <span className="sv err" id="s-er">
              {stats?.failed ?? 0}
            </span>
            <span className="sl">failed</span>
          </div>
          {/* .stat-best — hidden on narrow windows (see the 960px media query):
              the same numbers are visible in the table's ping/speed columns. */}
          <div className="stat stat-best">
            <span className="sv ms" id="s-ms">
              {stats?.best_ping ? stats.best_ping + 'ms' : '—'}
            </span>
            <span className="sl">best ping</span>
          </div>
          <div className="stat stat-best">
            <span className="sv sp" id="s-sp">
              {stats?.best_speed ? stats.best_speed.toFixed(1) + ' MB/s' : '—'}
            </span>
            <span className="sl">best speed</span>
          </div>
          {trafficEnabled && connected && traffic && (
            <div className="stat traf" id="stat-session">
              <span className="sv tx" id="s-sess">
                ↑{fmtBytes(traffic.session_up)} ↓{fmtBytes(traffic.session_down)}
              </span>
              <span className="sl">session</span>
            </div>
          )}
          {trafficEnabled && (
            <div className="stat traf" id="stat-total">
              <span className="sv tx" id="s-totl">
                ↑{fmtBytes(totalUp)} ↓{fmtBytes(totalDown)}
              </span>
              <span className="sl">total</span>
            </div>
          )}
        </div>
        <div className="spacer" />
        <div className="ctrls">
          <button
            className={'btn ghost' + (auto ? ' on' : '') + (autoLast?.state === 'switching' ? ' switching' : '')}
            id="btn-auto"
            title="Auto-connect"
            onClick={() => (IS_REMOTE ? setRemoteAutoOpen(true) : AutoService.OpenAuto())}
          >
            auto
          </button>
          <span className="ctrl-sep" />
          <button className="btn ghost" id="btn-reload" onClick={() => ConfigService.Reload(activeTab)}>
            reload
          </button>
          <button
            className={'btn ghost' + (pingRunning ? ' on' : '')}
            id="btn-ping-all"
            title={pingRunning ? 'Click to stop' : undefined}
            onClick={() =>
              pingRunning
                ? TestService.PingAll('', '', []) // re-call cancels; skip the index scan
                : TestService.PingAll(sort, filter, protos)
            }
          >
            {pingRunning ? 'stop ping' : 'ping all'}
          </button>
          <button
            className={'btn ghost' + (speedRunning ? ' on' : '')}
            id="btn-speed-all"
            title={speedRunning ? 'Click to stop' : undefined}
            onClick={() =>
              speedRunning
                ? TestService.SpeedAll('', '', []) // re-call cancels; skip the index scan
                : TestService.SpeedAll(sort, filter, protos)
            }
          >
            {speedRunning ? 'stop speed' : 'speed all'}
          </button>
          <span className="ctrl-sep" />
          <button
            className={'btn ghost icon' + (logsOpen ? ' on' : '')}
            id="btn-logs"
            title="Logs"
            onClick={() => (logsOpen ? closeLogs() : openLogs())}
          >
            {/* document/lines glyph — icon-only, like the settings gear */}
            <svg viewBox="0 0 16 16" fill="none" aria-hidden="true">
              <path d="M3 3h10M3 6.3h10M3 9.6h10M3 12.9h6" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
            </svg>
          </button>
          <button
            className={'btn ghost icon' + (connsOpen ? ' on' : '')}
            id="btn-conns"
            title={tt('Connections')}
            onClick={() => setConnsOpen((v) => !v)}
          >
            {/* two arrows passing each other — traffic, going both ways */}
            <svg viewBox="0 0 16 16" fill="none" aria-hidden="true">
              <path d="M2 5.5h9M8.5 3l2.5 2.5L8.5 8M14 10.5H5M7.5 8L5 10.5 7.5 13"
                stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
          </button>
          <button className="btn ghost icon" id="btn-settings" title="Settings" onClick={openSettings}>
            ⚙
          </button>
        </div>
      </header>

      <div className="prog-area">
        <div className="pbar-row">
          <div className="pbar-fill pbar-ping" id="pb-main" style={{ width: barPct + '%' }} />
        </div>
      </div>

      {/* ── Toolbar: tabs + filter/type/sort ── */}
      <div className="toolbar" id="toolbar" ref={toolbarRef}>
        <div className="tab-bar" id="tab-bar" ref={tabBarRef}>
          <button
            className="tab-dd-trigger"
            id="tab-dd-trigger"
            title="Switch tab"
            onClick={(e) => {
              e.stopPropagation()
              // A long press already opened the tab menu; the release must not
              // also drop the tab list on top of it.
              if (consumeLongPress()) return
              const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
              setTabDD((dd) => (dd ? null : { x: r.left, y: r.bottom + 4 }))
            }}
            onContextMenu={(e) => {
              e.preventDefault()
              const act = tabs.find((t) => t.id === activeTab)
              if (act) setMenu({ kind: 'tab', x: e.clientX, y: e.clientY, tab: act })
            }}
            onPointerDown={(e) =>
              beginLongPress(e, (x, y) => {
                const act = tabs.find((t) => t.id === activeTab)
                if (act) setMenu({ kind: 'tab', x, y, tab: act })
              })
            }
            {...longPressCancelProps}
          >
            <span className="tab-dd-name">{tabs.find((t) => t.id === activeTab)?.name || ''}</span>
            <span className="tab-dd-caret">▾</span>
          </button>
          {tabs
            .filter((tb) => !(tb.id === 'main' && stg?.sources_enabled === false))
            .map((tb) => (
              <button
                key={tb.id}
                data-id={tb.id}
                className={'tab-btn' + (tb.id === activeTab ? ' active' : '')}
                onPointerDown={(e) => {
                  if (e.button === 0) startTabDrag(e, tb.id)
                  beginLongPress(e, (x, y) => setMenu({ kind: 'tab', x, y, tab: tb }))
                }}
                {...longPressCancelProps}
                onClick={() => {
                  if (consumeLongPress()) return
                  if (suppressTabClickRef.current) {
                    suppressTabClickRef.current = false
                    return
                  }
                  switchTab(tb.id)
                }}
                onContextMenu={(e) => {
                  e.preventDefault()
                  setMenu({ kind: 'tab', x: e.clientX, y: e.clientY, tab: tb })
                }}
              >
                <span className="tab-label">{tb.name}</span>
              </button>
            ))}
          <button className="tab-add" title="New tab (Ctrl+V to paste configs)" onClick={createTab}>
            +
          </button>
        </div>
        <div className="toolbar-right">
          <span className="tl tl-lbl">filter</span>
          <input
            className="finput"
            id="fi"
            placeholder="name / host / type / transport…"
            title={tt(FILTER_TITLE)}
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            spellCheck={false}
          />
          {/* The filter has no paragraph anywhere to hide, so its mark does not
              come and go with the descriptions setting. It sits after the box
              rather than before it: the label names the box, and a mark between
              the two would read as belonging to neither. */}
          <InfoDot className="tl-i" desc={FILTER_HELP} />
          <span id="fc" style={{ fontSize: 11, color: 'var(--dim)' }}>
            {filtering ? total + ' / ' + tabTotal : ''}
          </span>
          <span className="tl tl-lbl" style={{ marginLeft: 6 }}>
            type
          </span>
          {/* Type filter as a dropdown (same pattern as the compact tab picker):
              the trigger shows the current selection, the menu keeps ctrl+click
              multi-select. */}
          <button
            className={'proto-dd-trigger' + (protos.length ? ' on' : '')}
            id="proto-dd-trigger"
            title="Click to filter by one type. Ctrl+click to multi-select."
            onClick={(e) => {
              if (protoDD) {
                setProtoDD(null)
                return
              }
              // The trigger sits at the RIGHT edge of the toolbar, so anchor the
              // menu to its right side and clamp it inside the viewport (a
              // left-anchored menu ran off the window).
              const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
              const W = 150 // keep in sync with .proto-dd-menu min-width
              const x = Math.max(6, Math.min(r.right - W, window.innerWidth - W - 6))
              setProtoDD({ x, y: r.bottom + 2 })
            }}
          >
            <span className="proto-dd-name">
              {protos.length === 0
                ? 'all'
                : protos.length <= 2
                  ? protos.map((v) => PROTOS.find(([, val]) => val === v)?.[0] || v).join(', ')
                  : protos.length + ' selected'}
            </span>
            <span className="proto-dd-caret">▾</span>
          </button>
        </div>
      </div>

      {/* ── Sub-bar: compact subscription info for the active tab ── */}
      {activeSubs.length > 0 && (
        <div id="sub-bar">
          {activeSubs.map((sub, i) => {
            const label =
              sub.title ||
              (() => {
                try {
                  return new URL(sub.url).hostname
                } catch {
                  return tt('Subscription')
                }
              })()
            const used = (sub.upload || 0) + (sub.download || 0)
            const pct = sub.total > 0 ? Math.min(100, Math.round((used / sub.total) * 100)) : 0
            const dl = sub.expire > 0 ? Math.ceil((sub.expire * 1000 - Date.now()) / 86400000) : 0
            const d = sub.expire > 0 ? new Date(sub.expire * 1000) : null
            const p = (n: number) => (n < 10 ? '0' : '') + n
            return (
              <span key={i} className="sb-item">
                <span className="sb-title">{label}</span>
                {sub.total > 0 && (
                  <>
                    <span style={{ opacity: 0.4 }}>·</span>
                    <span>
                      {fmtBytes(used)} / {fmtBytes(sub.total)}
                    </span>
                    <span className="sb-bar">
                      <span className="sb-fill" style={{ width: pct + '%' }} />
                    </span>
                  </>
                )}
                {d && (
                  <>
                    <span style={{ opacity: 0.4 }}>·</span>
                    <span className={dl <= 7 ? 'sb-warn' : undefined}>
                      {tt('until')} {p(d.getDate())}.{p(d.getMonth() + 1)}.{d.getFullYear()} ({dl}{' '}
                      {tt('days')})
                    </span>
                  </>
                )}
              </span>
            )
          })}
        </div>
      )}

      {/* ── Table ── */}
      {/* The header sits OUTSIDE the scrolling box again, so the scrollbar runs
          along the rows and not across the headings. What used to make that
          impossible — two tables, two widths — is gone: header and rows are two
          grids reading the same track list from --cols, so they cannot drift.
          The gutter the scrollbar occupies is measured once and mirrored here as
          padding, which is the only number the two share. */}
      {!loading && total > 0 && (
        <div className="thw">
          <div className="grow ghead" role="row">
            {!dropped.has('ci') && <div className="gh ci">#</div>}
            {sortableTH('cpr', 'type', 'Type')}
            {sortableTH('cn', 'name', 'Name')}
            {sortableTH('ch', 'host', 'Host')}
            {sortableTH('ct', 'transport', 'Transport')}
            {sortableTH('cs', 'security', 'Security')}
            {sortableTH('cp2', 'ping', 'Ping')}
            {sortableTH('cst', 'stab', 'Stability')}
            {sortableTH('csp', 'speed', 'Speed')}
            <div className="gh ca">Actions</div>
          </div>
        </div>
      )}
      {/* The header used to be a table of its own, outside the scrolling box, so
          the vertical scrollbar would span only the rows. Two tables meant two
          widths — they differ by the scrollbar's own width — and every attempt
          to keep their columns in step ended in one of them overflowing its box
          and putting a scrollbar along the bottom of the window. One table with
          a sticky header row has neither problem: there is one width, and the
          columns cannot drift from the headings because they ARE the headings'
          columns. See sizeColumns for how that width is divided. */}
      <div
        className="tw"
        ref={twRef}
        onContextMenu={(e) => {
          // A row handles its own menu; this one is for the space around
          // them. The test asked for a <tr>, which is what a row was before
          // the table became a grid — so it never matched, this handler ran
          // after the row's, and the row's menu was replaced by the short one
          // for empty space.
          if ((e.target as HTMLElement).closest('.grow')) return
          if (activeTabRef.current === 'main') return
          e.preventDefault()
          setMenu({ kind: 'empty', x: e.clientX, y: e.clientY })
        }}
      >
        {loading ? (
          <div className="center-msg" id="msg-area">
            <div className="ico">
              <span className="spinner" />
            </div>
            <p>{loading.op === 'delete' ? tt('Deleting configs…') : tt('Loading configs…')}</p>
          </div>
        ) : total === 0 && tabTotal === 0 && srcErr[activeTab] !== undefined ? (
          /* The tab HAS a source, it just couldn't be fetched — say that instead
             of the "add a config" hints, which imply the tab is unconfigured. */
          <div className="center-msg" id="msg-area">
            <p style={{ marginBottom: 6 }}>{tt('Source unavailable')}</p>
            {srcErr[activeTab] ? (
              <p style={{ marginBottom: 12, color: 'var(--dim)', fontSize: 12 }}>{srcErr[activeTab]}</p>
            ) : null}
            <div style={{ display: 'flex', gap: 8, justifyContent: 'center', flexWrap: 'wrap' }}>
              <button className="btn ghost" onClick={() => ConfigService.Reload(activeTab)}>
                {tt('Retry')}
              </button>
              <button className="btn ghost" onClick={() => setTabModalId(activeTab)}>
                {tt('Tab settings')}
              </button>
            </div>
          </div>
        ) : total === 0 && tabTotal === 0 && activeTab !== 'main' ? (
          /* Genuinely empty user tab: show the three ways to fill it (discoverability
             — the context-menu entry points are invisible until you know to
             right-click). Note tabTotal === 0: when the tab HAS configs and the view
             is empty only because of a filter / type selection, we fall through to
             the "nothing matches" message below instead of these hints. */
          <div className="center-msg" id="msg-area">
            <p style={{ marginBottom: 12 }}>{tt('This tab is empty')}</p>
            <div style={{ display: 'flex', gap: 8, justifyContent: 'center', flexWrap: 'wrap' }}>
              <button className="btn ghost" onClick={openConfigAdd}>{tt('Add config…')}</button>
              <button className="btn ghost" onClick={pasteFromClipboard}>{tt('Paste configs')}</button>
              <button className="btn ghost" onClick={() => setTabModalId(activeTab)}>{tt('Add a source')}</button>
              <button className="btn ghost" onClick={addWARP} disabled={warpBusy}>
                {warpBusy ? tt('Getting WARP…') : tt('Get a free WARP config')}
              </button>
            </div>
          </div>
        ) : total === 0 && tabTotal > 0 ? (
          /* The tab has configs but the current filter / type selection matches
             none — say so (and offer to clear), instead of the empty-tab hints. */
          <div className="center-msg" id="msg-area">
            <p style={{ marginBottom: 12 }}>{tt('No configs match the filter')}</p>
            <button className="btn ghost" onClick={() => { setFilter(''); setProtos([]) }}>
              {tt('Clear filter')}
            </button>
          </div>
        ) : (
          <div id="tbl" className="gtable" role="grid" aria-rowcount={total}>
            {padTop > 0 && <div className="vspacer" style={{ height: padTop }} />}
            {items.map((vi) => renderRow(vi))}
            {padBottom > 0 && <div className="vspacer" style={{ height: padBottom }} />}
          </div>
        )}
      </div>

      {/* ── Logs dock panel ── */}
      {logsOpen && (
        <div id="log-panel" style={{ height: logH }}>
          <div
            className="log-resize"
            id="log-resize"
            title="Drag to resize"
            onMouseDown={(e) => {
              e.preventDefault()
              const startY = e.clientY
              const startH = logH
              const move = (ev: MouseEvent) => {
                const h = startH + (startY - ev.clientY)
                setLogH(Math.min(Math.max(120, h), window.innerHeight - 200))
              }
              const up = () => {
                document.removeEventListener('mousemove', move)
                document.removeEventListener('mouseup', up)
              }
              document.addEventListener('mousemove', move)
              document.addEventListener('mouseup', up)
            }}
          />
          <div className="log-head">
            <span className="log-title" id="log-title">
              Logs
            </span>
            <Select className="log-sel" id="log-src" value={logSrc} onChange={(e) => setLogSrc(e.target.value)}>
              <option value="">all</option>
              <option value="xray">xray</option>
              <option value="singbox">sing-box</option>
              <option value="vair">vair</option>
              <option value="test">test</option>
            </Select>
            <Select className="log-sel" id="log-lvl" value={logLvl} onChange={(e) => setLogLvl(e.target.value)}>
              <option value="">all</option>
              <option value="important">important</option>
              <option value="info">info+</option>
              <option value="warn">warn+</option>
              <option value="error">error</option>
            </Select>
            <input
              className="log-find"
              id="log-find"
              placeholder="search…"
              title="Filter the lines by text (case-insensitive). Applies to copy too."
              value={logFind}
              onChange={(e) => setLogFind(e.target.value)}
              spellCheck={false}
            />
            {logFind && (
              <span className="log-find-n">
                {logShown.length}/{logBase.length}
              </span>
            )}
            <Check
              className="log-auto"
              checked={logAuto}
              onChange={setAutoScroll}
              label={<span id="log-auto-lbl">auto-scroll</span>}
            />
            {logHeldN > 0 && (
              <button className="btn ghost log-held" onClick={releaseHeld} title={tt('Show the lines that arrived while the view was held')}>
                +{logHeldN}
              </button>
            )}
            <div className="spacer" />
            <button className="btn ghost" id="log-copy" onClick={copyLogs}>
              copy
            </button>
            <button
              className="btn ghost"
              id="log-clear"
              onClick={() => {
                LogService.Clear()
                setLogLines([])
                setImpLines([])
              }}
            >
              clear
            </button>
            <button className="btn ghost" id="log-close" title="Close" onClick={closeLogs}>
              ✕
            </button>
          </div>
          <div className="log-view" id="log-view" ref={logViewRef}>
            {logShown.map((l, i) => (
              <span key={l._id ?? i} className={'log-line lvl-' + (l.lvl || 'raw')}>
                <span className="lt">{fmtLogTime(l.t)}</span>
                <span className={'ls src-' + (l.src || '')}>{l.src === 'singbox' ? 'sing-box' : l.src}</span>
                {l.msg}
                {'\n'}
              </span>
            ))}
            {logBase.length === 0 && <span className="log-empty">— no log lines yet —</span>}
          </div>
        </div>
      )}

      {/* ── Connection Bar ── */}
      <div id="conn-bar" className={stateCls}>
        <div className={'cdot' + (stateCls ? ' ' + stateCls : '')} id="cdot" />
        <span id="clabel" className={stateCls}>
          {clabel}
        </span>
        {/* What the running session was actually built with. One mark rather
            than a row of chips: the bar is narrow, and the panel behind it has
            room to grow. */}
        <button
          className="hint-i sess-i"
          title={tt('What this session is running with')}
          aria-label={tt('What this session is running with')}
          onClick={() => setSessionOpen(true)}
        >
          i
        </button>
        <span id="cdetail">
          {connected &&
            (conn.chain && conn.chain.length > 1 ? (
              <>
                chain:{' '}
                {conn.chain.map((h: string, i: number) => (
                  <span key={i}>
                    <span>{h}</span>
                    {i < conn.chain.length - 1 ? ' → ' : ''}
                  </span>
                ))}
                {' · ' + fmtUptime(conn.uptime_sec)}
              </>
            ) : (
              <>
                via <span>{conn.entry_name}</span>
                {' · ' + fmtUptime(conn.uptime_sec)}
              </>
            ))}
          {connecting && <span>{conn?.entry_name}</span>}
          {/* An error longer than the bar is cut off by the layout, and the part
              that gets cut is usually the half that says what to do about it.
              The whole sentence is on the hover. */}
          {isError && (
            <span className="conn-err" style={{ color: 'var(--red)' }} title={conn?.error || 'unknown error'}>
              {conn?.error || 'unknown error'}
            </span>
          )}
        </span>
        {/* The firewall block is standing with nothing connected: the machine
            has no way out, and without this nothing on screen would say why.
            Clicking it releases the block — the same thing disconnecting does,
            reachable when there is nothing to disconnect from. */}
        {!connected && conn?.blocked && <BlockedChip tt={tt} />}
        {connected && (conn.conn_raw || (conn.chain_raws && conn.chain_raws.length > 0)) && (
          /* Copy the CONNECTED config — handy when its row is gone from the tab
             (source refreshed, filtered out, deleted). Copies every hop for a
             chain, one per line. */
          <span
            id="ccopy"
            className={'cping ccopy' + (connCopied ? ' ok' : '')}
            title={tt('Copy the connected config')}
            onClick={copyConnected}
          >
            {connCopied ? tt('copied') : tt('copy')}
          </span>
        )}
        {connected && (
          <span
            id="cping"
            className={'cping' + (connPing?.st === 'ok' ? ' ok' : connPing?.st === 'failed' ? ' failed' : connPing?.st === 'testing_ping' ? ' testing' : '')}
            title="Click to re-check ping"
            onClick={rePingConn}
          >
            {connPing?.st === 'testing_ping'
              ? 'pinging…'
              : connPing?.st === 'ok'
                ? connPing.delay + ' ms'
                : connPing?.st === 'failed'
                  ? 'ping ✕'
                  : 'ping'}
          </span>
        )}
        {connected && (
          <span
            id="cexit"
            className={'cexit' + (exit?.cls ? ' ' + exit.cls : '')}
            title={exit?.title || 'Check the public exit IP and country as seen from the other end of the tunnel'}
            onClick={doCheckExit}
          >
            {exit?.label || '🌍 ' + tt('check IP')}
          </span>
        )}
        {/* className="on" rather than an inline display, so the narrow-window
            media query can hide these chips (inline styles always win). */}
        {connected && (
          <div id="cports" className="on">
            {isProxy ? (
              <>
                <div
                  className="pchip"
                  onClick={() => navigator.clipboard.writeText('127.0.0.1:' + conn.http_port).catch(() => {})}
                >
                  HTTP&nbsp;<b>{conn.http_port}</b>
                </div>
                <div
                  className="pchip"
                  onClick={() => navigator.clipboard.writeText('127.0.0.1:' + conn.socks_port).catch(() => {})}
                >
                  SOCKS5&nbsp;<b>{conn.socks_port}</b>
                </div>
              </>
            ) : (
              <>
                <div className="pchip">
                  TUN&nbsp;<b>{conn.tun_iface || 'vair-tun'}</b>
                </div>
                <div className="pchip" style={{ pointerEvents: 'none', color: 'var(--dim)' }}>
                  all traffic routed
                </div>
              </>
            )}
          </div>
        )}
        {!isActive && conn?.status !== 'disconnecting' && (
          <div className="mode-wrap" id="mode-wrap">
            <button
              className={'mpill' + (selectedMode === 'proxy' ? ' sel-proxy' : '')}
              id="mp-proxy"
              onClick={() => pickMode('proxy')}
            >
              proxy
            </button>
            <button
              className={
                'mpill' +
                (selectedMode === 'tun' ? ' sel-tun' : '') +
                (appInfo.caps?.tun_ready === false ? ' off' : '')
              }
              id="mp-tun"
              onClick={() => {
                // 1.10 setMode: TUN needs sing-box + admin; clicking while
                // unelevated offers the UAC relaunch instead of selecting.
                // "no_helper" is the Linux case, and from the user's side it is
                // the same gesture — one prompt — even though underneath it
                // installs a service rather than relaunching as root. Any other
                // reason is not actionable, so the click does nothing instead of
                // pretending.
                const why = appInfo.caps?.tun_block_reason
                if (appInfo.caps?.tun_ready === false) {
                  if (why === 'not_admin' || why === 'no_helper') acquirePrivilege()
                  return
                }
                pickMode('tun')
              }}
            >
              tun
            </button>
            {/* The note says WHY TUN is unavailable, and the reason comes from
                the backend rather than being inferred here. Inferring it read
                "requires admin" on Linux, where elevation would change nothing:
                TUN needs the privileged helper that does not exist yet, and
                offering a relaunch-as-root that cannot help is worse than
                saying so. */}
            {appInfo.caps?.tun_ready === false && appInfo.caps?.tun_block_reason && (
              <span
                className="mtip"
                id="mtip"
                style={
                  appInfo.caps.tun_block_reason !== 'not_admin' &&
                  appInfo.caps.tun_block_reason !== 'no_helper'
                    ? { cursor: 'default' }
                    : undefined
                }
                title={
                  appInfo.caps.tun_block_reason === 'not_admin'
                    ? 'Restart Vair as Administrator'
                    : appInfo.caps.tun_block_reason === 'no_helper'
                      ? tt('Installs a small system service that creates the tunnel. Asked once.')
                      : undefined
                }
                onClick={() => {
                  const why = appInfo.caps?.tun_block_reason
                  if (why === 'not_admin' || why === 'no_helper') acquirePrivilege()
                }}
              >
                {appInfo.caps.tun_block_reason === 'not_admin'
                  ? 'requires admin ↗'
                  : appInfo.caps.tun_block_reason === 'no_helper'
                    ? tt('install helper ↗')
                    : appInfo.caps.tun_block_reason === 'no_singbox'
                      ? 'sing-box not found'
                      : tt('not available on this system')}
              </span>
            )}
          </div>
        )}
        {isActive && (
          <button className="btn red" id="btn-dc" onClick={() => ConnService.Disconnect()}>
            disconnect
          </button>
        )}
      </div>

      {/* ── Compact-tab dropdown list ── */}
      {/* ── Type-filter dropdown (ctrl+click keeps the multi-select) ── */}
      {protoDD && (
        <FloatingMenu className="tab-dd-menu proto-dd-menu" x={protoDD.x} y={protoDD.y}>
          <div
            className={'tab-dd-item' + (protos.length === 0 ? ' active' : '')}
            onClick={() => {
              setProtos([])
              setProtoDD(null)
            }}
          >
            all
          </div>
          {PROTOS.map(([label, val]) => (
            <div
              key={val}
              className={'tab-dd-item' + (protos.includes(val) ? ' active' : '')}
              onClick={(e) => {
                onProtoClick(e, val)
                // Ctrl+click keeps the menu open so several types can be picked.
                if (!(e.ctrlKey || e.metaKey)) setProtoDD(null)
              }}
            >
              {label}
            </div>
          ))}
        </FloatingMenu>
      )}

      {tabDD && (
        <FloatingMenu className="tab-dd-menu" x={tabDD.x} y={tabDD.y}>
          {tabs
            .filter((tb) => !(tb.id === 'main' && stg?.sources_enabled === false))
            .map((tb) => (
              <div
                key={tb.id}
                className={'tab-dd-item' + (tb.id === activeTab ? ' active' : '')}
                onClick={() => {
                  switchTab(tb.id)
                  setTabDD(null)
                }}
              >
                {tb.name}
              </div>
            ))}
        </FloatingMenu>
      )}

      {/* ── Startup "new version" banner (1.10 upd-banner) ── */}
      {updBanner && (
        <div className="upd-banner" id="upd-banner">
          <span className="upd-banner-txt">
            {tt('New version available')}: <b>{updBanner.latest}</b>
            {updBanner.notes ? ' — ' + updBanner.notes : ''}
          </span>
          <span className="upd-banner-btns">
            <button
              className="btn sm"
              onClick={() => {
                setUpdBanner(null)
                openSettings()
              }}
            >
              {tt('Update')}
            </button>
            <button
              className="btn ghost sm"
              onClick={() => {
                UpdateService.Dismiss(updBanner.latest)
                setUpdBanner(null)
              }}
            >
              {tt("Don't show again")}
            </button>
            <button className="btn ghost sm" title={tt('Close')} onClick={() => setUpdBanner(null)}>
              ✕
            </button>
          </span>
        </div>
      )}

      {/* ── Toast (1.10 reload-toast) ── */}
      {toast && <div className={'reload-toast' + (toast.fade ? ' fade' : '')}>{toast.text}</div>}

      {/* ── Context menus ── */}
      {menu && (
        <FloatingMenu className="ctx-menu" x={menu.x} y={menu.y}>
          {menu.kind === 'row' && (
            <>
              {/* Ordered by what each item acts on, and inside that by how often
                  it is wanted: this row, then the measurements of it, then the
                  destructive ones, then the things that are about the TAB and
                  merely happen to have been opened over a row. Before this,
                  "rename" sat between the service check and delete, and adding a
                  config was three items below deleting one. */}
              {selCount > 1 && (
                <>
                  <div className="ctx-menu-item" onClick={connectChain}>
                    {tt('Connect as chain')} ({selCount})
                  </div>
                  <div className="ctx-sep" />
                </>
              )}
              <div className="ctx-menu-item" onClick={copySelected}>
                {tt('Copy')} {selLabel}
              </div>
              {selCount <= 1 && (
                <div className="ctx-menu-item" onClick={() => openConfigEditView(menu.idx, activeTab === 'main')}>
                  {tt(activeTab === 'main' ? 'View config…' : 'Edit config…')}
                </div>
              )}
              {selCount <= 1 && activeTab !== 'main' && (
                <div className="ctx-menu-item" onClick={() => startRename(menu.idx)}>
                  {tt('Rename')}
                </div>
              )}
              {selCount <= 1 && (
                <div className="ctx-menu-item" onClick={() => showQR(menu.idx)}>
                  {tt('Show QR')}
                </div>
              )}
              <div className="ctx-menu-item" onClick={selectAllInView}>
                {tt('Select all')}
              </div>

              <div className="ctx-sep" />
              <div className="ctx-menu-item" onClick={() => testSelected(menu.idx, false)}>
                {tt('Test ping')} — {selLabel}
              </div>
              <div className="ctx-menu-item" onClick={() => testSelected(menu.idx, true)}>
                {tt('Test speed')} — {selLabel}
              </div>
              <div className="ctx-menu-item" onClick={() => setUnlockOpen(true)}>
                {tt('Service check…')}
              </div>
              {selCount <= 1 && (
                <div
                  className="ctx-menu-item"
                  onClick={() => {
                    const i = menu.idx
                    setMenu(null)
                    setHistoryIdx(i)
                  }}
                >
                  {tt('Measurement history…')}
                </div>
              )}

              {activeTab !== 'main' && (
                <>
                  <div className="ctx-sep" />
                  <div className="ctx-menu-item danger" onClick={() => deleteSelectedRows(menu.idx)}>
                    {tt('Delete')} {selLabel}
                  </div>
                  {selCount <= 1 && (
                    <div
                      className="ctx-menu-item danger"
                      onClick={() => {
                        setMenu(null)
                        ConfigService.DeleteDuplicatesOf(menu.idx).then((n: number) =>
                          showToast(n > 0 ? tt('Duplicates removed') + ': ' + n : tt('No duplicates of this one')),
                        )
                      }}
                    >
                      {tt('Delete duplicates of this one')}
                    </div>
                  )}
                  {(stats?.failed ?? 0) > 0 && (
                    <div
                      className="ctx-menu-item danger"
                      onClick={() => {
                        ConfigService.DeleteFailed(activeTab)
                        clearSelection()
                      }}
                    >
                      {tt('Delete failed ping/speed')} ({stats.failed})
                    </div>
                  )}

                  <div className="ctx-sep" />
                  <div className="ctx-menu-item" onClick={openConfigAdd}>
                    {tt('Add config…')}
                  </div>
                  <div className="ctx-menu-item" onClick={pasteFromClipboard}>
                    {tt('Paste configs')}
                  </div>
                  <div className="ctx-menu-item" onClick={addWARP}>
                    {warpBusy ? tt('Getting WARP…') : tt('Get a free WARP config')}
                  </div>
                  {/* Screen capture needs a portal handshake on Wayland that
                      this build does not do, so the item is hidden rather than
                      left to fail with "no QR found" — which reads as "your QR
                      is bad", not "this cannot work here". Scanning from a FILE
                      is unaffected and stays. */}
                  {!IS_REMOTE && appInfo?.caps?.qr_screen !== false && (
                    <div className="ctx-menu-item" onClick={scanQRScreen}>
                      {tt('Scan QR from screen')}
                    </div>
                  )}
                  {/* The screen scanned and the file chosen are the computer's,
                      so from a phone both are gestures with no visible result. */}
                  {!IS_REMOTE && (
                    <div className="ctx-menu-item" onClick={scanQRFile}>
                      {tt('Scan QR from file')}
                    </div>
                  )}
                </>
              )}
            </>
          )}
          {menu.kind === 'empty' && (
            <>
              {activeTab !== 'main' && (
                <div className="ctx-menu-item" onClick={openConfigAdd}>
                  {tt('Add config…')}
                </div>
              )}
              <div className="ctx-menu-item" onClick={pasteFromClipboard}>
                {tt('Paste configs')}
              </div>
              <div className="ctx-menu-item" onClick={addWARP}>
                {warpBusy ? tt('Getting WARP…') : tt('Get a free WARP config')}
              </div>
              {!IS_REMOTE && appInfo?.caps?.qr_screen !== false && (
                <div className="ctx-menu-item" onClick={scanQRScreen}>
                  {tt('Scan QR from screen')}
                </div>
              )}
              {!IS_REMOTE && (
                <div className="ctx-menu-item" onClick={scanQRFile}>
                  {tt('Scan QR from file')}
                </div>
              )}
            </>
          )}
          {menu.kind === 'tab' &&
            (menu.tab.is_main ? (
              <div className="ctx-menu-item" onClick={() => setSourcesOpen(true)}>
                {tt('Settings')}
              </div>
            ) : (
              <>
                <div className="ctx-menu-item" onClick={() => setTabModalId(menu.tab.id)}>
                  {tt('Settings')}
                </div>
                <div className="ctx-sep" />
                <div className="ctx-menu-item danger" onClick={() => TabService.Delete(menu.tab.id)}>
                  {tt('Delete tab')}
                </div>
              </>
            ))}
        </FloatingMenu>
      )}

      {/* ── QR modal ── */}
      {qrSrc && (
        /* modal-nested: the QR is often opened from inside the tab-settings
           modal, so it has to stack above it. */
        <div className="modal-overlay modal-nested" onClick={() => setQrSrc(null)}>
          <div
            className="modal-box"
            style={{ width: 'auto', textAlign: 'center' }}
            onClick={(e) => e.stopPropagation()}
          >
            <div className="qr-wrap">
              <img className="qr-img" src={qrSrc} alt="QR" />
            </div>
            <div className="modal-btns" style={{ justifyContent: 'center' }}>
              <button className="btn ghost" onClick={() => setQrSrc(null)}>
                OK
              </button>
            </div>
          </div>
        </div>
      )}

      {/* ── Tab settings modal (exact 1.10 port, live apply) ── */}
      {tabModalId && (
        <TabModal
          tabId={tabModalId}
          onClose={() => setTabModalId(null)}
          onShowQR={showQRText}
          lang={lang}
        />
      )}

      {/* ── Add / edit / view a config manually (the form → share-link modal) ── */}
      {configModal && (
        <ConfigModal
          mode={configModal.mode}
          tabId={configModal.tabId}
          idx={configModal.idx}
          raw={configModal.raw}
          onClose={() => setConfigModal(null)}
          lang={lang}
          notify={showToast}
        />
      )}

      {/* ── Sources (main tab) settings modal (exact 1.10 port, live apply) ── */}
      {sourcesOpen && (
        <SourcesModal onClose={() => setSourcesOpen(false)} onShowQR={showQRText} lang={lang} />
      )}

      {/* ── Settings modal (exact 1.10 port, instant apply) ── */}
      {unlockOpen && (
        <UnlockModal
          onClose={() => setUnlockOpen(false)}
          lang={lang}
          tabId={activeTab}
          selected={[...selected]}
          total={total}
          sort={sort}
          filter={filter}
          proto={protos}
        />
      )}
      {sessionOpen && <SessionModal onClose={() => setSessionOpen(false)} lang={lang} />}
      {historyIdx >= 0 && (
        <HistoryModal idx={historyIdx} onClose={() => setHistoryIdx(-1)} lang={lang} />
      )}
      {connsOpen && (
        <ConnectionsModal
          onClose={() => setConnsOpen(false)}
          lang={lang}
          mode={conn?.mode || ''}
          verbose={!!stg?.rule_stats || !!stg?.verbose_logs}
        />
      )}
      {settingsOpen && stg && (
        <SettingsModal
          stg={stg}
          apply={applySettings}
          refresh={refreshStg}
          onClose={() => setSettingsOpen(false)}
          notify={showToast}
          lang={lang}
          version={version}
          caps={appInfo?.caps}
        />
      )}

      {/* ── AUTO panel (exact 1.10 port; forced open in the detached window) ── */}
      {STANDALONE_AUTO && stg && (
        <AutoModal
          stg={stg}
          apply={applySettings}
          auto={auto}
          setAutoEnabled={setAutoEnabled}
          autoLast={autoLast}
          conn={conn}
          tabs={tabs}
          lang={lang}
          onClose={() => AutoService.CloseAuto()}
        />
      )}

      {/* Browser (phone) mode: the AUTO panel as an in-page modal — OpenAuto
          would create a native window on the PC, useless from the phone. */}
      {IS_REMOTE && remoteAutoOpen && stg && (
        <AutoModal
          stg={stg}
          apply={applySettings}
          auto={auto}
          setAutoEnabled={setAutoEnabled}
          autoLast={autoLast}
          conn={conn}
          tabs={tabs}
          lang={lang}
          onClose={() => setRemoteAutoOpen(false)}
        />
      )}
    </HintHost>
  )
}
