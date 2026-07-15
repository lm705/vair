// AUTO panel — an exact port of the 1.10 openAuto() DOM: enable toggle, live
// STATUS (dot + text + reason), ranked candidate list (click to connect),
// "Switch now", collapsible Settings (expanded) and Logs (collapsed, src=vair).
// Always rendered in its OWN window (/?view=auto) alongside the main window;
// Detach hides the main window, Back-to-app restores it (swap driven by
// mainVisible), the titlebar ✕ closes just this window.
import { useEffect, useRef, useState } from 'react'
import { Events } from '@wailsio/runtime'
import { AutoService, LogService } from '../bindings/vair'
import { t10 } from './i18n'
import { IS_REMOTE } from './remote'

function fmtLogTime(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => (n < 10 ? '0' + n : '' + n)
  return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds())
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
  selectedMode,
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
  selectedMode: string
  lang: string
  onClose: () => void
}) {
  const tt = (en: string) => t10(lang, en)
  const [cands, setCands] = useState<any[]>([])
  // Collapse state persists across open/close and detach (localStorage) — the
  // 1.10 defaults (settings expanded, logs collapsed) apply only the first time.
  const [settingsOpen, setSettingsOpen] = useState(() => localStorage.getItem('auto.settingsOpen') !== '0')
  const [logsOpen, setLogsOpen] = useState(() => localStorage.getItem('auto.logsOpen') === '1')
  useEffect(() => {
    localStorage.setItem('auto.settingsOpen', settingsOpen ? '1' : '0')
  }, [settingsOpen])
  useEffect(() => {
    localStorage.setItem('auto.logsOpen', logsOpen ? '1' : '0')
  }, [logsOpen])
  const [logLines, setLogLines] = useState<any[]>([])
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
    if (logsOpen) LogService.Get().then((ls) => setLogLines(ls ?? []))
  }, [logsOpen])
  useEffect(() => {
    const off = Events.On('log', (e: any) => {
      if (!logsOpenRef.current) return
      const batch = e?.data?.payload
      if (Array.isArray(batch)) setLogLines((prev) => [...prev, ...batch].slice(-2000))
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

  const bySpeed = !!stg?.auto_rank_by_speed
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
          <div className="modal-hint">
            {tt('Connects to the fastest working config on launch and keeps it connected. While connected, the live link is monitored and, if it stops passing traffic, the app switches to another working config automatically.')}
          </div>
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
                else if (bySpeed && c.speed_mbps > 0)
                  metric = c.speed_mbps.toFixed(1) + ' MB/s' + (ms ? ' · ' + ms : '')
                else if (ms) metric = ms
                else metric = tt('untested')
                return (
                  <div
                    key={c.raw || i}
                    className={'auto-cand' + (c.current ? ' current' : '')}
                    title={tt('Click to connect to this config')}
                    onClick={() => AutoService.ConnectCand(c.raw, selectedMode)}
                  >
                    <span className="ac-rank">{i + 1}</span>
                    <span className="ac-name">
                      {c.name}
                      {c.current ? ' •' : ''}
                    </span>
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
            <div className="modal-hint">
              {tt("Which tabs' configs auto-connect may choose from. Defaults to Sources. Each tab's exclude filter is respected (hidden configs are never chosen), and each tab's own auto-refresh interval keeps its candidates up to date.")}
            </div>
            <div className="modal-row">
              <span className="modal-row-label">{tt('Re-test candidates after refresh')}</span>
              <Toggle
                on={stg?.auto_ping_refresh !== false}
                onChange={(v) => apply({ auto_ping_refresh: v })}
              />
            </div>
            <div className="modal-hint">
              {tt('After a candidate tab refreshes its config list, re-run ping on its configs so failover can rank them by real delay. Adds some test traffic on each refresh.')}
            </div>
            <div className="modal-row">
              <span className="modal-row-label">
                {tt('Prefer faster (by speed test)')} <span className="rec-badge">{tt('recommended')}</span>
              </span>
              <Toggle on={!!stg?.auto_rank_by_speed} onChange={(v) => apply({ auto_rank_by_speed: v })} />
            </div>
            <div className="modal-hint">
              {tt('Rank candidates by measured download speed (Mbps) instead of ping delay. Configs without a speed result fall back to ping order.')}
            </div>
          </div>
          <div className="settings-section">
            <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
              <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                {tt('Connection mode')}
              </span>
              <select
                className="modal-input"
                style={{ marginBottom: 0 }}
                value={stg?.auto_mode || ''}
                onChange={(e) => apply({ auto_mode: e.target.value })}
              >
                <option value="">{tt('Remember last')}</option>
                <option value="proxy">Proxy</option>
                <option value="tun">TUN</option>
              </select>
            </div>
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
            <div className="modal-hint">
              {tt('How often the live connection is probed, and how many checks in a row must fail before switching. Defaults: 15 s, 2.')}
            </div>
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
            <div className="modal-hint">
              {tt('Treat the current config as failing when the live probe is slower than this, then switch to the fastest available config. 0 disables the speed check.')}
            </div>
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
            <select className="log-sel" value={logSrc} onChange={(e) => setLogSrc(e.target.value)}>
              <option value="">all</option>
              <option value="xray">xray</option>
              <option value="singbox">sing-box</option>
              <option value="vair">vair</option>
              <option value="test">test</option>
            </select>
            <select className="log-sel" value={logLvl} onChange={(e) => setLogLvl(e.target.value)}>
              <option value="">all</option>
              <option value="info">info+</option>
              <option value="warn">warn+</option>
              <option value="error">error</option>
            </select>
            <label className="log-auto">
              <input type="checkbox" checked={logAuto} onChange={(e) => setLogAuto(e.target.checked)} />{' '}
              auto-scroll
            </label>
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

        <div className="modal-btns">
          <button className="btn ghost auto-close-btn" onClick={onClose}>
            {tt('close')}
          </button>
        </div>
      </div>
    </div>
  )
}
