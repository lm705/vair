// Settings modal — an exact port of the 1.10 openSettings() DOM (same classes,
// same sections/rows/hints, instant apply on change, no Save button).
import { createContext, useContext, useEffect, useLayoutEffect, useRef, useState } from 'react'
import Toggle from './Toggle'
import type { ReactNode } from 'react'
import { Events } from '@wailsio/runtime'
import { SettingsService, UpdateService, QRService, LogService, ConnService } from '../bindings/vair/internal/services'
import { t10 } from './i18n'
import { updErrText } from './reasons'
import Select from './Select'
import { useChipSelect } from './chipselect'
import FavoritesModal from './FavoritesModal'
import { IS_REMOTE, openLink } from './remote'
import { Hint, opt, whenOn, whenOff } from './hints'
import KeepBlockNotice, { keepBlockNoticeWanted } from './keepblock'
import type { HintOpt } from './hints'
import CloseX from './closex'
import Group from './setgroup'

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
// The "check IP" services. Any of them will do — the answer is read by field
// name, not by shape — so a custom URL is as good as a preset as long as it
// replies with JSON. Plain HTTP where the service offers it: this request has to
// get through whatever the routing is doing.
const CHECKIP_PRESETS = [
  { url: '', label: 'ipwho.is (default)' },
  { url: 'http://ip-api.com/json/?fields=status,message,country,countryCode,city,query,isp', label: 'ip-api.com (plain HTTP)' },
  { url: 'https://ipinfo.io/json', label: 'ipinfo.io' },
  { url: 'https://ifconfig.co/json', label: 'ifconfig.co' },
  { url: 'https://api.myip.com/', label: 'api.myip.com' },
]
// Resolvers for the tunnel side of DNS. Blank is what the program has always
// used (Cloudflare through the tunnel); the rest are here because a resolver
// that refuses to answer for advertising and malware domains blocks them for
// every program on the machine at once, and typing its address is the only
// thing standing in the way. Services run by other people, all of them: what
// each one filters is its own decision and can change, which is why the label
// says what it claims to do rather than promising it.
//
// Every label carries the exact value the field will hold, so the list and the
// box agree — the same rule the ping and speed-test lists follow. Cloudflare's
// default is an IP and not a name on purpose: it is the resolver that answers
// before any name can be resolved.
const REMOTE_DNS_PRESETS = [
  { url: '', label: 'Cloudflare · https://1.1.1.1/dns-query — no filtering (default)' },
  { url: 'https://dns.google/dns-query', label: 'Google · https://dns.google/dns-query — no filtering' },
  { url: 'https://dns.quad9.net/dns-query', label: 'Quad9 · https://dns.quad9.net/dns-query — malware' },
  { url: 'https://security.cloudflare-dns.com/dns-query', label: 'Cloudflare · https://security.cloudflare-dns.com/dns-query — malware' },
  { url: 'https://dns.adguard-dns.com/dns-query', label: 'AdGuard · https://dns.adguard-dns.com/dns-query — ads and trackers' },
  { url: 'https://family.adguard-dns.com/dns-query', label: 'AdGuard Family · https://family.adguard-dns.com/dns-query — ads, trackers, adult sites' },
  { url: 'https://family.cloudflare-dns.com/dns-query', label: 'Cloudflare Family · https://family.cloudflare-dns.com/dns-query — malware and adult sites' },
]
// The bootstrap slot resolves exactly one name — the VPN server's — before
// there is a tunnel, over DNS over HTTPS at the address (core lookupBootstrap).
// So: addresses, and no word about what a resolver blocks, because there is
// nothing here for it to block. What matters
// is whether the answer can be trusted, which is a matter of who runs it.
const BOOTSTRAP_DNS_PRESETS = [
  { url: '', label: 'Quad9 · 9.9.9.9 (default)' },
  { url: '1.1.1.1', label: 'Cloudflare · 1.1.1.1' },
  { url: '8.8.8.8', label: 'Google · 8.8.8.8' },
  { url: '94.140.14.14', label: 'AdGuard · 94.140.14.14' },
]
// The direct slot resolves what stays outside the tunnel — in a bypass mode
// that is most of what you visit — so what a resolver filters does matter here.
// Plain UDP again: a name has to be answerable without the tunnel.
const DIRECT_DNS_PRESETS = [
  { url: '', label: 'Yandex · 77.88.8.8 (default)' },
  // The machine's own resolver — the provider's or the router's. The only one
  // that knows names on the local network (core directDNSSystem).
  { url: 'system', label: 'System resolver — the one this computer uses' },
  { url: '8.8.8.8', label: 'Google · 8.8.8.8' },
  { url: '1.1.1.1', label: 'Cloudflare · 1.1.1.1' },
  { url: '9.9.9.9', label: 'Quad9 · 9.9.9.9 — malware' },
  { url: '94.140.14.14', label: 'AdGuard · 94.140.14.14 — ads and trackers' },
]
const SPEED_DEFAULT_URL = 'https://speed.cloudflare.com/__down?bytes=50000000'
const CACHEFLY = 'http://cachefly.cachefly.net/100mb.test'
const TLS_FPS = ['chrome', 'firefox', 'safari', 'ios', 'android', 'edge', '360', 'qq', 'random', 'randomized']

// normFragment checks a fragment field the way the program does (core
// normalizeFragmentSpec): a number, or a range "a-b", within the bounds, written
// low end first. '' is the default; null is a value that is not saved.
function normFragment(v: string, lo: number, hi: number): string | null {
  const t = v.trim()
  if (!t) return ''
  const m = /^(\d{1,6})(?:\s*-\s*(\d{1,6}))?$/.exec(t)
  if (!m) return null
  let a = +m[1]
  let b = m[2] === undefined ? a : +m[2]
  if (a > b) [a, b] = [b, a]
  if (a < lo || b > hi) return null
  return a === b ? String(a) : a + '-' + b
}

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
// validTestURL checks a test address the way the program does (core
// validTestURL): http or https, with a host, no spaces. Anything else failed
// every configuration it was used for, or stopped the program mid-test.
function validTestURL(v: string): boolean {
  if (!v || /\s/.test(v)) return false
  try {
    const u = new URL(v)
    return (u.protocol === 'http:' || u.protocol === 'https:') && !!u.hostname
  } catch {
    return false
  }
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
// isIPAddress accepts an IPv4 or IPv6 address and nothing else.
function isIPAddress(s: string): boolean {
  if (/^(25[0-5]|2[0-4]\d|1?\d?\d)(\.(25[0-5]|2[0-4]\d|1?\d?\d)){3}$/.test(s)) return true
  if (!s.includes(':') || !/^[0-9a-fA-F:.]+$/.test(s)) return false
  try {
    new URL('http://[' + s + ']/')
    return true
  } catch {
    return false
  }
}
// parseStaticHosts reads the text box. A line whose address is not one is left
// out and named in bad: it used to be saved, and one such line stopped every
// TUN connection with an error from the engine.
function parseStaticHosts(raw: string): { hosts: Record<string, string>; bad: string[] } {
  const out: Record<string, string> = {}
  const bad: string[] = []
  ;(raw || '').split(/\r?\n/).forEach((line) => {
    const t = line.trim()
    if (!t || t[0] === '#') return
    const m = t.split(/[\s=,]+/)
    if (m.length >= 2 && m[0] && m[1]) {
      if (isIPAddress(m[1])) out[m[0].toLowerCase()] = m[1]
      else bad.push(t)
    }
  })
  return { hosts: out, bad }
}

// ── small building blocks (1.10 markup) ─────────────────────────────

// One rule list's tallies: entry text → how it has been doing.
type RuleHit = { hits: number; last: number; pending: boolean; tracked: boolean; unused?: boolean }
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
  if (hit.unused)
    return (
      <span className="chip-hit muted" title={tt('Not used in proxy mode: programs can be excluded only in TUN mode')}>
        —
      </span>
    )
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

// Dependent wraps settings that only do something while another setting is in
// the right state.
//
// Dimmed and inert rather than hidden: a setting that vanishes teaches nobody
// what it was, and someone who saw it once then cannot find it again. It stays
// where it was, greyed, and says which setting it is waiting on.
//
// The note is not a description. It used to be one — a grey hint, dimmed with
// the group it explained, hidden altogether with "Show setting descriptions"
// off — and what was left was a block of grey controls with no reason given.
// It is always shown now, at full contrast, names the setting, and "Show"
// takes you to it when it is in this window.
//
// reqs are checked in order and the first one not met is the one explained:
// fixing it may reveal the next.
type Req = {
  ok: boolean
  text: string // already translated: which setting, and what it has to be
  target?: string // data-setting key of that setting's row, when it is here
}

// DepOff is true inside a Dependent that is already off. A group nested in one
// that is already dimmed and explained must not dim itself a second time or
// add a second note about a setting that cannot matter yet.
const DepOff = createContext(false)

// flashSetting scrolls to a setting's row and marks it for a moment.
function flashSetting(key: string) {
  const el = document.querySelector<HTMLElement>(`[data-setting="${key}"]`)
  if (!el) return
  el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  el.classList.remove('setting-flash')
  void el.offsetWidth // restart the animation if it is already running
  el.classList.add('setting-flash')
  window.setTimeout(() => el.classList.remove('setting-flash'), 1700)
}

// RevealSetting is how "Show" gets there. The dialog shows one tab at a time,
// so it provides a version that switches to the setting's tab first.
const RevealSetting = createContext<(key: string) => void>(flashSetting)

function Dependent({ reqs, tt, children }: { reqs: Req[]; tt: (en: string) => string; children: ReactNode }) {
  const outerOff = useContext(DepOff)
  const reveal = useContext(RevealSetting)
  const unmet = reqs.find((r) => !r.ok)
  if (!unmet || outerOff) return <>{children}</>
  return (
    <div className="dep">
      <div className="dep-note" role="note">
        <span>{unmet.text}</span>
        {unmet.target && (
          <button className="linkish" onClick={() => reveal(unmet.target!)}>
            {tt('Show')}
          </button>
        )}
      </div>
      <DepOff.Provider value={true}>
        <div className="dep-body" aria-disabled="true" inert>
          {children}
        </div>
      </DepOff.Provider>
    </div>
  )
}

// backupPercent is how far the bar under the backup buttons goes. An import
// is two passes over the same configurations — read back, then saved — so
// each fills half of it.
function backupPercent(p: any): number {
  const f = p.total > 0 ? p.done / p.total : 0
  if (p.op !== 'import') return Math.round(f * 100)
  if (p.phase === 'parse') return Math.round(f * 50)
  if (p.phase === 'save') return Math.round(50 + f * 50)
  return 0
}

function Chips({
  items,
  disabled,
  placeholder,
  onChange,
  keepCase,
  splitOn,
  hits,
  observed,
  tt,
  rules,
}: {
  items: string[]
  disabled?: boolean
  placeholder: string
  onChange: (next: string[]) => void
  // hits: the tallies for THIS list, when rule statistics are on (see RuleBadge).
  hits?: Record<string, RuleHit>
  observed?: number
  tt?: (s: string) => string
  // keepCase: don't lowercase entries (for URLs and for program paths, where the
  // spelling is the user's and not ours to fold).
  keepCase?: boolean
  // splitOn: what separates several entries pasted at once. The default splits on
  // spaces too; a list whose entries can CONTAIN a space — any Windows path under
  // Program Files — passes something narrower.
  splitOn?: RegExp
  // rules: a routing rule list. What is typed is read by the program the way the
  // configuration will read it (SettingsService.CheckRules): kept in that form —
  // "https://example.com/page" as example.com, сайт.рф in the form the engines
  // see — or left in the field with what is wrong with it. A broken regexp: line
  // used to be accepted and stop the TUN connection from starting.
  rules?: boolean
}) {
  const [val, setVal] = useState('')
  const [err, setErr] = useState('')
  // Lines already in the list that the program cannot read — written before
  // this check existed, or imported — marked rather than silently left out.
  const [bad, setBad] = useState<Record<string, string>>({})
  const ruleError = (c: { entry: string; error?: string; detail?: string }) =>
    (c.error === 'regexp' ? tt?.('Not a valid regular expression') || 'Not a valid regular expression' : tt?.('Not a domain name, IP address or CIDR') || 'Not a domain name, IP address or CIDR') +
    ': ' +
    c.entry +
    (c.detail ? ' (' + c.detail + ')' : '')
  useEffect(() => {
    if (!rules || !items.length) {
      setBad({})
      return
    }
    SettingsService.CheckRules(items).then((cs: any[] | null) => {
      const m: Record<string, string> = {}
      for (const c of cs || []) if (c.error) m[c.entry] = ruleError(c)
      setBad(m)
    })
  }, [rules, items.join('\n')])
  const { wrapRef, onMouseDown, isSel } = useChipSelect(items, (idxs) => {
    const drop = new Set(idxs)
    onChange(items.filter((_, i) => !drop.has(i)))
  })
  const push = (parts: string[]) => {
    const next = [...items]
    // Case-insensitively: with keepCase the list holds the user's spelling, and
    // adding Discord.exe next to discord.exe would be two chips for one program.
    for (const p of parts) if (!next.some((x) => x.toLowerCase() === p.toLowerCase())) next.push(p)
    if (next.length !== items.length) onChange(next)
  }
  const add = (raw: string) => {
    if (rules) {
      // An expression is one entry however many commas it holds — a{1,3} is
      // not two rules. A line that starts with regexp: is kept whole, spaces
      // and all; elsewhere entries split on spaces, and on commas and
      // semicolons only outside a regexp: entry.
      const parts = raw
        .split(/[\r\n]+/)
        .flatMap((line) =>
          /^\s*regexp:/i.test(line)
            ? [line.trim()]
            : line.split(/\s+/).flatMap((tok) => (/^regexp:/i.test(tok) ? [tok] : tok.split(/[,;]+/))),
        )
        .map((s) => s.trim())
        .filter(Boolean)
      if (!parts.length) return
      SettingsService.CheckRules(parts).then((cs: any[] | null) => {
        const ok = (cs || []).filter((c) => !c.error).map((c) => c.value)
        const wrong = (cs || []).filter((c) => c.error)
        push(ok)
        setErr(wrong.length ? ruleError(wrong[0]) : '')
        setVal(wrong.map((c) => c.entry).join(' '))
      })
      return
    }
    const parts = raw
      .split(splitOn || (keepCase ? /\s+/ : /[\s,;]+/))
      .map((s) => (keepCase ? s.trim() : s.trim().toLowerCase()))
      .filter(Boolean)
    if (!parts.length) return
    push(parts)
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
        <span key={d} className={'chip' + (isSel(i) ? ' sel' : '') + (bad[d] ? ' bad' : '')} title={bad[d] || undefined}>
          {d}
          {hits && tt && <RuleBadge hit={hits[d]} observed={observed || 0} tt={tt} />}
          <span className="chip-x" onClick={() => onChange(items.filter((x) => x !== d))}>
            x
          </span>
        </span>
      ))}
      {/* The leftover of the last chip row: something to start a drag-selection
          on, and a box that looks full. Only where there is a leftover — after
          the input, which takes a row of its own, it was a third row with
          nothing in it. */}
      {items.length > 0 && <span className="chip-fill" aria-hidden="true" />}
      <input
        className="chip-input"
        placeholder={placeholder}
        value={val}
        onChange={(e) => {
          setVal(e.target.value)
          if (err) setErr('')
        }}
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
      {err && (
        <div className="chip-err" role="alert">
          {err}
        </div>
      )}
    </div>
  )
}

