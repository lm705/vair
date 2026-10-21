// The connections panel: what is going through the tunnel now, and what has
// gone through it since the program started.
//
// Two tables, because they answer two questions and a single one would answer
// neither: the top is the present, the bottom is the session — one row per
// program and destination, which is where "who used four gigabytes" is visible
// at all.
//
// Both are sortable, searchable, selectable and copyable, and their columns are
// dragged the way a spreadsheet's are: pulling an edge takes width from the
// column on the other side of it. The table never grows past the panel, so
// there is nothing to scroll sideways for.
import { useEffect, useMemo, useState } from 'react'
import { ConnectionsService } from '../bindings/vair/internal/services'
import { t10 } from './i18n'
import { Hint } from './hints'
import type { HintOpt } from './hints'
import DataTable, { type Col } from './DataTable'
import Toggle from './Toggle'

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
function ago(tt: (s: string) => string, sec: number): string {
  if (sec < 5) return tt('now')
  if (sec < 60) return sec + ' ' + tt('s ago')
  if (sec < 3600) return Math.floor(sec / 60) + ' ' + tt('min ago')
  return Math.floor(sec / 3600) + ' ' + tt('h ago')
}

export default function ConnectionsModal({
  onClose,
  lang,
  mode,
  verbose,
}: {
  onClose: () => void
  lang: string
  mode: string // "tun" | "proxy" | ""
  verbose: boolean // the engine is running a detailed log
}) {
  const tt = (en: string) => t10(lang, en)
  // The description under a setting: the paragraph while descriptions are
  // on, the mark that opens the same text while they are off.
  const hint = (en: string, opts?: HintOpt[]) => <Hint en={en} opts={opts} />
  const [rows, setRows] = useState<Row[]>([])
  const [totals, setTotals] = useState<Total[]>([])
  const [bypass, setBypass] = useState<Bypass[]>([])
  const [query, setQuery] = useState('')
  const [copied, setCopied] = useState(false)
  const [tracking, setTracking] = useState(true)

  useEffect(() => {
    const pull = () => {
      ConnectionsService.List().then((r: any) => setRows(r || []))
      ConnectionsService.Totals().then((r: any) => setTotals(r || []))
      ConnectionsService.Tracking().then((v: boolean) => setTracking(v))
      ConnectionsService.Bypass().then((r: any) => setBypass(r || []))
    }
    pull()
    const id = setInterval(pull, 1000)
    return () => clearInterval(id)
  }, [])

  const liveCols: Col<Row>[] = [
    {
      id: 'process',
      label: tt('Program'),
      frac: 0.19,
      sort: (r) => r.process || '',
      cell: (r) => r.process || '—',
      title: (r) => r.process || '',
    },
    {
      id: 'dest',
      label: tt('Destination'),
      frac: 0.37,
      sort: (r) => r.dest,
      cell: (r) => r.dest,
      title: (r) => (r.rule ? tt('Rule') + ': ' + r.rule : r.dest),
      cls: () => 'dest',
    },
    {
      id: 'via',
      label: tt('Via'),
      frac: 0.12,
      sort: (r) => r.via || '',
      cell: (r) => (r.via ? tt(r.via) : '…'),
      title: (r) => (r.via ? '' : tt('The engine has not said yet')),
      cls: (r) => 'via ' + (r.via || ''),
    },
    { id: 'up', label: '↑', frac: 0.12, num: true, sort: (r) => r.up, cell: (r) => (r.exact ? bytes(r.up) : '—') },
    { id: 'down', label: '↓', frac: 0.12, num: true, sort: (r) => r.down, cell: (r) => (r.exact ? bytes(r.down) : '—') },
    { id: 'time', label: tt('Time'), frac: 0.08, num: true, sort: (r) => r.seconds, cell: (r) => clock(r.seconds) },
  ]

  // The fractions of each table MUST sum to 1. They are what the columns are
  // sized with and what the lines between them are drawn at, and a set summing
  // to 1.06 makes the browser scale the columns down while the lines stay where
  // the arithmetic put them — every line a little to the right of its column,
  // more so towards the end. normaliseFracs enforces it for saved layouts too.
  const totalCols: Col<Total>[] = [
    {
      id: 'process',
      label: tt('Program'),
      frac: 0.18,
      sort: (r) => r.process || '',
      cell: (r) => r.process || '—',
      title: (r) => r.process || '',
    },
    {
      id: 'dest',
      label: tt('Destination'),
      frac: 0.3,
      sort: (r) => r.dest,
      cell: (r) => r.dest,
      title: (r) => r.dest,
      cls: () => 'dest',
    },
    {
      id: 'via',
      label: tt('Via'),
      frac: 0.1,
      sort: (r) => r.via || '',
      cell: (r) => (r.via ? tt(r.via) : '—'),
      cls: (r) => 'via ' + (r.via || ''),
    },
    { id: 'count', label: tt('Connections'), frac: 0.1, num: true, sort: (r) => r.count, cell: (r) => r.count },
    { id: 'up', label: '↑', frac: 0.11, num: true, sort: (r) => r.up, cell: (r) => bytes(r.up) },
    { id: 'down', label: '↓', frac: 0.11, num: true, sort: (r) => r.down, cell: (r) => bytes(r.down) },
    {
      id: 'last',
      label: tt('Last seen'),
      frac: 0.1,
      num: true,
      sort: (r) => -r.seconds, // most recent first when sorted ascending
      cell: (r) => ago(tt, r.seconds),
    },
  ]

  // One search box over both tables: the question is "what did this program do",
  // and it is the same question upstairs and downstairs.
  const match = (haystack: string[]) => {
    const q = query.trim().toLowerCase()
    if (!q) return true
    return haystack.some((h) => (h || '').toLowerCase().includes(q))
  }
  const shownRows = useMemo(() => rows.filter((r) => match([r.process, r.dest, r.via])), [rows, query])
  const shownTotals = useMemo(() => totals.filter((t) => match([t.process, t.dest, t.via])), [totals, query])


  // What the machine is doing WITHOUT us. Read from the operating system's own
  // connection table, so it shows the programs that never touch the proxy —
  // which is the only place they can be seen at all.
  const bypassCols: Col<Bypass>[] = [
    {
      id: 'process',
      label: tt('Program'),
      frac: 0.22,
      sort: (r) => r.process || '',
      cell: (r) => r.process || '—',
      title: (r) => r.process || '',
    },
    {
      id: 'dest',
      label: tt('Destination'),
      frac: 0.46,
      sort: (r) => r.name || r.dest,
      // The address is what is true; the name is what is recognisable. Both,
      // because a reverse lookup can be wrong and an address means nothing.
      cell: (r) => (r.name ? r.name + '  ' + r.dest : r.dest),
      title: (r) => (r.name ? r.name + ' — ' + r.dest : r.dest),
      cls: () => 'dest',
    },
    { id: 'state', label: tt('State'), frac: 0.2, sort: (r) => r.state, cell: (r) => tt(r.state) },
    { id: 'count', label: tt('Connections'), frac: 0.12, num: true, sort: (r) => r.count, cell: (r) => r.count },
  ]
  const shownBypass = useMemo(
    () => bypass.filter((b) => match([b.process, b.dest, b.name])),
    [bypass, query],
  )

  // Copy: both tables as text, tab separated, so they paste into a spreadsheet
  // or a bug report unchanged. What is copied is what is on screen — the same
  // search and the same order.
  const copyAll = () => {
    const lines: string[] = []
    const dump = <T,>(cols: Col<T>[], data: T[]) => {
      lines.push(cols.map((c) => c.label).join('\t'))
      for (const r of data) lines.push(cols.map((c) => String(c.cell(r) ?? '')).join('\t'))
    }
    dump(liveCols, shownRows)
    lines.push('')
    dump(totalCols, shownTotals)
    const text = lines.join('\n')
    navigator.clipboard?.writeText?.(text).catch(() => {
      // WebView2 sometimes refuses the async API; the old way still works.
      const ta = document.createElement('textarea')
      ta.value = text
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    })
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
  const tunQuiet = mode === 'tun' && !verbose

  return (
    <div className="modal-overlay" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal-box conns-box">
        <div className="modal-title">{tt('Connections')}</div>
        {tunQuiet && hint('In TUN mode what goes through the tunnel is listed here with its byte counts; the rest is read from the engine\'s own log, which is quiet unless asked. Turn on Count rule matches or Verbose logs and reconnect to also see which program asked, which rule sent it, and the traffic routed around the tunnel.')}

        {/* A real switch, not a view filter: off means nothing is recorded at
            all — no row per connection, no reading of where a connection is
            going, no parsing of the engine's log. The tables freeze on what
            they were showing; the clear button is what empties them. */}
        <div className="modal-row conns-record">
          <span className="modal-row-label">{tt('Record connections')}</span>
          <Toggle
            on={tracking}
            onChange={(v) => {
              setTracking(v)
              ConnectionsService.SetTracking(v)
            }}
          />
        </div>
        {hint('Off stops all collection: no connection is recorded and nothing is read to find out where it is going. What is already here stays until you clear it.')}

        <div className="conns-search">
          <input
            className="modal-input"
            placeholder={tt('search program, destination…')}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {query && (
            <button className="btn ghost tiny" onClick={() => setQuery('')}>
              {tt('clear')}
            </button>
          )}
        </div>

        <div className="conns-head">
          <span className="conns-cap">{tt('Right now')}</span>
        </div>
        <DataTable
          tableId="live"
          cols={liveCols}
          data={shownRows}
          rowKey={(r) => String(r.id)}
          empty={tt('Nothing is going through the tunnel right now')}
        />

        <div className="conns-head">
          <span className="conns-cap">{tt('Since the program started')}</span>
          <span className="conns-note">{tt('Kept in memory only, until you clear it or close the program')}</span>
        </div>
        <DataTable
          tableId="tot"
          cols={totalCols}
          data={shownTotals}
          rowKey={(r) => r.process + ' ' + r.dest}
          empty={tt('Nothing yet')}
        />

        {mode === 'proxy' && (
          <>
            <div className="conns-head">
              <span className="conns-cap">{tt('Not through Vair')}</span>
              <span className="conns-note">
                {tt("The machine's own connections that did not go through the proxy — programs that do not use it")}
              </span>
            </div>
            <DataTable
              tableId="bypass"
              cols={bypassCols}
              data={shownBypass}
              rowKey={(r) => r.process + ' ' + r.dest}
              empty={tt('Nothing outside the proxy')}
            />
          </>
        )}

        <div className="modal-btns">
          <button className="btn ghost" onClick={copyAll}>
            {copied ? tt('copied') : tt('copy')}
          </button>
          <button className="btn ghost" onClick={clearAll}>
            {tt('clear')}
          </button>
          <button className="btn ghost" onClick={onClose}>
            {tt('close')}
          </button>
        </div>
      </div>
    </div>
  )
}
