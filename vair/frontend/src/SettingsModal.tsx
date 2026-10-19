// Settings modal — an exact port of the 1.10 openSettings() DOM (same classes,
// same sections/rows/hints, instant apply on change, no Save button).
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { Events } from '@wailsio/runtime'
import { SettingsService, UpdateService, QRService, LogService } from '../bindings/vair/internal/services'
import { t10 } from './i18n'
import Select from './Select'
import { useChipSelect } from './chipselect'

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

// One rule list's tallies: entry text → how it has been doing.
type RuleHit = { hits: number; last: number; pending: boolean; tracked: boolean }
type RuleStats = { enabled: boolean; observed: number; since: number; lists: Record<string, Record<string, RuleHit>> }

// agoText turns a unix second into "2 min ago" — coarse on purpose: the point
// of the badge is "recently / a while back / never", not a stopwatch.
function agoText(tt: (s: string) => string, unix: number): string {
  const secs = Math.max(0, Math.floor(Date.now() / 1000) - unix)
  if (secs < 60) return tt('just now')
  const mins = Math.floor(secs / 60)
  if (mins < 60) return mins + ' ' + tt('min ago')
  const hours = Math.floor(mins / 60)
  if (hours < 24) return hours + ' ' + tt('h ago')
  return Math.floor(hours / 24) + ' ' + tt('d ago')
}

// RuleBadge is the mark next to a rule: how many times it fired, or the reason
// there is no number. The case worth the whole feature is "never matched" — a
// rule with a typo in it looks exactly like a rule that works until something
// says this.
function RuleBadge({ hit, observed, tt }: { hit?: RuleHit; observed: number; tt: (s: string) => string }) {
  if (!hit) return null
  if (hit.pending)
    return (
      <span className="chip-hit pending" title={tt('Not loaded yet — applies on the next connection')}>
        •
      </span>
    )
  if (!hit.tracked)
    return (
      <span className="chip-hit muted" title={tt('Matched inside the engine\'s own database — not counted here')}>
        —
      </span>
    )
  if (hit.hits > 0)
    return (
      <span className="chip-hit" title={tt('Last match') + ': ' + agoText(tt, hit.last)}>
        {hit.hits}
      </span>
    )
  if (observed === 0)
    return (
      <span className="chip-hit muted" title={tt('Nothing has been routed yet')}>
        —
      </span>
    )
  return (
    <span className="chip-hit never" title={tt('Never matched — check the spelling')}>
      0
    </span>
  )
}

// Dependent wraps settings that only do something once another setting is on.
//
// Dimmed and inert rather than hidden: a setting that vanishes teaches nobody
// what it was, and someone who saw it once then cannot find it again. It stays
// where it was, greyed, and says which switch brings it to life.
function Dependent({
  on,
  needs,
  tt,
  children,
}: {
  on: boolean
  needs: string // the label of the setting that has to be on
  tt: (en: string) => string
  children: ReactNode
}) {
  if (on) return <>{children}</>
  return (
    <div style={{ opacity: 0.45, pointerEvents: 'none' }}>
      <div className="modal-hint" style={{ marginTop: 0 }}>
        {tt('Works only with this on:')} {tt(needs)}
      </div>
      {children}
    </div>
  )
}