// One tab per settings section, in the order the sections are written below.
// Kept as plain English names because that is what the section headers are
// keyed on: the tab is matched to its section by the header's translated text,
// so adding a section means adding its name here and nowhere else.
const SETTINGS_TABS = [
  'Sources',
  'Routing',
  'Local proxy',
  'Tunnel',
  'Connection',
  'DPI bypass',
  'Testing',
  'Remote access',
  'Appearance',
  'Statistics',
  'System',
  'Data',
  'Updates',
]

// PortStatus is the line under a proxy port field: whether the port can be
// used, and if not, what holds it. Asked of the program as the user types, a
// moment after the last key.
function PortStatus({
  port,
  other,
  otherName,
  byHand,
  tt,
}: {
  port: number
  other: number
  otherName: string
  // byHand: the proxy is used by programs set to it by hand — the system proxy
  // is off, or other devices reach it. A taken port then stops the connection
  // instead of moving (core: claimProxyPorts), and the line has to say which.
  byHand: boolean
  tt: (s: string) => string
}) {
  const [res, setRes] = useState<{ state: string; owner?: string; pid?: number } | null>(null)
  useEffect(() => {
    const h = setTimeout(() => {
      SettingsService.CheckProxyPort(port, other).then((r: any) => setRes(r))
    }, 300)
    return () => clearTimeout(h)
  }, [port, other])
  if (!res) return null
  let text = ''
  let color = 'var(--muted)'
  switch (res.state) {
    case 'free':
      text = tt('Free')
      color = 'var(--green)'
      break
    case 'ours':
      text = tt('In use by Vair — the current connection')
      break
    case 'busy':
      text =
        (res.owner
          ? tt('Taken by') + ' ' + res.owner + (res.pid ? ' (PID ' + res.pid + ')' : '')
          : tt('Taken by another program')) +
        ' — ' +
        (byHand
          ? tt('the connection will not come up until the port is free or changed')
          : tt('the connection will use another port, and programs set to this one by hand will not go through the VPN'))
      color = 'var(--red)'
      break
    case 'reserved':
      text = tt('Reserved by the system — choose another port')
      color = 'var(--red)'
      break
    case 'same':
      text = otherName === 'SOCKS' ? tt('Same as the SOCKS port') : tt('Same as the HTTP port')
      color = 'var(--red)'
      break
    case 'remote':
      text = tt('Used by remote access')
      color = 'var(--red)'
      break
    case 'invalid':
      text = tt('Must be between 1 and 65535')
      color = 'var(--red)'
      break
  }
  return (
    <div className="port-status" style={{ color }}>
      {text}
    </div>
  )
}

// presetLabel words a preset's "(default)" mark in the interface language; the
// rest of the label is an address or a service name and stays as it is.
function presetLabel(label: string, tt: (s: string) => string) {
  return label.replace(/\(default\)\s*$/, tt('(default)')).replace(/\(plain HTTP\)\s*$/, tt('(plain HTTP)'))
}

