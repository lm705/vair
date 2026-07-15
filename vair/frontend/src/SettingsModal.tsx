// Settings modal — an exact port of the 1.10 openSettings() DOM (same classes,
// same sections/rows/hints, instant apply on change, no Save button).
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Events } from '@wailsio/runtime'
import { SettingsService, UpdateService, QRService } from '../bindings/vair'
import { t10 } from './i18n'

// Exact 1.10 preset lists (web/index.html).
const PING_PRESETS = [
  { url: '', label: 'https://www.gstatic.com/generate_204 (default)' },
  { url: 'https://www.google.com/generate_204', label: 'https://www.google.com/generate_204' },
  { url: 'https://detectportal.firefox.com/success.txt', label: 'https://detectportal.firefox.com/success.txt' },
  { url: 'https://captive.apple.com/hotspot-detect.html', label: 'https://captive.apple.com/hotspot-detect.html' },
  { url: 'http://www.msftconnecttest.com/connecttest.txt', label: 'http://www.msftconnecttest.com/connecttest.txt' },
]
const SPEED_PRESETS = [
  { url: '', label: 'https://speed.cloudflare.com/__down?bytes=50000000 (default)' },
  { url: 'https://speed.cloudflare.com/__down?bytes=10000000', label: 'https://speed.cloudflare.com/__down?bytes=10000000' },
  { url: 'http://cachefly.cachefly.net/100mb.test', label: 'http://cachefly.cachefly.net/100mb.test' },
  { url: 'https://proof.ovh.net/files/100Mb.dat', label: 'https://proof.ovh.net/files/100Mb.dat' },
]
const SPEED_DEFAULT_URL = 'https://speed.cloudflare.com/__down?bytes=50000000'
const CACHEFLY = 'http://cachefly.cachefly.net/100mb.test'
const TLS_FPS = ['chrome', 'firefox', 'safari', 'ios', 'android', 'edge', 'random', 'randomized']

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
function socksRandHex(n: number): string {
  const b = new Uint8Array(Math.ceil(n / 2))
  crypto.getRandomValues(b)
  return [...b].map((x) => x.toString(16).padStart(2, '0')).join('').slice(0, n)
}
function isCustomURL(cur: string, presets: { url: string }[]): boolean {
  if (!cur) return false
  return !presets.some((p) => p.url === cur)
}
function isCustomFallback(cur: string): boolean {
  if (cur === '' || cur === '__none') return false
  return !SPEED_PRESETS.some((p) => (p.url || SPEED_DEFAULT_URL) === cur)
}
function staticHostsToText(obj: any): string {
  if (!obj || typeof obj !== 'object') return ''
  return Object.keys(obj)
    .sort()
    .map((k) => k + ' ' + obj[k])
    .join('\n')
}
function parseStaticHosts(raw: string): Record<string, string> {
  const out: Record<string, string> = {}
  ;(raw || '').split(/\r?\n/).forEach((line) => {
    const t = line.trim()
    if (!t || t[0] === '#') return
    const m = t.split(/[\s=,]+/)
    if (m.length >= 2 && m[0] && m[1]) out[m[0].toLowerCase()] = m[1]
  })
  return out
}

// ── small building blocks (1.10 markup) ─────────────────────────────
function Toggle({ on, onChange }: { on: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="toggle">
      <input type="checkbox" checked={on} onChange={(e) => onChange(e.target.checked)} />
      <span className="toggle-track" />
      <span className="toggle-thumb" />
    </label>
  )
}

function Chips({
  items,
  disabled,
  placeholder,
  onChange,
}: {
  items: string[]
  disabled?: boolean
  placeholder: string
  onChange: (next: string[]) => void
}) {
  const [val, setVal] = useState('')
  const add = (raw: string) => {
    const parts = raw
      .split(/[\s,;]+/)
      .map((s) => s.trim().toLowerCase())
      .filter(Boolean)
    if (!parts.length) return
    const next = [...items]
    for (const p of parts) if (!next.includes(p)) next.push(p)
    onChange(next)
  }
  return (
    <div className="chips-wrap" style={disabled ? { opacity: 0.45 } : undefined}>
      {items.map((d) => (
        <span key={d} className="chip">
          {d}
          <span className="chip-x" onClick={() => onChange(items.filter((x) => x !== d))}>
            x
          </span>
        </span>
      ))}
      <input
        className="chip-input"
        placeholder={placeholder}
        value={val}
        onChange={(e) => setVal(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            add(val)
            setVal('')
          }
        }}
        onPaste={(e) => {
          e.preventDefault()
          add(e.clipboardData.getData('text'))
          setVal('')
        }}
      />
    </div>
  )
}

