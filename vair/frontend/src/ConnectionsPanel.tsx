// The connections panel: what is going through the tunnel now, what has gone
// through it since the program started, and — in proxy mode — what goes
// around it.
//
// A pane of the dock under the table, beside the log when both are open,
// rather than a dialog: a dialog covered the table, and the question this
// panel answers ("what is that program doing") is asked while watching the
// table and the log, not instead of them. The dock is low, so the three
// tables take turns in it — a switch in the toolbar picks one — instead of
// being stacked three deep with a scrollbar between them.
//
// Part of the main window, so its words stay English like the rest of it.
import { useEffect, useMemo, useState } from 'react'
import { ConnectionsService } from '../bindings/vair/internal/services'
import DataTable, { type Col } from './DataTable'
import Check from './Check'
import { copyText } from './clipboard'

type Row = {
  id: number
  process: string
  dest: string
  rule: string
  via: string
  up: number
  down: number
  seconds: number
  exact: boolean
}

type Bypass = {
  process: string
  dest: string
  name: string
  state: string
  count: number
}

type Total = {
  process: string
  dest: string
  via: string
  count: number
  up: number
  down: number
  seconds: number
}

type View = 'live' | 'tot' | 'bypass'

function bytes(n: number): string {
  if (!n) return '—'
  if (n < 1024) return n + ' B'
  const u = ['KB', 'MB', 'GB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return (v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2)) + ' ' + u[i]
}

function clock(sec: number): string {
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return m + ':' + (s < 10 ? '0' : '') + s
}

// ago is for the totals: how long since that pair last carried anything.
function ago(sec: number): string {
  if (sec < 5) return 'now'
  if (sec < 60) return sec + ' s ago'
  if (sec < 3600) return Math.floor(sec / 60) + ' min ago'
  return Math.floor(sec / 3600) + ' h ago'
}

const VIEW_KEY = 'conns.view'

export default function ConnectionsPanel({
  onClose,
  mode,
  verbose,
  noSystemProxy,
  style,
}: {
  onClose: () => void
  mode: string // "tun" | "proxy" | ""
  verbose: boolean // the engine is running a detailed log
  noSystemProxy: boolean // "Set system proxy" is off
  style?: React.CSSProperties
}) {
  const [rows, setRows] = useState<Row[]>([])
  const [totals, setTotals] = useState<Total[]>([])
  const [bypass, setBypass] = useState<Bypass[]>([])
  const [query, setQuery] = useState('')
  const [copied, setCopied] = useState(false)
  // Off until the user turns it on; the first poll says what it really is.
  const [tracking, setTracking] = useState(false)
  // Recording was turned on during the running session (see MissedEarlier).
  const [missed, setMissed] = useState(false)
  const [view, setViewState] = useState<View>(() => {
    try {
      const v = localStorage.getItem(VIEW_KEY)
      if (v === 'live' || v === 'tot' || v === 'bypass') return v
    } catch {
      /* no storage: the first table */
    }
    return 'live'
  })
  const setView = (v: View) => {
    setViewState(v)
    try {
      localStorage.setItem(VIEW_KEY, v)
    } catch {
      /* remembered for this session only */
    }
  }

  useEffect(() => {
    const pull = () => {
      ConnectionsService.List().then((r: any) => setRows(r || []))
      ConnectionsService.Totals().then((r: any) => setTotals(r || []))
      ConnectionsService.Tracking().then((v: boolean) => setTracking(v))
      ConnectionsService.MissedEarlier().then((v: boolean) => setMissed(v))
      // The system's connection table is read only where it is shown: reading
      // it walks every TCP connection on the machine and names their programs,
      // once a second, for a table that exists only in proxy mode.
      if (mode === 'proxy') ConnectionsService.Bypass().then((r: any) => setBypass(r || []))
    }
    pull()
    const id = setInterval(pull, 1000)
    return () => clearInterval(id)
  }, [mode])

  // "Not through Vair" is read from the system's own table and means something
  // only in proxy mode; outside it the view falls back to the first table.
  const shownView: View = view === 'bypass' && mode !== 'proxy' ? 'live' : view

  // The fractions of each table MUST sum to 1 — see DataTable.
  const liveCols: Col<Row>[] = [
    { id: 'process', label: 'Program', frac: 0.19, sort: (r) => r.process || '', cell: (r) => r.process || '—', title: (r) => r.process || '' },
    {
      id: 'dest',
      label: 'Destination',
      frac: 0.37,
      sort: (r) => r.dest,
      cell: (r) => r.dest,
      title: (r) => (r.rule ? 'Rule: ' + r.rule : r.dest),
      cls: () => 'dest',
    },
    {
      id: 'via',
      label: 'Via',
      frac: 0.12,
      sort: (r) => r.via || '',
      cell: (r) => r.via || '…',
      title: (r) => (r.via ? '' : 'The engine has not said yet'),
      cls: (r) => 'via ' + (r.via || ''),
    },
    { id: 'up', label: '↑', frac: 0.12, num: true, descFirst: true, sort: (r) => r.up, cell: (r) => (r.exact ? bytes(r.up) : '—') },
    { id: 'down', label: '↓', frac: 0.12, num: true, descFirst: true, sort: (r) => r.down, cell: (r) => (r.exact ? bytes(r.down) : '—') },
    { id: 'time', label: 'Time', frac: 0.08, num: true, sort: (r) => r.seconds, cell: (r) => clock(r.seconds) },
  ]
  const totalCols: Col<Total>[] = [
    { id: 'process', label: 'Program', frac: 0.18, sort: (r) => r.process || '', cell: (r) => r.process || '—', title: (r) => r.process || '' },
    { id: 'dest', label: 'Destination', frac: 0.27, sort: (r) => r.dest, cell: (r) => r.dest, title: (r) => r.dest, cls: () => 'dest' },
    { id: 'via', label: 'Via', frac: 0.09, sort: (r) => r.via || '', cell: (r) => r.via || '—', cls: (r) => 'via ' + (r.via || '') },
    // "Count", not "Connections": the header is set in capitals and the long
    // word did not fit the column it heads.
    { id: 'count', label: 'Count', frac: 0.09, num: true, descFirst: true, sort: (r) => r.count, cell: (r) => r.count },
    { id: 'up', label: '↑', frac: 0.11, num: true, descFirst: true, sort: (r) => r.up, cell: (r) => bytes(r.up) },
    { id: 'down', label: '↓', frac: 0.11, num: true, descFirst: true, sort: (r) => r.down, cell: (r) => bytes(r.down) },
    // Seconds since it last carried anything, smallest first: the first click
    // shows the most recent. Wide enough for "59 min ago".
    { id: 'last', label: 'Last seen', frac: 0.15, num: true, sort: (r) => r.seconds, cell: (r) => ago(r.seconds) },
  ]
  // What the machine is doing WITHOUT us. Read from the operating system's own
  // connection table, so it shows the programs that never touch the proxy —
  // which is the only place they can be seen at all.
  const bypassCols: Col<Bypass>[] = [
    { id: 'process', label: 'Program', frac: 0.22, sort: (r) => r.process || '', cell: (r) => r.process || '—', title: (r) => r.process || '' },
    {
      id: 'dest',
      label: 'Destination',
      frac: 0.46,
      sort: (r) => r.name || r.dest,
      // The address is what is true; the name is what is recognisable. Both,
      // because a reverse lookup can be wrong and an address means nothing.
      cell: (r) => (r.name ? r.name + '  ' + r.dest : r.dest),
      title: (r) => (r.name ? r.name + ' — ' + r.dest : r.dest),
      cls: () => 'dest',
    },
    { id: 'state', label: 'State', frac: 0.2, sort: (r) => r.state, cell: (r) => r.state },
    { id: 'count', label: 'Count', frac: 0.12, num: true, descFirst: true, sort: (r) => r.count, cell: (r) => r.count },
  ]

  // One search over all three: the question is "what did this program do", and
  // it is the same question whichever table is showing.
  const match = (haystack: string[]) => {
    const q = query.trim().toLowerCase()
    if (!q) return true
    return haystack.some((h) => (h || '').toLowerCase().includes(q))
  }
  const shownRows = useMemo(() => rows.filter((r) => match([r.process, r.dest, r.via])), [rows, query])
  const shownTotals = useMemo(() => totals.filter((t) => match([t.process, t.dest, t.via])), [totals, query])
  const shownBypass = useMemo(() => bypass.filter((b) => match([b.process, b.dest, b.name])), [bypass, query])

  const views: { v: View; label: string; n: number }[] = [
    { v: 'live', label: 'Right now', n: rows.length },
    { v: 'tot', label: 'Since start', n: totals.length },
  ]
  if (mode === 'proxy') views.push({ v: 'bypass', label: 'Not through Vair', n: bypass.length })
  const shownN = shownView === 'live' ? shownRows.length : shownView === 'tot' ? shownTotals.length : shownBypass.length
  const allN = shownView === 'live' ? rows.length : shownView === 'tot' ? totals.length : bypass.length

  // Copy: the table on screen as text, tab separated, so it pastes into a
  // spreadsheet or a bug report unchanged — with the same search and order.
  const copyShown = () => {
    const lines: string[] = []
    const dump = <T,>(cols: Col<T>[], data: T[]) => {
      lines.push(cols.map((c) => c.label).join('\t'))
      for (const r of data) lines.push(cols.map((c) => String(c.cell(r) ?? '')).join('\t'))
    }
    if (shownView === 'live') dump(liveCols, shownRows)
    else if (shownView === 'tot') dump(totalCols, shownTotals)
    else dump(bypassCols, shownBypass)
    const text = lines.join('\n')
    copyText(text)
    setCopied(true)
    window.setTimeout(() => setCopied(false), 1200)
  }

  // Clears the panel, not one table of three. The live rows are left alone
  // while recording: they are connections that are still open, and a row is
  // created when a connection starts, so dropping one hides traffic that is
  // still moving and never comes back. The next poll re-reads all three anyway.
  const clearAll = () =>
    ConnectionsService.ClearTotals().then(() => {
      setTotals([])
      setBypass([])
      if (!tracking) setRows([])
    })

  // In TUN mode the rows come from two places and only one of them is always
  // talking: Vair's own relay carries everything bound for the tunnel and can
  // count it, while the engine's log — which is what knows the program, the
  // rule, and the traffic routed AROUND the tunnel — says nothing unless asked.
  // Saying which half is missing beats a table that looks broken.
  const tunQuiet = mode === 'tun' && !verbose && shownView !== 'bypass'
  // Proxy mode without the system proxy: only programs pointed at the proxy by
  // hand pass through it, and the list is nearly empty.
  const proxyBare = mode === 'proxy' && noSystemProxy
  // Why a table is empty or short, one reason at a time. Recording is off until
  // it is asked for, and turned on mid-session it misses what was already open.
  const note = (() => {
    if (!tracking)
      return 'Recording is off. Turn on record to list the connections made through Vair: the program, the destination and the traffic. The list is kept in memory only, until you clear it or close Vair.'
    if (missed)
      return 'Recording was turned on during this connection. Connections that were already open are not listed and their traffic is not counted; browsers keep them open for minutes. Reconnect to the configuration to list everything.'
    if (proxyBare)
      return 'Set system proxy is off: only programs you pointed at the local proxy yourself go through it, so only their connections are listed. Everything else on this computer goes directly and does not appear here.'
    if (tunQuiet)
      return 'In TUN mode, connections through the tunnel are listed here with their byte counts. Turn on Count rule matches or Verbose logs and reconnect to also see which program opened the connection, which rule routed it, and traffic sent around the tunnel.'
    if (shownView === 'bypass')
      return 'TCP connections over IPv4 and IPv6 that do not go through Vair, read from the system. UDP, including QUIC, is not listed: it has no connections for the system to report.'
    return ''
  })()

  return (
    <div className="dock-pane conns-pane" id="conns-panel" style={style}>
      <div className="log-head">
        <span className="log-title">Connections</span>
        <div className="seg-group conns-views" role="radiogroup">
          {views.map((x) => (
            <button
              key={x.v}
              type="button"
              role="radio"
              aria-checked={shownView === x.v}
              className={'seg-btn' + (shownView === x.v ? ' active' : '')}
              onClick={() => setView(x.v)}
            >
              {x.label} <span className="conns-n">{x.n}</span>
            </button>
          ))}
        </div>
        <input
          className="log-find"
          placeholder="search…"
          title="Filter by program, destination or route (case-insensitive). Applies to copy too."
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          spellCheck={false}
        />
        {query && (
          <span className="log-find-n">
            {shownN}/{allN}
          </span>
        )}
        {/* A real switch, not a view filter: off means nothing is recorded at
            all — no row per connection, no reading of where a connection is
            going, no parsing of the engine's log. The tables freeze on what
            they were showing; clear is what empties them. */}
        <Check
          className="log-auto"
          checked={tracking}
          title="Off by default. On: connections made through Vair are listed with their program, destination and traffic, in memory only. Off stops all collection; what is already listed stays until you clear it."
          onChange={(v) => {
            setTracking(v)
            ConnectionsService.SetTracking(v).then(() => ConnectionsService.MissedEarlier().then((m: boolean) => setMissed(m)))
          }}
          label="record"
        />
        <div className="dock-tools">
          <div className="bgroup">
            <button className="btn ghost" onClick={copyShown}>
              {copied ? 'copied' : 'copy'}
            </button>
            <button className="btn ghost" onClick={clearAll} title="Clear the session totals and what was seen outside the proxy">
              clear
            </button>
          </div>
          <button className="btn ghost xbtn" title="Close" aria-label="Close" onClick={onClose}>
            ✕
          </button>
        </div>
      </div>
      {note && <div className="dock-note">{note}</div>}
      <div className="conns-body">
        {shownView === 'live' && (
          <DataTable
            tableId="live"
            cols={liveCols}
            data={shownRows}
            rowKey={(r) => String(r.id)}
            empty="Nothing is going through the tunnel right now"
          />
        )}
        {shownView === 'tot' && (
          <DataTable
            tableId="tot"
            cols={totalCols}
            data={shownTotals}
            rowKey={(r) => r.process + ' ' + r.dest}
            empty="Nothing yet. Kept in memory only, until you clear it or close the program."
          />
        )}
        {shownView === 'bypass' && (
          <DataTable
            tableId="bypass"
            cols={bypassCols}
            data={shownBypass}
            rowKey={(r) => r.process + ' ' + r.dest}
            empty="Nothing outside the proxy"
          />
        )}
      </div>
    </div>
  )
}