// badStatusTokens returns the entries of a status list the program will skip —
// the same grammar as core parseStatusSet: 429, 500-599, 5xx.
function badStatusTokens(spec: string): string[] {
  return spec
    .split(/[,;\s]+/)
    .map((t) => t.trim())
    .filter(Boolean)
    .filter((t) => {
      if (/^[1-5]xx$/i.test(t)) return false
      const r = /^(\d{3})-(\d{3})$/.exec(t)
      if (r) return !(+r[1] >= 100 && +r[2] <= 599 && +r[1] <= +r[2])
      return !(/^\d{3}$/.test(t) && +t >= 100 && +t <= 599)
    })
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
  // Raised by turning the persistent block on, not by opening this dialog:
  // the consequence starts at the switch.
  const [blockNotice, setBlockNotice] = useState(false)
  // On blur, empty/invalid input snaps back to the default IN THE FIELD (not only
  // on reopen): coerce, write the value back into the box, then persist it.
  // Every numeric field already carries the range it accepts as min/max — the
  // browser only enforces those on a form submit, which a settings dialog never
  // does, so a typed 500 was saved as 500. Read the bounds back off the element
  // instead of repeating them at each call site, where they would drift.
  const numBlur =
    (key: string, def: number) => (e: React.FocusEvent<HTMLInputElement>) => {
      let v = +e.target.value || def
      const lo = e.target.min === '' ? null : +e.target.min
      const hi = e.target.max === '' ? null : +e.target.max
      if (lo !== null && v < lo) v = lo
      if (hi !== null && v > hi) v = hi
      e.target.value = String(v)
      apply({ [key]: v })
    }
  const [importTabs, setImportTabs] = useState(true)
  // The helper service's state, from the capabilities, and read again after a
  // repair: the dialog's caps are the parent's and do not change under it.
  const [svcState, setSvcState] = useState<string>(caps?.helper_service || '')
  const [svcBusy, setSvcBusy] = useState(false)
  useEffect(() => setSvcState(caps?.helper_service || ''), [caps?.helper_service])
  const repairService = () => {
    setSvcBusy(true)
    ConnService.RestartAdmin()
      .then((why: string) => {
        if (why) notify(why)
        return ConnService.AppInfo().then((i: any) => setSvcState(i?.caps?.helper_service || ''))
      })
      .finally(() => setSvcBusy(false))
  }
  // The export or import in progress, or how the last one ended. Updates can
  // overtake each other (see BackupProgress in core): the newest by seq wins.
  const [bk, setBk] = useState<any>(null)
  const bkSeq = useRef(0)
  useEffect(() => {
    const take = (p: any) => {
      if (!p || typeof p.seq !== 'number' || p.seq <= bkSeq.current) return
      bkSeq.current = p.seq
      setBk(p)
    }
    if (!IS_REMOTE) SettingsService.BackupProgress().then(take)
    const off = Events.On('backup_progress', (e: any) => take(e?.data?.payload ?? e?.data))
    return () => {
      if (typeof off === 'function') off()
    }
  }, [])
  const [favOpen, setFavOpen] = useState(false)
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
  // A block that was asked for and is not in place — its filters are missing
  // from the filtering engine. Said under the switch that promised it, not only
  // in the log.
  const [blockNotInForce, setBlockNotInForce] = useState('')
  useEffect(() => {
    if (!stg.kill_switch_persistent) {
      setBlockNotInForce('')
      return
    }
    SettingsService.BlockNotInForce().then((r: string) => setBlockNotInForce(r || ''))
  }, [stg.kill_switch_persistent])
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
        setUpdMsg(t10(lang, 'Could not check for updates') + ': ' + updErrText(j.error, (s) => t10(lang, s)))
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
          setUpdMsg(t10(lang, 'Update failed') + ': ' + updErrText(p.msg || '', (s) => t10(lang, s)))
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
  const [customCheckIP, setCustomCheckIP] = useState(isCustomURL(stg.check_ip_url || '', CHECKIP_PRESETS))
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
  // What the check said about the custom address, for the line under it.
  const [bindState, setBindState] = useState('')
  // Static host lines that were not saved, for the line under the box.
  const [hostsBad, setHostsBad] = useState<string[]>([])
  // Fragment fields whose last value was not saved, for the line under each.
  const [fragBad, setFragBad] = useState({ length: false, interval: false })
  // Test address fields whose last value was not saved, for the line under each.
  const [urlBad, setUrlBad] = useState<Record<string, boolean>>({})
  useEffect(() => {
    if (proxyCustomVal) SettingsService.CheckProxyBind(proxyCustomVal).then((r: string) => setBindState(r))
  }, [proxyCustomVal])
  // The proxy is used by hand when the system proxy is off or other devices
  // reach it — then a taken port stops the connection (see PortStatus).
  const byHand = !!stg.no_system_proxy || proxyBindStored !== '127.0.0.1'
  const proxyBindShown =
    proxySelect === 'local'
      ? '127.0.0.1'
      : proxySelect === 'lan'
        ? `${remote?.ips?.[0] || '0.0.0.0'} (0.0.0.0)`
        : proxyCustomVal || '—'
  const [customRemoteDNS, setCustomRemoteDNS] = useState(isCustomURL(stg.remote_dns || '', REMOTE_DNS_PRESETS))
  // The proxy ports as typed, before they are saved — what the status line
  // under each field checks.
  const [httpDraft, setHttpDraft] = useState<number>(stg.http_port || 10819)
  const [socksDraft, setSocksDraft] = useState<number>(stg.socks_port || 10818)
  const [customBootDNS, setCustomBootDNS] = useState(isCustomURL(stg.bootstrap_dns || '', BOOTSTRAP_DNS_PRESETS))
  const [customDirectDNS, setCustomDirectDNS] = useState(isCustomURL(stg.direct_dns || '', DIRECT_DNS_PRESETS))
  const [customFb, setCustomFb] = useState(isCustomFallback(stg.speed_test_url_fallback || ''))
  // The fallback status list as typed, for the line that names what will be ignored.
  const [fbCodes, setFbCodes] = useState<string>(stg.speed_fallback_on || '')

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
  const [tab, setTab] = useState<string>(SETTINGS_TABS[0])
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
      sec.classList.remove('set-hidden')
      sec.querySelectorAll<HTMLElement>('.set-hidden').forEach((el) => el.classList.remove('set-hidden'))
      if (!query) {
        // No search: one tab at a time. Matched on the header's own text, which
        // is already translated, so the tab names are the section names in
        // whichever language is on.
        sec.classList.toggle('set-hidden', (header?.textContent || '').trim() !== tt(tab))
        continue
      }
      if ((header?.textContent || '').toLowerCase().includes(query)) continue
      // Within a tab, block by block: a block whose title matches shows whole;
      // otherwise its rows are matched one by one, each with what belongs to
      // it — its description, its sub-rows — and a block with no match hides.
      let any = false
      for (const grp of Array.from(sec.querySelectorAll<HTMLElement>(':scope > .set-group'))) {
        const head = grp.querySelector<HTMLElement>('.set-group-head')
        if ((head?.textContent || '').toLowerCase().includes(query)) {
          any = true
          continue
        }
        const card = grp.querySelector<HTMLElement>('.set-card-in')
        const kids = card ? (Array.from(card.children) as HTMLElement[]) : []
        let found = false
        let group: HTMLElement[] = []
        const flush = () => {
          if (!group.length) return
          const show = group
            .map((g) => g.textContent || '')
            .join(' ')
            .toLowerCase()
            .includes(query)
          group.forEach((g) => g.classList.toggle('set-hidden', !show))
          if (show) found = true
          group = []
        }
        for (const k of kids) {
          if (k.classList.contains('modal-row') && !k.classList.contains('row-cont')) flush()
          group.push(k)
        }
        flush()
        grp.classList.toggle('set-hidden', !found)
        if (found) any = true
      }
      sec.classList.toggle('set-hidden', !any)
    }
  }, [q, tab, stg, lang])

  // "Show" on a dependency note: the setting may be on another tab, or hidden
  // by the search. Its tab is the one whose title heads its section.
  const revealSetting = (key: string) => {
    const el = boxRef.current?.querySelector<HTMLElement>(`[data-setting="${key}"]`)
    const head = el?.closest('.settings-section')?.querySelector('.section-header')?.textContent?.trim()
    const owner = SETTINGS_TABS.find((t) => tt(t) === head)
    setQ('')
    if (owner) setTab(owner)
    // After the tab is drawn: the row has no position while its section is hidden.
    window.setTimeout(() => flashSetting(key), 60)
  }

  // Shown under the file-logging toggle so the user can find the file.
  const [logPath, setLogPath] = useState('')
  useEffect(() => {
    LogService.FilePath().then((p) => setLogPath(p || ''))
  }, [])
  // The description under a setting: what it is, then its states (whenOn /
  // whenOff for a switch, opt for a choice or a value), the default marked. With
  // descriptions switched off it becomes the mark that opens the same text.
  const hint = (en: string, opts?: HintOpt[]) => <Hint en={en} opts={opts} />
  // What the dependent settings wait on (see Dependent). The conditions are the
  // ones the program applies, not an approximation of them.
  // label draws a setting's name. A name that starts with "TUN" or "PROXY"
  // (every translation writes it first) gets it as a mark instead: the word is
  // not part of the name, it says the one mode the setting works in, and
  // fourteen names beginning with the same three letters made the list hard to
  // scan. A setting that does something in both modes has no mark.
  const label = (name: string): ReactNode => {
    const m = /^(TUN|PROXY):?\s+(.+)$/.exec(name)
    if (!m) return name
    return (
      <>
        <span
          className="mode-tag"
          title={m[1] === 'TUN' ? tt('Applies in TUN mode only') : tt('Applies in proxy mode only')}
        >
          {m[1]}
        </span>
        {m[2].charAt(0).toUpperCase() + m[2].slice(1)}
      </>
    )
  }
  // The same name in running text, as the row shows it: without the prefix,
  // in quotes.
  const settingName = (name: string) => {
    const bare = name.replace(/^(TUN|PROXY):?\s+/, '')
    const n = bare.charAt(0).toUpperCase() + bare.slice(1)
    return lang === 'ru' ? '«' + n + '»' : '“' + n + '”'
  }
  const needsOn = (ok: boolean, name: string, target: string): Req => ({
    ok,
    text: tt('Settings in this group work only with this on:') + ' ' + settingName(tt(name)),
    target,
  })
  const leakOn = !!stg.dns_leak_protection
  // The routing modes that tunnel only listed sites build their own DNS module,
  // with these servers, whether leak protection is on or not (core buildDNSBlock).
  const listsOnly = ['only_blocked', 'only_blocked_cn', 'only_selected'].includes(rmode)
  const reqLeak = needsOn(leakOn, 'TUN DNS leak protection', 'dns_leak_protection')
  const reqDNSModule: Req = {
    ok: leakOn || listsOnly,
    text:
      tt('Settings in this group work only with this on:') +
      ' ' +
      settingName(tt('TUN DNS leak protection')) +
      ' ' +
      tt('— or with a routing mode that sends only listed sites through the tunnel'),
    target: 'dns_leak_protection',
  }
  const reqAutoOff = (why: string): Req => ({ ok: !stg.auto_connect, text: why })

  return (
    <RevealSetting.Provider value={revealSetting}>
    <div
      className="modal-overlay"
      id="settings-modal"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className="modal-box" ref={boxRef}>
        {/* Theme and language sit in the title bar rather than in Appearance,
            eleven sections down. They are the two settings someone reaches for
            without knowing where they live — and the two whose effect is
            visible the moment they change — so they are on screen whichever
            section is open. */}
        <div className="modal-title set-title">
          <span>{tt('Settings')}</span>
          <div className="set-title-controls">
            <Select
              className="modal-input set-title-sel"
              aria-label={tt('Theme')}
              title={tt('Theme')}
              value={stg.theme === 'light' ? 'light' : 'dark'}
              onChange={(e) => apply({ theme: e.target.value })}
            >
              <option value="dark">{tt('Dark')}</option>
              <option value="light">{tt('Light')}</option>
            </Select>
            <Select
              className="modal-input set-title-sel"
              aria-label={tt('Language')}
              title={tt('Language')}
              value={stg.language === 'ru' ? 'ru' : 'en'}
              onChange={(e) => apply({ language: e.target.value })}
            >
              <option value="en">English</option>
              <option value="ru">Русский</option>
            </Select>
            <CloseX onClick={onClose} title={tt('Close')} />
          </div>
        </div>
        <input
          className="modal-input"
          id="set-search"
          placeholder={tt('Search settings…')}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          style={{ marginBottom: 10 }}
        />
        {/* One entry per section, in a column beside the settings rather than a
            row above them: thirteen names do not fit on a line, and wrapping
            them made the bar taller in Russian than in English. Searching looks
            across ALL sections and the list steps aside while it does — a
            search that only found what was already on screen would be a
            filter. */}
        <div className="set-split">
          <div className="set-side">
            {SETTINGS_TABS.map((name) => (
              <button
                key={name}
                className={'set-tab' + (tab === name && !q.trim() ? ' on' : '') + (q.trim() ? ' dim' : '')}
                onClick={() => {
                  setQ('')
                  setTab(name)
                }}
              >
                {tt(name)}
              </button>
            ))}
          </div>
          <div className="set-pane">

          {/* ── Sources ── */}
          <div className="settings-section">
            <div className="section-header">{tt('Sources')}</div>
            <Group>
            <div className="modal-row">
              <span className="modal-row-label">{tt('Enable Sources tab')}</span>
              <Toggle on={stg.sources_enabled !== false} onChange={(v) => apply({ sources_enabled: v })} />
            </div>
            {hint('The Sources tab: a ready-made list of public servers published by the Vair project on GitHub. The list is downloaded from raw.githubusercontent.com, or from the raw.githack.com mirror when GitHub does not answer: on the first start directly, afterwards through the tunnel of the first connection, and on RELOAD.', [
              whenOn('the tab is shown and the list is downloaded.', true),
              whenOff('the tab is hidden and the list is not downloaded.'),
            ])}
            </Group>
          </div>

          {/* ── Routing ── */}
          <div className="settings-section">
            <div className="section-header">{tt('Routing')}</div>
            <Group title={tt('Mode')} sub={tt('What goes through the tunnel')}>
            <div className="modal-row" data-setting="routing_mode" style={{ display: 'block', marginBottom: 12 }}>
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
            {/* All five modes in the description: while you are choosing, the
                difference between them is the whole question, and it used to be
                answerable only by picking each one in turn and reading what
                appeared. */}
            {hint('How traffic is split between the VPN and a direct connection. Takes effect on the next connection.', [
              opt('All traffic through VPN', 'everything goes through the VPN. Only the private addresses of your own network stay on the direct connection.', true),
              opt('Everything except selected countries', 'sites of the countries ticked below go direct, everything else through the VPN. Local sites of these countries often refuse foreign and VPN addresses (banks, government services); sending them direct keeps them working. A country\'s sites are recognised by its domain zone (*.ru, *.cn, *.ir, *.kz), its list of known sites and its IP ranges. IP ranges match only connections made to an address: a site a program opens by name may be recognised by the zone and the site list alone — for Kazakhstan, which has no site list, by the .kz zone. With no country ticked, everything goes through the VPN.'),
              opt('Only blocked-in-Russia resources', 'ordinary traffic goes direct; only resources blocked in Russia go through the VPN — matched by a community list that updates itself, plus your own lists below.'),
              opt('Only blocked-in-China resources', 'ordinary traffic goes direct; only resources blocked in China go through the VPN — matched by the community GFW list, which updates itself, plus your own lists below.'),
              opt('Only selected sites', 'only the sites on your through-VPN lists below go through the VPN; everything else goes direct. With the lists empty, nothing goes through the tunnel. With Set system proxy off, this concerns only the programs you pointed at the local proxy yourself.'),
            ])}
            {rmode === 'bypass_countries' && (
              <div id="bypass-countries-opts">
                <div className="set-note" style={{ marginTop: 8 }}>
                  {tt('Sites of these countries go direct (bypass the VPN):')}
                </div>
                {BYPASS_COUNTRIES.map((c) => (
                  <div className="modal-row row-cont" key={c.cc}>
                    <span className="modal-row-label">
                      {c.flag} {tt(c.label)}
                    </span>
                    <Toggle on={bypassCC.includes(c.cc)} onChange={(v) => setBypassCC(c.cc, v)} />
                  </div>
                ))}
              </div>
            )}

            </Group>
            <Group title={tt('Custom rules')} sub={tt('Exceptions to the mode, by site or by program')}>
            <div className="modal-row" style={{ marginTop: 12 }}>
              <span className="modal-row-label">{tt('Count rule matches')}</span>
              <Toggle on={!!stg.rule_stats} onChange={(v) => apply({ rule_stats: v })} />
            </div>
            {hint('Counts how many times each rule below has matched. Takes effect on the next connection.', [
              whenOn('each rule shows its match count, and rules that never matched are marked — usually a typo. It is costly: the engine logs every routing decision and DNS lookup, which on a busy connection means thousands of lines per second and noticeable processor load.'),
              whenOff('no counters. The Connections panel works without them.', true),
            ])}

            {/* ── Custom rules (all modes): domain / IP / CIDR / full:/regexp: ── */}
            <div className="modal-row" style={{ marginTop: 12, marginBottom: 4 }}>
              <span className="modal-row-label">{tt('Custom rules — without VPN')}</span>
              <Toggle on={!stg.direct_domains_disabled} onChange={(v) => apply({ direct_domains_disabled: !v })} />
            </div>
            <Chips
              items={stg.direct_domains || []}
              hits={ruleHits('direct')}
              rules
              observed={ruleStats?.observed || 0}
              tt={tt}
              disabled={!!stg.direct_domains_disabled}
              placeholder={tt('domain, IP or CIDR — press Enter')}
              onChange={(next) => apply({ direct_domains: next })}
            />
            {hint('Addresses kept off the VPN: they go direct. Accepts a domain (subdomains included), an IP address or a CIDR (for example 10.0.0.0/8), or a prefix: full: for one exact name, regexp: for an expression, domain: for a domain with its subdomains. A link or *.example.com is reduced to its domain; an entry that cannot be read is not added. IP and CIDR entries match only connections made to an address, and a program that opens a site by name may not make one — give sites by their domain. geosite: and geoip: name categories in xray’s own databases; they do not work in every connection mode. Takes effect on the next connection.', [
              whenOn('the list is applied.', true),
              whenOff('the list is kept but not applied.'),
            ])}

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
              rules
              observed={ruleStats?.observed || 0}
              tt={tt}
                  disabled={!!stg.proxy_domains_disabled}
                  placeholder={tt('domain, IP or CIDR — press Enter')}
                  onChange={(next) => apply({ proxy_domains: next })}
                />
                {hint('Addresses sent through the VPN, even in the modes that leave other sites direct. The same syntax, and the same note on IP entries, as in the list above. In Only selected sites this is the main list. Takes effect on the next connection.', [
                  whenOn('the list is applied.', true),
                  whenOff('the list is kept but not applied.'),
                ])}

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
                {hint('Lists of sites to send through the VPN, from links: plain-text lists, one domain per line, downloaded and kept up to date automatically. Not used in All traffic through VPN, where everything goes through it anyway. Lines are read as in the list above, IP and CIDR lines with the same note. A new link is first downloaded directly, before the tunnel is up; later updates go through the tunnel. If a list cannot be downloaded, the connection runs without it and the log says so. Press Enter to add a link. Takes effect on the next connection.', [
                  whenOn('the lists are applied.', true),
                  whenOff('the links are kept but not used.'),
                ])}
              </>
            )}

            <div className="modal-row" style={{ marginTop: 10, marginBottom: 4 }}>
              <span className="modal-row-label">{tt('Custom rules — block')}</span>
              <Toggle on={!stg.block_domains_disabled} onChange={(v) => apply({ block_domains_disabled: !v })} />
            </div>
            <Chips
              items={stg.block_domains || []}
              hits={ruleHits('block')}
              rules
              observed={ruleStats?.observed || 0}
              tt={tt}
              disabled={!!stg.block_domains_disabled}
              placeholder={tt('domain, IP or CIDR — press Enter')}
              onChange={(next) => apply({ block_domains: next })}
            />
            {hint('Addresses that are blocked entirely: ads, telemetry, unwanted hosts. The same syntax as above. A block wins over every other rule, including the local network and the apps without VPN. Takes effect on the next connection.', [
              whenOn('the list is applied.', true),
              whenOff('the list is kept but not applied.'),
            ])}
            {/* Per-application routing is a TUN feature, and TUN is not on every
                platform yet. A list the engine will never consult looks like a
                setting that quietly stopped working. */}
            {caps?.split_tunnel !== false && (
              <>
                <div className="modal-row" style={{ marginTop: 10, marginBottom: 4 }}>
                  <span className="modal-row-label">{label(tt('TUN Apps without VPN'))}</span>
                  <Toggle on={!stg.direct_apps_disabled} onChange={(v) => apply({ direct_apps_disabled: !v })} />
                </div>
                <Chips
                  items={stg.direct_apps || []}
                  hits={ruleHits('apps')}
                  observed={ruleStats?.observed || 0}
                  tt={tt}
                  disabled={!!stg.direct_apps_disabled}
                  placeholder={tt('e.g. Discord.exe or firefox, press Enter')}
                  onChange={(next) => apply({ direct_apps: next })}
                  // A program path is the user's spelling, and it can contain a
                  // space — so neither fold the case nor split on one.
                  keepCase
                  splitOn={/[\n\r,;]+/}
                />
                {/* The picker is an action, so it is a button of its own: inside the
                    description it went with the description when those are hidden. */}
                {caps?.process_picker !== false && (
                  <div className="set-actions">
                    <button className="btn ghost" onClick={openProcPicker}>
                      {tt('Browse running processes')}
                    </button>
                  </div>
                )}
                {hint('Programs whose traffic goes around the VPN, by name; case does not matter. A full path tells apart two programs with the same name. Takes effect on the next connection.', [
                  whenOn('the list is applied.', true),
                  whenOff('the list is kept but not applied.'),
                ])}
              </>
            )}
            </Group>
          </div>

          {/* ── Ports ── */}
          <div className="settings-section">
            <div className="section-header">{tt('Local proxy')}</div>
            <Group title={tt('Ports and access')} sub={tt('Where the local proxy listens')}>
            <div className="modal-row">
              <span className="modal-row-label">{tt('HTTP proxy port')}</span>
              <input
                className="modal-input num-input"
                type="number"
                min={1}
                max={65535}
                defaultValue={stg.http_port || 10819}
                onChange={(e) => setHttpDraft(+e.target.value || 0)}
                onBlur={(e) => {
                  numBlur('http_port', 10819)(e)
                  // numBlur puts the default back for an empty or out-of-range
                  // value; the status line has to describe that, not what was typed.
                  setHttpDraft(+e.target.value)
                }}
              />
            </div>
            <PortStatus port={httpDraft} other={socksDraft} otherName="SOCKS" byHand={byHand} tt={tt} />
            {hint('The port the local HTTP proxy listens on while connected. If another program holds it: with Set system proxy on and the proxy open to this computer only, the connection takes another port, the system proxy follows, and a notification says so — programs set to the port by hand then do not go through the VPN. Otherwise the connection does not come up, and the error names the program that holds the port. Takes effect on the next connection.', [
              opt('10819', '', true),
              opt('Another port', 'if 10819 is taken by another program.'),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('SOCKS proxy port')}</span>
              <input
                className="modal-input num-input"
                type="number"
                min={1}
                max={65535}
                defaultValue={stg.socks_port || 10818}
                onChange={(e) => setSocksDraft(+e.target.value || 0)}
                onBlur={(e) => {
                  numBlur('socks_port', 10818)(e)
                  setSocksDraft(+e.target.value)
                }}
              />
            </div>
            <PortStatus port={socksDraft} other={httpDraft} otherName="HTTP" byHand={byHand} tt={tt} />
            {hint('The port the local SOCKS5 proxy listens on while connected. A port held by another program is handled as for the HTTP port. UDP through it works for programs on this computer and for devices on the network. Takes effect on the next connection.', [
              opt('10818', '', true),
              opt('Another port', 'if 10818 is taken by another program.'),
            ])}
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
            {hint('Which addresses may reach the two proxy ports above. For access beyond this computer, Vair opens the ports in Windows Firewall for devices on the local subnet of private and domain networks only: public networks stay closed, and nothing outside the subnet is let in, over IPv6 either. The Vair helper service, which the setup program installs, opens them; a portable copy without it needs Vair running as administrator, otherwise Windows either asks for permission when the proxy starts or blocks other devices. Takes effect on the next connection.', [
              opt('Localhost only', 'this computer and nothing else.', true),
              opt('Allow LAN access', 'the devices on your local network. Use it only on a network you trust: both proxies are open to them until you set passwords below.'),
              opt('Custom', 'one address of this computer, for the other devices; this computer still reaches the proxy at 127.0.0.1. The same note on passwords applies.'),
            ])}
            {proxyCustom && (
              <>
                <div className="modal-row row-cont">
                  <span className="modal-row-label">{tt('Bind address')}</span>
                  <input
                    className="modal-input"
                    style={{ width: 'auto', marginBottom: 0 }}
                    placeholder="192.168.0.10"
                    defaultValue={proxyCustomVal}
                    onBlur={(e) => {
                      // Checked before it is kept: text that is not an address
                      // used to be saved and then quietly replaced by
                      // 127.0.0.1, while this screen showed what was typed.
                      const v = e.target.value.trim()
                      if (!v) {
                        e.target.value = '127.0.0.1'
                        setBindState('')
                        apply({ proxy_bind: '127.0.0.1', proxy_allow_lan: false })
                        return
                      }
                      SettingsService.CheckProxyBind(v).then((r: string) => {
                        setBindState(r)
                        if (r !== 'invalid') apply({ proxy_bind: v, proxy_allow_lan: false })
                      })
                    }}
                  />
                </div>
                {bindState === 'invalid' && (
                  <div className="port-status" style={{ color: 'var(--red)' }}>
                    {tt('Not an IP address — not saved')}
                  </div>
                )}
                {bindState === 'absent' && (
                  <div className="port-status" style={{ color: 'var(--red)' }}>
                    {tt('This computer has no such address now — the connection will not come up until it does')}
                  </div>
                )}
                {hint('An address this computer has, for example 192.168.0.10: devices on the network reach the proxy there, and this computer at 127.0.0.1. Text that is not an IP address is not saved; with an address the computer does not have, the connection does not come up until it does.')}
              </>
            )}
            <div className="set-note">
              {tt('Address the proxy will use')}: <b>{proxyBindShown}</b>
            </div>
            </Group>
            <Group title={tt('System proxy')} sub={tt('How programs on this computer are pointed at the proxy')}>
            <div className="modal-row" data-setting="no_system_proxy">
              <span className="modal-row-label">{label(tt('PROXY Set system proxy'))}</span>
              <Toggle on={!stg.no_system_proxy} onChange={(v) => apply({ no_system_proxy: !v })} />
            </div>
            {hint('Points the system proxy setting at Vair while connected, so every program that follows it goes through the VPN. Takes effect immediately, even on a live connection.', [
              whenOn('for the connection Vair also switches off “Automatically detect settings” and a setup script (PAC), which Windows would otherwise use instead of the address. On disconnect your own settings come back; after Vair stops unexpectedly they come back at its next start — until then, programs that follow the system proxy cannot connect.', true),
              whenOff('Vair only runs the local HTTP and SOCKS proxies on the ports above and changes nothing else: only programs where you set that proxy yourself go through the VPN, the rest of the system connects directly.'),
            ])}
            {/* What the switch actually REACHES, which is not the same everywhere.
                On Windows one registry setting covers nearly everything; on Linux
                the desktop's setting covers its own apps and browsers, and nothing
                that reads http_proxy from its environment — a difference users
                would otherwise discover as "the VPN doesn't work in my terminal". */}
            {caps?.system_proxy_backend === 'gsettings' &&
              hint('On this desktop the setting is written to GNOME (gsettings). GNOME programs and browsers set to “use system proxy settings” follow it. Terminal tools — curl, git, apt, ssh — read the http_proxy environment variable instead and are not covered; point them at the proxy yourself.')}
            {caps?.system_proxy_backend === 'kde' &&
              hint('On this desktop the setting is written to KDE (kioslaverc). KDE programs and browsers set to “use system proxy settings” follow it. Terminal tools — curl, git, apt, ssh — read the http_proxy environment variable instead and are not covered; point them at the proxy yourself.')}
            {/* Only meaningful where a backend exists to be hijacked. */}
            {caps?.system_proxy_backend !== 'none' && (
              <Dependent reqs={[needsOn(!stg.no_system_proxy, 'PROXY Set system proxy', 'no_system_proxy')]} tt={tt}>
                <div className="modal-row">
                  <span className="modal-row-label">{label(tt('PROXY Keep the system proxy set'))}</span>
                  <Toggle
                    on={!stg.proxy_guard_disabled}
                    onChange={(v) => apply({ proxy_guard_disabled: !v })}
                  />
                </div>
                {hint('Checks every 20 seconds while connected that the system proxy still points to Vair.', [
                  whenOn('if another program changed it — another VPN client, a policy update, a browser extension — or switched automatic detection or a PAC script back on, Vair restores it; otherwise traffic would go around the tunnel while Vair still shows “connected”. If the setting is overwritten repeatedly, Vair stops restoring it and says so in the log.', true),
                  whenOff('Vair does not touch the system proxy after connecting — for when another program is supposed to manage it.'),
                ])}
              </Dependent>
            )}
            {caps?.system_proxy_backend === 'none' && (
              <Hint>
                {(tt) => (
                  <>
                    {tt('This desktop has no system proxy setting Vair can drive, so this switch changes nothing here. Point applications at the local proxy yourself:')}{' '}
                    <code>export http_proxy=http://127.0.0.1:{stg.http_port || 10819}</code>
                  </>
                )}
              </Hint>
            )}
            </Group>
            <Group title={tt('Protection')} sub={tt('Traffic that goes around the proxy')}>
            {caps?.kill_switch_persistent !== false && (
              <>
                <div className="modal-row">
                  <span className="modal-row-label">{label(tt('PROXY Kill-switch'))}</span>
                  <Toggle
                    on={!!stg.proxy_kill_switch}
                    onChange={(v) => apply({ proxy_kill_switch: v })}
                  />
                </div>
                {hint('Blocks the direct traffic of programs that do not use the system proxy, for as long as a proxy session lasts. Uses the Vair helper service, which the setup program installs; a portable copy without it needs Vair running as administrator.', [
                  whenOn('only Vair and its engines, local connections on this computer (the proxy runs there) and the local network — unless “Block LAN traffic” is on — can connect. The block holds through a switch to another server, manual or automatic. If no session is re-established within 20 seconds, traffic returns to the normal connection; to keep it blocked, turn on the persistent kill-switch. The block is also lifted when you disconnect, turn this setting off, quit Vair or connect in TUN mode without the persistent kill-switch, and when Vair stops unexpectedly: at once, or 20 seconds later with the helper service.'),
                  whenOff('programs that do not use the system proxy connect directly.', true),
                ])}
              </>
            )}
            </Group>
            <Group title={tt('Authentication')} sub={tt('The login and password clients must send')}>
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
            {hint('A username and password for the local SOCKS5 proxy, so other programs on this computer and devices on the network cannot use it without them. Also covers the SOCKS proxy that “Share proxy over LAN” opens. Takes effect on the next connection.', [
              whenOn('clients must send the username and password below. The HTTP port stays open unless HTTP authentication is on too.'),
              whenOff('the SOCKS port is open to anyone who can reach it.', true),
            ])}
            {!!stg.socks_auth && (
              <div id="socks-creds">
                <div className="modal-row row-cont" style={{ display: 'block', marginBottom: 8 }}>
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
                {hint('The username a SOCKS5 client must send. If the field is left empty, a new one is generated.', [
                  opt('Random', 'generated when authentication is turned on.', true),
                  opt('Your own', 'enter one if a client requires a specific name.'),
                ])}
                <div className="modal-row row-cont" style={{ display: 'block', marginBottom: 6 }}>
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
                <Hint
                  style={{ marginTop: 0 }}
                  en="The password that goes with it — enter both in your SOCKS5 client. Reset draws a new pair, and clients still holding the old one are refused until you update them."
                />
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
            {hint('A username and password for the local HTTP proxy (Basic auth). Also covers the HTTP proxy that “Share proxy over LAN” opens. Takes effect on the next connection.', [
              whenOn('clients must send the username and password below. The Windows system proxy cannot carry a password: with Set system proxy on, browsers ask for these credentials and most other programs that use the system proxy stop connecting — use it for clients you set up yourself, with Set system proxy off.'),
              whenOff('the HTTP port is open to anyone who can reach it.', true),
            ])}
            {/* Said where the two meet, not only in a description that can be
                hidden: with both on, the programs that follow the system
                proxy get an authentication request they cannot answer. */}
            {!!stg.http_auth && !stg.no_system_proxy && (
              <div className="dep-note" role="note">
                <span>
                  {tt('HTTP authentication is on while “Set system proxy” is on: programs that use the system proxy are asked for a password and most of them cannot connect.')}
                </span>
                <button className="linkish" onClick={() => revealSetting('no_system_proxy')}>
                  {tt('Show')}
                </button>
              </div>
            )}
            {!!stg.http_auth && (
              <div id="http-creds">
                <div className="modal-row row-cont" style={{ display: 'block', marginBottom: 8 }}>
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
                {hint('The username for Basic authentication on the local HTTP proxy. If the field is left empty, a new one is generated.', [
                  opt('Random', 'generated when authentication is turned on.', true),
                  opt('Your own', 'enter one if a client requires a specific name.'),
                ])}
                <div className="modal-row row-cont" style={{ display: 'block', marginBottom: 6 }}>
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
                <Hint
                  style={{ marginTop: 0 }}
                  en="The password that goes with it — enter both in your HTTP client. Reset draws a new pair, and clients still holding the old one are refused until you update them."
                />
              </div>
            )}
            </Group>
          </div>
          {/* ── Tunnel ── */}
          <div className="settings-section">
            <div className="section-header">{tt('Tunnel')}</div>
            <Group title={tt('Adapter')} sub={tt('The virtual network interface')}>
            <div className="modal-row">
              <span className="modal-row-label">{label(tt('TUN MTU'))}</span>
              <input
                className="modal-input num-input"
                type="number"
                min={576}
                max={9000}
                defaultValue={stg.tun_mtu || 9000}
                onBlur={numBlur('tun_mtu', 9000)}
              />
            </div>
            {hint('The largest packet size in the tunnel, in bytes. Takes effect on the next connection.', [
              opt('9000', 'large packets (jumbo frames).', true),
              opt('A lower value', 'may help when downloads stall or sites hang; try 1500 or 1408. While IPv6 goes into the tunnel (IPv6 set to Blocked or Through the VPN), values below 1280 are raised to 1280: IPv6 does not work with less.'),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{label(tt('TUN TCP/IP stack'))}</span>
              <Select
                className="modal-input"
                style={{ width: 'auto', marginBottom: 0 }}
                value={stg.tun_stack || 'gvisor'}
                onChange={(e) => apply({ tun_stack: e.target.value })}
              >
                <option value="gvisor">gVisor</option>
                <option value="mixed">{tt('Mixed (system TCP)')}</option>
                <option value="system">{tt('System stack')}</option>
              </Select>
            </div>
            {hint('What handles TCP traffic inside the tunnel. Takes effect on the next connection.', [
              opt('gVisor', 'built into the engine and behaves the same on every system. It processes every packet inside the program rather than in the system kernel, so it loads the processor more than the other options.', true),
              opt('Mixed (system TCP)', 'the operating system handles TCP, gVisor handles UDP. If the tunnel loads the processor, try this first.'),
              opt('System stack', 'the operating system handles TCP; the processor load is much lower.'),
            ])}
            </Group>
            <Group title={tt('Protection')} sub={tt('What cannot leave around the tunnel')}>
            <div className="modal-row" data-setting="dns_leak_protection">
              <span className="modal-row-label">{label(tt('TUN DNS leak protection'))}</span>
              <Toggle on={!!stg.dns_leak_protection} onChange={(v) => apply({ dns_leak_protection: v })} />
            </div>
            {hint('Hides the names of the sites you open through the tunnel from your provider. FakeIP and the kill-switch do not work without it. Bootstrap DNS, Direct DNS, static hosts and local names (such as printer.lan) work either way. Takes effect on the next connection.', [
              whenOn('site names are resolved inside the tunnel through Remote DNS, and other programs on this computer cannot ask a DNS server outside the tunnel.', true),
              whenOff('site names go to the system DNS outside the tunnel, and your provider sees them. In the modes that tunnel only listed sites, Vair still resolves those sites through the tunnel, but the system asks the provider as well. The kill-switch turns off at once, without waiting for a reconnect.'),
            ])}
            <Dependent reqs={[reqLeak]} tt={tt}>
              <div id="security-deps">
                {/* A safety switch that cannot be enforced must not be offered:
                    someone who turns it on and believes it would be worse off
                    than someone who knows it is missing. */}
                {caps?.kill_switch !== false && (
                  <>
                    <div className="modal-row" data-setting="kill_switch">
                      <span className="modal-row-label">{label(tt('TUN Kill-switch'))}</span>
                      <Toggle on={!!stg.kill_switch} onChange={(v) => apply({ kill_switch: v })} />
                    </div>
                    {hint('Keeps internet traffic from leaving outside the tunnel while the tunnel is up. Works only with DNS leak protection on — that is what holds DNS queries in. Takes effect immediately, including on the current connection.', [
                      whenOn('the block holds through a switch to another server, manual or automatic. If no new tunnel comes up within 20 seconds, the block is lifted and traffic goes through the normal connection. To keep it from being lifted, turn on the persistent kill-switch.', true),
                      whenOff('nothing holds traffic in while the tunnel drops or changes. On Windows, programs bound to the physical network adapter go around the tunnel.'),
                    ])}
                    {/* Only meaningful once the kill-switch above is on: this is
                        that block, made to outlive the session that holds it. */}
                    {caps?.kill_switch_persistent !== false && (
                      <Dependent reqs={[needsOn(!!stg.kill_switch, 'TUN Kill-switch', 'kill_switch')]} tt={tt}>
                        <div className="modal-row">
                          <span className="modal-row-label">{label(tt('TUN Persistent kill-switch'))}</span>
                          <Toggle
                            on={!!stg.kill_switch_persistent}
                            onChange={(v) => {
                              apply({ kill_switch_persistent: v })
                              if (v && keepBlockNoticeWanted()) setBlockNotice(true)
                            }}
                          />
                        </div>
                        {hint('Extends the kill-switch block to all the time there is no VPN tunnel. Works only with the kill-switch and DNS leak protection on. Turning it on takes effect immediately if a tunnel is up, otherwise on the next connection; turning it off lifts the block immediately.', [
                          whenOn('without a VPN tunnel this computer does not reach the internet — after a failed reconnect, after Vair is closed or crashes, and after Windows restarts. Vair and its engines can still connect, so the tunnel can be brought back, and so can the local network unless “Block LAN traffic” is on. The block does not lift by itself: disconnect, turn this setting off, or press the red “traffic blocked ✕” button on the connection bar.'),
                          whenOff('only the ordinary kill-switch holds traffic in — while the tunnel is up and for 20 seconds after it drops.', true),
                        ])}
                        {!!stg.kill_switch_persistent && blockNotInForce && (
                          <div className="set-note" style={{ color: 'var(--red)' }}>
                            {tt('This switch is on, but the block is not in force on this machine:')} {blockNotInForce}
                          </div>
                        )}
                        {/* The warning itself is a window now — see keepblock.tsx.
                            What stays here is the one line someone comes BACK to
                            this screen to find: how to open the firewall again. */}
                        {!!stg.kill_switch_persistent && (
                          <div className="set-note" style={{ color: 'var(--red)' }}>
                            {svcState && svcState !== 'absent'
                              ? tt('To lift the block without the Vair window, run:')
                              : tt('To lift the block without the Vair window, run as administrator:')}{' '}
                            <code>Vair.exe --release-block</code>{' '}
                            <button className="linkish" onClick={() => setBlockNotice(true)}>
                              {tt('what this means')}
                            </button>
                          </div>
                        )}
                      </Dependent>
                    )}
                  </>
                )}
              </div>
            </Dependent>
            </Group>
            <Group title={tt('Network')} sub={tt('The local network and IPv6')}>
            {/* Not part of the group above: the LAN rule is in the routing rules
                whether leak protection is on or not. */}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Block LAN traffic')}</span>
              <Toggle on={!!stg.block_lan} onChange={(v) => apply({ block_lan: v })} />
            </div>
            {hint('Decides whether traffic to private addresses of the local network (192.168.x.x and the like) goes through the tunnel. Takes effect on the next connection.', [
              whenOn('such addresses go through the tunnel, which usually breaks local services. The network this computer is attached to is still routed past the tunnel by the system: with the kill-switch on that traffic is blocked, without it it goes direct.'),
              whenOff('such addresses go around the VPN, so printers, network storage and the router\'s web page keep working.', true),
            ])}
            {/* Meaningless without TUN mode, which not every platform has yet. */}
            {caps?.tun !== false && (
              <>
                <div className="modal-row">
                  <span className="modal-row-label">{label(tt('TUN Share proxy over LAN'))}</span>
                  <Toggle on={!!stg.tun_share_proxy} onChange={(v) => apply({ tun_share_proxy: v })} />
                </div>
                {hint('Lets other devices, such as a phone, reach the internet through this computer\'s VPN. Takes effect on the next connection.', [
                  whenOn('Vair also opens the HTTP and SOCKS proxies on the address from “Proxy access”, while this computer stays in the tunnel. For other devices to reach them, set “Proxy access” to “Allow LAN access” or to an address of your own, and point the device at the HTTP or the SOCKS port. UDP works through the SOCKS port.'),
                  whenOff('only this computer uses the tunnel.', true),
                ])}
              </>
            )}
            <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
              <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                {tt('IPv6')}
              </span>
              <Select
                className="modal-input"
                style={{ marginBottom: 0 }}
                value={stg.ipv6_mode || 'blocked'}
                onChange={(e) => apply({ ipv6_mode: e.target.value })}
              >
                <option value="blocked">{tt('Blocked')}</option>
                <option value="tunnel">{tt('Through the VPN')}</option>
                <option value="direct">{tt('Direct — outside the VPN')}</option>
              </Select>
            </div>
            {hint('What happens to IPv6 traffic while connected. Local addresses are not affected. Takes effect on the next connection.', [
              opt('Blocked', 'IPv6 connections are refused and names resolve to IPv4 addresses only. No IPv6 traffic leaves this computer.', true),
              opt('Through the VPN', 'IPv6 goes through the server, if the server supports it. If it does not, the log says so and IPv4 keeps working.'),
              opt('Direct — outside the VPN', 'IPv6 bypasses the tunnel and uses your real address. Choose it only if you need resources reachable over IPv6 alone.'),
            ])}
            </Group>
            <Group id="dns-section" title={tt('DNS')} sub={tt('Who resolves names, and how')}>
              <Dependent
                reqs={[
                  reqLeak,
                  {
                    ok: !listsOnly,
                    text: tt('Not used in the chosen routing mode: names outside the lists are resolved directly, to their real addresses.'),
                    target: 'routing_mode',
                  },
                ]}
                tt={tt}
              >
              <div className="modal-row">
                <span className="modal-row-label">{label(tt('TUN FakeIP'))}</span>
                <Toggle on={!stg.fakeip_disabled} onChange={(v) => apply({ fakeip_disabled: !v })} />
              </div>
              {hint('How Vair answers programs\' DNS queries. Works only with DNS leak protection on, and is not used in the modes that tunnel only listed sites. Takes effect on the next connection.', [
                whenOn('a program gets a placeholder address at once, and Vair finds the real one inside the tunnel when the connection is made. This is faster, and no DNS query leaves before the tunnel exists. A placeholder is valid for a few seconds; addresses are handed out afresh with every connection. Programs that do their own DNS and expect real addresses may not work.'),
                whenOff('the real address is resolved through the tunnel before the connection starts. Every program understands such an answer.', true),
              ])}
              </Dependent>
              {/* Bootstrap, the strict switch, Direct DNS and the static hosts wait on
                  nothing: none of them is about the names behind the tunnel, which
                  is all leak protection decides. Remote DNS is that, so it waits
                  on protection — or a list-only mode, which resolves its lists
                  through the tunnel either way. */}
              <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
                <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                  {tt('Bootstrap DNS')}{' '}
                  <span style={{ color: 'var(--muted)', fontWeight: 400 }}>
                    {tt('(resolves the VPN server\'s name)')}
                  </span>
                </span>
                <Select
                  className="modal-input"
                  style={{ marginBottom: 0 }}
                  value={customBootDNS ? '__custom' : stg.bootstrap_dns || ''}
                  onChange={(e) => {
                    if (e.target.value === '__custom') {
                      setCustomBootDNS(true)
                      return
                    }
                    setCustomBootDNS(false)
                    apply({ bootstrap_dns: e.target.value })
                  }}
                >
                  {BOOTSTRAP_DNS_PRESETS.map((p) => (
                    <option key={p.label} value={p.url}>
                      {tt(p.label)}
                    </option>
                  ))}
                  <option value="__custom">{tt('Own address…')}</option>
                </Select>
              </div>
              {customBootDNS && (
                <div className="modal-row row-cont" style={{ display: 'block', marginBottom: 12 }}>
                  <input
                    className="modal-input"
                    style={{ marginBottom: 0 }}
                    placeholder="9.9.9.9"
                    defaultValue={stg.bootstrap_dns || ''}
                    onBlur={(e) => apply({ bootstrap_dns: e.target.value.trim() })}
                  />
                </div>
              )}
              {hint('The DNS server Vair asks for the address of the VPN server itself. The address is needed before the tunnel is up, so the query goes outside it. If the server does not answer, Vair asks the system DNS (unless the next setting forbids it) and says so in the log. Configurations with an IP address instead of a name need no query. Takes effect on the next connection and the next server check.', [
                opt('Quad9, 9.9.9.9', 'the query goes over DNS over HTTPS: the network sees that you are asking Quad9, but not which name.', true),
                opt('Another server', 'pick one from the list or enter your own address. A server that supports DNS over HTTPS is asked the same way; one that does not is asked in plain DNS, which anyone on the path can read, and the log says so.'),
              ])}
              <div className="modal-row">
                <span className="modal-row-label">{tt('Bootstrap DNS only, no system fallback')}</span>
                <Toggle on={!!stg.bootstrap_strict} onChange={(v) => apply({ bootstrap_strict: v })} />
              </div>
              {hint('Decides what Vair does when Bootstrap DNS does not answer. Configurations with an IP address and names in “Static hosts” are not affected. Takes effect on the next connection and the next server check.', [
                whenOn('the connection fails. The VPN server\'s name never reaches your provider\'s DNS server, but while Bootstrap DNS is unreachable you cannot connect.'),
                whenOff('Vair asks the system DNS — usually your provider\'s. The connection goes on, but the VPN server\'s name reaches the provider\'s DNS server.', true),
              ])}
              <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
                <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                  {label(tt('TUN Direct DNS'))}{' '}
                  <span style={{ color: 'var(--muted)', fontWeight: 400 }}>
                    {tt('(for traffic outside the tunnel)')}
                  </span>
                </span>
                <Select
                  className="modal-input"
                  style={{ marginBottom: 0 }}
                  value={customDirectDNS ? '__custom' : stg.direct_dns || ''}
                  onChange={(e) => {
                    if (e.target.value === '__custom') {
                      setCustomDirectDNS(true)
                      return
                    }
                    setCustomDirectDNS(false)
                    apply({ direct_dns: e.target.value })
                  }}
                >
                  {DIRECT_DNS_PRESETS.map((p) => (
                    <option key={p.label} value={p.url}>
                      {tt(p.label)}
                    </option>
                  ))}
                  <option value="__custom">{tt('Own address…')}</option>
                </Select>
              </div>
              {customDirectDNS && (
                <div className="modal-row row-cont" style={{ display: 'block', marginBottom: 12 }}>
                  <input
                    className="modal-input"
                    style={{ marginBottom: 0 }}
                    placeholder="77.88.8.8"
                    defaultValue={stg.direct_dns || ''}
                    onBlur={(e) => apply({ direct_dns: e.target.value.trim() })}
                  />
                </div>
              )}
              {hint('The DNS server for sites that open outside the tunnel: the sites of countries left direct in “Everything except selected countries”, the without-VPN rules, and, in the modes that tunnel only listed sites, every site not on those lists. In “All traffic through VPN” it is used only for names on the without-VPN list. Local names (.lan, .local, .home, .internal) always go to the system DNS. Takes effect on the next connection.', [
                opt('Yandex, 77.88.8.8', 'a Russian server, suited if you are in Russia.', true),
                opt('Another server', 'choose one near you — through a foreign DNS server, local sites may return foreign servers or refuse. “System resolver” is the DNS this computer is set to use; choose it if the without-VPN list holds internal names under an ordinary domain, such as a company\'s.'),
              ])}
              <Dependent reqs={[reqDNSModule]} tt={tt}>
              <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
                <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                  {label(tt('TUN Remote DNS'))}{' '}
                  <span style={{ color: 'var(--muted)', fontWeight: 400 }}>
                    {tt('(through proxy; DoH URL or IP)')}
                  </span>
                </span>
                <Select
                  className="modal-input"
                  style={{ marginBottom: 0 }}
                  value={customRemoteDNS ? '__custom' : stg.remote_dns || ''}
                  onChange={(e) => {
                    if (e.target.value === '__custom') {
                      setCustomRemoteDNS(true)
                      return
                    }
                    setCustomRemoteDNS(false)
                    apply({ remote_dns: e.target.value })
                  }}
                >
                  {REMOTE_DNS_PRESETS.map((p) => (
                    <option key={p.label} value={p.url}>
                      {tt(p.label)}
                    </option>
                  ))}
                  <option value="__custom">{tt('Own address…')}</option>
                </Select>
              </div>
              {customRemoteDNS && (
                <div className="modal-row row-cont" style={{ display: 'block', marginBottom: 12 }}>
                  <input
                    className="modal-input"
                    style={{ marginBottom: 0 }}
                    placeholder="https://1.1.1.1/dns-query"
                    defaultValue={stg.remote_dns || ''}
                    onBlur={(e) => apply({ remote_dns: e.target.value.trim() })}
                  />
                </div>
              )}
              {hint('The DNS server for sites that open through the tunnel — in most modes, nearly all traffic. Queries travel inside the tunnel and your provider does not see them. Without DNS leak protection it is used only in the modes that tunnel only listed sites, and only for the names on those lists. Takes effect on the next connection.', [
                opt('Cloudflare', 'filters nothing.', true),
                opt('Another server', 'any server reachable from your VPN server works: a DoH address or a plain IP. The list includes servers that block advertising, tracking or malware domains — for every program on this computer at once. Filtering is done by the third-party service under its own rules.'),
              ])}
              </Dependent>
              <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
                <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                  {tt('Static hosts')}{' '}
                  <span style={{ color: 'var(--muted)', fontWeight: 400 }}>
                    {tt('(domain → IP; one per line)')}
                  </span>
                </span>
                <textarea
                  className="modal-input"
                  style={{ marginBottom: 0, minHeight: 60 }}
                  placeholder="vpn.example.com 1.2.3.4"
                  defaultValue={staticHostsToText(stg.static_hosts)}
                  onBlur={(e) => {
                    const { hosts, bad } = parseStaticHosts(e.target.value)
                    setHostsBad(bad)
                    apply({ static_hosts: hosts })
                  }}
                />
              </div>
              {hostsBad.length > 0 && (
                <div className="port-status" style={{ color: 'var(--red)' }}>
                  {tt('Not saved — the address is not an IP address:')} {hostsBad.join('; ')}
                </div>
              )}
              {hint('Your own name–address entries, checked before any DNS server. One per line: the name, a space, the address — for example example.com 93.184.216.34. A line whose address is not an IP address is not saved. Takes effect on the next connection and the next server check.', [
                opt('Empty', 'every name is resolved by DNS servers.', true),
                opt('With entries', 'the VPN server\'s address is always taken from here, other names not in every connection mode. Use it to pin the address of the VPN server or to correct a name that resolves wrongly.'),
              ])}
            </Group>
          </div>

          {/* ── Connection ── */}
          <div className="settings-section">
            <div className="section-header">{tt('Connection')}</div>
            <Group>
            <Dependent reqs={[reqAutoOff(tt('Not used while auto-connect is on: AUTO chooses the server itself when Vair starts.'))]} tt={tt}>
            <div className="modal-row">
              <span className="modal-row-label">{tt('Restore the connection on start')}</span>
              <Toggle on={!!stg.reconnect_on_start} onChange={(v) => apply({ reconnect_on_start: v })} />
            </div>
            {hint('What happens on start to the connection that was active when Vair was closed. If auto-connect is on, it takes precedence and picks the server itself.', [
              whenOn('Vair reconnects to the same configuration in the same mode; a chain comes back as the same chain. If the network is not up yet, Vair waits for it and keeps trying for up to 2 minutes. If the configuration, or a server of the chain, is no longer in any tab, nothing is connected and the connection bar says why. With the persistent kill-switch the block survives a restart, and this brings the tunnel back under it automatically.'),
              whenOff('Vair starts without connecting.', true),
            ])}
            </Dependent>
            <Dependent reqs={[reqAutoOff(tt('Not used while auto-connect is on: AUTO runs the same checks and switches to another server instead of disconnecting.'))]} tt={tt}>
            <div className="modal-row">
              <span className="modal-row-label">{tt('Monitor the connected server')}</span>
              <Toggle on={!!stg.watch_connection} onChange={(v) => apply({ watch_connection: v })} />
            </div>
            {hint('Checks that the connected server still passes traffic. Your own traffic is the first signal; the server is contacted only when the connection is idle or data is sent without a reply. How often an idle connection is checked and how many failed checks end it are the AUTO settings Health-check interval and Failure threshold (15 s and 2 by default). Local failures — the engine stopping or the tunnel adapter disappearing — are detected either way.', [
              whenOn('when the server stops passing traffic, Vair disconnects and shows a desktop notification. Not needed with AUTO: it performs the same checks and switches to another server instead of disconnecting.'),
              whenOff('Vair stays connected; the connection bar still marks a server that stops answering.', true),
            ])}
            </Dependent>
            {notifyBlocked && <div className="set-note" style={{ color: 'var(--red)' }}>{notifyBlocked}</div>}
            </Group>
          </div>

          {/* ── DPI bypass ── */}
          <div className="settings-section">
            <div className="section-header">{tt('DPI bypass')}</div>
            <Group>
            <div className="modal-row">
              <span className="modal-row-label">{tt('TLS fragmentation (DPI bypass)')}</span>
              <Toggle on={!!stg.tls_fragment} onChange={(v) => apply({ tls_fragment: v })} />
            </div>
            {hint('Splits the TLS handshake (ClientHello) into pieces so a DPI cannot match the connection in a single packet. Applies to configurations over TLS or REALITY (VLESS, VMess, Trojan, HTTPS proxies), including PING and SPEED tests; in a chain, to the first server only — the one your network sees. Hysteria2, TUIC and WireGuard run over UDP and are not affected. Configurations that run on sing-box (those with Allow insecure) are cut inside the server name, into two pieces: the length does not apply to them, and the pause is the upper value of the interval. Takes effect on the next connection.', [
              whenOn('the handshake is sent in pieces. Helps when the server is alive but the handshake is reset.'),
              whenOff('the handshake is sent whole.', true),
            ])}
            {!!stg.tls_fragment && (
              <div id="tlsfrag-deps">
                <div className="modal-row">
                  <span className="modal-row-label">{tt('Fragment length')}</span>
                  <input
                    className="modal-input num-input"
                    style={{ width: 110 }}
                    placeholder="100-200"
                    defaultValue={stg.tls_fragment_length || ''}
                    onBlur={(e) => {
                      const v = normFragment(e.target.value, 1, 1000)
                      setFragBad((b) => ({ ...b, length: v === null }))
                      if (v === null) return
                      e.target.value = v
                      apply({ tls_fragment_length: v })
                    }}
                  />
                </div>
                {fragBad.length && (
                  <div className="port-status" style={{ color: 'var(--red)' }}>
                    {tt('Not saved — enter a number from 1 to 1000 or a range such as 100-200.')}
                  </div>
                )}
                {hint('How many bytes go into each piece of the handshake: a number from 1 to 1000, or a range “min-max”. A size is drawn from the range for every piece, so no two handshakes are cut the same way.', [
                  opt('100-200', 'used when the field is empty.', true),
                  opt('Another value', 'a handshake is about 1700 bytes, so pieces close to that size leave it almost whole.'),
                ])}
                <div className="modal-row">
                  <span className="modal-row-label">{tt('Fragment interval (ms)')}</span>
                  <input
                    className="modal-input num-input"
                    style={{ width: 110 }}
                    placeholder="10-20"
                    defaultValue={stg.tls_fragment_interval || ''}
                    onBlur={(e) => {
                      const v = normFragment(e.target.value, 0, 1000)
                      setFragBad((b) => ({ ...b, interval: v === null }))
                      if (v === null) return
                      e.target.value = v
                      apply({ tls_fragment_interval: v })
                    }}
                  />
                </div>
                {fragBad.interval && (
                  <div className="port-status" style={{ color: 'var(--red)' }}>
                    {tt('Not saved — enter a number from 0 to 1000 or a range such as 10-20.')}
                  </div>
                )}
                {hint('The pause between the pieces of the handshake, in milliseconds: a number from 0 to 1000, or a range. A handshake has about a dozen pieces, so each new connection is delayed by about a dozen pauses.', [
                  opt('10-20', 'used when the field is empty.', true),
                  opt('Another value', 'longer pauses defeat more inspection and slow down every connection.'),
                ])}
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
            {hint('Which browser\'s TLS handshake is imitated (uTLS) when a configuration has no fp= of its own. Without it the connection uses the engine\'s own TLS signature, which is easy to tell apart. Applies to TLS and REALITY configurations on both engines and to HTTPS proxies. Takes effect on the next connection.', [
              opt('chrome', 'imitates Chrome.', true),
              opt('Another browser', 'firefox, safari, ios, android and edge imitate those browsers; 360 and qq are the 360 Secure and QQ browsers.'),
              opt('random', 'picks one of the browsers for each connection.'),
              opt('randomized', 'makes up a handshake that matches no browser — unique, which can itself stand out.'),
            ])}
            </Group>
          </div>

          {/* ── Testing ── */}
          <div className="settings-section">
            <div className="section-header">{tt('Testing')}</div>
            <Group title={tt('Parallelism and timeouts')} sub={tt('How many at once, how long to wait')}>
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
            {hint('How many configurations are pinged at the same time. Each test runs an engine of its own. Also used by AUTO when it checks the servers. Takes effect on the next test run.', [
              opt('10', '', true),
              opt('A higher value', 'tests a large tab faster but loads the processor and memory and shares your network link: results get less accurate, and on a weak computer configurations start to fail only because their engine did not start in time.'),
            ])}
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
            {hint('How many configurations are speed-tested at the same time. Also used by AUTO when it ranks servers by speed. Takes effect on the next test run.', [
              opt('5', '', true),
              opt('A higher value', 'parallel tests split your bandwidth, so each one measures only part of the real speed.'),
            ])}
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
            {hint('Time limit for the first, unmeasured request that sets up the connection — TCP plus the TLS or Reality handshake.', [
              opt('4000', '', true),
              opt('A higher value', 'use it if working configurations are reported as “timeout”.'),
            ])}
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
            {hint('Time limit for one ping round. Each configuration is pinged three times and the best result is shown.', [
              opt('1500', '', true),
            ])}
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
            {hint('How long the test download runs before the speed is calculated.', [
              opt('4', '', true),
              opt('A higher value', 'steadier results, but more traffic per configuration.'),
            ])}
            </Group>
            <Group title={tt('Addresses')} sub={tt('What the tests request')}>
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
                    {presetLabel(p.label, tt)}
                  </option>
                ))}
                <option value="__custom">{tt('Custom URL…')}</option>
              </Select>
            </div>
            {hint('The address the ping test requests. Any HTTP response counts, including an error — only the time until it arrives is measured.', [
              opt('www.gstatic.com/generate_204', '', true),
              opt('Another address', 'choose the one that responds fastest from your network.'),
            ])}
            {customPing && (
              <>
                <div className="modal-row row-cont" style={{ display: 'block', marginBottom: 12 }}>
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
                      const bad = !!v && !validTestURL(v)
                      setUrlBad((b) => ({ ...b, ping_test_url: bad }))
                      if (bad) return
                      if (!v) setCustomPing(false) // cleared → drop back to the default preset
                      apply({ ping_test_url: v })
                    }}
                  />
                </div>
                {urlBad.ping_test_url && (
                  <div className="port-status" style={{ color: 'var(--red)' }}>
                    {tt('Not saved — enter an address that starts with http:// or https://.')}
                  </div>
                )}
                {hint('An address of your own for the ping test. Emptying the field returns to the list above.')}
              </>
            )}
            <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
              <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                {tt('Check IP service')}
              </span>
              <Select
                className="modal-input"
                style={{ marginBottom: 0 }}
                value={customCheckIP ? '__custom' : stg.check_ip_url || ''}
                onChange={(e) => {
                  if (e.target.value === '__custom') {
                    setCustomCheckIP(true)
                    return
                  }
                  setCustomCheckIP(false)
                  apply({ check_ip_url: e.target.value })
                }}
              >
                {CHECKIP_PRESETS.map((p) => (
                  <option key={p.label} value={p.url}>
                    {presetLabel(p.label, tt)}
                  </option>
                ))}
                <option value="__custom">{tt('Custom URL…')}</option>
              </Select>
            </div>
            {hint('The service the check IP button uses to find your public address. In bypass routing modes this host always goes through the tunnel, so the result reflects the VPN connection.', [
              opt('ipwho.is', 'answers over HTTPS, so nobody on the way can read or alter the answer.', true),
              opt('ip-api.com', 'answers only over plain HTTP.'),
              opt('Another service', 'any service that answers with JSON works.'),
            ])}
            {customCheckIP && (
              <>
                <div className="modal-row row-cont" style={{ display: 'block', marginBottom: 12 }}>
                  <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                    {tt('Custom check IP URL')}
                  </span>
                  <input
                    className="modal-input"
                    style={{ marginBottom: 0 }}
                    placeholder="https://..."
                    defaultValue={isCustomURL(stg.check_ip_url || '', CHECKIP_PRESETS) ? stg.check_ip_url : ''}
                    onBlur={(e) => {
                      const v = e.target.value.trim()
                      const bad = !!v && !validTestURL(v)
                      setUrlBad((b) => ({ ...b, check_ip_url: bad }))
                      if (bad) return
                      if (!v) setCustomCheckIP(false)
                      apply({ check_ip_url: v })
                    }}
                  />
                </div>
                {urlBad.check_ip_url && (
                  <div className="port-status" style={{ color: 'var(--red)' }}>
                    {tt('Not saved — enter an address that starts with http:// or https://.')}
                  </div>
                )}
                {hint('A service of your own. It has to answer with JSON carrying the address; the country and the provider are read from it when they are there. Emptying the field returns to the list above.')}
              </>
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
                    {presetLabel(p.label, tt)}
                  </option>
                ))}
                <option value="__custom">{tt('Custom URL…')}</option>
              </Select>
            </div>
            {hint('The file the speed test downloads. The test runs for the number of seconds set above however large the file is, so any big enough file will do — what matters is that the host itself is fast.', [
              opt('speed.cloudflare.com', '', true),
              opt('Another file', 'pick one from the list or enter your own address.'),
            ])}
            {customSpeed && (
              <>
                <div className="modal-row row-cont" style={{ display: 'block', marginBottom: 12 }}>
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
                      const bad = !!v && !validTestURL(v)
                      setUrlBad((b) => ({ ...b, speed_test_url: bad }))
                      if (bad) return
                      if (!v) setCustomSpeed(false) // cleared → drop back to the default preset
                      apply({ speed_test_url: v })
                    }}
                  />
                </div>
                {urlBad.speed_test_url && (
                  <div className="port-status" style={{ color: 'var(--red)' }}>
                    {tt('Not saved — enter an address that starts with http:// or https://.')}
                  </div>
                )}
                {hint('A file of your own for the speed test. Emptying the field returns to the list above.')}
              </>
            )}
            <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
              <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                {tt('Speed URL fallback')}{' '}
                <span style={{ color: 'var(--muted)', fontWeight: 400 }}>
                  {tt('(used when the main URL answers with one of the codes below)')}
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
                  if (url === CACHEFLY) lbl += ' ' + tt('(default)')
                  return (
                    <option key={url} value={url}>
                      {lbl}
                    </option>
                  )
                })}
                <option value="__custom">{tt('Custom URL…')}</option>
              </Select>
            </div>
            {hint('A second file for the speed test, used when the main one answers with one of the HTTP codes set below. Public test files rate-limit a whole tab being measured one configuration after another.', [
              opt('cachefly.cachefly.net', '', true),
              opt('None — no fallback', 'no retry: those configurations are simply reported without a speed.'),
            ])}
            {customFb && (
              <>
                <div className="modal-row row-cont" style={{ display: 'block', marginBottom: 12 }}>
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
                    onBlur={(e) => {
                      const v = e.target.value.trim()
                      const bad = !!v && !validTestURL(v)
                      setUrlBad((b) => ({ ...b, speed_test_url_fallback: bad }))
                      if (bad) return
                      // Cleared: back to the list, like the other address fields — the
                      // field stayed on "Custom URL…" while the default file was in use.
                      if (!v) setCustomFb(false)
                      apply({ speed_test_url_fallback: v })
                    }}
                  />
                </div>
                {urlBad.speed_test_url_fallback && (
                  <div className="port-status" style={{ color: 'var(--red)' }}>
                    {tt('Not saved — enter an address that starts with http:// or https://.')}
                  </div>
                )}
                {hint('A file of your own for the retry — best on a different host from the main one, or it will be rate-limited at the same moment.')}
              </>
            )}
            {stg.speed_test_url_fallback !== '__none' && (
              <>
                <div className="modal-row" style={{ display: 'block', marginBottom: 12 }}>
                  <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                    {tt('Use the fallback on these HTTP codes')}
                  </span>
                  <input
                    className="modal-input"
                    style={{ marginBottom: 0 }}
                    placeholder="429"
                    defaultValue={stg.speed_fallback_on || ''}
                    onChange={(e) => setFbCodes(e.target.value)}
                    onBlur={(e) => apply({ speed_fallback_on: e.target.value.trim() })}
                  />
                </div>
                {badStatusTokens(fbCodes).length > 0 && (
                  <div className="port-status" style={{ color: 'var(--red)' }}>
                    {tt('Not an HTTP status code, ignored:')} {badStatusTokens(fbCodes).join(', ')}
                  </div>
                )}
                {hint('HTTP status codes from the main file that switch the test to the fallback file: single codes (429), ranges (500-599) or classes (5xx), separated by commas. Only answers from the file\'s server count: when the connection itself fails, the configuration is at fault and a second file would not help.', [
                  opt('429', 'the code public test files answer with when they limit the rate.', true),
                ])}
              </>
            )}
            </Group>
          </div>

          {/* ── Remote access ── */}
          <div className="settings-section">
            <div className="section-header">{tt('Remote access')}</div>
            <Group>
            <div className="modal-row">
              <span className="modal-row-label">{tt('Web UI')}</span>
              <Toggle on={!!stg.remote_enabled} onChange={(v) => apply({ remote_enabled: v })} />
            </div>
            {hint('A small web server on this computer, so the same interface can be opened in a browser on another device — a phone, a tablet or another computer. Access requires a secret key, included in the link and the QR code below; the key gives the same control as the window itself, including reading your configurations, their passwords and the connection history.', [
              whenOn('the server runs. The connection uses plain HTTP and is not encrypted: everything, including the key, crosses the local network in the clear, and anyone who can read that traffic can control Vair. Windows lets other devices in only on networks it treats as private; on a network marked public the link does not open from another device. Use it only on a trusted network.'),
              whenOff('the server does not run — keep it so when you do not need it.', true),
            ])}
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
            {stg.remote_enabled && remote?.problem && (
              <div className="port-status" style={{ color: 'var(--red)' }}>
                {/^port \d+ is taken/.test(remote.problem)
                  ? tt('The web interface is not running: the port is taken by another program. Choose another port.')
                  : tt('The web interface is not running; the log says why.')}
              </div>
            )}
            {hint('The port the web interface listens on.', [
              opt('19876', '', true),
              opt('Another port', 'if something else on this computer already listens on 19876.'),
            ])}
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
              <>
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
                {hint('This computer has several addresses; the link and the QR code below are built from the one you pick. Choose the address on the network the other device is on — the server itself listens on all of them.')}
              </>
            )}
            {stg.remote_enabled && remoteURL && (
              <div className="modal-row" style={{ display: 'block' }}>
                <span className="modal-row-label" style={{ display: 'block', marginBottom: 4 }}>
                  {tt('Link and QR code')}
                </span>
                <div
                  style={{ display: 'flex', gap: 8, alignItems: 'center', margin: '0 0 10px', flexWrap: 'wrap' }}
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
                <Hint
                  style={{ marginTop: 10 }}
                  en={'The address to open on the other device, with the key inside it — treat the link and the QR code as you would a password. "New key" issues a different one, and every link or code handed out before stops working: that is how a device you no longer trust is cut off.'}
                />
              </div>
            )}
            </Group>
          </div>

          {/* ── Appearance ── */}
          <div className="settings-section">
            <div className="section-header">{tt('Appearance')}</div>
            <Group>
            {/* Theme and language are in the title bar of this dialog, above.
                The line stays so that a search for either word lands here and
                says where they went — the alternative is a section that used to
                have them and now silently does not. */}
            <div className="set-note">
              {tt('Theme and Language are at the top of this window, on the right — always visible, whichever section is open.')}
            </div>
            <div className="modal-row">
              <span className="modal-row-label">{tt('Settings font size (px)')}</span>
              <input
                className="modal-input num-input"
                type="number"
                min={9}
                max={20}
                defaultValue={stg.modal_font_size || 12}
                onBlur={numBlur('modal_font_size', 12)}
              />
            </div>
            {hint('The text size in this window and the other dialogs — tab settings, AUTO, connections, the service check. From 9 to 20 px.', [
              opt('12', '', true),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Interface font size (px)')}</span>
              <input
                className="modal-input num-input"
                type="number"
                min={10}
                max={18}
                defaultValue={stg.app_font_size || 13}
                onBlur={numBlur('app_font_size', 13)}
              />
            </div>
            {hint('The text size in the main window — toolbar, tabs, table, connection bar and log panel; buttons, fields and table rows grow with it. From 10 to 18 px — narrower than for dialogs, because the table has to fit across the window: with larger text, fewer columns fit.', [
              opt('13', '', true),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Show setting descriptions')}</span>
              <Toggle on={!stg.hide_hints} onChange={(v) => apply({ hide_hints: !v })} />
            </div>
            {/* Deliberately still shown with descriptions off — it is the way back. */}
            <div className="set-note">
              {tt('The explanatory text under each setting, here and in the other dialogs — tab settings, auto-connect, measurement history. Off by default: an i mark next to a setting\'s name opens its description. Turn it on to show every description under its setting.')}
            </div>
            </Group>
          </div>

          {/* ── Statistics ── */}
          <div className="settings-section">
            <div className="section-header">{tt('Statistics')}</div>
            <Group>
            <div className="modal-row">
              <span className="modal-row-label">{tt('Enable traffic statistics')}</span>
              <Toggle on={!stg.stats_disabled} onChange={(v) => apply({ stats_disabled: !v })} />
            </div>
            {hint('Counts the bytes crossing the tunnel and shows them in the main window. Some connections that run inside sing-box alone (Hysteria2, TUIC, WireGuard, configurations without a certificate check) are not counted, and the main window says so.', [
              whenOn('the counters run.', true),
              whenOff('the counters stop where they are and nothing is recorded; traffic that passes while it is off is not counted after it is turned back on either.'),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">
                {tt('Lifetime total')}: ↑{fmtBytes(stg.stats_total_up || 0)} ↓
                {fmtBytes(stg.stats_total_down || 0)}
              </span>
              <button
                className="btn ghost"
                onClick={() => {
                  SettingsService.ResetStats().then(refresh)
                }}
              >
                {tt('reset total')}
              </button>
            </div>
            {hint('Everything that has gone through the tunnel since this counter was last reset, kept across restarts. "reset total" puts it back to zero; the session counter beside it zeroes itself on every connection.')}
            </Group>
          </div>

          {/* ── System ── */}
          <div className="settings-section">
            <div className="section-header">{tt('System')}</div>
            {/* The Windows helper service — there or not, running or not, and
                the one thing to do about it. Linux has its own helper flow, on
                the TUN switch, so the row is Windows-only (caps say so). */}
            {svcState && (
              <Group title={tt('Helper service')} sub={tt('What lets Vair work without administrator rights')}>
                <div className="modal-row">
                  <span className="modal-row-label">
                    {tt('Status')}:{' '}
                    {svcState === 'running'
                      ? tt('running')
                      : svcState === 'stopped'
                        ? tt('stopped')
                        : svcState === 'missing'
                          ? tt('missing')
                          : tt('not installed')}
                  </span>
                  {/* Repair is for a copy that comes with the service: a portable
                      one has nothing to repair, and asking from a phone would put
                      the prompt on a screen nobody is looking at. */}
                  {!IS_REMOTE &&
                    svcState !== 'absent' &&
                    (svcState !== 'running' || caps?.tun_block_reason === 'no_service') && (
                      <button className="btn" disabled={svcBusy} onClick={repairService}>
                        {svcBusy ? tt('repairing…') : tt('Repair')}
                      </button>
                    )}
                </div>
                {/* Said in the open, not behind the description mark: a portable
                    copy has no way to get the service itself, and the row above
                    reading "not installed" with nothing to press looked broken. */}
                {caps?.portable && (
                  <div className="set-note">
                    {tt('Only the setup version of Vair installs the service. This portable copy uses it when Vair is also installed on this computer; without it, TUN mode needs Vair running as administrator.')}
                  </div>
                )}
                {hint('A small system service that creates the tunnel, holds the kill-switch filters and opens the firewall rules, so Vair itself runs without administrator rights. The setup program installs it. Repair starts a stopped service without asking, and reinstalls a missing or damaged one after one administrator prompt. A portable copy works without the service: TUN mode then needs Vair running as administrator.')}
              </Group>
            )}
            <Group title={tt('Window and startup')} sub={tt('How Vair starts and closes')}>
            {/* Hidden where there is no tray to minimize INTO: turning it on
                there would hide the window with nothing left to reopen it from,
                while the app keeps running and holding the system proxy. */}
            {caps?.tray !== false && (
              <>
                <div className="modal-row">
                  <span className="modal-row-label">{tt('Minimize to tray on close')}</span>
                  <Toggle on={!!stg.tray_enabled} onChange={(v) => apply({ tray_enabled: v })} />
                </div>
                {hint('What the window\'s close button does. You can always quit from the tray menu.', [
                  whenOn('it hides Vair in the tray, and the connection carries on.'),
                  whenOff('it disconnects and quits.', true),
                ])}
              </>
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
                {hint('Starts Vair when you log in, minimized to the tray.', [
                  whenOn('on Windows Vair starts with your ordinary rights. That is enough for TUN mode when the Vair helper service is installed, as the setup program does. A portable copy without the service needs administrator rights for TUN: a TUN connection that “Restore the connection on start” was to bring back does not come up then, and the connection bar says so.'),
                  whenOff('Vair starts only when you open it.', true),
                ])}
              </>
            )}
            {caps?.deep_link_toggle !== false && (
              <>
                <div className="modal-row">
                  <span className="modal-row-label">{tt('Handle vair:// links')}</span>
                  <Toggle on={stg.deep_link_enabled !== false} onChange={(v) => apply({ deep_link_enabled: v })} />
                </div>
                {hint('Registers the vair:// link scheme in the system.', [
                  whenOn('clicking a vair://import/… link — in a browser or Telegram, say — opens Vair and offers to add the subscription or configuration: Vair shows what the link contains and the tab it goes to, and adds it only after you confirm.', true),
                  whenOff('such links do not open Vair.'),
                ])}
              </>
            )}
            </Group>
            <Group title={tt('Logs')} sub={tt('What is recorded, and where')}>
            <div className="modal-row">
              <span className="modal-row-label">{tt('Verbose logs')}</span>
              <Toggle on={!!stg.verbose_logs} onChange={(v) => apply({ verbose_logs: v })} />
            </div>
            {hint('How much detail the engines write to the log. Takes effect on the next connection.', [
              whenOn('xray and sing-box log at level info: their own steps for every connection — which route it took, which name was recognised.'),
              whenOff('only the essentials; the address of every connection is logged either way.', true),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Log speed/ping tests')}</span>
              <Toggle on={!!stg.log_tests} onChange={(v) => apply({ log_tests: v })} />
            </div>
            {hint('Whether ping and speed test results go to the log.', [
              whenOn('each result is logged together with the full engine output during the test, so you can see why a configuration is unavailable.'),
              whenOff('test results are not logged — bulk tests would fill the log.', true),
            ])}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Save logs to a file')}</span>
              <Toggle on={!!stg.log_to_file} onChange={(v) => apply({ log_to_file: v })} />
            </div>
            {hint('Copies the Logs panel to a file. The in-app log is kept in memory only and starts empty every launch.', [
              whenOn('the log survives a restart or a crash. The file is rotated at 5 MiB with one old copy kept (about 10 MiB at most). The log names the address of every connection, so the file records which sites you visited — with verbose logs and without them. Share it only if you are fine with that.'),
              whenOff('nothing is written to disk.', true),
            ])}
            {logPath && <div className="set-note log-path">{logPath}</div>}
            </Group>
          </div>

          {/* ── Data ── */}
          <div className="settings-section">
            <div className="section-header">{tt('Data')}</div>
            <Group>
            {/* These open something on the COMPUTER — a folder window, a file
                to save, a file to choose. Asked for from a phone they appear to
                do nothing at all, because what they opened is on a screen the
                person holding the phone is not looking at. */}
            {!IS_REMOTE && (
              <>
                <div className="modal-row">
                  <span className="modal-row-label">{tt('Storage location')}</span>
                  <button className="btn ghost" onClick={() => SettingsService.OpenDataFolder()}>
                    {tt('Open folder')}
                  </button>
                </div>
                {hint('Opens the folder holding the whole state of the program — settings, tabs, measurement history, logs and the downloaded routing lists. Copy it to move Vair by hand, or open it to read a log.')}
                <div className="modal-row">
                  <span className="modal-row-label">{tt('Settings backup')}</span>
                  <span style={{ display: 'inline-flex', gap: 6 }}>
                    <button
                      className="btn ghost"
                      disabled={!!bk?.running}
                      onClick={() =>
                        SettingsService.Export().then((res) => {
                          // Said in the main window too: the settings may have
                          // been closed while a long export ran.
                          if (res === '') notify(tt('Settings exported'))
                          else if (res !== 'cancelled') notify(res)
                        })
                      }
                    >
                      {tt('Export')}
                    </button>
                    <button
                      className="btn ghost"
                      disabled={!!bk?.running}
                      onClick={() =>
                        SettingsService.Import(importTabs).then((res) => {
                          if (res === '') {
                            refresh()
                            notify(tt('Settings imported'))
                          } else if (res !== 'cancelled') notify(res)
                        })
                      }
                    >
                      {tt('Import')}
                    </button>
                  </span>
                </div>
                {/* How far an export or an import has got, and how it ended:
                    not a description, so it stays with descriptions hidden. */}
                {bk?.running && (
                  <div className="set-note bk-prog">
                    <span>
                      {bk.phase === 'read'
                        ? tt('Reading the file…')
                        : (bk.phase === 'parse'
                            ? tt('Reading configurations:')
                            : bk.phase === 'save'
                              ? tt('Saving configurations:')
                              : tt('Exporting configurations:')) +
                          ' ' +
                          bk.done +
                          ' / ' +
                          bk.total}
                    </span>
                    <div className="upd-bar">
                      <div className="upd-fill" style={{ width: backupPercent(bk) + '%' }} />
                    </div>
                  </div>
                )}
                {!bk?.running && bk?.path && (
                  <div className="set-note bk-done">
                    {tt(bk.op === 'import' ? 'Imported from' : 'Exported to')} <span className="bk-path">{bk.path}</span>
                  </div>
                )}
                {!bk?.running && bk?.error && (
                  <div className="set-note bk-fail">
                    {tt(bk.op === 'import' ? 'Import failed:' : 'Export failed:')} {bk.error}
                  </div>
                )}
                {hint('Export writes the tabs, their settings and the app settings to a JSON file. Import reads one back and replaces what is there now, ending the current connection first — the way to move Vair to another computer. The file holds in the clear your configurations, the local proxy passwords, GitHub access tokens and the remote-access key, so keep it as you would keep them.')}
                <div className="modal-row">
                  <span className="modal-row-label">{tt('Import tabs and tab settings')}</span>
                  <Toggle on={importTabs} onChange={setImportTabs} />
                </div>
                {hint('What Import takes from the file.', [
                  whenOn('everything: the tabs, their settings and the app settings replace yours.', true),
                  whenOff('only the app settings; your tabs are left alone — for carrying settings over without touching the configurations you already have.'),
                ])}
              </>
            )}
            <div className="modal-row">
              <span className="modal-row-label">{tt('Favorites')}</span>
              <button className="btn ghost" onClick={() => setFavOpen(true)}>
                {tt('Show list')}
              </button>
            </div>
            {hint('All starred configurations, including those no longer in any tab: a star is attached to the server itself and is kept even after its subscription changes. Nothing is removed automatically; servers that cannot be found are marked and can be removed on request.')}
            </Group>
          </div>

          {/* ── Updates ── */}
          <div className="settings-section">
            <div className="section-header">{tt('Updates')}</div>
            <Group>
            <div className="modal-row">
              <span className="modal-row-label">
                Vair <b>{version}</b>
              </span>
              <button className="btn ghost" disabled={updBusy} onClick={checkUpdate}>
                {tt('Check for updates')}
              </button>
            </div>
            {/* The result of a check is not a description: it stays on screen with
                descriptions hidden, where the paragraph it replaces does not. */}
            {updMsg ? <div className="set-note">{updMsg}</div> : hint('Checks for a newer version and installs it. The list of versions is signed by the author and the download is checked against the checksum in it before it replaces the app, so a file changed on the way is refused. Vair also checks by itself once per start, through the tunnel of the first connection. This button checks now: through the tunnel when connected, otherwise directly.')}
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
                      openLink(updLatest.download_url || 'https://github.com/lm705/vair/releases')
                    }
                  >
                    {updLatest.download_url ? tt('Download') : tt('Open releases')}
                  </button>
                )}
              </div>
            )}
            {updLatest && caps?.self_update === 'inplace' && (
              <div className="set-note">
                {tt('Update now fetches that build — through the tunnel when you are connected — checks it against the published checksum, puts it in place of this program and restarts it. Settings, tabs and history stay where they are.')}
              </div>
            )}
            {updLatest && caps?.self_update === 'package' && (
              <div className="set-note">
                {tt('This copy was installed from a package — update it through your package manager.')}
              </div>
            )}
            {updLatest && caps?.self_update === 'appimage' && (
              <div className="set-note">
                {tt('This copy runs as an AppImage — download the new one and replace this file.')}
              </div>
            )}
            {updLatest && caps?.self_update === 'installer' && (
              <div className="set-note">
                {tt('This copy was installed by the setup program, which updates the program and its engines together. Download the setup program of the new version and run it; your settings and tabs stay. An installation for all users asks for administrator rights.')}
              </div>
            )}
            </Group>
          </div>
          </div>
        </div>

        {/* The credit, centred. The dialog closes from the ✕ in its title bar. */}
        <div className="set-foot">
          <div className="upd-attrib">
            Vair · by <a onClick={() => openLink('https://github.com/lm705/vair')}>lm705</a>
          </div>
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
            <div className="modal-head">
              <div className="modal-title">{tt('Running processes')}</div>
              <CloseX onClick={() => setProcOpen(false)} title={tt('Close')} />
            </div>
            <input
              className="modal-input"
              placeholder={tt('filter…')}
              autoFocus
              value={procFilter}
              onChange={(e) => setProcFilter(e.target.value)}
            />
            <div className="set-note" style={{ marginTop: 0 }}>
              {tt('Click a process to add it to the Apps without VPN list.')}
            </div>
            <div
              style={{
                maxHeight: 300,
                overflowY: 'auto',
                border: '1px solid var(--border2)',
                borderRadius: 6,
                background: 'var(--bg2)',
                padding: 4,
              }}
            >
              {(() => {
                const f = procFilter.trim().toLowerCase()
                const existing = new Set(((stg.direct_apps || []) as string[]).map((s) => s.toLowerCase()))
                const shown = procList.filter((n) => !f || n.toLowerCase().indexOf(f) >= 0).slice(0, 500)
                if (shown.length === 0)
                  return (
                    <div style={{ padding: 8, color: 'var(--dim)', fontSize: 'var(--modal-fs-base)', textAlign: 'center' }}>
                      {procList.length === 0
                        ? tt('No process list available — only works in the desktop build.')
                        : tt('No matches')}
                    </div>
                  )
                return shown.map((n) => {
                  const already = existing.has(n.toLowerCase())
                  return (
                    <div
                      key={n}
                      style={{
                        padding: '5px 8px',
                        cursor: 'pointer',
                        fontSize: 'var(--modal-fs-base)',
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
            </div>
          </div>
        </div>
      )}

      {favOpen && <FavoritesModal onClose={() => setFavOpen(false)} lang={lang} />}
      {blockNotice && (
        <KeepBlockNotice
          lang={lang}
          service={!!svcState && svcState !== 'absent'}
          onClose={() => setBlockNotice(false)}
        />
      )}
    </div>
    </RevealSetting.Provider>
  )
}
