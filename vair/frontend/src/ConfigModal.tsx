import { useEffect, useState } from 'react'
import { ConfigService } from '../bindings/vair'
import type { ConfigForm } from '../bindings/vair/core'
import { t10 } from './i18n'

type Mode = 'add' | 'edit' | 'view'

const PROTOCOLS = ['vless', 'vmess', 'trojan', 'ss', 'hysteria2', 'tuic']
const PROTO_LABEL: Record<string, string> = {
  vless: 'VLESS', vmess: 'VMess', trojan: 'Trojan', ss: 'Shadowsocks', hysteria2: 'Hysteria2', tuic: 'TUIC',
}
const FP_OPTS = ['', 'chrome', 'firefox', 'safari', 'ios', 'android', 'edge', 'random', 'randomized']
const SS_METHODS = [
  'aes-256-gcm', 'aes-128-gcm', 'chacha20-ietf-poly1305', 'chacha20-poly1305',
  '2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm', '2022-blake3-chacha20-poly1305',
]

// protoDefaults seeds the fields a freshly-picked protocol needs so the preview
// isn't stuck on a "required" error the moment you switch to it.
function protoDefaults(p: string): Record<string, any> {
  switch (p) {
    case 'vless': return { security: 'tls', network: 'tcp' }
    case 'vmess': return { security: 'tls', network: 'tcp', encryption: 'auto', header_type: 'none' }
    case 'trojan': return { security: 'tls', network: 'tcp' }
    case 'ss': return { method: 'aes-256-gcm' }
    default: return {}
  }
}