function Chips({
  items,
  disabled,
  placeholder,
  onChange,
  keepCase,
  hits,
  observed,
  tt,
}: {
  items: string[]
  disabled?: boolean
  placeholder: string
  onChange: (next: string[]) => void
  // hits: the tallies for THIS list, when rule statistics are on (see RuleBadge).
  hits?: Record<string, RuleHit>
  observed?: number
  tt?: (s: string) => string
  // keepCase: don't lowercase entries and split only on whitespace (for URLs,
  // where the path is case-sensitive and ',' / ';' can appear legitimately).
  keepCase?: boolean
}) {
  const [val, setVal] = useState('')
  const { wrapRef, onMouseDown, isSel } = useChipSelect(items, (idxs) => {
    const drop = new Set(idxs)
    onChange(items.filter((_, i) => !drop.has(i)))
  })
  const add = (raw: string) => {
    const parts = raw
      .split(keepCase ? /\s+/ : /[\s,;]+/)
      .map((s) => (keepCase ? s.trim() : s.trim().toLowerCase()))
      .filter(Boolean)
    if (!parts.length) return
    const next = [...items]
    for (const p of parts) if (!next.includes(p)) next.push(p)
    onChange(next)
  }
  return (
    <div
      ref={wrapRef}
      className="chips-wrap"
      style={disabled ? { opacity: 0.45 } : undefined}
      // Selection of the chip values: click / Ctrl+click / Shift+click / drag /
      // Ctrl+A, then Ctrl+C to copy. See useChipSelect.
      onMouseDown={onMouseDown}
    >
      {items.map((d, i) => (
        <span key={d} className={'chip' + (isSel(i) ? ' sel' : '')}>
          {d}
          {hits && tt && <RuleBadge hit={hits[d]} observed={observed || 0} tt={tt} />}
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
          } else if (e.key === 'Backspace' && val === '' && items.length) {
            // Empty input + Backspace removes the last chip (same as its ✕).
            onChange(items.slice(0, -1))
          }
        }}
        onPaste={(e) => {
          e.preventDefault()
          add(e.clipboardData.getData('text'))
          setVal('')
        }}
      />
      <span className="chip-fill" aria-hidden="true" />
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
  caps,
}: {
  stg: any
  apply: (patch: Record<string, any>) => void
  refresh: () => void
  onClose: () => void
  notify: (msg: string) => void
  lang: string
  version: string
  caps?: any
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
  const [updLatest, setUpdLatest] = useState<{ latest: string; notes?: string; download_url?: string } | null>(null)
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
  // Rule statistics: poll while the screen is open and the counting is on. The
  // numbers only move while a connection is up, so a slow poll is plenty and a
  // closed settings screen costs nothing.
  // Whether this machine will actually show the watch's notification. Windows
  // has a master switch for notifications, and with it off the toast is
  // accepted and displayed to nobody — worth saying under the promise.
  const [notifyBlocked, setNotifyBlocked] = useState('')
  useEffect(() => {
    if (!stg.watch_connection) {
      setNotifyBlocked('')
      return
    }
    SettingsService.NotificationsBlocked().then((r: string) => setNotifyBlocked(r || ''))
  }, [stg.watch_connection])
  const [ruleStats, setRuleStats] = useState<RuleStats | null>(null)
  useEffect(() => {
    if (!stg.rule_stats) {
      setRuleStats(null)
      return
    }
    const pull = () => SettingsService.RuleStats().then((r: any) => setRuleStats(r))
    pull()
    const id = setInterval(pull, 3000)
    return () => clearInterval(id)
  }, [stg.rule_stats])
  const ruleHits = (list: string) => (stg.rule_stats ? ruleStats?.lists?.[list] : undefined)

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
        setUpdLatest({ latest: j.latest, notes: j.notes, download_url: j.download_url })
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

  // Legacy "bypass_ru" (and the pre-mode ru_sites_direct bool) display as the
  // generalized bypass-countries mode with Russia active; the backend maps them
  // the same way, so nothing changes until the user actually touches a toggle.
  const rmodeRaw = stg.routing_mode || (stg.ru_sites_direct ? 'bypass_ru' : 'proxy_all')
  const rmode = rmodeRaw === 'bypass_ru' ? 'bypass_countries' : rmodeRaw
  const bypassCC: string[] =
    rmodeRaw === 'bypass_ru' ? ['ru'] : ((stg.bypass_countries || []) as string[])
  const BYPASS_COUNTRIES: { cc: string; flag: string; label: string }[] = [
    { cc: 'ru', flag: '🇷🇺', label: 'Russian sites' },
    { cc: 'cn', flag: '🇨🇳', label: 'Chinese sites' },
    { cc: 'ir', flag: '🇮🇷', label: 'Iranian sites' },
    { cc: 'kz', flag: '🇰🇿', label: 'Kazakh sites' },
  ]
  const setBypassCC = (cc: string, on: boolean) => {
    const next = on ? [...bypassCC.filter((c) => c !== cc), cc] : bypassCC.filter((c) => c !== cc)
    // Writing the list also normalizes a legacy bypass_ru value to the new mode.
    apply({ routing_mode: 'bypass_countries', bypass_countries: next })
  }
  // Through-VPN URL list. A pre-2.1 single blocklist_url shows as one chip until
  // the user edits (any edit writes blocklist_urls and clears the legacy field).
  const blocklistUrls: string[] =
    stg.blocklist_urls && stg.blocklist_urls.length
      ? (stg.blocklist_urls as string[])
      : stg.blocklist_url
        ? [stg.blocklist_url]
        : []
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

  // Shown under the file-logging toggle so the user can find the file.
  const [logPath, setLogPath] = useState('')
  useEffect(() => {
    LogService.FilePath().then((p) => setLogPath(p || ''))
  }, [])
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
            <Select
              className="modal-input"
              style={{ marginBottom: 0 }}
              value={rmode}
              onChange={(e) => {
                const m = e.target.value
                if (m === 'bypass_countries' && bypassCC.length === 0) {
                  // First entry into the mode: seed Russia so it behaves like the
                  // old "everything except Russian sites" until customized.
                  apply({ routing_mode: m, bypass_countries: ['ru'] })
                } else {
                  apply({ routing_mode: m })
                }
              }}
            >
              <option value="proxy_all">{tt('All traffic through VPN')}</option>
              <option value="bypass_countries">{tt('Everything except selected countries')}</option>
              <option value="only_blocked">{tt('Only blocked-in-Russia resources')}</option>
              <option value="only_blocked_cn">{tt('Only blocked-in-China resources')}</option>
              <option value="only_selected">{tt('Only selected sites')}</option>
            </Select>
          </div>
          {hint('How traffic is split between the VPN and a direct connection. Takes effect on next connection.')}
          {rmode === 'bypass_countries' && (
            <div id="bypass-countries-opts">
              <div className="modal-hint" style={{ marginTop: 8 }}>
                {tt('Sites of these countries go direct (bypass the VPN):')}
              </div>
              {BYPASS_COUNTRIES.map((c) => (
                <div className="modal-row" key={c.cc}>
                  <span className="modal-row-label">
                    {c.flag} {tt(c.label)}
                  </span>
                  <Toggle on={bypassCC.includes(c.cc)} onChange={(v) => setBypassCC(c.cc, v)} />
                </div>
              ))}
              {hint('Local sites of these countries often refuse foreign/VPN addresses (banks, government services) — routing them direct keeps them working while everything else rides the VPN. Each country is matched by its TLD zone (*.ru, *.cn, *.ir, *.kz), its known-domains list and its IP ranges. With nothing selected, everything goes through the VPN. Takes effect on next connection.')}
            </div>
          )}
          {rmode === 'only_selected' &&
            hint('Whitelist mode: ONLY the domains you list below go through the VPN — everything else stays on the direct connection. With an empty list, nothing is tunnelled. If "Set system proxy" is off, the app filter also applies (only apps pointed at the proxy, AND only these domains).')}
          {rmode === 'only_blocked_cn' &&
            hint('Regular traffic goes DIRECT; only resources blocked in China go through the VPN — matched by the community GFW list (Loyalsoldier build, auto-updated), plus your own lists below.')}

          <div className="modal-row" style={{ marginTop: 12 }}>
            <span className="modal-row-label">{tt('Count rule matches')}</span>
            <Toggle on={!!stg.rule_stats} onChange={(v) => apply({ rule_stats: v })} />
          </div>
          {hint(
            'Shows next to each rule below how many times it has fired, and marks the ones that never have — usually a typo. The engine has to report every routing decision for this, so its log becomes considerably more verbose. Takes effect on next connection.'
          )}

          {/* ── Custom rules (all modes): domain / IP / CIDR / full:/regexp: ── */}
          <div className="modal-row" style={{ marginTop: 12, marginBottom: 4 }}>
            <span className="modal-row-label">{tt('Custom rules — without VPN')}</span>
            <Toggle on={!stg.direct_domains_disabled} onChange={(v) => apply({ direct_domains_disabled: !v })} />
          </div>
          <Chips
            items={stg.direct_domains || []}
            hits={ruleHits('direct')}
            observed={ruleStats?.observed || 0}
            tt={tt}
            disabled={!!stg.direct_domains_disabled}
            placeholder={tt('domain, IP or CIDR — press Enter')}
            onChange={(next) => apply({ direct_domains: next })}
          />
          {hint('Kept OFF the VPN (direct). Accepts a domain (all subdomains included), an IP or a CIDR (e.g. 10.0.0.0/8), or an advanced prefix — full:, regexp:, geosite:, geoip: (the geosite:/geoip: categories work in proxy mode only). Takes effect on next connection.')}

          {/* "Through VPN" rules are pointless when EVERYTHING already goes
              through the VPN — hidden in proxy_all (the lists persist). */}
          {rmode !== 'proxy_all' && (
            <>
              <div className="modal-row" style={{ marginTop: 10, marginBottom: 4 }}>
                <span className="modal-row-label">{tt('Custom rules — through VPN')}</span>
                <Toggle on={!stg.proxy_domains_disabled} onChange={(v) => apply({ proxy_domains_disabled: !v })} />
              </div>
              <Chips
                items={stg.proxy_domains || []}
            hits={ruleHits('proxy')}
            observed={ruleStats?.observed || 0}
            tt={tt}
                disabled={!!stg.proxy_domains_disabled}
                placeholder={tt('domain, IP or CIDR — press Enter')}
                onChange={(next) => apply({ proxy_domains: next })}
              />
              {hint('Forced THROUGH the VPN — even in a bypass mode. Same syntax as above. In whitelist mode (Only selected sites) this is the main list. Takes effect on next connection.')}

              <div className="modal-row" style={{ marginTop: 10, marginBottom: 4 }}>
                <span className="modal-row-label">{tt('Custom rules — through VPN (URL)')}</span>
                <Toggle
                  on={!stg.blocklist_urls_disabled}
                  onChange={(v) => apply({ blocklist_urls_disabled: !v })}
                />
              </div>
              <Chips
                keepCase
                items={blocklistUrls}
                disabled={!!stg.blocklist_urls_disabled}
                placeholder={tt('https://…/domains.txt — press Enter')}
                onChange={(next) => apply({ blocklist_urls: next, blocklist_url: '' })}
              />
              {hint('A URL-backed version of the list above: one or more links to plain-text domain lists (one domain per line), fetched, auto-updated and routed THROUGH the VPN. Handy for large or community-maintained lists instead of typing each domain. Press Enter to add a link. Takes effect on next connection.')}
            </>
          )}

          <div className="modal-row" style={{ marginTop: 10, marginBottom: 4 }}>
            <span className="modal-row-label">{tt('Custom rules — block')}</span>
            <Toggle on={!stg.block_domains_disabled} onChange={(v) => apply({ block_domains_disabled: !v })} />
          </div>
          <Chips
            items={stg.block_domains || []}
            hits={ruleHits('block')}
            observed={ruleStats?.observed || 0}
            tt={tt}
            disabled={!!stg.block_domains_disabled}
            placeholder={tt('domain, IP or CIDR — press Enter')}
            onChange={(next) => apply({ block_domains: next })}
          />
          {hint('Dropped entirely (ads, telemetry, unwanted hosts). Same syntax as above. Highest priority: block wins over the without-VPN and through-VPN lists. Takes effect on next connection.')}
          {/* Per-application routing is a TUN feature, and TUN is not on every
              platform yet. A list the engine will never consult looks like a
              setting that quietly stopped working. */}
          {caps?.split_tunnel !== false && (
            <>
              <div className="modal-row" style={{ marginTop: 10, marginBottom: 4 }}>
                <span className="modal-row-label">{tt('TUN Apps without VPN')}</span>
                <Toggle on={!stg.direct_apps_disabled} onChange={(v) => apply({ direct_apps_disabled: !v })} />
              </div>
              <Chips
                items={stg.direct_apps || []}
                hits={ruleHits('apps')}
                observed={ruleStats?.observed || 0}
                tt={tt}
                disabled={!!stg.direct_apps_disabled}
                placeholder={tt('e.g. chrome.exe or firefox, press Enter')}
                onChange={(next) => apply({ direct_apps: next })}
              />
              <div className="modal-hint">
                {tt("Process names that bypass VPN. Only works in TUN mode (system proxy can't be excluded per-app at the OS level).")}{' '}
                {caps?.process_picker !== false && (
                  <>
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
                    .{' '}
                  </>
                )}
                {tt('Takes effect on next connection.')}
              </div>
            </>
          )}
        </div>

        {/* ── Ports ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Local proxy')}</div>
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
            <Select
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
            </Select>
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
          <div className="modal-row">
            <span className="modal-row-label">{tt('Set system proxy')}</span>
            <Toggle on={!stg.no_system_proxy} onChange={(v) => apply({ no_system_proxy: !v })} />
          </div>
          {hint('When on (default), connecting in proxy mode points the system proxy setting at Vair, so every app that respects it goes through the VPN. Turn off for a "non-system" proxy: Vair only runs the local HTTP/SOCKS proxy on the ports above and changes nothing else — only apps where you explicitly set that proxy go through the VPN, the rest of the system stays direct. Applies immediately, even to a live connection.')}
          {/* What the switch actually REACHES, which is not the same everywhere.
              On Windows one registry setting covers nearly everything; on Linux
              the desktop's setting covers its own apps and browsers, and nothing
              that reads http_proxy from its environment — a difference users
              would otherwise discover as "the VPN doesn't work in my terminal". */}
          {caps?.system_proxy_backend === 'gsettings' &&
            hint('On this desktop the setting is written to GNOME (gsettings). GNOME apps and browsers set to "use system proxy settings" follow it. Terminal tools — curl, git, apt, ssh — read the http_proxy environment variable instead and are NOT covered; point those at the proxy yourself.')}
          {caps?.system_proxy_backend === 'kde' &&
            hint('On this desktop the setting is written to KDE (kioslaverc). KDE apps and browsers set to "use system proxy settings" follow it. Terminal tools — curl, git, apt, ssh — read the http_proxy environment variable instead and are NOT covered; point those at the proxy yourself.')}
          {/* Only meaningful where a backend exists to be hijacked. */}
          {caps?.system_proxy_backend !== 'none' && (
            <Dependent on={!stg.no_system_proxy} needs="Set system proxy" tt={tt}>
              <div className="modal-row">
                <span className="modal-row-label">{tt('Keep the system proxy set')}</span>
                <Toggle
                  on={!stg.proxy_guard_disabled}
                  onChange={(v) => apply({ proxy_guard_disabled: !v })}
                />
              </div>
              {hint(
                'While connected, checks every 20 seconds that the system proxy setting still points at Vair and puts it back if something else changed it — another VPN client, a policy refresh, a browser extension. Without this, traffic leaves outside the tunnel while the app still says "connected", and nothing tells you. If it is overwritten again and again, Vair stops correcting it and says so in the log rather than fighting the other program. Turn off if something else is meant to own that setting.',
              )}
            </Dependent>
          )}
          {caps?.kill_switch_persistent !== false && (
            <>
              <div className="modal-row">
                <span className="modal-row-label">{tt('Proxy kill-switch')}</span>
                <Toggle
                  on={!!stg.proxy_kill_switch}
                  onChange={(v) => apply({ proxy_kill_switch: v })}
                />
              </div>
              {hint(
                'Proxy mode only carries the applications that honour the system proxy — everything else has always gone straight out, connected or not. This blocks that at the firewall while a proxy session is up: what stays open is the engine and loopback, which is the proxy. Needs the same administrator rights TUN mode does, and releases on disconnect.'
              )}
            </>
          )}
          {caps?.system_proxy_backend === 'none' && (
            <div className="modal-hint">
              {tt('This desktop has no system proxy setting Vair can drive, so this switch changes nothing here. Point applications at the local proxy yourself:')}{' '}
              <code>export http_proxy=http://127.0.0.1:{stg.http_port || 10819}</code>
            </div>
          )}
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
            <span className="modal-row-label">{tt('HTTP authentication')}</span>
            <Toggle
              on={!!stg.http_auth}
              onChange={(v) => {
                const patch: any = { http_auth: v }
                if (v) {
                  if (!stg.http_user) patch.http_user = socksRandHex(16)
                  if (!stg.http_pass) patch.http_pass = socksRandHex(32)
                }
                apply(patch)
              }}
            />
          </div>
          {hint("Protects the local HTTP proxy with a username/password (Basic auth). Applies to BOTH proxy mode and the TUN “Share proxy over LAN” HTTP proxy. Off by default. Takes effect on next connection.")}
          {!!stg.http_auth && (
            <div id="http-creds">
              <div className="modal-row" style={{ display: 'block', marginBottom: 8 }}>
                <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                  {tt('HTTP username')}
                </span>
                <input
                  className="modal-input"
                  style={{ marginBottom: 0 }}
                  value={stg.http_user || ''}
                  onChange={(e) => apply({ http_user: e.target.value })}
                  onBlur={(e) => {
                    if (!e.target.value.trim()) apply({ http_user: socksRandHex(16) })
                  }}
                />
              </div>
              <div className="modal-row" style={{ display: 'block', marginBottom: 6 }}>
                <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                  {tt('HTTP password')}
                </span>
                <div style={{ display: 'flex', gap: 6, alignItems: 'stretch' }}>
                  <input
                    className="modal-input"
                    style={{ marginBottom: 0, flex: 1 }}
                    value={stg.http_pass || ''}
                    onChange={(e) => apply({ http_pass: e.target.value })}
                    onBlur={(e) => {
                      if (!e.target.value.trim()) apply({ http_pass: socksRandHex(32) })
                    }}
                  />
                  <button
                    className="btn ghost sm"
                    title={tt('Generate new credentials')}
                    onClick={() => apply({ http_user: socksRandHex(16), http_pass: socksRandHex(32) })}
                  >
                    {tt('Reset')}
                  </button>
                </div>
              </div>
              <div className="modal-hint" style={{ marginTop: 0 }}>
                {tt('Enter these in your HTTP client. Reset generates new random credentials.')}
              </div>
            </div>
          )}
        </div>
        {/* ── Tunnel ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Tunnel')}</div>
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
            <span className="modal-row-label">{tt('TUN DNS leak protection')}</span>
            <Toggle on={!!stg.dns_leak_protection} onChange={(v) => apply({ dns_leak_protection: v })} />
          </div>
          {hint("Forces all DNS queries through the tunnel using sing-box's built-in FakeIP. Without this, system DNS can escape through your ISP. Takes effect on next connection. Applies only to TUN mode.")}
          <Dependent on={!!stg.dns_leak_protection} needs="TUN DNS leak protection" tt={tt}>
            <div id="security-deps">
              {/* A safety switch that cannot be enforced must not be offered:
                  someone who turns it on and believes it would be worse off
                  than someone who knows it is missing. */}
              {caps?.kill_switch !== false && (
                <>
                  <div className="modal-row">
                    <span className="modal-row-label">{tt('TUN Kill-switch')}</span>
                    <Toggle on={!!stg.kill_switch} onChange={(v) => apply({ kill_switch: v })} />
                  </div>
                  {hint('Drops all traffic if the VPN drops — no fallback to your physical network (strict routing; on Windows a WFP filter). Also hardens DNS-leak protection. On by default; turn it off to let traffic fall back to a direct connection when the tunnel dies. Applies only to TUN mode.')}
                  {/* Only meaningful once the kill-switch above is on: this is
                      that block, made to outlive the engine that enforces it. */}
                  {caps?.kill_switch_persistent !== false && (
                    <Dependent on={!!stg.kill_switch} needs="TUN Kill-switch" tt={tt}>
                      <div className="modal-row">
                        <span className="modal-row-label">{tt('TUN Keep blocking after the tunnel is gone')}</span>
                        <Toggle
                          on={!!stg.kill_switch_persistent}
                          onChange={(v) => apply({ kill_switch_persistent: v })}
                        />
                      </div>
                      {hint(
                        'The kill-switch above is enforced by the engine, so it ends when the engine does — a crash, or Vair being closed, returns the machine to its ordinary connection without saying so. This holds the block in the firewall instead: while it is engaged, nothing leaves except through the tunnel. It also covers the switch between two configurations, where the old adapter goes away and the ordinary routes come back for a second.'
                      )}
                      {!!stg.kill_switch_persistent && (
                        <div className="modal-hint" style={{ color: 'var(--red)' }}>
                          {tt('Read this: if Vair is killed while connected, this machine has no internet until you start Vair again (it releases the block at startup) or run in PowerShell as administrator:')}{' '}
                          <code>Set-NetFirewallProfile -All -DefaultOutboundAction Allow</code>
                        </div>
                      )}
                    </Dependent>
                  )}
                </>
              )}
              <div className="modal-row">
                <span className="modal-row-label">{tt('TUN Block LAN traffic')}</span>
                <Toggle on={!!stg.block_lan} onChange={(v) => apply({ block_lan: v })} />
              </div>
              {hint('By default 192.168.x.x and similar private addresses bypass the VPN so printers, NAS, and router admin pages still work. Enable this to force LAN traffic through the tunnel too — usually breaks local services.')}
            </div>
          </Dependent>
          {/* Meaningless without TUN mode, which not every platform has yet. */}
          {caps?.tun !== false && (
            <>
              <div className="modal-row">
                <span className="modal-row-label">{tt('TUN Share proxy over LAN')}</span>
                <Toggle on={!!stg.tun_share_proxy} onChange={(v) => apply({ tun_share_proxy: v })} />
              </div>
              {hint('In TUN mode Vair normally routes only THIS PC through the tunnel. Turn this on to ALSO open the HTTP/SOCKS proxy (above) on the Proxy-access address, so another device (a phone) can route through this PC into the VPN while the PC itself stays fully tunnelled. Set Proxy access to "Allow LAN access" (or a custom address) for it to be reachable from other devices, and point the device at the HTTP port. Off by default; applies on the next connection.')}
            </>
          )}
          <div id="dns-section">
            <div className="section-header">{tt('DNS')}</div>
            <Dependent on={!!stg.dns_leak_protection} needs="TUN DNS leak protection" tt={tt}>
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
            </Dependent>
          </div>
        </div>

        {/* ── Connection ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Connection')}</div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Watch the connected server')}</span>
            <Toggle on={!!stg.watch_connection} onChange={(v) => apply({ watch_connection: v })} />
          </div>
          {hint(
            'While connected, checks every few seconds that the server is still answering, and when it stops, closes the connection and sends a desktop notification instead of leaving "connected" on screen. Off by default: it ends a connection on the strength of a probe. Does not overlap with AUTO — while AUTO is on it does the watching itself and switches to another server rather than closing; this takes over only when AUTO is off.'
          )}
          {notifyBlocked && <div className="modal-hint" style={{ color: 'var(--red)' }}>{notifyBlocked}</div>}
        </div>

        {/* ── DPI bypass ── */}
        <div className="settings-section">
          <div className="section-header">{tt('DPI bypass')}</div>
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
            <Select
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
            </Select>
          </div>
          {hint("Which browser's TLS handshake xray imitates (uTLS) when a config has no fp= of its own — without it the connection uses an easily-fingerprinted TLS signature. Applies to TLS/Reality nodes. Default: chrome. Takes effect on next connection.")}
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
            <Select
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
            </Select>
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
            <Select
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
            </Select>
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
            <Select
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
            </Select>
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

        {/* ── Remote access ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Remote access')}</div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Web UI')}</span>
            <Toggle on={!!stg.remote_enabled} onChange={(v) => apply({ remote_enabled: v })} />
          </div>
          {hint(
            'Runs a small web server on this PC so you can open the same interface in a browser on another device — a phone, a tablet, the computer next door. Access is protected by a secret key (in the link/QR below); anyone without it is refused. The connection is plain HTTP, NOT encrypted: everything, including the key, travels the local network in the clear, and whoever can read that traffic can control this program. Use it on a network you trust, and turn it off when you do not need it.',
          )}
          <div className="modal-row">
            <span className="modal-row-label">{tt('Web UI port')}</span>
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
          {hint('Default 19876. Change it if something else on this machine already listens on that port.')}
          {/* On Windows the app adds its own inbound firewall rule when this is
              switched on. Here it cannot: opening a port needs root, which the
              GUI deliberately does not have. Saying nothing would leave the user
              with a link that works on this machine and times out on the phone,
              with no clue why — so show the command instead of pretending. */}
          {stg.remote_enabled && caps?.firewall_rules === false && (
            <div className="cmd-note">
              <div className="cmd-note-text">
                {tt('Vair cannot open a port in the firewall on this system. If the phone cannot reach the address below, allow it once:')}
              </div>
              <code>sudo ufw allow {stg.remote_port || 19876}/tcp</code>
              <button
                className="btn ghost sm"
                onClick={() => {
                  const cmd = `sudo ufw allow ${stg.remote_port || 19876}/tcp`
                  navigator.clipboard?.writeText(cmd).then(
                    () => notify(tt('Copied')),
                    () => notify(tt('Could not copy')),
                  )
                }}
              >
                {tt('copy')}
              </button>
            </div>
          )}
          {stg.remote_enabled && remote?.ips?.length > 1 && (
            <div className="modal-row">
              <span className="modal-row-label">{tt('IP address')}</span>
              <Select
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
              </Select>
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

        {/* ── Appearance ── */}
        <div className="settings-section">
          <div className="section-header">{tt('Appearance')}</div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Theme')}</span>
            <Select
              className="modal-input num-input"
              style={{ width: 120, textAlign: 'left' }}
              value={stg.theme === 'light' ? 'light' : 'dark'}
              onChange={(e) => apply({ theme: e.target.value })}
            >
              <option value="dark">{tt('Dark')}</option>
              <option value="light">{tt('Light')}</option>
            </Select>
          </div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Language')}</span>
            <Select
              className="modal-input num-input"
              style={{ width: 120, textAlign: 'left' }}
              value={stg.language === 'ru' ? 'ru' : 'en'}
              onChange={(e) => apply({ language: e.target.value })}
            >
              <option value="en">English</option>
              <option value="ru">Русский</option>
            </Select>
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
          <div className="modal-row">
            <span className="modal-row-label">{tt('Show setting descriptions')}</span>
            <Toggle on={!stg.hide_hints} onChange={(v) => apply({ hide_hints: !v })} />
          </div>
          {/* Deliberately still shown with descriptions off — it is the way back. */}
          <div className="modal-hint" style={{ display: 'block' }}>
            {tt('The explanatory text under each setting, here and in the tab and auto-connect dialogs. Turn it off once the settings are familiar and the dialogs become much shorter.')}
          </div>
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
          {/* Hidden where there is no tray to minimize INTO: turning it on
              there would hide the window with nothing left to reopen it from,
              while the app keeps running and holding the system proxy. */}
          {caps?.tray !== false && (
            <div className="modal-row">
              <span className="modal-row-label">{tt('Minimize to tray on close')}</span>
              <Toggle on={!!stg.tray_enabled} onChange={(v) => apply({ tray_enabled: v })} />
            </div>
          )}
          {/* Both rows are gated on the capability rather than shown-and-dead:
              a switch that flips and changes nothing is worse than no switch,
              because the user then trusts a setting that was never applied. */}
          {caps?.autostart !== false && (
            <>
              <div className="modal-row">
                <span className="modal-row-label">{tt('Launch at system startup')}</span>
                <Toggle on={!!stg.autostart_enabled} onChange={(v) => apply({ autostart_enabled: v })} />
              </div>
              {hint('Starts Vair automatically when you log in, minimized to the tray. Off by default.')}
            </>
          )}
          {caps?.deep_link_toggle !== false && (
            <>
              <div className="modal-row">
                <span className="modal-row-label">{tt('Handle vair:// links')}</span>
                <Toggle on={stg.deep_link_enabled !== false} onChange={(v) => apply({ deep_link_enabled: v })} />
              </div>
              {hint('Registers the vair:// scheme so clicking a vair://import/… link (e.g. in a browser or Telegram) opens Vair and adds the subscription or config. On by default.')}
            </>
          )}
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
          <div className="modal-row">
            <span className="modal-row-label">{tt('Save logs to a file')}</span>
            <Toggle on={!!stg.log_to_file} onChange={(v) => apply({ log_to_file: v })} />
          </div>
          {hint(
            'Mirrors the Logs panel to a file that survives a restart or a crash — the in-app log is memory-only and starts empty every launch. Rotated at 5 MiB, one old copy kept (≈10 MiB max). Off by default: with verbose logs on, the file records which sites you visited, so share it only if you are fine with that.',
          )}
          {logPath && <div className="modal-hint log-path">{logPath}</div>}
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
              {/* "Update now" only where the app can actually install one. A
                  packaged build belongs to dpkg/rpm and an AppImage is a file
                  the user placed themselves; offering a button that downloads
                  and then fails is worse than sending them to the release. */}
              {caps?.self_update === 'inplace' ? (
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
              ) : (
                <button
                  className="btn ghost"
                  onClick={() =>
                    // Straight to the artifact for THIS install when the
                    // manifest names one; the releases page is the fallback for
                    // a manifest that predates per-platform assets.
                    SettingsService.OpenURL(
                      updLatest.download_url || 'https://github.com/lm705/vair/releases',
                    )
                  }
                >
                  {updLatest.download_url ? tt('Download') : tt('Open releases')}
                </button>
              )}
            </div>
          )}
          {updLatest && caps?.self_update === 'package' && (
            <div className="modal-hint">
              {tt('This copy was installed from a package — update it through your package manager.')}
            </div>
          )}
          {updLatest && caps?.self_update === 'appimage' && (
            <div className="modal-hint">
              {tt('This copy runs as an AppImage — download the new one and replace this file.')}
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