export default function SettingsModal({
  stg,
  apply,
  refresh,
  onClose,
  notify,
  lang,
  version,
}: {
  stg: any
  apply: (patch: Record<string, any>) => void
  refresh: () => void
  onClose: () => void
  notify: (msg: string) => void
  lang: string
  version: string
}) {
  const tt = (en: string) => t10(lang, en)
  // On blur, empty/invalid input snaps back to the default IN THE FIELD (not only
  // on reopen): coerce, write the value back into the box, then persist it.
  const numBlur =
    (key: string, def: number) => (e: React.FocusEvent<HTMLInputElement>) => {
      const v = +e.target.value || def
      e.target.value = String(v)
      apply({ [key]: v })
    }
  const txtBlur =
    (key: string, def: string, extra?: Record<string, any>) =>
    (e: React.FocusEvent<HTMLInputElement>) => {
      const v = e.target.value.trim() || def
      e.target.value = v
      apply({ [key]: v, ...(extra || {}) })
    }
  const [importTabs, setImportTabs] = useState(true)
  const [procOpen, setProcOpen] = useState(false)
  const [procList, setProcList] = useState<string[]>([])
  const [procFilter, setProcFilter] = useState('')
  const openProcPicker = () => {
    SettingsService.ListProcesses().then((l) => setProcList(l ?? []))
    setProcFilter('')
    setProcOpen(true)
  }
  // Updates section state (1.10 checkUpdate/onUpdateStatus).
  const [updMsg, setUpdMsg] = useState<string | null>(null)
  const [updLatest, setUpdLatest] = useState<{ latest: string; notes?: string } | null>(null)
  const [updPct, setUpdPct] = useState<number | null>(null)
  const [updBusy, setUpdBusy] = useState(false)

  // Remote access (control from a phone on the same LAN). The server listens on
  // every interface, so the IP choice only decides which address goes into the
  // displayed URL + QR (a PC with VPN/virtual adapters has several).
  const [remote, setRemote] = useState<any>(null)
  const [remoteQR, setRemoteQR] = useState('')
  const remoteIP =
    stg.remote_ip && remote?.ips?.includes(stg.remote_ip) ? stg.remote_ip : remote?.ips?.[0] || ''
  const remoteURL = remoteIP ? `http://${remoteIP}:${remote.port}/?key=${remote.token}` : ''
  useEffect(() => {
    SettingsService.Remote().then((r: any) => setRemote(r))
  }, [stg.remote_enabled, stg.remote_port])

  useEffect(() => {
    if (remoteURL) QRService.ForText(remoteURL).then((d: string) => setRemoteQR(d || ''))
    else setRemoteQR('')
  }, [remoteURL])
  const checkUpdate = () => {
    setUpdBusy(true)
    setUpdMsg(t10(lang, 'Checking…'))
    UpdateService.Check().then((j: any) => {
      setUpdBusy(false)
      if (j?.error) {
        setUpdMsg(t10(lang, 'Could not check for updates') + ': ' + j.error)
        setUpdLatest(null)
        return
      }
      if (j?.available) {
        setUpdMsg(t10(lang, 'Update available') + ': ' + j.latest + (j.notes ? ' — ' + j.notes : ''))
        setUpdLatest({ latest: j.latest, notes: j.notes })
      } else {
        setUpdMsg(t10(lang, 'You have the latest version.'))
        setUpdLatest(null)
      }
    })
  }
  useEffect(() => {
    const off = Events.On('update_status', (e: any) => {
      const p = e?.data?.payload
      if (!p) return
      switch (p.state) {
        case 'checking':
          setUpdMsg(t10(lang, 'Checking…'))
          break
        case 'downloading':
          setUpdPct(p.pct || 0)
          setUpdMsg(t10(lang, 'Downloading update') + ' ' + (p.msg || '') + ' — ' + (p.pct || 0) + '%')
          break
        case 'verifying':
          setUpdMsg(t10(lang, 'Verifying…'))
          break
        case 'ready':
          setUpdMsg(t10(lang, 'Update ready — restarting…'))
          break
        case 'uptodate':
          setUpdPct(null)
          setUpdMsg(t10(lang, 'You have the latest version.'))
          setUpdBusy(false)
          break
        case 'error':
          setUpdPct(null)
          setUpdMsg(t10(lang, 'Update failed') + ': ' + (p.msg || ''))
          setUpdBusy(false)
          break
      }
    })
    return () => {
      if (typeof off === 'function') off()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lang])
  // "Custom URL…" rows stay open after selecting __custom even while the saved
  // URL is still empty (1.10 shows the input without saving until typed).
  const [customPing, setCustomPing] = useState(isCustomURL(stg.ping_test_url || '', PING_PRESETS))
  const [customSpeed, setCustomSpeed] = useState(isCustomURL(stg.speed_test_url || '', SPEED_PRESETS))
  // Proxy bind "Custom" stays selected while its address is still empty.
  const [proxyCustom, setProxyCustom] = useState(() => {
    const b = stg.proxy_bind || (stg.proxy_allow_lan ? '0.0.0.0' : '127.0.0.1')
    return b !== '127.0.0.1' && b !== '0.0.0.0'
  })
  // Proxy bind address: localhost (default) / LAN (0.0.0.0) / custom. proxyCustom
  // (above) keeps "custom" selected even while its address is still empty.
  const proxyBindStored = stg.proxy_bind || (stg.proxy_allow_lan ? '0.0.0.0' : '127.0.0.1')
  const proxyCustomVal = proxyBindStored !== '127.0.0.1' && proxyBindStored !== '0.0.0.0' ? proxyBindStored : ''
  const proxySelect = proxyCustom ? 'custom' : proxyBindStored === '0.0.0.0' ? 'lan' : 'local'
  const proxyBindShown =
    proxySelect === 'local'
      ? '127.0.0.1'
      : proxySelect === 'lan'
        ? `${remote?.ips?.[0] || '0.0.0.0'} (0.0.0.0)`
        : proxyCustomVal || '—'
  const [customFb, setCustomFb] = useState(isCustomFallback(stg.speed_test_url_fallback || ''))

  const rmode = stg.routing_mode || (stg.ru_sites_direct ? 'bypass_ru' : 'proxy_all')
  const fbCur = (() => {
    const cur = stg.speed_test_url_fallback || ''
    return cur === '' ? CACHEFLY : cur
  })()

  // ── settings search ──────────────────────────────────────────────
  // Filters the rendered rows by text (so it matches whichever language is
  // shown). DOM-driven on purpose: the modal is a 1:1 port of the 1.10 DOM and
  // restructuring every section into filterable data would fork it from the
  // reference. Rows are grouped as ".modal-row + following non-row siblings"
  // (hints, chip boxes and sub-blocks belong to the row label above them); a
  // section whose header matches shows whole; a section with no matches hides.
  const boxRef = useRef<HTMLDivElement>(null)
  const [q, setQ] = useState('')
  // Hiding/showing goes through the .set-hidden class (display:none!important),
  // NEVER through style.display: many rows carry an intentional inline
  // display:block (the 1.10 stacked label-above-control layout — DNS, test
  // URLs, routing mode), and resetting style.display would wipe it, collapsing
  // them back to the flex label|control row.
  useLayoutEffect(() => {
    const box = boxRef.current
    if (!box) return
    const query = q.trim().toLowerCase()
    for (const sec of Array.from(box.querySelectorAll<HTMLElement>('.settings-section'))) {
      const header = sec.querySelector<HTMLElement>('.section-header')
      const kids = Array.from(sec.children) as HTMLElement[]
      kids.forEach((k) => k.classList.remove('set-hidden'))
      sec.classList.remove('set-hidden')
      if (!query) continue
      if ((header?.textContent || '').toLowerCase().includes(query)) continue
      let any = false
      let group: HTMLElement[] = []
      const flush = () => {
        if (!group.length) return
        const show = group
          .map((g) => g.textContent || '')
          .join(' ')
          .toLowerCase()
          .includes(query)
        group.forEach((g) => g.classList.toggle('set-hidden', !show))
        if (show) any = true
        group = []
      }
      for (const k of kids) {
        if (k === header) continue
        if (k.classList.contains('modal-row')) flush()
        group.push(k)
      }
      flush()
      sec.classList.toggle('set-hidden', !any)
    }
  }, [q, stg])

  const hint = (en: string) => <div className="modal-hint">{tt(en)}</div>

  return (
    <div
      className="modal-overlay"
      id="settings-modal"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className="modal-box" ref={boxRef}>
        <div className="modal-title">{tt('Settings')}</div>
        <input
          className="modal-input"
          id="set-search"
          placeholder={tt('Search settings…')}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          style={{ marginBottom: 14 }}
        />

        {/* ── Sources ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Sources')}</div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Enable Sources tab')}</span>
            <Toggle on={stg.sources_enabled !== false} onChange={(v) => apply({ sources_enabled: v })} />
          </div>
        </div>

        {/* ── Routing ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Routing')}</div>
          <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
            <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
              {tt('Routing mode')}
            </span>
            <select
              className="modal-input"
              style={{ marginBottom: 0 }}
              value={rmode}
              onChange={(e) => apply({ routing_mode: e.target.value })}
            >
              <option value="proxy_all">{tt('All traffic through VPN')}</option>
              <option value="bypass_ru">{tt('Everything except Russian sites')}</option>
              <option value="only_blocked">{tt('Only blocked-in-Russia resources')}</option>
            </select>
          </div>
          {hint('How traffic is split between the VPN and a direct connection. Takes effect on next connection.')}
          {rmode === 'only_blocked' && (
            <div id="only-blocked-opts">
              <div className="modal-row" style={{ marginTop: 10, marginBottom: 4 }}>
                <span className="modal-row-label">{tt('Custom domains through VPN')}</span>
                <Toggle
                  on={!stg.proxy_domains_disabled}
                  onChange={(v) => apply({ proxy_domains_disabled: !v })}
                />
              </div>
              <Chips
                items={stg.proxy_domains || []}
                disabled={!!stg.proxy_domains_disabled}
                placeholder={tt('e.g. youtube.com, press Enter')}
                onChange={(next) => apply({ proxy_domains: next })}
              />
              {hint('Domains routed THROUGH the VPN in addition to the built-in blocked-list. Takes effect on next connection.')}
              <div className="modal-row" style={{ display: 'block', marginTop: 10, marginBottom: 4 }}>
                <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                  {tt('Custom blocklist URL')}
                </span>
                <input
                  className="modal-input"
                  style={{ marginBottom: 0 }}
                  defaultValue={stg.blocklist_url || ''}
                  placeholder="https://…/domains.txt"
                  onBlur={(e) => apply({ blocklist_url: e.target.value.trim() })}
                />
              </div>
              <div className="modal-hint" style={{ marginTop: 0 }}>
                {tt('Optional plain-text domain list (one per line) fetched and routed through the VPN. Auto-updated.')}
              </div>
            </div>
          )}
          <div className="modal-row" style={{ marginTop: 10, marginBottom: 4 }}>
            <span className="modal-row-label">{tt('Custom domains without VPN')}</span>
            <Toggle
              on={!stg.direct_domains_disabled}
              onChange={(v) => apply({ direct_domains_disabled: !v })}
            />
          </div>
          <Chips
            items={stg.direct_domains || []}
            disabled={!!stg.direct_domains_disabled}
            placeholder={tt('e.g. vk.com, press Enter')}
            onChange={(next) => apply({ direct_domains: next })}
          />
          {hint('Enter a domain — all its subdomains are included automatically. Takes effect on next connection.')}
          <div className="modal-row" style={{ marginTop: 10, marginBottom: 4 }}>
            <span className="modal-row-label">{tt('Apps without VPN (TUN mode only)')}</span>
            <Toggle on={!stg.direct_apps_disabled} onChange={(v) => apply({ direct_apps_disabled: !v })} />
          </div>
          <Chips
            items={stg.direct_apps || []}
            disabled={!!stg.direct_apps_disabled}
            placeholder={tt('e.g. chrome.exe, press Enter')}
            onChange={(next) => apply({ direct_apps: next })}
          />
          <div className="modal-hint">
            {tt("Process names that bypass VPN. Only works in TUN mode (system proxy can't be excluded per-app at the OS level).")}{' '}
            <a
              href="#"
              onClick={(e) => {
                e.preventDefault()
                openProcPicker()
              }}
              style={{ color: 'var(--accent)', textDecoration: 'underline' }}
            >
              {tt('Browse running processes')}
            </a>
            . {tt('Takes effect on next connection.')}
          </div>
        </div>

        {/* ── Ports ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Ports')}</div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('HTTP proxy port')}</span>
            <input
              className="modal-input num-input"
              type="number"
              min={1}
              max={65535}
              defaultValue={stg.http_port || 10819}
              onBlur={numBlur('http_port', 10819)}
            />
          </div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('SOCKS proxy port')}</span>
            <input
              className="modal-input num-input"
              type="number"
              min={1}
              max={65535}
              defaultValue={stg.socks_port || 10818}
              onBlur={numBlur('socks_port', 10818)}
            />
          </div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Proxy access')}</span>
            <select
              className="modal-input"
              style={{ width: 'auto', marginBottom: 0 }}
              value={proxySelect}
              onChange={(e) => {
                const m = e.target.value
                if (m === 'custom') {
                  setProxyCustom(true) // show the input; don't save until an address is typed
                } else {
                  setProxyCustom(false)
                  apply({ proxy_bind: m === 'lan' ? '0.0.0.0' : '127.0.0.1', proxy_allow_lan: false })
                }
              }}
            >
              <option value="local">{tt('Localhost only')} (127.0.0.1)</option>
              <option value="lan">{tt('Allow LAN access')} (0.0.0.0)</option>
              <option value="custom">{tt('Custom')}</option>
            </select>
          </div>
          {proxyCustom && (
            <div className="modal-row">
              <span className="modal-row-label">{tt('Bind address')}</span>
              <input
                className="modal-input"
                style={{ width: 'auto', marginBottom: 0 }}
                placeholder="192.168.0.10"
                defaultValue={proxyCustomVal}
                onBlur={txtBlur('proxy_bind', '127.0.0.1', { proxy_allow_lan: false })}
              />
            </div>
          )}
          <div className="modal-hint">
            {tt('Address the proxy will use')}: <b>{proxyBindShown}</b>
          </div>
          {hint('The address the local HTTP/SOCKS proxy listens on when connected. Localhost = this PC only. Allow LAN access = other devices on your network can reach it (binds 0.0.0.0). Custom = a specific interface address. Ports: HTTP 10819, SOCKS 10818 by default (above). Beyond localhost, use only on a trusted network — SOCKS keeps its password, HTTP has none. Applies on the next connection.')}
        </div>

        {/* ── Testing ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Testing')}</div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Ping concurrency')}</span>
            <input
              className="modal-input num-input"
              type="number"
              min={1}
              max={200}
              defaultValue={stg.ping_concurrency || 10}
              onBlur={numBlur('ping_concurrency', 10)}
            />
          </div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Speed concurrency')}</span>
            <input
              className="modal-input num-input"
              type="number"
              min={1}
              max={100}
              defaultValue={stg.speed_concurrency || 5}
              onBlur={numBlur('speed_concurrency', 5)}
            />
          </div>
          {hint('How many configs are pinged or speed-tested in parallel. Defaults: ping 10, speed 5. Takes effect on the next bulk test run.')}
          <div className="modal-row">
            <span className="modal-row-label">{tt('Warm-up timeout (ms)')}</span>
            <input
              className="modal-input num-input"
              type="number"
              min={200}
              max={20000}
              step={100}
              defaultValue={stg.warmup_timeout_ms || 4000}
              onBlur={numBlur('warmup_timeout_ms', 4000)}
            />
          </div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Ping timeout (ms)')}</span>
            <input
              className="modal-input num-input"
              type="number"
              min={200}
              max={10000}
              step={100}
              defaultValue={stg.ping_timeout_ms || 1500}
              onBlur={numBlur('ping_timeout_ms', 1500)}
            />
          </div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Speed test duration (s)')}</span>
            <input
              className="modal-input num-input"
              type="number"
              min={1}
              max={60}
              defaultValue={stg.speed_duration_sec || 4}
              onBlur={numBlur('speed_duration_sec', 4)}
            />
          </div>
          {hint('Warm-up timeout bounds the first un-measured request that establishes the tunnel (TCP + TLS/Reality handshake) — raise it if working configs are wrongly marked "timeout". Ping timeout is per round (3 rounds run, best is reported). Speed duration is how long the test downloads before computing throughput. Defaults: warm-up 4000 ms, ping 1500 ms, speed 4 s.')}
          <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
            <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
              {tt('Ping URL')}
            </span>
            <select
              className="modal-input"
              style={{ marginBottom: 0 }}
              value={customPing ? '__custom' : stg.ping_test_url || ''}
              onChange={(e) => {
                if (e.target.value === '__custom') {
                  setCustomPing(true)
                  return
                }
                setCustomPing(false)
                apply({ ping_test_url: e.target.value })
              }}
            >
              {PING_PRESETS.map((p) => (
                <option key={p.label} value={p.url}>
                  {p.label}
                </option>
              ))}
              <option value="__custom">Custom URL…</option>
            </select>
          </div>
          {customPing && (
            <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
              <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                {tt('Custom ping URL')}
              </span>
              <input
                className="modal-input"
                style={{ marginBottom: 0 }}
                placeholder="https://..."
                defaultValue={isCustomURL(stg.ping_test_url || '', PING_PRESETS) ? stg.ping_test_url : ''}
                onBlur={(e) => {
                  const v = e.target.value.trim()
                  if (!v) setCustomPing(false) // cleared → drop back to the default preset
                  apply({ ping_test_url: v })
                }}
              />
            </div>
          )}
          <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
            <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
              {tt('Speed URL')}
            </span>
            <select
              className="modal-input"
              style={{ marginBottom: 0 }}
              value={customSpeed ? '__custom' : stg.speed_test_url || ''}
              onChange={(e) => {
                if (e.target.value === '__custom') {
                  setCustomSpeed(true)
                  return
                }
                setCustomSpeed(false)
                apply({ speed_test_url: e.target.value })
              }}
            >
              {SPEED_PRESETS.map((p) => (
                <option key={p.label} value={p.url}>
                  {p.label}
                </option>
              ))}
              <option value="__custom">Custom URL…</option>
            </select>
          </div>
          {customSpeed && (
            <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
              <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                {tt('Custom speed URL')}
              </span>
              <input
                className="modal-input"
                style={{ marginBottom: 0 }}
                placeholder="https://..."
                defaultValue={isCustomURL(stg.speed_test_url || '', SPEED_PRESETS) ? stg.speed_test_url : ''}
                onBlur={(e) => {
                  const v = e.target.value.trim()
                  if (!v) setCustomSpeed(false) // cleared → drop back to the default preset
                  apply({ speed_test_url: v })
                }}
              />
            </div>
          )}
          <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
            <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
              {tt('Speed URL fallback')}{' '}
              <span style={{ color: 'var(--muted)', fontWeight: 400 }}>
                {tt('(used when the main URL returns HTTP 429)')}
              </span>
            </span>
            <select
              className="modal-input"
              style={{ marginBottom: 0 }}
              value={
                customFb ? '__custom' : stg.speed_test_url_fallback === '__none' ? '__none' : fbCur
              }
              onChange={(e) => {
                const v = e.target.value
                if (v === '__custom') {
                  setCustomFb(true)
                  return
                }
                setCustomFb(false)
                apply({ speed_test_url_fallback: v })
              }}
            >
              <option value="__none">{tt('None — no fallback')}</option>
              {SPEED_PRESETS.map((p) => {
                const url = p.url || SPEED_DEFAULT_URL
                let lbl = (p.label || '').replace(/\s*\(default\)\s*$/, '')
                if (url === CACHEFLY) lbl += ' (default)'
                return (
                  <option key={url} value={url}>
                    {lbl}
                  </option>
                )
              })}
              <option value="__custom">Custom URL…</option>
            </select>
          </div>
          {customFb && (
            <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
              <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                {tt('Custom speed fallback URL')}
              </span>
              <input
                className="modal-input"
                style={{ marginBottom: 0 }}
                placeholder="https://..."
                defaultValue={
                  isCustomFallback(stg.speed_test_url_fallback || '') ? stg.speed_test_url_fallback : ''
                }
                onBlur={(e) => apply({ speed_test_url_fallback: e.target.value.trim() })}
              />
            </div>
          )}
          <div className="modal-hint">
            {tt('Speed test runs for ~4 seconds regardless of file size, measuring throughput. Ping test accepts any HTTP response — pick whichever endpoint your provider routes best.')}{' '}
            {tt('Pick "None" to disable the retry.')}
          </div>
        </div>

        {/* ── Network ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Network')}</div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('TUN MTU')}</span>
            <input
              className="modal-input num-input"
              type="number"
              min={576}
              max={9000}
              defaultValue={stg.tun_mtu || 9000}
              onBlur={numBlur('tun_mtu', 9000)}
            />
          </div>
          {hint('Default 9000 (jumbo frames). If you see download stalls or sites hanging, try 1500 or 1408. Takes effect on next connection.')}
          <div className="modal-row">
            <span className="modal-row-label">{tt('TLS fragmentation (DPI bypass)')}</span>
            <Toggle on={!!stg.tls_fragment} onChange={(v) => apply({ tls_fragment: v })} />
          </div>
          {hint("Splits the TLS handshake (ClientHello) into pieces so a DPI can't match the connection in a single packet. Helps when the server is alive but the handshake is being reset. xray protocols only (VLESS/VMess/Trojan/SS over TLS). Takes effect on next connection.")}
          {!!stg.tls_fragment && (
            <div id="tlsfrag-deps">
              <div className="modal-row">
                <span className="modal-row-label">{tt('Fragment length')}</span>
                <input
                  className="modal-input num-input"
                  style={{ width: 110 }}
                  placeholder="100-200"
                  defaultValue={stg.tls_fragment_length || ''}
                  onBlur={(e) => apply({ tls_fragment_length: e.target.value.trim() })}
                />
              </div>
              <div className="modal-row">
                <span className="modal-row-label">{tt('Fragment interval (ms)')}</span>
                <input
                  className="modal-input num-input"
                  style={{ width: 110 }}
                  placeholder="10-20"
                  defaultValue={stg.tls_fragment_interval || ''}
                  onBlur={(e) => apply({ tls_fragment_interval: e.target.value.trim() })}
                />
              </div>
              {hint('Ranges as "min-max" (or a single number). Defaults: length 100-200, interval 10-20. Leave empty to use the defaults.')}
            </div>
          )}
          <div className="modal-row">
            <span className="modal-row-label">{tt('TLS fingerprint (uTLS)')}</span>
            <select
              className="modal-input"
              style={{ width: 130 }}
              value={stg.tls_fingerprint || 'chrome'}
              onChange={(e) => apply({ tls_fingerprint: e.target.value })}
            >
              {TLS_FPS.map((f) => (
                <option key={f} value={f}>
                  {f}
                </option>
              ))}
            </select>
          </div>
          {hint("Which browser's TLS handshake xray imitates (uTLS) when a config has no fp= of its own — without it the connection uses an easily-fingerprinted TLS signature. Applies to TLS/Reality nodes. Default: chrome. Takes effect on next connection.")}
        </div>

        {/* ── Remote access ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Remote access')}</div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Control from a phone on this network')}</span>
            <Toggle on={!!stg.remote_enabled} onChange={(v) => apply({ remote_enabled: v })} />
          </div>
          {hint(
            'Runs a small web server on this PC so you can open the same interface in a phone — or another device — browser on the same Wi-Fi. Access is protected by a secret key (in the link/QR below); anyone without it is refused. Turn off when not needed.',
          )}
          <div className="modal-row">
            <span className="modal-row-label">{tt('Server port')}</span>
            <input
              className="modal-input num-input"
              type="number"
              min={1}
              max={65535}
              defaultValue={stg.remote_port || 19876}
              onBlur={(e) => {
                const v = +e.target.value
                const valid = v >= 1 && v <= 65535 && v !== 19876
                e.target.value = String(valid ? v : 19876) // empty/invalid snaps to the default
                apply({ remote_port: valid ? v : 0 })
              }}
            />
          </div>
          {hint('Default 19876. Change it if that port is taken — e.g. while the 1.10 release is still running on this PC.')}
          {stg.remote_enabled && remote?.ips?.length > 1 && (
            <div className="modal-row">
              <span className="modal-row-label">{tt('IP address')}</span>
              <select
                className="modal-input"
                style={{ width: 'auto', marginBottom: 0 }}
                value={remoteIP}
                onChange={(e) => apply({ remote_ip: e.target.value === remote.ips[0] ? '' : e.target.value })}
              >
                {remote.ips.map((ip: string) => (
                  <option key={ip} value={ip}>
                    {ip}
                  </option>
                ))}
              </select>
            </div>
          )}
          {stg.remote_enabled && remoteURL && (
            <div className="modal-row" style={{ display: 'block' }}>
              <div
                style={{ display: 'flex', gap: 8, alignItems: 'center', margin: '4px 0 10px', flexWrap: 'wrap' }}
              >
                <input
                  className="modal-input"
                  readOnly
                  value={remoteURL}
                  style={{ flex: 1, minWidth: 200, marginBottom: 0 }}
                />
                <button
                  className="btn ghost"
                  onClick={() => {
                    navigator.clipboard?.writeText(remoteURL).then(
                      () => notify(tt('Copied')),
                      () => notify(remoteURL),
                    )
                  }}
                >
                  {tt('Copy')}
                </button>
                <button
                  className="btn ghost"
                  title={tt('Generate a new key — old links and QR codes stop working')}
                  onClick={() => {
                    SettingsService.RegenerateRemoteToken().then((r: any) => {
                      setRemote(r)
                      notify(tt('New key generated'))
                    })
                  }}
                >
                  ↻ {tt('New key')}
                </button>
              </div>
              {remoteQR && (
                <img
                  src={remoteQR}
                  alt="QR"
                  width={160}
                  height={160}
                  style={{ display: 'block', borderRadius: 6, background: '#fff', padding: 6 }}
                />
              )}
            </div>
          )}
        </div>

        {/* ── Security ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Security')}</div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('SOCKS authentication')}</span>
            <Toggle
              on={!!stg.socks_auth}
              onChange={(v) => {
                const patch: any = { socks_auth: v }
                if (v) {
                  if (!stg.socks_user) patch.socks_user = socksRandHex(16)
                  if (!stg.socks_pass) patch.socks_pass = socksRandHex(32)
                }
                apply(patch)
              }}
            />
          </div>
          {hint("Protects the local SOCKS5 proxy (proxy mode) with a username/password so other local apps can't use it or probe your VPN server. Off by default; turn it on to require credentials. Takes effect on next connection.")}
          {!!stg.socks_auth && (
            <div id="socks-creds">
              <div className="modal-row" style={{ display: 'block', marginBottom: 8 }}>
                <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                  {tt('SOCKS username')}
                </span>
                <input
                  className="modal-input"
                  style={{ marginBottom: 0 }}
                  value={stg.socks_user || ''}
                  onChange={(e) => apply({ socks_user: e.target.value })}
                  onBlur={(e) => {
                    if (!e.target.value.trim()) apply({ socks_user: socksRandHex(16) })
                  }}
                />
              </div>
              <div className="modal-row" style={{ display: 'block', marginBottom: 6 }}>
                <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                  {tt('SOCKS password')}
                </span>
                <div style={{ display: 'flex', gap: 6, alignItems: 'stretch' }}>
                  <input
                    className="modal-input"
                    style={{ marginBottom: 0, flex: 1 }}
                    value={stg.socks_pass || ''}
                    onChange={(e) => apply({ socks_pass: e.target.value })}
                    onBlur={(e) => {
                      if (!e.target.value.trim()) apply({ socks_pass: socksRandHex(32) })
                    }}
                  />
                  <button
                    className="btn ghost sm"
                    title={tt('Generate new credentials')}
                    onClick={() => apply({ socks_user: socksRandHex(16), socks_pass: socksRandHex(32) })}
                  >
                    {tt('Reset')}
                  </button>
                </div>
              </div>
              <div className="modal-hint" style={{ marginTop: 0 }}>
                {tt('Enter these in your SOCKS5 client. Reset generates new random credentials.')}
              </div>
            </div>
          )}
          <div className="modal-row">
            <span className="modal-row-label">{tt('TUN DNS leak protection')}</span>
            <Toggle on={!!stg.dns_leak_protection} onChange={(v) => apply({ dns_leak_protection: v })} />
          </div>
          {hint("Forces all DNS queries through the tunnel using sing-box's built-in FakeIP. Without this, system DNS can escape through your ISP. Takes effect on next connection. Applies only to TUN mode.")}
          {!!stg.dns_leak_protection && (
            <div id="security-deps">
              <div className="modal-row">
                <span className="modal-row-label">{tt('TUN Kill-switch')}</span>
                <Toggle on={!!stg.kill_switch} onChange={(v) => apply({ kill_switch: v })} />
              </div>
              {hint('Drops all traffic if the VPN goes down — no fallback to your physical network. Relies on the same strict-routing mechanism as DNS leak protection.')}
              <div className="modal-row">
                <span className="modal-row-label">{tt('TUN Block LAN traffic')}</span>
                <Toggle on={!!stg.block_lan} onChange={(v) => apply({ block_lan: v })} />
              </div>
              {hint('By default 192.168.x.x and similar private addresses bypass the VPN so printers, NAS, and router admin pages still work. Enable this to force LAN traffic through the tunnel too — usually breaks local services.')}
            </div>
          )}
        </div>

        {/* ── DNS (only with leak protection on) ── */}
        {!!stg.dns_leak_protection && (
          <div className="settings-section" id="dns-section">
            <div className="section-header">{tt('DNS')}</div>
            <div className="modal-row">
              <span className="modal-row-label">{tt('TUN FakeIP')}</span>
              <Toggle on={!stg.fakeip_disabled} onChange={(v) => apply({ fakeip_disabled: !v })} />
            </div>
            {hint('FakeIP returns pseudo-addresses instantly and resolves the real domain inside the tunnel — fastest, no leak. Turn off to use a real DoH server through the proxy (slower but more compatible with apps that do their own DNS).')}
            <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
              <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                {tt('TUN Bootstrap DNS')}{' '}
                <span style={{ color: 'var(--muted)', fontWeight: 400 }}>
                  {tt('(resolves VPN server; plain UDP)')}
                </span>
              </span>
              <input
                className="modal-input"
                style={{ marginBottom: 0 }}
                placeholder="9.9.9.9"
                defaultValue={stg.bootstrap_dns || ''}
                onBlur={(e) => apply({ bootstrap_dns: e.target.value.trim() })}
              />
            </div>
            <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
              <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                {tt('TUN Direct DNS')}{' '}
                <span style={{ color: 'var(--muted)', fontWeight: 400 }}>
                  {tt('(for RU bypass / direct domains)')}
                </span>
              </span>
              <input
                className="modal-input"
                style={{ marginBottom: 0 }}
                placeholder="77.88.8.8"
                defaultValue={stg.direct_dns || ''}
                onBlur={(e) => apply({ direct_dns: e.target.value.trim() })}
              />
            </div>
            <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
              <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                {tt('TUN Remote DNS')}{' '}
                <span style={{ color: 'var(--muted)', fontWeight: 400 }}>
                  {tt('(through proxy; DoH URL or IP)')}
                </span>
              </span>
              <input
                className="modal-input"
                style={{ marginBottom: 0 }}
                placeholder="https://1.1.1.1/dns-query"
                defaultValue={stg.remote_dns || ''}
                onBlur={(e) => apply({ remote_dns: e.target.value.trim() })}
              />
            </div>
            {hint('Leave blank for defaults: Quad9 / Yandex / Cloudflare-over-IP. Pick servers that work on your ISP for bootstrap and direct; remote goes through the tunnel so anything reachable from your VPN server works.')}
            <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
              <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                {tt('TUN Static hosts')}{' '}
                <span style={{ color: 'var(--muted)', fontWeight: 400 }}>
                  {tt('(domain → IP; one per line)')}
                </span>
              </span>
              <textarea
                className="modal-input"
                style={{ marginBottom: 0, minHeight: 60, fontSize: 11 }}
                placeholder="vpn.example.com 1.2.3.4"
                defaultValue={staticHostsToText(stg.static_hosts)}
                onBlur={(e) => apply({ static_hosts: parseStaticHosts(e.target.value) })}
              />
            </div>
            {hint('Hard-coded answers checked before any DNS server. Useful for pinning the VPN server IP or working around broken DNS. Format: domain ip separated by spaces, one per line.')}
          </div>
        )}

        {/* ── Appearance ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Appearance')}</div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Theme')}</span>
            <select
              className="modal-input num-input"
              style={{ width: 120, textAlign: 'left' }}
              value={stg.theme === 'light' ? 'light' : 'dark'}
              onChange={(e) => apply({ theme: e.target.value })}
            >
              <option value="dark">{tt('Dark')}</option>
              <option value="light">{tt('Light')}</option>
            </select>
          </div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Language')}</span>
            <select
              className="modal-input num-input"
              style={{ width: 120, textAlign: 'left' }}
              value={stg.language === 'ru' ? 'ru' : 'en'}
              onChange={(e) => apply({ language: e.target.value })}
            >
              <option value="en">English</option>
              <option value="ru">Русский</option>
            </select>
          </div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Settings font size (px)')}</span>
            <input
              className="modal-input num-input"
              type="number"
              min={9}
              max={20}
              defaultValue={stg.modal_font_size || 11}
              onBlur={numBlur('modal_font_size', 11)}
            />
          </div>
          {hint("Increase or decrease the text size in the Settings and Tab settings modals only. The main window's typography is unchanged.")}
        </div>

        {/* ── Statistics ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Statistics')}</div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Enable traffic statistics')}</span>
            <Toggle on={!stg.stats_disabled} onChange={(v) => apply({ stats_disabled: !v })} />
          </div>
          <div className="modal-row">
            <span className="modal-row-label">
              {tt('Lifetime total')}: ↑{fmtBytes(stg.stats_total_up || 0)} ↓
              {fmtBytes(stg.stats_total_down || 0)}
            </span>
            <button
              className="btn ghost sm"
              onClick={() => {
                SettingsService.ResetStats().then(refresh)
              }}
            >
              {tt('reset total')}
            </button>
          </div>
          {hint('Tracks bytes through the VPN tunnel in both modes. The lifetime total persists across sessions; the live session counter resets on every connect.')}
        </div>

        {/* ── System ── */}
        <div className="settings-section">
          <div className="section-header">{tt('System')}</div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Minimize to tray on close')}</span>
            <Toggle on={!!stg.tray_enabled} onChange={(v) => apply({ tray_enabled: v })} />
          </div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Launch at Windows startup')}</span>
            <Toggle on={!!stg.autostart_enabled} onChange={(v) => apply({ autostart_enabled: v })} />
          </div>
          {hint('Starts Vair automatically when you log in to Windows, minimized to the tray. Off by default.')}
          <div className="modal-row">
            <span className="modal-row-label">{tt('Handle vair:// links')}</span>
            <Toggle on={stg.deep_link_enabled !== false} onChange={(v) => apply({ deep_link_enabled: v })} />
          </div>
          {hint('Registers the vair:// scheme so clicking a vair://import/… link (e.g. in a browser or Telegram) opens Vair and adds the subscription or config. On by default.')}
          <div className="modal-row">
            <span className="modal-row-label">{tt('Verbose logs')}</span>
            <Toggle on={!!stg.verbose_logs} onChange={(v) => apply({ verbose_logs: v })} />
          </div>
          {hint('Raises xray/sing-box log detail (level info) so the Logs panel shows per-connection lines. Takes effect on next connection.')}
          <div className="modal-row">
            <span className="modal-row-label">{tt('Log speed/ping tests')}</span>
            <Toggle on={!!stg.log_tests} onChange={(v) => apply({ log_tests: v })} />
          </div>
          {hint('Logs each ping/speed result plus the full core output during the test (so you can see why a config is unavailable). Off by default — bulk tests can be noisy.')}
        </div>

        {/* ── Data ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Data')}</div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Storage location')}</span>
            <button className="btn ghost" onClick={() => SettingsService.OpenDataFolder()}>
              {tt('Open folder')}
            </button>
          </div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Settings backup')}</span>
            <span style={{ display: 'inline-flex', gap: 6 }}>
              <button
                className="btn ghost"
                onClick={() =>
                  SettingsService.Export().then((res) => {
                    if (res && res !== 'cancelled') notify(res)
                  })
                }
              >
                {tt('Export')}
              </button>
              <button
                className="btn ghost"
                onClick={() =>
                  SettingsService.Import(importTabs).then((res) => {
                    if (res === '') refresh()
                    else if (res !== 'cancelled') notify(res)
                  })
                }
              >
                {tt('Import')}
              </button>
            </span>
          </div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Import tabs and tab settings')}</span>
            <Toggle on={importTabs} onChange={setImportTabs} />
          </div>
          {hint('Exports tabs, tab settings and app settings to a JSON file. Import replaces the current state — useful when moving Vair to another computer.')}
          {hint('Turn the toggle off to import only the app settings and keep your existing tabs.')}
        </div>

        {/* ── Updates ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Updates')}</div>
          <div className="modal-row">
            <span className="modal-row-label">
              Vair <b>{version}</b>
            </span>
            <button className="btn ghost sm" disabled={updBusy} onClick={checkUpdate}>
              {tt('Check for updates')}
            </button>
          </div>
          <div className="modal-hint">
            {updMsg ??
              tt('Checks for a newer build and installs it (downloads through the tunnel when connected). The download is verified by checksum before it replaces the app.')}
          </div>
          {updPct !== null && (
            <div className="upd-bar">
              <div className="upd-fill" style={{ width: updPct + '%' }} />
            </div>
          )}
          {updLatest && (
            <div className="modal-row">
              <span className="modal-row-label">
                {tt('New version')}: {updLatest.latest}
              </span>
              <button
                className="btn"
                onClick={() => {
                  if (!window.confirm(tt('Download and install the update now? Vair will restart.'))) return
                  setUpdBusy(true)
                  UpdateService.Apply()
                }}
              >
                {tt('Update now')}
              </button>
            </div>
          )}
          <div className="upd-attrib">
            Vair · by{' '}
            <a onClick={() => SettingsService.OpenURL('https://github.com/lm705/vair')}>lm705</a>
          </div>
        </div>

        <div className="modal-btns">
          <button className="btn ghost" onClick={onClose}>
            {tt('close')}
          </button>
        </div>
      </div>

      {/* ── Running processes picker (1.10 proc-modal) ── */}
      {procOpen && (
        <div
          className="modal-overlay"
          onClick={(e) => {
            if (e.target === e.currentTarget) setProcOpen(false)
            e.stopPropagation()
          }}
        >
          <div className="modal-box">
            <div className="modal-title">{tt('Running processes')}</div>
            <input
              className="modal-input"
              placeholder={tt('filter…')}
              autoFocus
              value={procFilter}
              onChange={(e) => setProcFilter(e.target.value)}
            />
            <div className="modal-hint" style={{ marginTop: 0 }}>
              {tt('Click a process to add it to the Apps without VPN list.')}
            </div>
            <div
              style={{
                maxHeight: 300,
                overflowY: 'auto',
                border: '1px solid var(--border2)',
                borderRadius: 3,
                background: 'var(--bg2)',
                padding: 4,
              }}
            >
              {(() => {
                const f = procFilter.trim().toLowerCase()
                const existing = new Set(((stg.direct_apps || []) as string[]).map((s) => s.toLowerCase()))
                const shown = procList.filter((n) => !f || n.indexOf(f) >= 0).slice(0, 500)
                if (shown.length === 0)
                  return (
                    <div style={{ padding: 8, color: 'var(--dim)', fontSize: 11, textAlign: 'center' }}>
                      {procList.length === 0
                        ? tt('No process list available — only works in the desktop build.')
                        : tt('No matches')}
                    </div>
                  )
                return shown.map((n) => {
                  const already = existing.has(n)
                  return (
                    <div
                      key={n}
                      style={{
                        padding: '5px 8px',
                        cursor: 'pointer',
                        fontSize: 11,
                        borderRadius: 2,
                        opacity: already ? 0.5 : 1,
                      }}
                      onMouseOver={(e) => ((e.currentTarget as HTMLElement).style.background = 'rgba(232,197,71,.12)')}
                      onMouseOut={(e) => ((e.currentTarget as HTMLElement).style.background = '')}
                      onClick={() => {
                        if (already) return
                        apply({ direct_apps: [...(stg.direct_apps || []), n] })
                      }}
                    >
                      {n}
                      {already ? '  (' + tt('already added') + ')' : ''}
                    </div>
                  )
                })
              })()}
            </div>
            <div className="modal-btns">
              <button
                className="btn ghost"
                onClick={() => SettingsService.ListProcesses().then((l) => setProcList(l ?? []))}
              >
                {tt('refresh')}
              </button>
              <button className="btn ghost" onClick={() => setProcOpen(false)}>
                {tt('close')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
