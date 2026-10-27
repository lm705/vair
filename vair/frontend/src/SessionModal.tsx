// What the running session is actually made of.
//
// The settings dialog answers "what will happen": nearly everything in it
// applies from the next connection. This answers "what is happening" — the
// values the engine was started with, read back from the moment it started.
// Where a setting has been changed since, the line says so, because that gap is
// the answer to most of "why is it behaving like that".
//
// Deliberately its own file with one insertion point (the mark on the
// connection bar): the whole thing can be taken out again by reverting one
// commit.
import { useEffect, useState } from 'react'
import { ConnService } from '../bindings/vair/internal/services'
import { t10 } from './i18n'
import CloseX from './closex'

// tt0 marks a string for translation where it is defined; tt() translates it
// where it is shown.
const tt0 = (en: string) => en

type Row = { label: string; value: string; now?: string; note?: string }
type Info = { connected: boolean; groups: string[]; rows: Row[][] }

// The backend speaks in fixed words so it never has to know a language; the
// dialog turns them into the one on screen.
//
// The notes under a row are the same kind of fixed phrase. Listed here so the
// translation check sees them — they reach tt() as values, not literals.
export const SESSION_NOTES = [
  tt0('The check runs a few seconds after the connection comes up.'),
  tt0('Test packets and DNS queries sent from the physical network adapter, around the tunnel, did not get out: the protection holds traffic in.'),
  tt0('DNS queries sent from the physical network adapter, around the tunnel, got no answer: names are looked up only through the tunnel. Other traffic is not held in, because the kill-switch is off.'),
  tt0('A DNS query sent from the physical network adapter, around the tunnel, was answered: names can be looked up outside the tunnel, where the provider sees them. The log names the adapter and the resolver.'),
  tt0('A test packet sent from the physical network adapter, around the tunnel, was answered: traffic can leave outside the tunnel. The log names the adapter.'),
  tt0('The check could not run; the log says why.'),
  tt0('This system has no way to send the test packets.'),
  tt0('Proxy mode without its kill-switch does not hold other programs in: those not set to use the proxy go out directly by design, so there is nothing to test. Turn on the proxy kill-switch to have it checked.'),
  tt0('Without TUN DNS leak protection the names of the sites behind the tunnel go to the system resolver, outside it, by design, so there is nothing to test. Turn DNS leak protection on to have it checked.'),
  tt0('The hops of the chain differ; this is the weakest of them.'),
  tt0('Server certificate'),
  // Proxy-mode rows and the kill-switch note (core session_api).
  tt0('Protection'),
  tt0('Kill-switch (proxy)'),
  tt0('Sites through the proxy'),
  tt0('Sites outside the proxy'),
  tt0('On in the settings, but it works only together with TUN DNS leak protection.'),
]

const WORD: Record<string, string> = {
  on: 'on',
  off: 'off',
  // Not the routing rules' "proxy", which reads "through VPN" in Russian.
  proxy: 'Proxy',
  TUN: 'TUN',
  'not connected': 'not connected',
  present: 'present',
  proxy_all: 'All traffic through VPN',
  bypass_countries: 'Everything except selected countries',
  only_blocked: 'Only blocked-in-Russia resources',
  only_blocked_cn: 'Only blocked-in-China resources',
  only_selected: 'Only selected sites',
  gvisor: 'gVisor',
  system: 'system',
  mixed: 'mixed',
  blocked: 'Blocked',
  tunnel: 'Through the VPN',
  direct: 'Direct — outside the VPN',
  // The block and the leak check. The block's three words were missing, and
  // the Russian panel showed them in English.
  blocking: 'blocking',
  'not blocking': 'not blocking',
  'everything outside the tunnel': 'everything outside the tunnel',
  'everything outside the proxy': 'everything outside the proxy',
  'blocking, but not in force': 'blocking, but not in force',
  'not run yet': 'not run yet',
  passed: 'passed',
  'LEAK — traffic gets out': 'LEAK — traffic gets out',
  'does not apply': 'does not apply',
  'could not run': 'could not run',
  'not available on this system': 'not available on this system',
  // Where a session's names went when it was not one of the DNS fields.
  'system resolver': 'system resolver',
  'system resolver, outside the tunnel': 'system resolver, outside the tunnel',
  // How the server's certificate is checked (core certCheckLabel).
  checked: 'checked',
  'pinned (SHA-256)': 'pinned (SHA-256)',
  'not checked (allowInsecure)': 'not checked (allowInsecure)',
  'REALITY key': 'REALITY key',
  'checked against the name in the link': 'checked against the name in the link',
  'the VPN server': 'the VPN server',
}

export default function SessionModal({ onClose, lang }: { onClose: () => void; lang: string }) {
  const tt = (en: string) => t10(lang, en)
  const say = (v: string) => (WORD[v] ? tt(WORD[v]) : v)
  const [info, setInfo] = useState<Info | null>(null)

  useEffect(() => {
    const load = () => ConnService.SessionInfo().then((r: any) => setInfo(r))
    load()
    // The uptime and the counters are elsewhere; what is here changes only when
    // a connection starts or a setting is touched. A slow refresh keeps the
    // "settings say" column honest without polling for its own sake.
    const t = window.setInterval(load, 5000)
    return () => window.clearInterval(t)
  }, [])

  return (
    <div className="modal-overlay" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal-box sess-box">
        <div className="modal-head">
          <div className="modal-title">{tt('Session')}</div>
          <CloseX onClick={onClose} title={tt('Close')} />
        </div>
        {!info ? (
          <div className="hist-say">{tt('Loading…')}</div>
        ) : (
          <>
            {!info.connected && (
              <div className="sess-say">
                {tt('Nothing is connected, so this is what the settings would produce.')}
              </div>
            )}
            {info.groups.map((g, i) => (
              <div key={g}>
                <div className="hist-cap">{tt(g)}</div>
                {(info.rows[i] || []).map((r) => (
                  <div className="hist-row" key={r.label}>
                    <span className="hist-label">{tt(r.label)}</span>
                    <span className="hist-value">{say(r.value)}</span>
                    {r.now ? (
                      <span className="sess-changed">
                        {tt('settings now say')} {say(r.now)}
                      </span>
                    ) : null}
                    {r.note ? <span className="sess-note">{tt(r.note)}</span> : null}
                  </div>
                ))}
              </div>
            ))}
          </>
        )}
      </div>
    </div>
  )
}
