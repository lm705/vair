// AUTO panel — an exact port of the 1.10 openAuto() DOM: enable toggle, live
// STATUS (dot + text + reason), ranked candidate list (click to connect),
// "Switch now", collapsible Settings (expanded) and Logs (collapsed, src=vair).
// Always rendered in its OWN window (/?view=auto) alongside the main window;
// Detach hides the main window, Back-to-app restores it (swap driven by
// mainVisible), the titlebar ✕ closes just this window.
import { useEffect, useRef, useState } from 'react'
import Check from './Check'
import { Events } from '@wailsio/runtime'
import { AutoService, LogService } from '../bindings/vair/internal/services'
import { t10 } from './i18n'
import LangSelect from './langselect'
import { Hint, opt, whenOn, whenOff } from './hints'
import type { HintOpt } from './hints'
import { IS_REMOTE } from './remote'
import Select from './Select'
import HistoryModal from './HistoryModal'
import { endReasonText } from './reasons'
import CloseX from './closex'
import Group from './setgroup'
import { copyText } from './clipboard'
import { checkExit, exitChecking } from './exitcheck'
import type { ExitView } from './exitcheck'

function fmtLogTime(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => (n < 10 ? '0' + n : '' + n)
  return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds())
}

// A session's start: the day only when it was not today, because most of the
// log is today and repeating the date on every line hides the time.
function fmtSessTime(unixSec: number): string {
  const d = new Date(unixSec * 1000)
  const p = (n: number) => (n < 10 ? '0' + n : '' + n)
  const hm = p(d.getHours()) + ':' + p(d.getMinutes())
  const now = new Date()
  const sameDay =
    d.getDate() === now.getDate() && d.getMonth() === now.getMonth() && d.getFullYear() === now.getFullYear()
  return sameDay ? hm : p(d.getDate()) + '.' + p(d.getMonth() + 1) + ' ' + hm
}

// How long it held, in the largest unit that still says something.
function fmtDur(sec: number): string {
  if (sec < 60) return sec + 's'
  if (sec < 3600) return Math.floor(sec / 60) + 'm ' + (sec % 60) + 's'
  const h = Math.floor(sec / 3600)
  return h + 'h ' + Math.floor((sec % 3600) / 60) + 'm'
}

function Toggle({ on, onChange }: { on: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="toggle">
      <input type="checkbox" checked={on} onChange={(e) => onChange(e.target.checked)} />
      <span className="toggle-track" />
      <span className="toggle-thumb" />
    </label>
  )
}



// NumField is a number setting that shows what is saved. The fields were
// uncontrolled: a typed 0 stayed on screen while the default was saved, a value
// past the maximum was saved as typed, and a change made in Settings → Testing
// never reached the field. Now the typed text is kept only while the field has
// focus; leaving it saves the number within [min, max] — an empty or unreadable
// entry keeps the saved value — and the field shows what was saved.
function NumField({
  value,
  min,
  max,
  onCommit,
}: {
  value: number
  min: number
  max: number
  onCommit: (v: number) => void
}) {
  const [txt, setTxt] = useState(String(value))
  const [editing, setEditing] = useState(false)
  useEffect(() => {
    if (!editing) setTxt(String(value))
  }, [value, editing])
  return (
    <input
      className="modal-input num-input"
      type="number"
      min={min}
      max={max}
      value={txt}
      onFocus={() => setEditing(true)}
      onChange={(e) => setTxt(e.target.value)}
      onBlur={() => {
        setEditing(false)
        const n = parseInt(txt, 10)
        if (isNaN(n)) {
          setTxt(String(value))
          return
        }
        const v = Math.min(max, Math.max(min, n))
        setTxt(String(v))
        if (v !== value) onCommit(v)
      }}
      onKeyDown={(e) => {
        if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
      }}
    />
  )
}

