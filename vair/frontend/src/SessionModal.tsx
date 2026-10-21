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

type Row = { label: string; value: string; now?: string }
type Info = { connected: boolean; groups: string[]; rows: Row[][] }

// The backend speaks in fixed words so it never has to know a language; the
// dialog turns them into the one on screen.
const WORD: Record<string, string> = {
  on: 'on',
  off: 'off',
  proxy: 'proxy',
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
  direct: 'Direct (outside the tunnel)',
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
        <div className="modal-title">{tt('Session')}</div>
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
                  </div>
                ))}
              </div>
            ))}
          </>
        )}
        <div className="modal-btns">
          <button className="btn ghost" onClick={onClose}>
            {tt('Close')}
          </button>
        </div>
      </div>
    </div>
  )
}
