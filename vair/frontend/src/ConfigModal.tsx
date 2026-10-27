import { useEffect, useRef, useState } from 'react'
import { ConfigService } from '../bindings/vair/internal/services'
import type { ConfigForm } from '../bindings/vair/core'
import { t10 } from './i18n'
import { Hint } from './hints'
import type { HintOpt } from './hints'
import Select from './Select'
import CloseX from './closex'
import Group from './setgroup'
import Toggle from './Toggle'
import ConfirmDiscard from './confirm'
import { formErrText } from './reasons'

type Mode = 'add' | 'edit' | 'view'

const PROTOCOLS = ['vless', 'vmess', 'trojan', 'ss', 'hysteria2', 'tuic', 'wireguard', 'socks', 'http', 'json']
const PROTO_LABEL: Record<string, string> = {
  vless: 'VLESS', vmess: 'VMess', trojan: 'Trojan', ss: 'Shadowsocks', hysteria2: 'Hysteria2', tuic: 'TUIC',
  wireguard: 'WireGuard', socks: 'SOCKS5', http: 'HTTP', json: 'JSON (Xray / sing-box)',
}
const FP_OPTS = ['', 'chrome', 'firefox', 'safari', 'ios', 'android', 'edge', '360', 'qq', 'random', 'randomized']
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
    // A plain proxy has no transport and no security of its own; the port is the
    // one thing that is almost always the same.
    // WireGuard configures an interface, so it needs an address of its own:
    // without one the tunnel comes up and carries nothing.
    case 'wireguard': return { port: 51820, addresses: '172.16.0.2/32', mtu: 1420 }
    case 'socks': return { security: 'none', port: 1080 }
    case 'http': return { security: 'none', port: 8080 }
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
  // The description under a setting: the paragraph while descriptions are
  // on, the mark that opens the same text while they are off.
  const hint = (en: string, opts?: HintOpt[]) => <Hint en={en} opts={opts} />
  const readOnly = mode === 'view'
  const [form, setForm] = useState<Record<string, any>>(() => ({
    protocol: 'vless', name: '', address: '', port: 443, ...protoDefaults('vless'),
  }))
  const [preview, setPreview] = useState('')
  // What a JSON config was recognised as — shown instead of its link.
  const [summary, setSummary] = useState('')
  const [err, setErr] = useState('')
  // Why an existing configuration could not be put into the form. Kept apart
  // from err, which the live preview rewrites on every change: it used to be
  // replaced at once by the preview's verdict on the empty default form, and
  // the dialog showed an empty VLESS form for a configuration it had failed to
  // read — one Save away from replacing it.
  const [loadErr, setLoadErr] = useState('')
  const [busy, setBusy] = useState(false)
  // What the form held when it opened (after the prefill), to tell whether
  // closing it throws anything away.
  const initial = useRef<string>(JSON.stringify(form))
  const [askClose, setAskClose] = useState(false)

  // Prefill from an existing config (edit / view).
  useEffect(() => {
    if ((mode === 'edit' || mode === 'view') && raw) {
      ConfigService.ConfigToForm(raw).then((res) => {
        if (res.error) setLoadErr(res.error)
        else {
          const f = { ...res.form } as Record<string, any>
          initial.current = JSON.stringify(f)
          setForm(f)
        }
      })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const dirty = !readOnly && JSON.stringify(form) !== initial.current
  // A click beside the form, or its ✕, used to close it and drop everything
  // typed. With changes on screen it asks first; Cancel is the explicit way out.
  const requestClose = () => (dirty ? setAskClose(true) : onClose())

  // Live share-link preview + validation (debounced).
  useEffect(() => {
    const h = setTimeout(() => {
      ConfigService.BuildConfigURL((form as ConfigForm)).then((res) => {
        setPreview(res.url || '')
        setSummary(res.summary || '')
        setErr(res.error || '')
      })
    }, 200)
    return () => clearTimeout(h)
  }, [form])

  const set = (k: string, v: any) => setForm((f) => ({ ...f, [k]: v }))
  const changeProtocol = (p: string) => setForm((f) => ({ ...f, protocol: p, ...protoDefaults(p) }))
  const isJSON = form.protocol === 'json'

  const submit = async () => {
    setBusy(true)
    let e = ''
    if (mode === 'add') e = await ConfigService.AddConfig(tabId, (form as ConfigForm))
    // raw: the configuration the form was opened on, so a refresh of the tab's
    // source while it was open cannot send the edit to another row.
    else if (mode === 'edit' && idx != null) e = await ConfigService.UpdateConfig(tabId, idx, raw || '', (form as ConfigForm))
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
  const sel = (k: string, opts: string[], labels?: Record<string, string>) => {
    const v = form[k] ?? opts[0]
    const all = v != null && !opts.includes(v) ? [...opts, v] : opts
    return (
      <Select className="modal-input" style={iStyle} value={v}
        disabled={readOnly} onChange={(e) => set(k, e.target.value)}>
        {all.map((o) => <option key={o} value={o}>{labels?.[o] ?? (o === '' ? tt('(default)') : o)}</option>)}
      </Select>
    )
  }
  // A setting that is on or off is a switch, as everywhere else; a view of a
  // configuration shows it without letting it change.
  const tog = (k: string) => (
    <span className={readOnly ? 'toggle-wrap off-limits' : 'toggle-wrap'} inert={readOnly}>
      <Toggle on={!!form[k]} onChange={(v) => { if (!readOnly) set(k, v) }} />
    </span>
  )

  const p = form.protocol
  const hasStream = p === 'vless' || p === 'vmess' || p === 'trojan'
  const isProxy = p === 'socks' || p === 'http'
  const isWG = p === 'wireguard'
  const net = form.network
  const isReality = p === 'vless' && form.security === 'reality'
  const title = mode === 'add' ? 'Add configuration' : mode === 'edit' ? 'Edit configuration' : 'View configuration'

  return (
    <div className="modal-overlay" onClick={requestClose}>
      <div className="modal-box cardset" style={{ width: 520, maxWidth: '92vw', maxHeight: '88vh', overflowY: 'auto' }}
        onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div className="modal-title">{tt(title)}</div>
          <CloseX onClick={requestClose} title={tt('Close')} />
        </div>

        <Group title={isJSON ? undefined : tt('Basic')}>
        {row('Protocol', mode === 'add'
          ? (
            <Select className="modal-input" style={iStyle} value={p} onChange={(e) => changeProtocol(e.target.value)}>
              {PROTOCOLS.map((o) => <option key={o} value={o}>{PROTO_LABEL[o]}</option>)}
            </Select>
          )
          : <span className="modal-input" style={{ ...iStyle, opacity: 0.8 }}>{PROTO_LABEL[p] || p}</span>)}

        {isJSON && row('Name', txt('name', tt('(from the configuration)')))}
        {!isJSON && (
          <>
        {row('Name', txt('name', '🇩🇪 Frankfurt-01'))}
        {row('Address', txt('address', 'example.com'))}
        {row('Port', num('port'))}
        {(p === 'vless' || p === 'vmess' || p === 'tuic') && row('UUID', txt('uuid'))}
        {(p === 'trojan' || p === 'ss' || p === 'hysteria2' || p === 'tuic') && row('Password', txt('password'))}
        {isWG && row('Private key', txt('uuid', 'base64, 32 bytes'))}
        {isWG && row("Peer's public key", txt('peer_public_key', 'base64, 32 bytes'))}
        {isWG && row('Pre-shared key', txt('pre_shared_key'))}
        {isWG && row('Interface address', txt('addresses', '172.16.0.2/32'))}
        {isWG && row('MTU', num('mtu'))}
        {isWG && row('Reserved', txt('reserved', '1,2,3'))}
        {isWG && hint('WireGuard does not report configuration errors: with a wrong key, a missing interface address or wrong reserved bytes, the tunnel comes up but carries no traffic.')}
        {isProxy && row('User name', txt('uuid'))}
        {isProxy && row('Password', txt('password'))}
        {p === 'ss' && row('Encryption method', sel('method', SS_METHODS))}
        {p === 'ss' && row('Plugin', txt('ss_plugin', 'obfs-local;obfs=tls;obfs-host=example.com'))}
          </>
        )}
        </Group>

        {isJSON ? (
          <Group title="JSON">
            <div className="modal-row" style={{ display: 'block' }}>
            <textarea
              className="modal-input"
              spellCheck={false}
              readOnly={readOnly}
              value={form.json ?? ''}
              placeholder={'{ "protocol": "vless", "settings": { … }, "streamSettings": { … } }'}
              onChange={(e) => set('json', e.target.value)}
              style={{
                width: '100%', minHeight: 240, resize: 'vertical', marginBottom: 0,
                fontFamily: 'var(--font-mono, monospace)', fontSize: 'var(--modal-fs-base)', lineHeight: 1.45,
                whiteSpace: 'pre', overflowX: 'auto',
                // The app turns text selection off everywhere; a JSON box has to be
                // selectable to be edited or copied.
                userSelect: 'text', WebkitUserSelect: 'text',
              }}
            />
            </div>
            {hint('An outbound for Xray (it has "protocol") or for sing-box (it has "type"), or a whole configuration with one server in it. It has to go to a VPN server: outbounds that send traffic straight out, and ones that connect through another outbound, are not accepted. To add every server from a file at once, paste the whole text with Paste configs.')}
          </Group>
        ) : (
          <>

        {hasStream && (
          <Group title={tt('Transport')}>
            {row('Network', sel('network', ['tcp', 'ws', 'grpc', 'http', 'httpupgrade', 'xhttp', 'kcp']))}
            {(net === 'ws' || net === 'http') && row('Path', txt('path', '/path'))}
            {(net === 'ws' || net === 'http') && row('Host header', txt('host_header', 'cdn.example.com'))}
            {net === 'grpc' && row('Service name', txt('service_name'))}
            {net === 'grpc' && row('gRPC mode', sel('grpc_mode', ['', 'gun', 'multi']))}
            {(net === 'httpupgrade' || net === 'xhttp') && row('Path', txt('path', '/path'))}
            {(net === 'httpupgrade' || net === 'xhttp') && row('Host header', txt('host_header', 'cdn.example.com'))}
            {net === 'xhttp' && row('xhttp mode', sel('grpc_mode', ['', 'auto', 'packet-up', 'stream-up', 'stream-one']))}
            {net === 'kcp' && row('mKCP header', sel('header_type_kcp', ['none', 'srtp', 'utp', 'wechat-video', 'dtls', 'wireguard']))}
            {net === 'kcp' && row('mKCP seed', txt('seed'))}
            {(net === 'xhttp' || net === 'kcp') && hint('This transport is xray-only: a configuration using it connects through xray in both modes, and cannot be an intermediate hop of a sing-box chain.')}
            {p === 'vmess' && net === 'tcp' && row('Header type', sel('header_type', ['none', 'http']))}
          </Group>
        )}

        {hasStream && (
          <Group title={tt('Security')}>
            {row('Security', sel('security', p === 'vless' ? ['none', 'tls', 'reality'] : ['none', 'tls']))}
            {form.security !== 'none' && (
              <>
                {row('SNI', txt('sni', 'example.com'))}
                {row('ALPN', txt('alpn', 'h2,http/1.1'))}
                {row('Fingerprint', sel('fingerprint', FP_OPTS))}
                {row('Allow insecure', tog('allow_insecure'))}
                {form.security === 'tls' && (p === 'vless' || p === 'trojan') && (
                  <>
                    {row('Certificate SHA-256', txt('pinned_cert'))}
                    {hint("Allow insecure turns off the check of the server's certificate. xray no longer allows that, so such a configuration runs on sing-box. Certificate SHA-256 (pcs= in links) is the safer way to reach a server with a self-signed certificate: the connection is accepted only from a server that presents exactly that certificate. 64 hexadecimal characters; several, separated by commas.")}
                  </>
                )}
              </>
            )}
            {isReality && (
              <>
                {row('Public key', txt('public_key'))}
                {row('Short ID', txt('short_id'))}
                {row('SpiderX', txt('spider_x', '/'))}
                {row('Flow', sel('flow', ['', 'xtls-rprx-vision']))}
              </>
            )}
          </Group>
        )}

        {isProxy && (
          <Group title={tt('Security')}>
            {row('Security', sel('security', ['none', 'tls']))}
            {form.security === 'tls' && (
              <>
                {row('SNI', txt('sni', 'example.com'))}
                {row('Allow insecure', tog('allow_insecure'))}
              </>
            )}
            {hint('A plain proxy, with no encryption of its own: TLS here only wraps the connection to the proxy itself. Everything it carries is as protected as the protocol inside it.')}
          </Group>
        )}

        {p === 'vmess' && (
          <Group title="VMess">
            {row('AlterID', num('alter_id'))}
            {row('Encryption', sel('encryption', ['auto', 'none', 'aes-128-gcm', 'chacha20-poly1305']))}
          </Group>
        )}

        {(p === 'hysteria2' || p === 'tuic') && (
          <Group title={tt('Security')}>
            {row('SNI', txt('sni', 'example.com'))}
            {row('ALPN', txt('alpn', 'h3'))}
            {row('Allow insecure', tog('allow_insecure'))}
          </Group>
        )}

        {p === 'hysteria2' && (
          <>
            <Group title={tt('Bandwidth and ports')}>
            {row('Upload (Mbps)', txt('up_mbps', '50'))}
            {row('Download (Mbps)', txt('down_mbps', '200'))}
            {row('Port hopping', txt('port_hopping', '443-450'))}
            {row('Certificate pin', txt('pin_sha256'))}
            </Group>
            <Group title={tt('Obfuscation')}>
            {row('Obfs type', sel('obfs_type', ['', 'salamander']))}
            {form.obfs_type && row('Obfs password', txt('obfs_password'))}
            </Group>
          </>
        )}

        {p === 'tuic' && (
          <Group title="TUIC">
            {row('Congestion control', sel('congestion_control', ['', 'bbr', 'cubic', 'new_reno']))}
            {row('UDP relay mode', sel('udp_relay_mode', ['', 'native', 'quic']))}
          </Group>
        )}

          </>
        )}

        <Group title={tt(isJSON ? 'Recognised as' : 'Share link')} bare>
        <div style={{
          background: 'var(--bg)', border: '1px solid var(--border2)', borderRadius: 6, padding: '8px 10px',
          fontSize: 'var(--modal-fs-base)', lineHeight: 1.5, color: err || loadErr ? 'var(--red)' : 'var(--green)', wordBreak: 'break-all',
          maxHeight: 90, overflowY: 'auto',
          // The app disables text selection globally; let this preview be selected
          // so the generated link can be copied with the mouse.
          userSelect: 'text', WebkitUserSelect: 'text', cursor: 'text',
        }}>
          {loadErr
            ? tt('This configuration cannot be shown in the form') + ': ' + formErrText(loadErr, tt)
            : err ? formErrText(err, tt) : ((isJSON ? summary : preview) || '—')}
        </div>
        </Group>

        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 14 }}>
          {!readOnly && <button className="btn ghost" onClick={onClose}>{tt('Cancel')}</button>}
          {!readOnly && (
            <button
              className="btn primary"
              disabled={busy || !preview || !!loadErr}
              onClick={submit}
            >
              {tt(mode === 'add' ? 'Add config' : 'Save')}
            </button>
          )}
        </div>
      </div>
      {askClose && (
        <ConfirmDiscard
          text={tt('Close the form? What you have entered will be lost.')}
          discardLabel={tt('Close without saving')}
          keepLabel={tt('Keep editing')}
          onDiscard={() => {
            setAskClose(false)
            onClose()
          }}
          onKeep={() => setAskClose(false)}
        />
      )}
    </div>
  )
}
