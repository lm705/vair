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
import { Hint, opt } from './hints'
import type { HintOpt } from './hints'
import { IS_REMOTE } from './remote'
import Select from './Select'
import HistoryModal from './HistoryModal'
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
  // The last check IP, for the connection it was made on: a new server or a
  // disconnect makes it stale, so it goes.
  const connected = conn?.status === 'connected'
  const [exit, setExit] = useState<ExitView | null>(null)
  useEffect(() => setExit(null), [conn?.status, conn?.conn_raw])
  const doCheckExit = () => {
    if (!connected || exit?.cls === 'testing') return
    setExit(exitChecking(tt))
    checkExit(tt).then(setExit)
  }
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
    navigator.clipboard.writeText(text).catch(() => {})
  }

  // ── status block (exact renderAutoStatus port) ──────────────────────
  const autoReasonText = (r: string): string => {
    if (!r) return ''
    if (r.indexOf('too slow') === 0) return tt('previous config too slow')
    if (r === 'probe failed') return tt('previous config stopped responding')
    if (r === 'slow but best') return tt('slow, but the fastest available')
    if (r === 'manual switch') return tt('manual switch')
    if (r === 'tabs changed') return tt('candidate tabs changed')
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
    } else if (st === 'paused') {
      txt = tt('Paused — reconnect to resume')
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
  const num = (v: string, min: number, def: number) => {
    const n = parseInt(v, 10)
    return isNaN(n) || n < min ? def : n
  }

  return (
    <div
      className="modal-overlay"
      id="auto-modal"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className="modal-box">
        <div className="auto-title-row">
          <div className="modal-title" style={{ marginBottom: 0 }}>
            {tt('Auto-connect')}
          </div>
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
        </div>

        <div className="settings-section">
          <div className="modal-row">
            <span className="modal-row-label">{tt('Enable auto-connect')}</span>
            <Toggle on={auto} onChange={setAutoEnabled} />
          </div>
          {hint('Connects to the fastest working config on launch and keeps it connected. While connected, the live link is monitored and, if it stops passing traffic, the app switches to another working config automatically.')}
        </div>

        {/* STATUS — live state + ranked candidates */}
        <div className="settings-section">
          <div className="section-header">{tt('Status')}</div>
          <div className={'auto-status' + (cls ? ' ' + cls : '')} id="auto-status">
            <span className="as-dot" />
            {txt}
            {sub && <div style={{ marginTop: 3, fontSize: '.85em', opacity: 0.7 }}>↻ {sub}</div>}
          </div>
          <div className="auto-cands" id="auto-cands">
            {cands.length === 0 ? (
              <div className="modal-hint" style={{ margin: 0 }}>
                {tt('No eligible candidates yet — run ping on the candidate tabs.')}
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
          <button className="btn ghost auto-switch-now" onClick={() => AutoService.SwitchNow()}>
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
          {exit && (
            <div className={'auto-exit ' + exit.cls} title={exit.title} onClick={doCheckExit}>
              {exit.label}
            </div>
          )}
        </div>

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
          <div className="settings-section">
            <div className="section-header">{tt('Candidate tabs')}</div>
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
            {hint("Which tabs' configs auto-connect may choose from. Defaults to Sources. Each tab's exclude filter is respected (hidden configs are never chosen), and each tab's own auto-refresh interval keeps its candidates up to date.")}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Re-test candidates after refresh')}</span>
              <Toggle
                on={stg?.auto_ping_refresh !== false}
                onChange={(v) => apply({ auto_ping_refresh: v })}
              />
            </div>
            {hint('After a candidate tab refreshes its config list, re-test its configs so failover has something real to rank them by — ping, and the speed test as well when the ranking uses speed. Adds test traffic on each refresh, and more of it in a speed mode.')}
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
            {/* One line per mode, said where the choice is made rather than in
                four paragraphs nobody finishes — and all four in the mark, where
                they have been asked for. Written out rather than looked up in a
                map: the translation checker reads literals, and a hint it cannot
                see is a hint that ships in English. */}
            <Hint
              en="What auto-connect compares when it ranks the candidates, and what it re-compares before leaving the server you are on. The two steady modes weigh several measurements instead of the last one."
              opts={[
                opt('Delay (last ping)', 'The last measured ping. Cheap and quick to react, and as noisy as one measurement taken under load.'),
                opt('Speed (last test)', 'The last measured download speed. Configs with no speed result fall back to ping order.'),
                opt('Delay, steady (history)', 'Typical delay over several measurements, charged for how much it varies and for probes that failed. A server answering in 80 ms two tries out of three ranks below one answering in 100 ms every time — right for a call or a page load, wrong for a leaderboard. Needs a few measurements per config.'),
                opt('Speed, steady (history)', 'The speed a server can be relied on: its typical speed less how much that varies, so steady beats bursty at the same average. A failed speed test is not counted against the server — that is usually the test endpoint refusing. Needs a few speed tests per config, and forgets them after six hours, because a speed measured this morning describes this morning.'),
              ]}
            >
              {(tt) =>
                rankMode === 'speed'
                  ? tt('The last measured download speed. Configs with no speed result fall back to ping order.')
                  : rankMode === 'history'
                    ? tt(
                        'Typical delay over several measurements, charged for how much it varies and for probes that failed. A server answering in 80 ms two tries out of three ranks below one answering in 100 ms every time — right for a call or a page load, wrong for a leaderboard. Needs a few measurements per config.',
                      )
                    : rankMode === 'speed_history'
                      ? tt(
                          'The speed a server can be relied on: its typical speed less how much that varies, so steady beats bursty at the same average. A failed speed test is not counted against the server — that is usually the test endpoint refusing. Needs a few speed tests per config, and forgets them after six hours, because a speed measured this morning describes this morning.',
                        )
                      : tt('The last measured ping. Cheap and quick to react, and as noisy as one measurement taken under load.')
              }
            </Hint>

            {/* The same two settings as Settings → Testing, deliberately: this is
                where the user is when they watch auto-connect test a pool, and
                sending them to another dialog to change how wide it tests is a
                detour. They write the same keys, so the two dialogs cannot drift
                — and the hint says so, because a duplicated control that quietly
                changed something elsewhere would be worse than no control. */}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Ping concurrency')}</span>
              <input
                className="modal-input num-input"
                type="number"
                min={1}
                max={200}
                defaultValue={stg?.ping_concurrency || 10}
                onBlur={(e) => apply({ ping_concurrency: num(e.target.value, 1, 10) })}
              />
            </div>
            <Hint>
              {(tt) => (
                <>
                  {tt('How many candidates are pinged at once. Default 10. Higher gets through a large pool sooner and makes each number noisier, since the tests share one channel.')}{' '}
                  <b>{tt('The same setting as Settings → Testing: changing it here changes it there, and it applies to manual PING ALL runs too.')}</b>
                </>
              )}
            </Hint>
            <div className="modal-row">
              <span className="modal-row-label">{tt('Speed concurrency')}</span>
              <input
                className="modal-input num-input"
                type="number"
                min={1}
                max={100}
                defaultValue={stg?.speed_concurrency || 5}
                onBlur={(e) => apply({ speed_concurrency: num(e.target.value, 1, 5) })}
              />
            </div>
            <Hint>
              {(tt) => (
                <>
                  {tt('How many candidates are speed-tested at once — only used by the ranking modes that measure speed. Default 5. This one divides your bandwidth rather than sharing a channel: several at once each measure a fraction of what they would reach alone.')}{' '}
                  <b>{tt('The same setting as Settings → Testing: changing it here changes it there, and it applies to manual SPEED ALL runs too.')}</b>
                </>
              )}
            </Hint>
          </div>
          <div className="settings-section">
            <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
              <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                {tt('Connection mode')}
              </span>
              <Select
                className="modal-input"
                style={{ marginBottom: 0 }}
                value={stg?.auto_mode || ''}
                onChange={(e) => apply({ auto_mode: e.target.value })}
              >
                <option value="">{tt('Follow the main window')}</option>
                <option value="proxy">Proxy</option>
                <option value="tun">TUN</option>
              </Select>
            </div>
            {hint('Which mode auto-connect brings a server up in, including when it switches on its own. "Follow the main window" uses the proxy/tun choice showing there, so an auto switch never comes up in a mode you did not pick.')}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Health-check interval (s)')}</span>
              <input
                className="modal-input num-input"
                type="number"
                min={5}
                max={600}
                defaultValue={stg?.auto_health_sec || 15}
                onBlur={(e) => apply({ auto_health_sec: num(e.target.value, 5, 15) })}
              />
            </div>
            {hint('How long the live connection may go unasked, while auto-connect is on. A limit rather than a schedule: while your own traffic is crossing the tunnel and coming back it has already answered the question, and no probe is sent. The probe is for a quiet connection — and for one that has been sending with nothing coming back, which is asked about at once whatever this says. Default 15 s.')}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Failure threshold')}</span>
              <input
                className="modal-input num-input"
                type="number"
                min={1}
                max={10}
                defaultValue={stg?.auto_fail_threshold || 2}
                onBlur={(e) => apply({ auto_fail_threshold: num(e.target.value, 1, 2) })}
              />
            </div>
            {hint('How many probes in a row have to fail before auto-connect gives up on the server and moves to the next one. Default 2 — one failure is often just a lost packet, and switching on it would move you off a working server.')}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Max latency (ms, 0 = off)')}</span>
              <input
                className="modal-input num-input"
                type="number"
                min={0}
                max={10000}
                defaultValue={stg?.auto_max_latency_ms || 0}
                onBlur={(e) => apply({ auto_max_latency_ms: num(e.target.value, 0, 0) })}
              />
            </div>
            {hint('Treat the current config as failing when the live probe is slower than this, then switch to the fastest available config. 0 disables the speed check.')}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Switch tolerance (ms)')}</span>
              <input
                className="modal-input num-input"
                type="number"
                min={0}
                max={5000}
                defaultValue={stg?.auto_switch_tolerance_ms || 20}
                onBlur={(e) => apply({ auto_switch_tolerance_ms: num(e.target.value, 0, 20) })}
              />
            </div>
            {hint('How much lower a candidate has to be before AUTO leaves a working server. Used by the two ping modes: in "by ping" that is the live round-trip against the candidate\'s ping, in "by ping, steady" it is history against history. Below this margin the two are the same server as far as you are concerned, and switching only costs you the connection you had.')}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Switch tolerance (speed, %)')}</span>
              <input
                className="modal-input num-input"
                type="number"
                min={0}
                max={500}
                defaultValue={stg?.auto_switch_speed_pct || 15}
                onBlur={(e) => apply({ auto_switch_speed_pct: num(e.target.value, 0, 15) })}
              />
            </div>
            {hint('The same margin for the two speed modes: how much faster a candidate has to be, in per cent. In "by speed" that compares the last two measurements, in "by speed, steady" the two history scores — the average less its spread. The millisecond margin above is not used in these modes.')}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Minimum time on a server (s)')}</span>
              <input
                className="modal-input num-input"
                type="number"
                min={0}
                max={3600}
                defaultValue={stg?.auto_min_dwell_sec || 60}
                onBlur={(e) => apply({ auto_min_dwell_sec: num(e.target.value, 0, 60) })}
              />
            </div>
            {hint('How long AUTO must stay on a server before it may leave for a faster one. Both margins and this delay hold back the slow-but-alive case only — a server that stops answering is left as soon as the failure threshold is reached.')}

          </div>
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
                  <span className="sess-why" title={s.reason}>
                    {s.reason}
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

        {/* Collapsible LOGS (collapsed by default; labels stay English) */}
        <div
          className={'auto-collapse-head' + (logsOpen ? '' : ' collapsed')}
          title="Show / hide logs"
          onClick={() => setLogsOpen((v) => !v)}
        >
          <span className="auto-collapse-arrow">▾</span>
          <span>Logs</span>
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
            <Check className="log-auto" checked={logAuto} onChange={setLogAuto} label="auto-scroll" />
            <div className="spacer" style={{ flex: 1 }} />
            <button className="btn ghost" onClick={copyLogs}>
              copy
            </button>
            <button
              className="btn ghost"
              onClick={() => {
                LogService.Clear()
                setLogLines([])
              }}
            >
              clear
            </button>
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
            {logLines.length === 0 && <span className="log-empty">— no log lines yet —</span>}
          </div>
        </div>

        <div className="modal-btns">
          <button className="btn ghost auto-close-btn" onClick={onClose}>
            {tt('close')}
          </button>
        </div>
      </div>
      {histRaw ? (
        <HistoryModal raw={histRaw} nested onClose={() => setHistRaw('')} lang={lang} />
      ) : null}
    </div>
  )
}