export default function AutoModal({
  stg,
  apply,
  auto,
  setAutoEnabled,
  autoLast,
  conn,
  tabs,
  lang,
  onClose,
}: {
  stg: any
  apply: (patch: Record<string, any>) => void
  auto: boolean
  setAutoEnabled: (on: boolean) => void
  autoLast: any
  conn: any
  tabs: any[]
  lang: string
  onClose: () => void
}) {
  const tt = (en: string) => t10(lang, en)
  // The description under a setting: the paragraph while descriptions are
  // on, the mark that opens the same text while they are off.
  const hint = (en: string, opts?: HintOpt[]) => <Hint en={en} opts={opts} />
  const [cands, setCands] = useState<any[]>([])
  // Why "Switch now" did nothing, when it did nothing.
  const [switchNote, setSwitchNote] = useState('')
  // The last check IP, for the connection it was made on: a new server or a
  // disconnect makes it stale, so it goes.
  const connected = conn?.status === 'connected' && !conn?.checking
  const [exit, setExit] = useState<ExitView | null>(null)
  useEffect(() => setExit(null), [conn?.status, conn?.conn_raw])
  const doCheckExit = () => {
    if (!connected || exit?.cls === 'testing') return
    setExit(exitChecking(tt))
    checkExit(tt).then(setExit)
  }
  const [logCopied, setLogCopied] = useState(false)
  // How far the test of the candidate pool has got. A big pool is tested
  // before the first connection, and for all that time the status said only
  // "waiting".
  const [sweep, setSweep] = useState<{ running: boolean; done: number; total: number; speed: boolean } | null>(null)
  // The candidate whose measurement history is open. Held here rather than in
  // the app: this panel also runs in its own window, where the app's dialogs
  // are not on screen at all.
  const [histRaw, setHistRaw] = useState('')
  // Collapse state persists across open/close and detach (localStorage) — the
  // 1.10 defaults (settings expanded, logs collapsed) apply only the first time.
  const [settingsOpen, setSettingsOpen] = useState(() => localStorage.getItem('auto.settingsOpen') !== '0')
  const [logsOpen, setLogsOpen] = useState(() => localStorage.getItem('auto.logsOpen') === '1')
  const [sessOpen, setSessOpen] = useState(() => localStorage.getItem('auto.sessOpen') === '1')
  const [sessions, setSessions] = useState<any[]>([])
  useEffect(() => {
    localStorage.setItem('auto.settingsOpen', settingsOpen ? '1' : '0')
  }, [settingsOpen])
  useEffect(() => {
    localStorage.setItem('auto.logsOpen', logsOpen ? '1' : '0')
  }, [logsOpen])
  // The connection log: read when the section is opened, and again whenever a
  // connection ends — that is the moment a row appears.
  useEffect(() => {
    localStorage.setItem('auto.sessOpen', sessOpen ? '1' : '0')
    if (sessOpen) AutoService.Sessions().then((r: any) => setSessions(r ?? []))
  }, [sessOpen, conn?.status, conn?.conn_raw])
  const [logLines, setLogLines] = useState<any[]>([])
  const [impLines, setImpLines] = useState<any[]>([])
  const [logSrc, setLogSrc] = useState('vair') // 1.10 default for the AUTO panel
  const [logLvl, setLogLvl] = useState('')
  const [logAuto, setLogAuto] = useState(true)
  const logViewRef = useRef<HTMLDivElement>(null)
  const logsOpenRef = useRef(false)
  // Detach / Back-to-app: which one shows depends on the MAIN window's
  // visibility. Refreshed by the backend's main_vis event and on window focus
  // (the user must focus this window to click anything, so the state is always
  // fresh by the time a click can happen).
  const [mainVisible, setMainVisible] = useState(true)
  useEffect(() => {
    const refresh = () => AutoService.MainVisible().then(setMainVisible)
    refresh()
    const off = Events.On('main_vis', refresh)
    window.addEventListener('focus', refresh)
    return () => {
      if (typeof off === 'function') off()
      window.removeEventListener('focus', refresh)
    }
  }, [])

  // Candidate list: load on open, then refresh EVENT-DRIVEN with a 500ms
  // debounce (the 1.10 autoPanelRefreshSoon) — no polling. entry_update fires
  // per ping/speed result, conn_update/auto_update on connection changes.
  const loadCands = () => {
    AutoService.Candidates().then((list) => setCands(list ?? []))
  }
  // The newest update wins, not the last to arrive: the events can overtake
  // each other (see AutoSweepProgress.Seq in core), and a late "8 of 8,
  // running" after "finished" left the bar on screen for good.
  const sweepSeq = useRef(0)
  useEffect(() => {
    const take = (p: any) => {
      if (!p || typeof p.total !== 'number') return
      const seq = typeof p.seq === 'number' ? p.seq : 0
      if (seq && seq <= sweepSeq.current) return
      if (seq) sweepSeq.current = seq
      setSweep(p)
    }
    AutoService.Sweep().then(take)
    const off = Events.On('auto_sweep', (e: any) => take(e?.data?.payload ?? e?.data))
    return () => {
      if (typeof off === 'function') off()
    }
  }, [])
  useEffect(() => {
    loadCands()
    let t = 0
    const refreshSoon = () => {
      if (t) return
      t = window.setTimeout(() => {
        t = 0
        loadCands()
      }, 500)
    }
    const offs = [
      Events.On('entry_update', refreshSoon),
      Events.On('conn_update', refreshSoon),
      Events.On('auto_update', refreshSoon),
      Events.On('loaded', refreshSoon),
    ]
    return () => {
      if (t) window.clearTimeout(t)
      offs.forEach((f) => typeof f === 'function' && f())
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Logs: fetch when expanded, then live-append from the "log" event.
  useEffect(() => {
    logsOpenRef.current = logsOpen
    if (logsOpen) {
      LogService.Get().then((ls) => setLogLines(ls ?? []))
      // The important lines live in their own, much smaller ring on the backend
      // too: the main buffer is capped, so an engine flood can push a warning
      // out of it within seconds while this one still has it minutes later.
      LogService.GetImportant().then((ls) => setImpLines(ls ?? []))
    }
  }, [logsOpen])
  useEffect(() => {
    const off = Events.On('log', (e: any) => {
      if (!logsOpenRef.current) return
      const batch = e?.data?.payload
      if (!Array.isArray(batch)) return
      setLogLines((prev) => [...prev, ...batch].slice(-2000))
      const imp = batch.filter((l: any) => l.lvl !== 'info' || l.src === 'vair' || l.src === 'test')
      if (imp.length) setImpLines((prev) => [...prev, ...imp].slice(-2000))
    })
    return () => {
      if (typeof off === 'function') off()
    }
  }, [])
  useEffect(() => {
    if (logAuto && logViewRef.current) logViewRef.current.scrollTop = logViewRef.current.scrollHeight
  }, [logLines, logAuto, logsOpen])

  const logPass = (l: any): boolean => {
    if (logSrc && l.src !== logSrc) return false
    if (!logLvl) return true // "all"
    // "important" = everything except the engines' [Info] narration, the same
    // rule the main log panel uses (and core.isImportant on the other side).
    if (logLvl === 'important') return l.lvl !== 'info' || l.src === 'vair' || l.src === 'test'
    // Untagged engine lines rank BELOW info, or "info+" would equal "all".
    const rank: any = { raw: 0, info: 1, warn: 2, error: 3 }
    const need: any = { info: 1, warn: 2, error: 3 }
    return (rank[l.lvl] ?? 0) >= (need[logLvl] ?? 0)
  }
  // In "important" mode the panel reads the dedicated buffer instead of the
  // main one, so the lines survive an engine flood.
  const logBase = () => (logLvl === 'important' ? impLines : logLines)
  const copyLogs = () => {
    const text = logBase()
      .filter(logPass)
      .map((l) => fmtLogTime(l.t) + ' ' + (l.src || '') + ' ' + l.msg)
      .join('\n')
    // The shared copy: it falls back where the browser has no clipboard API —
    // the web interface over plain HTTP, where this button did nothing.
    copyText(text)
    setLogCopied(true)
    window.setTimeout(() => setLogCopied(false), 1500)
  }

  // ── status block (exact renderAutoStatus port) ──────────────────────
  const autoReasonText = (r: string): string => {
    if (!r) return ''
    if (r.indexOf('too slow') === 0) return tt('previous config too slow')
    if (r === 'probe failed') return tt('previous config stopped responding')
    if (r === 'slow but best') return tt('slow, but the fastest available')
    if (r === 'manual switch') return tt('manual switch')
    if (r === 'tabs changed') return tt('no longer among the candidates')
    if (r === 'auto-connect') return tt('auto-connected')
    return r
  }
  let cls = ''
  let txt = ''
  let sub = ''
  if (!auto) {
    txt = tt('Auto-connect is off')
  } else if (conn && conn.status === 'connected') {
    // Dot colour follows the live mode (proxy = green, TUN = blue) instead of a
    // flat amber, matching the main window's connection indicator.
    cls = 'ok ' + (conn.mode === 'tun' ? 'm-tun' : 'm-proxy')
    const nm = conn.entry_name || autoLast?.current_name || ''
    const sameCfg = !!(autoLast?.current_raw && conn.conn_raw && autoLast.current_raw === conn.conn_raw)
    const rtt = sameCfg && autoLast?.rtt_ms > 0 ? ' · ' + autoLast.rtt_ms + ' ms' : ''
    txt = tt('Connected') + (nm ? ': ' + nm : '') + rtt
    if (sameCfg && autoLast?.reason) sub = autoReasonText(autoLast.reason)
  } else {
    const st = autoLast?.state || ''
    if (st === 'switching') {
      cls = 'warn'
      txt = tt('Switching…') + (autoLast?.current_name ? ' → ' + autoLast.current_name : '')
    } else if (st === 'all_down') {
      cls = 'down'
      txt = tt('All candidates are down')
    } else if (st === 'needs_admin') {
      cls = 'down'
      txt = tt('Not connecting: TUN mode needs administrator rights')
      sub = tt('Restart Vair as administrator, or set the connection mode below to proxy.')
    } else if (st === 'needs_service') {
      cls = 'down'
      txt = tt('Not connecting: TUN mode needs the Vair helper service, and it is missing')
      sub = tt('Repair it in Settings → System, or set the connection mode below to proxy.')
    } else if (st === 'needs_helper') {
      cls = 'down'
      txt = tt('Not connecting: TUN mode needs the helper')
      sub = tt('Install it with the button next to the TUN switch, or set the connection mode below to proxy.')
    } else if (st === 'paused') {
      txt = tt('Paused — reconnect to resume')
    } else if (sweep?.running) {
      txt = tt('Testing the candidates…')
    } else {
      txt = tt('Idle — waiting')
    }
  }

  // Candidate-tabs pills: empty auto_tabs ⇒ default to main.
  const sel: string[] = stg?.auto_tabs || []
  const defMain = !sel || sel.length === 0
  const tabOn = (id: string) => (defMain ? id === 'main' : sel.indexOf(id) >= 0)
  const toggleTab = (id: string) => {
    const cur = defMain ? ['main'] : [...sel]
    const i = cur.indexOf(id)
    if (i >= 0) cur.splice(i, 1)
    else cur.push(id)
    apply({ auto_tabs: cur })
  }

  // The candidate list shows the figure the ranking is USING, so either speed
  // mode shows speed and either delay mode shows delay.
  // Derived exactly as core.autoRankMode does, so a settings file written by an
  // older build shows the mode that is actually in force rather than a default.
  const rankMode: string =
    stg?.auto_rank_mode || (stg?.auto_rank_by_speed ? 'speed' : stg?.auto_rank_stable ? 'history' : 'delay')
  // The list is the pool in ranked order, so a change of either is a new list.
  // It used to wait for the next test result or connection event: tabs picked
  // or dropped above went on showing the old candidates.
  const poolKey = JSON.stringify(stg?.auto_tabs || [])
  useEffect(() => {
    loadCands()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [poolKey, rankMode])

  return (
    <div
      className="modal-overlay"
      id="auto-modal"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className="modal-box cardset">
        <div className="auto-title-row">
          <div className="modal-title" style={{ marginBottom: 0 }}>
            {tt('Auto-connect')}
          </div>
          <div className="auto-title-tools">
          <LangSelect lang={lang} />
          {/* Detach opens a second NATIVE window — impossible from a browser. */}
          {IS_REMOTE ? null : mainVisible ? (
            <button
              className="btn ghost sm auto-detach"
              title={tt('Detach as a compact window')}
              onClick={() => AutoService.Detach()}
            >
              ⧉ {tt('Detach')}
            </button>
          ) : (
            <button className="btn ghost sm auto-return" onClick={() => AutoService.Attach()}>
              ← {tt('Back to app')}
            </button>
          )}
          {/* Hidden in the detached window, which has a close button of its own. */}
          <CloseX className="auto-close-btn" onClick={onClose} title={tt('Close')} />
          </div>
        </div>

        <Group>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Enable auto-connect')}</span>
            <Toggle on={auto} onChange={setAutoEnabled} />
          </div>
          {hint('Automatic choice of a server, and keeping the connection up.', [
            whenOn('Vair connects when it starts or when this is switched on — to the best configuration by the ranking mode, or to the one used last time when “Connect first to” says so. Before the first connection the candidates are tested; the status above shows how far that has got. If the connection stops passing traffic, Vair switches to another working configuration.'),
            whenOff('Vair does not connect or switch servers by itself. Turning it off leaves the current connection as it is.', true),
          ])}
        </Group>

        {/* STATUS — live state + ranked candidates. No card: the status and
            the list draw their own frames. */}
        <Group title={tt('Status')} bare>
          <div className={'auto-status' + (cls ? ' ' + cls : '')} id="auto-status">
            <span className="as-dot" />
            {txt}
            {sub && (
              <div style={{ marginTop: 3, fontSize: '.85em', opacity: 0.7 }}>
                {/^needs_/.test(autoLast?.state || '') ? '' : '↻ '}
                {sub}
              </div>
            )}
            {/* The pool test, counted: the same thin bar as under the main
                window's header. Shown while connected too — a re-test after a
                refresh is the same work. */}
            {auto && sweep?.running && sweep.total > 0 && (
              <div className="as-sweep">
                <div className="as-sweep-row">
                  <span>{tt(sweep.speed ? 'Testing ping and speed' : 'Testing ping')}</span>
                  <span className="as-sweep-n">
                    {sweep.done} / {sweep.total}
                  </span>
                </div>
                <div className="pbar-row as-sweep-bar">
                  <div className="pbar-fill pbar-ping" style={{ width: Math.round((sweep.done / sweep.total) * 100) + '%' }} />
                </div>
              </div>
            )}
          </div>
          <div className="auto-cands" id="auto-cands">
            {cands.length === 0 ? (
              <div className="msg-note">
                {tt('Nothing to choose from: the candidate tabs have no configurations, or their exclude filters hide all of them.')}
              </div>
            ) : (
              cands.map((c, i) => {
                const ms = c.status === 'ok' && c.delay > 0 ? c.delay + ' ms' : ''
                let metric: string
                if (c.status === 'failed') metric = tt('failed')
                else if (rankMode.startsWith('speed') && c.speed_mbps > 0)
                  metric = c.speed_mbps.toFixed(1) + ' MB/s' + (ms ? ' · ' + ms : '')
                else if (ms) metric = ms
                else metric = tt('untested')
                // A config AUTO is still skipping after a failure, and how long
                // for: without it the top of this list could be one the
                // supervisor would not touch for another twenty minutes.
                const paused = (c as any).cooldown_sec > 0 ? fmtDur((c as any).cooldown_sec) : ''
                return (
                  <div
                    key={c.raw || i}
                    className={
                      'auto-cand' + (c.current ? ' current' : '') + (paused ? ' cooling' : '')
                    }
                    title={
                      paused
                        ? tt('Skipped after a failure — auto-connect will try it again in') + ' ' + paused
                        : tt('Click to connect to this config')
                    }
                    onClick={() => AutoService.ConnectCand(c.raw, '')}
                  >
                    <span className="ac-rank">{i + 1}</span>
                    <span className="ac-name">
                      {c.name}
                      {c.current ? ' •' : ''}
                    </span>
                    {/* Why this row sits where it does, when it was the history
                        that decided: a 100 ms server above an 80 ms one reads as
                        a bug until something says the 80 keeps failing — and one
                        click opens the record it was decided on. The flag alone
                        is clickable; anywhere else on the row still connects, so
                        the stop below is what keeps the two apart. */}
                    {(c as any).stable ? (
                      <span
                        className="ac-flag click"
                        role="button"
                        tabIndex={0}
                        title={tt('Ranked on its measurement history — click to see it')}
                        onClick={(e) => {
                          e.stopPropagation()
                          setHistRaw(c.raw)
                        }}
                        onKeyDown={(e) => {
                          if (e.key !== 'Enter' && e.key !== ' ') return
                          e.stopPropagation()
                          e.preventDefault()
                          setHistRaw(c.raw)
                        }}
                      >
                        {tt('history')}
                      </span>
                    ) : null}
                    {paused ? <span className="ac-flag cooling">{tt('paused') + ' ' + paused}</span> : null}
                    <span className="ac-metric">{metric}</span>
                  </div>
                )
              })
            )}
          </div>
          <button
            className="btn ghost auto-switch-now"
            onClick={() =>
              AutoService.SwitchNow().then((why: string) => {
                setSwitchNote(why ? tt(why) : '')
                if (why) window.setTimeout(() => setSwitchNote(''), 6000)
              })
            }
          >
            {tt('Switch now')}
          </button>
          <button
            className="btn ghost auto-switch-now"
            title={tt('Re-fetch the sources of the candidate tabs')}
            onClick={() => AutoService.ReloadPool()}
          >
            {tt('Reload')}
          </button>
          {/* The connection bar's check IP, here as well: after a switch this
              is where you are, and the panel may be the only window open. */}
          <button
            className="btn ghost auto-switch-now"
            disabled={!connected || exit?.cls === 'testing'}
            title={
              connected
                ? tt('Check the public exit IP and country as seen from the other end of the tunnel')
                : tt('Connect first: the address is checked through the connection.')
            }
            onClick={doCheckExit}
          >
            {tt('Check IP')}
          </button>
          {switchNote && <div className="msg-note" style={{ marginTop: 6 }}>{switchNote}</div>}
          {exit && (
            <div className={'auto-exit ' + exit.cls} title={exit.title} onClick={doCheckExit}>
              {exit.label}
            </div>
          )}
        </Group>

        {/* Collapsible SETTINGS (expanded by default) */}
        <div
          className={'auto-collapse-head' + (settingsOpen ? '' : ' collapsed')}
          title={tt('Show / hide settings')}
          onClick={() => setSettingsOpen((v) => !v)}
        >
          <span className="auto-collapse-arrow">▾</span>
          <span>{tt('Settings')}</span>
        </div>
        <div className={'auto-collapse-body' + (settingsOpen ? '' : ' collapsed')}>
          <Group>
            <div className="modal-row" style={{ display: 'block' }}>
              <span className="modal-row-label stack">{tt('Candidate tabs')}</span>
            <div className="auto-tabs-wrap" id="auto-tabs">
              {tabs.map((tb) => (
                <button
                  key={tb.id}
                  type="button"
                  className={'auto-tab-pill' + (tabOn(tb.id) ? ' active' : '')}
                  aria-pressed={tabOn(tb.id)}
                  onClick={() => toggleTab(tb.id)}
                >
                  <span className="atp-name">{tb.name}</span>
                </button>
              ))}
            </div>
            </div>
            {hint('Tabs whose configurations auto-connect may choose from. Each tab\'s exclude filter applies: hidden configurations are never chosen, and a connection the filter comes to hide is switched away from after the minimum time on a server — as is one whose tab is deselected. Each tab\'s auto-refresh interval keeps its list current.', [
              opt('Sources', '', true),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Re-test candidates after refresh')}</span>
              <Toggle
                on={stg?.auto_ping_refresh !== false}
                onChange={(v) => apply({ auto_ping_refresh: v })}
              />
            </div>
            {hint('What happens after a candidate tab refreshes its list.', [
              whenOn('its configurations are re-tested, so auto-connect ranks them on current results: ping, plus a speed test when the ranking mode uses speed. Adds test traffic on every refresh — noticeably more in the speed modes.', true),
              whenOff('auto-connect ranks the refreshed list without fresh results.'),
            ])}
            {/* One question — what to rank candidates by — instead of two
                switches that were never independent: speed decided and history
                only broke ties, so four states meant three things and one of
                them could not be reached at all. */}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Rank candidates by')}</span>
              <Select
                className="modal-input"
                style={{ marginBottom: 0, width: 'auto' }}
                value={rankMode}
                onChange={(e) => apply({ auto_rank_mode: e.target.value })}
              >
                <option value="delay">{tt('Delay (last ping)')}</option>
                <option value="speed">{tt('Speed (last test)')}</option>
                <option value="history">{tt('Delay, steady (history)')}</option>
                <option value="speed_history">{tt('Speed, steady (history)')}</option>
              </Select>
            </div>
            {hint('The measure auto-connect uses to rank candidates and to compare them with the current server before switching. The two steady modes use several measurements instead of the last one.', [
              opt('Delay (last ping)', 'the last measured ping. Reacts quickly and costs little, but a single measurement is noisy, especially under load.', true),
              opt('Speed (last test)', 'the last measured download speed. Configurations without a speed result are ranked by ping.'),
              opt('Delay, steady (history)', 'typical delay over several measurements, with a penalty for variation and for failed checks. A server that answers in 80 ms two times out of three ranks below one that always answers in 100 ms. Suited to calls and browsing, where consistency matters more than the lowest number. Needs several measurements per configuration.'),
              opt('Speed, steady (history)', 'speed you can rely on: typical speed minus its variation, so at the same average a steady server wins over an erratic one. Failed speed tests are not held against the server — they usually mean the test host refused. Needs several speed tests per configuration; results older than six hours are ignored.'),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Connect first to')}</span>
              <Select
                className="modal-input"
                style={{ marginBottom: 0, width: 'auto' }}
                value={stg?.auto_prefer_last ? 'last' : 'best'}
                onChange={(e) => apply({ auto_prefer_last: e.target.value === 'last' })}
              >
                <option value="best">{tt('The best by ranking')}</option>
                <option value="last">{tt('The last connected')}</option>
              </Select>
            </div>
            {hint('Which configuration auto-connect tries first when it connects from nothing: at start, when switched on, after a disconnect. Switching away from a failing server always follows the ranking.', [
              opt('The best by ranking', 'the top of the candidate list above.', true),
              opt('The last connected', 'the server used last time, if it is among the candidates and answered its latest ping; otherwise the top of the list — also when the last connection was a chain, which auto-connect does not build.'),
            ])}

            {/* The same two settings as Settings → Testing, deliberately: this is
                where the user is when they watch auto-connect test a pool, and
                sending them to another dialog to change how wide it tests is a
                detour. They write the same keys, so the two dialogs cannot drift
                — and the hint says so, because a duplicated control that quietly
                changed something elsewhere would be worse than no control. */}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Ping concurrency')}</span>
              <NumField value={stg?.ping_concurrency || 10} min={1} max={200} onCommit={(v) => apply({ ping_concurrency: v })} />
            </div>
            {hint('How many candidates are pinged at the same time. Shared with Settings → Testing: a change here applies there too, including manual PING ALL runs.', [
              opt('10', '', true),
              opt('A higher value', 'finishes a large pool faster but makes each result less accurate, because the tests share one connection.'),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Speed concurrency')}</span>
              <NumField value={stg?.speed_concurrency || 5} min={1} max={100} onCommit={(v) => apply({ speed_concurrency: v })} />
            </div>
            {hint('How many candidates are speed-tested at the same time; used only by the ranking modes that measure speed. Shared with Settings → Testing: a change here applies there too, including manual SPEED ALL runs.', [
              opt('5', '', true),
              opt('A higher value', 'parallel tests split your bandwidth, so each one measures only part of the server\'s real speed.'),
            ])}
          </Group>
          <Group>
            <div className="modal-row">
              <span className="modal-row-label">{tt('Connection mode')}</span>
              <Select
                className="modal-input"
                style={{ width: 'auto', marginBottom: 0 }}
                value={stg?.auto_mode || ''}
                onChange={(e) => apply({ auto_mode: e.target.value })}
              >
                <option value="">{tt('Follow the main window')}</option>
                <option value="proxy">Proxy</option>
                <option value="tun">TUN</option>
              </Select>
            </div>
            {hint('The mode auto-connect connects in, including when it switches servers on its own. TUN needs the Vair helper service or administrator rights; without them auto-connect does not connect and the status above says so.', [
              opt('Follow the main window', 'uses the proxy or TUN choice made there, so an automatic switch never changes the mode you selected.', true),
              opt('Proxy', 'always connects in proxy mode.'),
              opt('TUN', 'always connects in TUN mode.'),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Health-check interval (s)')}</span>
              <NumField value={stg?.auto_health_sec || 15} min={5} max={600} onCommit={(v) => apply({ auto_health_sec: v })} />
            </div>
            {hint('The longest time the active connection may go without a check while auto-connect is on, in seconds, from 5 to 600. No check is sent while your own traffic passes through the tunnel in both directions — that already confirms the connection works. A connection that sends data but receives nothing is checked at once, regardless of this value.', [
              opt('15', '', true),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Failure threshold')}</span>
              <NumField value={stg?.auto_fail_threshold || 2} min={1} max={10} onCommit={(v) => apply({ auto_fail_threshold: v })} />
            </div>
            {hint('How many checks in a row must fail before auto-connect leaves the server and switches to the next one, from 1 to 10.', [
              opt('2', 'a single failure is often just a lost packet.', true),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Max latency (ms, 0 = off)')}</span>
              <NumField value={stg?.auto_max_latency_ms ?? 300} min={0} max={10000} onCommit={(v) => apply({ auto_max_latency_ms: v })} />
            </div>
            {hint('A latency limit for a working server, in milliseconds. When the check of the active connection takes longer than this as many times in a row as the failure threshold, auto-connect switches to the fastest available configuration — only to one faster than the current connection, and not before the minimum time on a server has passed. The server still works, so it is not treated as failed. A chain is not affected: it is closed only when it stops answering.', [
              opt('300', '', true),
              opt('0', 'the check is off: auto-connect stays on a working server however slow it is.'),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Switch tolerance (ms)')}</span>
              <NumField value={stg?.auto_switch_tolerance_ms ?? 20} min={0} max={5000} onCommit={(v) => apply({ auto_switch_tolerance_ms: v })} />
            </div>
            {hint('How much lower, in milliseconds, a candidate\'s delay must be before AUTO leaves a working server that has gone over Max latency. Used by the two delay modes: “Delay (last ping)” compares the live round-trip time with the candidate\'s last ping, “Delay, steady (history)” compares their histories. A smaller difference is not noticeable, while switching interrupts the current connection.', [
              opt('20', '', true),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Switch tolerance (speed, %)')}</span>
              <NumField value={stg?.auto_switch_speed_pct ?? 15} min={0} max={500} onCommit={(v) => apply({ auto_switch_speed_pct: v })} />
            </div>
            {hint('The same margin for the two speed modes, in per cent: how much faster a candidate must be. “Speed (last test)” compares the last measurements, “Speed, steady (history)” compares the history scores (average minus variation). The millisecond margin above is not used in these modes.', [
              opt('15', '', true),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Minimum time on a server (s)')}</span>
              <NumField value={stg?.auto_min_dwell_sec ?? 60} min={0} max={3600} onCommit={(v) => apply({ auto_min_dwell_sec: v })} />
            </div>
            {hint('How many seconds AUTO stays on a server before it may switch to a faster one. AUTO leaves a working server only when it goes over Max latency, or when the server\'s tab is removed from the candidate tabs; with Max latency at 0 it stays on a working server however fast the others are. This delay and the margins apply only to working servers: a server that stops responding is left as soon as the failure threshold is reached.', [
              opt('60', '', true),
              opt('0', 'no wait.'),
            ])}
          </Group>
        </div>

        {/* Collapsible CONNECTION LOG — what was connected, for how long, and
            what ended it. Collapsed by default; it answers a question that is
            only asked afterwards. */}
        <div
          className={'auto-collapse-head' + (sessOpen ? '' : ' collapsed')}
          title={tt('Show / hide the connection log')}
          onClick={() => setSessOpen((v) => !v)}
        >
          <span className="auto-collapse-arrow">▾</span>
          <span>{tt('Connection log')}</span>
        </div>
        <div className={'auto-collapse-body' + (sessOpen ? '' : ' collapsed')}>
          <Hint
            style={{ marginBottom: 6 }}
            en="The last hundred connections and what ended each one. Kept on this computer; nothing about the traffic itself is recorded."
          />
          {sessions.length === 0 ? (
            <div className="sess-empty">{tt('No connections recorded yet')}</div>
          ) : (
            <div className="sess-list">
              {sessions.map((s, i) => (
                <div className="sess-row" key={i}>
                  <span className="sess-when">{fmtSessTime(s.started_at)}</span>
                  <span className="sess-name" title={s.name}>
                    {s.name || '—'}
                  </span>
                  <span className="sess-mode">{s.mode === 'tun' ? 'TUN' : 'proxy'}</span>
                  <span className="sess-dur">{fmtDur(s.seconds)}</span>
                  <span className="sess-why" title={endReasonText(s.reason, tt)}>
                    {endReasonText(s.reason, tt)}
                  </span>
                </div>
              ))}
            </div>
          )}
          <div className="sess-actions">
            <button
              className="btn ghost"
              onClick={() => AutoService.ClearSessions().then(() => setSessions([]))}
            >
              {tt('clear')}
            </button>
          </div>
        </div>

        {/* Collapsible LOGS (collapsed by default). The filters name the
            engines and the levels as the log lines print them, so they stay
            English; the buttons are the dialog's and translate. */}
        <div
          className={'auto-collapse-head' + (logsOpen ? '' : ' collapsed')}
          title={tt('Show / hide logs')}
          onClick={() => setLogsOpen((v) => !v)}
        >
          <span className="auto-collapse-arrow">▾</span>
          <span>{tt('Logs')}</span>
        </div>
        <div className={'auto-collapse-body' + (logsOpen ? '' : ' collapsed')}>
          <div className="auto-log-head">
            <Select className="log-sel" value={logSrc} onChange={(e) => setLogSrc(e.target.value)}>
              <option value="">all</option>
              <option value="xray">xray</option>
              <option value="singbox">sing-box</option>
              <option value="vair">vair</option>
              <option value="test">test</option>
            </Select>
            <Select className="log-sel" value={logLvl} onChange={(e) => setLogLvl(e.target.value)}>
              <option value="">all</option>
              <option value="important">important</option>
              <option value="info">info+</option>
              <option value="warn">warn+</option>
              <option value="error">error</option>
            </Select>
            <Check className="log-auto" checked={logAuto} onChange={setLogAuto} label={tt('auto-scroll')} />
            <div className="spacer" style={{ flex: 1 }} />
            <div className="bgroup">
              <button className="btn ghost" onClick={copyLogs}>
                {logCopied ? tt('copied') : tt('copy')}
              </button>
              <button
                className="btn ghost"
                onClick={() => {
                  LogService.Clear()
                  setLogLines([])
                }}
              >
                {tt('clear')}
              </button>
            </div>
          </div>
          <div className="log-view" id="auto-log-view" ref={logViewRef}>
            {logBase().filter(logPass).map((l, i) => (
              <span key={i} className={'log-line lvl-' + (l.lvl || 'raw')}>
                <span className="lt">{fmtLogTime(l.t)}</span>
                <span className={'ls src-' + (l.src || '')}>{l.src === 'singbox' ? 'sing-box' : l.src}</span>
                {l.msg}
                {'\n'}
              </span>
            ))}
            {logLines.length === 0 && <span className="log-empty">{tt('— no log lines yet —')}</span>}
          </div>
        </div>
      </div>
      {histRaw ? (
        <HistoryModal raw={histRaw} nested onClose={() => setHistRaw('')} lang={lang} />
      ) : null}
    </div>
  )
}