export default function ConfigModal({
  mode, tabId, idx, raw, onClose, onDone, lang, notify,
}: {
  mode: Mode
  tabId: string
  idx?: number
  raw?: string
  onClose: () => void
  onDone?: () => void
  lang: string
  notify: (t: string) => void
}) {
  const tt = (en: string) => t10(lang, en)
  const readOnly = mode === 'view'
  const [form, setForm] = useState<Record<string, any>>(() => ({
    protocol: 'vless', name: '', address: '', port: 443, ...protoDefaults('vless'),
  }))
  const [preview, setPreview] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  // Prefill from an existing config (edit / view).
  useEffect(() => {
    if ((mode === 'edit' || mode === 'view') && raw) {
      ConfigService.ConfigToForm(raw).then((res) => {
        if (res.error) setErr(res.error)
        else setForm({ ...res.form } as Record<string, any>)
      })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Live share-link preview + validation (debounced).
  useEffect(() => {
    const h = setTimeout(() => {
      ConfigService.BuildConfigURL((form as ConfigForm)).then((res) => {
        setPreview(res.url || '')
        setErr(res.error || '')
      })
    }, 200)
    return () => clearTimeout(h)
  }, [form])

  const set = (k: string, v: any) => setForm((f) => ({ ...f, [k]: v }))
  const changeProtocol = (p: string) => setForm((f) => ({ ...f, protocol: p, ...protoDefaults(p) }))

  const submit = async () => {
    setBusy(true)
    let e = ''
    if (mode === 'add') e = await ConfigService.AddConfig(tabId, (form as ConfigForm))
    else if (mode === 'edit' && idx != null) e = await ConfigService.UpdateConfig(tabId, idx, (form as ConfigForm))
    setBusy(false)
    if (e) { setErr(e); return }
    notify(tt(mode === 'add' ? 'Config added' : 'Config updated'))
    onDone?.()
    onClose()
  }

  // ── field render helpers (reuse the app's modal-row/input classes) ──
  const iStyle = { width: 'auto', marginBottom: 0 } as const
  const row = (label: string, node: React.ReactNode) => (
    <div className="modal-row"><span className="modal-row-label">{tt(label)}</span>{node}</div>
  )
  const txt = (k: string, ph = '') => (
    <input className="modal-input" style={iStyle} placeholder={ph} value={form[k] ?? ''}
      disabled={readOnly} onChange={(e) => set(k, e.target.value)} />
  )
  const num = (k: string) => (
    <input className="modal-input" type="number" min={1} max={65535} style={iStyle} value={form[k] ?? 0}
      disabled={readOnly} onChange={(e) => set(k, +e.target.value || 0)} />
  )
  const sel = (k: string, opts: string[], labels?: Record<string, string>) => (
    <select className="modal-input" style={iStyle} value={form[k] ?? opts[0]}
      disabled={readOnly} onChange={(e) => set(k, e.target.value)}>
      {opts.map((o) => <option key={o} value={o}>{labels ? labels[o] : (o === '' ? tt('(default)') : o)}</option>)}
    </select>
  )
  const chk = (k: string) => (
    <input type="checkbox" checked={!!form[k]} disabled={readOnly} onChange={(e) => set(k, e.target.checked)} />
  )

  const p = form.protocol
  const hasStream = p === 'vless' || p === 'vmess' || p === 'trojan'
  const net = form.network
  const isReality = p === 'vless' && form.security === 'reality'
  const title = mode === 'add' ? 'Add configuration' : mode === 'edit' ? 'Edit configuration' : 'View configuration'

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal-box" style={{ width: 520, maxWidth: '92vw', maxHeight: '88vh', overflowY: 'auto' }}
        onClick={(e) => e.stopPropagation()}>
        <div className="modal-title">{tt(title)}</div>

        {row('Protocol', mode === 'add'
          ? (
            <select className="modal-input" style={iStyle} value={p} onChange={(e) => changeProtocol(e.target.value)}>
              {PROTOCOLS.map((o) => <option key={o} value={o}>{PROTO_LABEL[o]}</option>)}
            </select>
          )
          : <span className="modal-input" style={{ ...iStyle, opacity: 0.8 }}>{PROTO_LABEL[p] || p}</span>)}

        <div className="section-header">{tt('Basic')}</div>
        {row('Name', txt('name', '🇩🇪 Frankfurt-01'))}
        {row('Address', txt('address', 'example.com'))}
        {row('Port', num('port'))}
        {(p === 'vless' || p === 'vmess' || p === 'tuic') && row('UUID', txt('uuid'))}
        {(p === 'trojan' || p === 'ss' || p === 'hysteria2' || p === 'tuic') && row('Password', txt('password'))}
        {p === 'ss' && row('Encryption method', sel('method', SS_METHODS))}

        {hasStream && (
          <>
            <div className="section-header">{tt('Transport')}</div>
            {row('Network', sel('network', ['tcp', 'ws', 'grpc', 'http']))}
            {(net === 'ws' || net === 'http') && row('Path', txt('path', '/path'))}
            {(net === 'ws' || net === 'http') && row('Host header', txt('host_header', 'cdn.example.com'))}
            {net === 'grpc' && row('Service name', txt('service_name'))}
            {p === 'vmess' && net === 'tcp' && row('Header type', sel('header_type', ['none', 'http']))}
          </>
        )}

        {hasStream && (
          <>
            <div className="section-header">{tt('Security')}</div>
            {row('Security', sel('security', p === 'vless' ? ['none', 'tls', 'reality'] : ['none', 'tls']))}
            {form.security !== 'none' && (
              <>
                {row('SNI', txt('sni', 'example.com'))}
                {row('ALPN', txt('alpn', 'h2,http/1.1'))}
                {row('Fingerprint', sel('fingerprint', FP_OPTS))}
                {row('Allow insecure', chk('allow_insecure'))}
              </>
            )}
            {isReality && (
              <>
                {row('Public key', txt('public_key'))}
                {row('Short ID', txt('short_id'))}
                {row('Flow', sel('flow', ['', 'xtls-rprx-vision']))}
              </>
            )}
          </>
        )}

        {p === 'vmess' && (
          <>
            <div className="section-header">VMess</div>
            {row('AlterID', num('alter_id'))}
            {row('Encryption', sel('encryption', ['auto', 'none', 'aes-128-gcm', 'chacha20-poly1305']))}
          </>
        )}

        {(p === 'hysteria2' || p === 'tuic') && (
          <>
            <div className="section-header">{tt('Security')}</div>
            {row('SNI', txt('sni', 'example.com'))}
            {row('ALPN', txt('alpn', 'h3'))}
            {row('Allow insecure', chk('allow_insecure'))}
          </>
        )}

        {p === 'hysteria2' && (
          <>
            <div className="section-header">{tt('Obfuscation')}</div>
            {row('Obfs type', sel('obfs_type', ['', 'salamander']))}
            {form.obfs_type && row('Obfs password', txt('obfs_password'))}
          </>
        )}

        {p === 'tuic' && (
          <>
            <div className="section-header">TUIC</div>
            {row('Congestion control', sel('congestion_control', ['', 'bbr', 'cubic', 'new_reno']))}
            {row('UDP relay mode', sel('udp_relay_mode', ['', 'native', 'quic']))}
          </>
        )}

        <div className="section-header">{tt('Share link')}</div>
        <div style={{
          background: 'var(--bg)', border: '1px solid var(--border2)', borderRadius: 4, padding: '8px 10px',
          fontSize: 11, lineHeight: 1.5, color: err ? 'var(--red)' : 'var(--green)', wordBreak: 'break-all',
          maxHeight: 90, overflowY: 'auto',
          // The app disables text selection globally; let this preview be selected
          // so the generated link can be copied with the mouse.
          userSelect: 'text', WebkitUserSelect: 'text', cursor: 'text',
        }}>
          {err ? err : (preview || '—')}
        </div>

        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 14 }}>
          <button className="btn ghost" onClick={onClose}>{tt(readOnly ? 'Close' : 'Cancel')}</button>
          {!readOnly && (
            <button
              className="btn ghost"
              style={{
                borderColor: 'var(--accent)',
                color: 'var(--accent)',
                opacity: busy || !preview ? 0.45 : 1,
                cursor: busy || !preview ? 'default' : 'pointer',
              }}
              disabled={busy || !preview}
              onClick={submit}
            >
              {tt(mode === 'add' ? 'Add config' : 'Save')}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
