// Vair 2.0 main window — an exact port of the 1.10 web/index.html DOM (same
// ids/classes, so style.css is the verbatim 1.10 stylesheet). The main window
// stays English on purpose (as in 1.10); context menus/chips translate via t10.
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
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
} from '../bindings/vair'
import { t10 } from './i18n'
import SettingsModal from './SettingsModal'
import TabModal from './TabModal'
import SourcesModal from './SourcesModal'
import AutoModal from './AutoModal'
import ConfigModal from './ConfigModal'

type Row = NonNullable<Awaited<ReturnType<typeof ConfigService.Window>>>[number]
type Sort = 'idx' | 'ping' | 'speed'
type Menu =
  | { kind: 'row'; x: number; y: number; idx: number }
  | { kind: 'empty'; x: number; y: number }
  | { kind: 'tab'; x: number; y: number; tab: any }

const ROW_H = 33 // measured 1.10 row: act-cell button 22px + td padding 10 + border 1
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
]
// The detached AUTO window loads the same app at /?view=auto; the 1.10
// body.auto-standalone CSS then hides everything except the titlebar + panel.
const STANDALONE_AUTO = new URLSearchParams(window.location.search).get('view') === 'auto'

const FILTER_TITLE =
  'Filter by name, host, type, transport or security. + combines conditions: germany+ws+tls needs all three (name, transport, security); germany+poland matches either (same column). Use field: to target a column, e.g. name:russia.'

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
function flagEmoji(cc: string): string {
  if (!cc || cc.length !== 2) return ''
  cc = cc.toUpperCase()
  if (!/^[A-Z]{2}$/.test(cc)) return ''
  return String.fromCodePoint(0x1f1e6 + (cc.charCodeAt(0) - 65), 0x1f1e6 + (cc.charCodeAt(1) - 65))
}
function rawBody(raw: string): string {
  const i = (raw || '').lastIndexOf('#')
  return i >= 0 ? raw.slice(0, i) : raw || ''
}
function protoLabel(pr: string): string {
  return pr === 'hysteria2' ? 'hy2' : pr
}
function fmtLogTime(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => (n < 10 ? '0' + n : '' + n)
  return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds())
}
// copyText copies to the OS clipboard with a WebView2-safe fallback: the async
// navigator.clipboard API is often blocked in the embedded webview (non-secure
// custom scheme), so fall back to a hidden-textarea + execCommand('copy').
function copyText(text: string) {
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

function containsNodeURL(text: string): boolean {
  if (!text) return false
  return /(vless|vmess|trojan|ss|hysteria2|hy2|tuic):\/\//i.test(text)
}
// looksLikeSubURL: a single bare http(s) link — a subscription the server
// fetches and imports. Used for scanned QRs, pasted links and deep links.
function looksLikeSubURL(text: string): boolean {
  const t = (text || '').trim()
  return t.indexOf('\n') < 0 && /^https?:\/\/\S+$/i.test(t)
}
function pasteworthy(text: string): boolean {
  if (!text) return false
  if (containsNodeURL(text)) return true
  if (looksLikeSubURL(text)) return true
  // base64-encoded subscription body
  const t = text.trim()
  return t.length > 50 && /^[A-Za-z0-9+/=\r\n]+$/.test(t)
}

export default function App() {
  // table / view state
  const [total, setTotal] = useState(0)
  const [tabTotal, setTabTotal] = useState(0)
  const [stats, setStats] = useState<any>(null)
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
  const [winW, setWinW] = useState(0)
  // logs
  const [logsOpen, setLogsOpen] = useState(false)
  const [logLines, setLogLines] = useState<any[]>([])
  const [logSrc, setLogSrc] = useState('')
  const [logLvl, setLogLvl] = useState('')
  const [logAuto, setLogAuto] = useState(true)
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
  useEffect(() => {
    viewSeqRef.current++
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
    for (let w = startW; w <= endW; w += WINDOW) {
      // Fresh for this run? skip. Otherwise (re)fetch — stale rows stay rendered
      // until the fresh window overwrites them in place.
      if (winFreshRef.current.has(w) || pendingRef.current.has(w)) continue
      pendingRef.current.add(w)
      ConfigService.Window(activeTab, sort, filter, protos, w, WINDOW).then((rows) => {
        if (seq !== viewSeqRef.current) return // view moved on — drop stale response
        pendingRef.current.delete(w)
        ;(rows ?? []).forEach((r, i) => targetCache.set(w + i, r))
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
    refreshTabs()
    switchTab(nt.id)
  }

  // ── row helpers (exact buildRow logic) ──────────────────────────────
  const connLit = (r: Row): boolean => {
    const cs = conn
    if (!cs || (cs.status !== 'connected' && cs.status !== 'connecting')) return false
    const chainRaws: string[] = cs.chain_raws || []
    if (chainRaws.length && r.raw && chainRaws.indexOf(r.raw) >= 0) return true
    if (cs.conn_raw) return rawBody(cs.conn_raw) === rawBody(r.raw)
    return cs.conn_tab === activeTab && cs.entry_index === r.index
  }
  const findRowByIndex = (idx: number): Row | null => {
    for (const row of cacheRef.current.values()) if (row.index === idx) return row
    return null
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
      if (rawBody(row.raw) === rawBody(cs.conn_raw)) return row.index
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
  // connectChain connects the selection (top→bottom screen order) as a chain.
  const connectChain = () => {
    const sel = [...selRef.current]
    if (sel.length < 2) return
    ensureAllIndices(() => {
      const pos = allPosRef.current
      const ordered = sel.sort((a, b) => (pos?.get(a) ?? 0) - (pos?.get(b) ?? 0))
      ConnService.ConnectChain(ordered, selectedMode).then((err) => {
        if (err) showToast(err)
      })
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
  // addSubToTabSource appends a subscription URL to a tab's persistent
  // sources (server re-fetches) — 1.10 addSubToTabSource.
  const addSubToTabSource = (tabId: string, url: string) => {
    // The add triggers a re-fetch whose "+N" delta toast would stack on ours.
    suppressDeltaRef.current = true
    setTimeout(() => (suppressDeltaRef.current = false), 8000)
    TabService.AddSourceURL(tabId, url).then((added) =>
      showToast(added ? tt('Subscription added to tab sources') : tt('Subscription already in tab sources')),
    )
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
    const isSub = looksLikeSubURL(trimmed)
    const run = (tabId: string) => {
      if (isSub) {
        addSubToTabSource(tabId, trimmed)
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

  // ── conn-bar chips ────────────────────────────────────────────────
  const rePingConn = () => {
    const idx = findConnIdx()
    if (idx >= 0) TestService.PingOne(idx)
    else TestService.PingConnected() // connected config lives on another tab
  }
  const doCheckExit = () => {
    if (!connRef.current || connRef.current.status !== 'connected') return
    if (exit?.cls === 'testing') return
    setExit({ cls: 'testing', label: '🌍 ' + tt('checking…') })
    ConnService.CheckExit().then((d: any) => {
      if (!d || d.error) {
        setExit({ cls: 'failed', label: '🌍 ' + (d?.error || tt('check failed')) + ' ↻' })
        return
      }
      const flag = flagEmoji(d.country_code)
      const loc = [d.city, d.country].filter(Boolean).join(', ')
      setExit({
        cls: 'ok',
        label: (flag ? flag + ' ' : '') + (loc || d.country_code || '?') + (d.ip ? ' · ' + d.ip : ''),
        title: (d.isp ? d.isp + ' — ' : '') + 'Exit IP as seen through the tunnel. Click to re-check.',
      })
    })
  }

  // ── settings modal (instant apply, as in 1.10 — no Save button) ────
  const applyTheme = (th?: string) => document.body.classList.toggle('theme-light', th === 'light')
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
      document.documentElement.style.setProperty('--modal-fs-base', (patch.modal_font_size || 11) + 'px')
    SettingsService.Set(merged).then((applied: any) => {
      setStg(applied)
      setAuto(!!applied?.auto_connect)
    })
  }
  const setAutoEnabled = (on: boolean) => {
    setAuto(on)
    AutoService.SetEnabled(on)
  }

  // ── logs ──────────────────────────────────────────────────────────
  const openLogs = () => {
    logsOpenRef.current = true
    setLogsOpen(true)
    LogService.Get().then((ls) => setLogLines(ls ?? []))
  }
  const closeLogs = () => {
    logsOpenRef.current = false
    setLogsOpen(false)
  }
  const logPass = (l: any): boolean => {
    if (logSrc && l.src !== logSrc) return false
    if (logLvl) {
      const rank: any = { raw: 1, info: 1, warn: 2, error: 3 }
      const need: any = { info: 1, warn: 2, error: 3 }
      if ((rank[l.lvl] || 1) < (need[logLvl] || 0)) return false
    }
    return true
  }
  const copyLogs = () => {
    const text = logLines
      .filter(logPass)
      .map((l) => fmtLogTime(l.t) + ' ' + (l.src || '') + ' ' + l.msg)
      .join('\n')
    navigator.clipboard.writeText(text).catch(() => {})
  }
  useEffect(() => {
    if (logAuto && logViewRef.current) logViewRef.current.scrollTop = logViewRef.current.scrollHeight
  }, [logLines, logAuto])

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
      if (s?.language) setLang(s.language)
      if (s?.last_connected_raw) setLastRaw(s.last_connected_raw)
      if (s?.last_connected_mode === 'tun') setSelectedMode('tun')
      document.documentElement.style.setProperty('--modal-fs-base', (s?.modal_font_size || 11) + 'px')
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
          if (ccs && ccs.status === 'connected' && ccs.conn_raw && p.raw && rawBody(p.raw) === rawBody(ccs.conn_raw))
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
            })
            bump((v) => v + 1)
            break
          }
        }
        scheduleStatsRefresh() // live ping ok / failed / best counters
        scheduleResort() // live re-order when sorted by ping/speed
        const cs = connRef.current
        if (cs && cs.status === 'connected' && cs.conn_raw && p.raw && rawBody(p.raw) === rawBody(cs.conn_raw))
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
        const batch = e?.data?.payload
        if (Array.isArray(batch)) setLogLines((prev) => [...prev, ...batch].slice(-2000))
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
      const rows = new Set<number>()
      btns.forEach((b) => rows.add((b as HTMLElement).offsetTop))
      if (rows.size === btns.length) compact = true
    }
    if (compact) {
      bar.classList.add('compact')
      tbar.classList.add('compact')
    } else if (tabDD) {
      setTabDD(null)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tabs, activeTab, winW, stg?.sources_enabled])

  // ── derived ───────────────────────────────────────────────────────
  const connected = !!conn && conn.status === 'connected'
  const connecting = conn?.status === 'connecting'
  const isError = conn?.status === 'error'
  const isActive = connected || connecting || isError
  const isProxy = conn?.mode !== 'tun'
  const stateCls = connected ? (isProxy ? 'cp' : 'conn-tun') : connecting ? 'cc' : isError ? 'ce' : ''
  const clabel = connected
    ? isProxy
      ? 'SYSTEM PROXY'
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

  const onProtoClick = (e: React.MouseEvent, val: string) => {
    if (e.ctrlKey || e.metaKey)
      setProtos((p) => (p.includes(val) ? p.filter((x) => x !== val) : [...p, val]))
    else setProtos([val])
  }

  const padTop = items.length > 0 ? items[0].start : 0
  const padBottom = items.length > 0 ? virtualizer.getTotalSize() - items[items.length - 1].end : 0

  // ── row painter (exact buildRow port) ───────────────────────────────
  const renderRow = (vi: { index: number }) => {
    const r = cacheRef.current.get(vi.index)
    const pos = vi.index + 1
    if (!r)
      return (
        <tr key={'p' + vi.index} style={{ height: ROW_H }}>
          <td className="ci">{pos}</td>
          <td colSpan={8} />
        </tr>
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
      st = r.speed_live > 0 ? r.speed_live.toFixed(1) + ' MB/s' : 'connecting…'
    } else if (r.speed_st === 'ok') {
      sp = 'pill ok-speed'
      st = r.speed.toFixed(2) + ' MB/s'
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
    const isLast = !!(r.raw && lastRaw && r.raw === lastRaw)
    const hostport = (r.host || '') + (r.port ? ':' + r.port : '')
    const cls =
      (selected.has(r.index) ? 'selected ' : '') +
      (flashTabRef.current === activeTab && flashIdxRef.current.has(r.index) ? 'row-new ' : '') +
      (rowLit ? (conn?.mode === 'tun' ? 'row-ct' : 'row-cp') : '')

    return (
      <tr
        key={r.index}
        className={cls.trim() || undefined}
        style={{ height: ROW_H }}
        onClick={(e) => {
          if ((e.target as HTMLElement).closest('.act-cell')) return
          toggleRowSelect(r.index, e)
        }}
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
        <td className="ci">{pos}</td>
        <td className="cpr">
          <span className={'pb ' + (r.proto || '')} title={r.proto}>
            {protoLabel(r.proto || '?')}
          </span>
        </td>
        <td className="cn">
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
        </td>
        <td className="ch">
          <span className="nh" title={hostport}>
            {hostport}
          </span>
        </td>
        <td className="ct">
          <span className={'nb ' + ncls}>{r.net || 'tcp'}</span>
        </td>
        <td className="cs">
          <span className={'sb ' + (r.sec || 'none')} title={r.sec || 'none'}>
            {r.sec || 'none'}
          </span>
        </td>
        <td className="cp2">
          <div className="vc">
            <span className={pp} title={pt}>
              {pt}
            </span>
          </div>
        </td>
        <td className="csp">
          <div className="vc">
            <span className={sp} title={st}>
              {st}
            </span>
          </div>
        </td>
        <td className="ca">
          <div className="act-cell">
            {isConn && inChain && !isChainExit ? (
              <span className="chain-hop-tag" title={tt('chain hop')}>
                ⛓
              </span>
            ) : isConn ? (
              <button className="btn sm-disc" title="Disconnect" onClick={() => ConnService.Disconnect()}>
                disconnect
              </button>
            ) : (
              <button
                className="btn sm ghost"
                title={selectedMode === 'tun' ? 'TUN mode (all traffic)' : 'System Proxy (HTTP/SOCKS)'}
                onClick={() => ConnService.Connect(r.index, selectedMode)}
              >
                connect
              </button>
            )}
            <button className="btn sm ghost" title="Ping" onClick={() => TestService.PingOne(r.index)}>
              ping
            </button>
            <button className="btn sm ghost" title="Speed" onClick={() => TestService.SpeedOne(r.index)}>
              speed
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
        </td>
      </tr>
    )
  }

  const selCount = selected.size
  const selLabel = selCount > 1 ? selCount + ' ' + tt('configs') : tt('config')

  return (
    <>

      {/* ── Custom title bar ── */}
      <div id="titlebar" className="active">
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
              else if (stg?.tray_enabled) Window.Hide() // minimize-to-tray on close
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
          <div className="stat">
            <span className="sv ms" id="s-ms">
              {stats?.best_ping ? stats.best_ping + 'ms' : '—'}
            </span>
            <span className="sl">best ping</span>
          </div>
          <div className="stat">
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
              const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
              setTabDD((dd) => (dd ? null : { x: r.left, y: r.bottom + 4 }))
            }}
            onContextMenu={(e) => {
              e.preventDefault()
              const act = tabs.find((t) => t.id === activeTab)
              if (act) setMenu({ kind: 'tab', x: e.clientX, y: e.clientY, tab: act })
            }}
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
                }}
                onClick={() => {
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
          <span id="fc" style={{ fontSize: 11, color: 'var(--dim)' }}>
            {filtering ? total + ' / ' + tabTotal : ''}
          </span>
          <span className="tl tl-lbl" style={{ marginLeft: 6 }}>
            type
          </span>
          <div className="proto-group" title="Click to filter by one type. Ctrl+click to multi-select.">
            <button
              className={'proto-btn' + (protos.length === 0 ? ' active' : '')}
              id="proto-all"
              onClick={() => setProtos([])}
            >
              all
            </button>
            {PROTOS.map(([label, val]) => (
              <button
                key={val}
                className={'proto-btn' + (protos.includes(val) ? ' active' : '')}
                id={'proto-' + val}
                onClick={(e) => onProtoClick(e, val)}
              >
                {label}
              </button>
            ))}
          </div>
          <span className="tl tl-lbl" style={{ marginLeft: 6 }}>
            sort
          </span>
          <div className="sort-group">
            <button
              className={'sort-btn' + (sort === 'idx' ? ' active' : '')}
              id="sort-idx"
              onClick={() => setSort('idx')}
            >
              default
            </button>
            <button
              className={'sort-btn' + (sort === 'ping' ? ' active' : '')}
              id="sort-ping"
              onClick={() => setSort('ping')}
            >
              ping ↑
            </button>
            <button
              className={'sort-btn' + (sort === 'speed' ? ' active' : '')}
              id="sort-speed"
              onClick={() => setSort('speed')}
            >
              speed ↓
            </button>
          </div>
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
      {/* Header lives OUTSIDE the scroll viewport (.thw, non-scrolling) so the
          vertical scrollbar spans only the body rows and never overlaps the
          column headers. Both tables share table-layout:fixed and the same width
          classes; .thw's right padding mirrors the body's 8px scrollbar gutter so
          the columns line up exactly. */}
      {!loading && (
        <div className="thw">
          <table id="tblh">
            <colgroup>
              <col className="ci" />
              <col className="cpr" />
              <col className="cn" />
              <col className="ch" />
              <col className="ct" />
              <col className="cs" />
              <col className="cp2" />
              <col className="csp" />
              <col className="ca" />
            </colgroup>
            <thead>
              <tr>
                <th className="ci">#</th>
                <th className="cpr">Type</th>
                <th className="cn">Name</th>
                <th className="ch">Host</th>
                <th className="ct">Transport</th>
                <th className="cs">Security</th>
                <th className="cp2">Ping</th>
                <th className="csp">Speed</th>
                <th className="ca">Actions</th>
              </tr>
            </thead>
          </table>
        </div>
      )}
      <div
        className="tw"
        ref={twRef}
        onContextMenu={(e) => {
          if ((e.target as HTMLElement).closest('tr')) return
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
        ) : total === 0 && activeTab !== 'main' ? (
          /* Empty user tab: show the three ways to fill it (discoverability — the
             context-menu entry points are invisible until you know to right-click). */
          <div className="center-msg" id="msg-area">
            <p style={{ marginBottom: 12 }}>{tt('This tab is empty')}</p>
            <div style={{ display: 'flex', gap: 8, justifyContent: 'center', flexWrap: 'wrap' }}>
              <button className="btn ghost" onClick={openConfigAdd}>{tt('Add config…')}</button>
              <button className="btn ghost" onClick={pasteFromClipboard}>{tt('Paste configs')}</button>
              <button className="btn ghost" onClick={() => setTabModalId(activeTab)}>{tt('Add a source')}</button>
            </div>
          </div>
        ) : (
          <table id="tbl">
            <colgroup>
              <col className="ci" />
              <col className="cpr" />
              <col className="cn" />
              <col className="ch" />
              <col className="ct" />
              <col className="cs" />
              <col className="cp2" />
              <col className="csp" />
              <col className="ca" />
            </colgroup>
            <tbody id="tb">
              {padTop > 0 && (
                <tr className="vspacer" style={{ height: padTop }}>
                  <td colSpan={9} />
                </tr>
              )}
              {items.map((vi) => renderRow(vi))}
              {padBottom > 0 && (
                <tr className="vspacer" style={{ height: padBottom }}>
                  <td colSpan={9} />
                </tr>
              )}
            </tbody>
          </table>
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
            <select className="log-sel" id="log-src" value={logSrc} onChange={(e) => setLogSrc(e.target.value)}>
              <option value="">all</option>
              <option value="xray">xray</option>
              <option value="singbox">sing-box</option>
              <option value="vair">vair</option>
              <option value="test">test</option>
            </select>
            <select className="log-sel" id="log-lvl" value={logLvl} onChange={(e) => setLogLvl(e.target.value)}>
              <option value="">all</option>
              <option value="info">info+</option>
              <option value="warn">warn+</option>
              <option value="error">error</option>
            </select>
            <label className="log-auto">
              <input type="checkbox" checked={logAuto} onChange={(e) => setLogAuto(e.target.checked)} />{' '}
              <span id="log-auto-lbl">auto-scroll</span>
            </label>
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
              }}
            >
              clear
            </button>
            <button className="btn ghost" id="log-close" title="Close" onClick={closeLogs}>
              ✕
            </button>
          </div>
          <div className="log-view" id="log-view" ref={logViewRef}>
            {logLines.filter(logPass).map((l, i) => (
              <span key={i} className={'log-line lvl-' + (l.lvl || 'raw')}>
                <span className="lt">{fmtLogTime(l.t)}</span>
                <span className={'ls src-' + (l.src || '')}>{l.src === 'singbox' ? 'sing-box' : l.src}</span>
                {l.msg}
                {'\n'}
              </span>
            ))}
            {logLines.length === 0 && <span className="log-empty">— no log lines yet —</span>}
          </div>
        </div>
      )}

      {/* ── Connection Bar ── */}
      <div id="conn-bar" className={stateCls}>
        <div className={'cdot' + (stateCls ? ' ' + stateCls : '')} id="cdot" />
        <span id="clabel" className={stateCls}>
          {clabel}
        </span>
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
          {isError && <span style={{ color: 'var(--red)' }}>{conn?.error || 'unknown error'}</span>}
        </span>
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
        {connected && (
          <div id="cports" style={{ display: 'flex' }}>
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
              onClick={() => setSelectedMode('proxy')}
            >
              proxy
            </button>
            <button
              className={
                'mpill' +
                (selectedMode === 'tun' ? ' sel-tun' : '') +
                (!appInfo.singbox_available || !appInfo.is_admin ? ' off' : '')
              }
              id="mp-tun"
              onClick={() => {
                // 1.10 setMode: TUN needs sing-box + admin; clicking while
                // unelevated offers the UAC relaunch instead of selecting.
                if (!appInfo.singbox_available) return
                if (!appInfo.is_admin) {
                  ConnService.RestartAdmin()
                  return
                }
                setSelectedMode('tun')
              }}
            >
              tun
            </button>
            {(!appInfo.singbox_available || !appInfo.is_admin) && (
              <span
                className="mtip"
                id="mtip"
                style={!appInfo.singbox_available ? { cursor: 'default' } : undefined}
                title={appInfo.singbox_available ? 'Restart Vair as Administrator' : undefined}
                onClick={() => {
                  if (appInfo.singbox_available && !appInfo.is_admin) ConnService.RestartAdmin()
                }}
              >
                {appInfo.singbox_available ? 'requires admin ↗' : 'sing-box not found'}
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
      {tabDD && (
        <div className="tab-dd-menu" style={{ left: tabDD.x, top: tabDD.y }}>
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
        </div>
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
        <div className="ctx-menu" style={{ left: menu.x, top: menu.y }}>
          {menu.kind === 'row' && (
            <>
              <div className="ctx-menu-item" onClick={copySelected}>
                {tt('Copy')} {selLabel}
              </div>
              <div className="ctx-menu-item" onClick={selectAllInView}>
                {tt('Select all')}
              </div>
              {selCount <= 1 && (
                <div className="ctx-menu-item" onClick={() => showQR(menu.idx)}>
                  {tt('Show QR')}
                </div>
              )}
              {selCount <= 1 && (
                <div className="ctx-menu-item" onClick={() => openConfigEditView(menu.idx, activeTab === 'main')}>
                  {tt(activeTab === 'main' ? 'View config…' : 'Edit config…')}
                </div>
              )}
              {selCount > 1 && (
                <div className="ctx-menu-item" onClick={connectChain}>
                  {tt('Connect as chain')} ({selCount})
                </div>
              )}
              <div className="ctx-sep" />
              <div className="ctx-menu-item" onClick={() => testSelected(menu.idx, false)}>
                {tt('Test ping')} — {selLabel}
              </div>
              <div className="ctx-menu-item" onClick={() => testSelected(menu.idx, true)}>
                {tt('Test speed')} — {selLabel}
              </div>
              {activeTab !== 'main' && (
                <>
                  {selCount <= 1 && (
                    <div className="ctx-menu-item" onClick={() => startRename(menu.idx)}>
                      {tt('Rename')}
                    </div>
                  )}
                  <div className="ctx-menu-item danger" onClick={() => deleteSelectedRows(menu.idx)}>
                    {tt('Delete')} {selLabel}
                  </div>
                  {(stats?.failed ?? 0) > 0 && (
                    <>
                      <div className="ctx-sep" />
                      <div
                        className="ctx-menu-item danger"
                        onClick={() => {
                          ConfigService.DeleteFailed(activeTab)
                          clearSelection()
                        }}
                      >
                        {tt('Delete failed ping/speed')} ({stats.failed})
                      </div>
                    </>
                  )}
                  <div className="ctx-sep" />
                  <div className="ctx-menu-item" onClick={openConfigAdd}>
                    {tt('Add config…')}
                  </div>
                  <div className="ctx-menu-item" onClick={pasteFromClipboard}>
                    {tt('Paste configs')}
                  </div>
                  <div className="ctx-menu-item" onClick={scanQRScreen}>
                    {tt('Scan QR from screen')}
                  </div>
                  <div className="ctx-menu-item" onClick={scanQRFile}>
                    {tt('Scan QR from file')}
                  </div>
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
              <div className="ctx-menu-item" onClick={scanQRScreen}>
                {tt('Scan QR from screen')}
              </div>
              <div className="ctx-menu-item" onClick={scanQRFile}>
                {tt('Scan QR from file')}
              </div>
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
        </div>
      )}

      {/* ── QR modal ── */}
      {qrSrc && (
        <div className="modal-overlay" onClick={() => setQrSrc(null)}>
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
      {settingsOpen && stg && (
        <SettingsModal
          stg={stg}
          apply={applySettings}
          refresh={refreshStg}
          onClose={() => setSettingsOpen(false)}
          notify={showToast}
          lang={lang}
          version={version}
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
          selectedMode={selectedMode}
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
          selectedMode={selectedMode}
          lang={lang}
          onClose={() => setRemoteAutoOpen(false)}
        />
      )}
    </>
  )
}
