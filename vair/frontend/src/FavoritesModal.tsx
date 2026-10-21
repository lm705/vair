// The starred configurations, all of them.
//
// A star is stored in the settings file keyed by the node body, so it survives
// a rename, a refresh and a move between tabs. The consequence nobody arranged:
// it also survives the configuration. A server that left the subscription a
// year ago is still starred, and there is no row anywhere for it to show on —
// the list only ever grew, and nothing in the program showed it.
//
// Nothing here expires on its own. A source that failed to load looks exactly
// like a server that is gone, and a star is something the user set by hand, so
// the program says which ones it cannot find and leaves the decision alone.
import { useEffect, useState } from 'react'
import { ConfigService } from '../bindings/vair/internal/services'
import { t10 } from './i18n'
import { copyText } from './clipboard'

type Fav = {
  body: string
  raw: string
  name: string
  host: string
  proto: string
  tab: string
  present: boolean
}

export default function FavoritesModal({ onClose, lang }: { onClose: () => void; lang: string }) {
  const tt = (en: string) => t10(lang, en)
  const [list, setList] = useState<Fav[] | null>(null)
  // Which row was copied last, so the button can say so for a moment. The whole
  // point of the list is to look at configurations there is no row for any
  // more — the only way to get one back out of it is to copy it.
  const [copied, setCopied] = useState('')

  const load = () => ConfigService.Favorites().then((r: any) => setList(r || []))
  useEffect(() => {
    load()
  }, [])

  const remove = (bodies: string[]) => {
    if (bodies.length === 0) return
    ConfigService.RemoveFavorites(bodies).then(load)
  }

  const copy = (f: Fav) => {
    copyText(f.raw || f.body)
    setCopied(f.body)
    setTimeout(() => setCopied((c) => (c === f.body ? '' : c)), 1200)
  }

  const copyAll = () => {
    const all = (list || []).map((f) => f.raw || f.body).join('\n')
    if (!all) return
    copyText(all)
    setCopied('*')
    setTimeout(() => setCopied((c) => (c === '*' ? '' : c)), 1200)
  }

  const missing = (list || []).filter((f) => !f.present)

  return (
    <div className="modal-overlay" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal-box fav-box">
        <div className="modal-title">{tt('Favorites')}</div>
        <div className="hist-hint hist-para">
          {tt('A star is kept against the server itself, so it survives a rename and a list refresh — and it outlives the configuration too. What no tab holds right now is marked below; a source that failed to load looks the same, so nothing is removed on its own.')}
        </div>

        {!list ? (
          <div className="hist-say">{tt('Loading…')}</div>
        ) : list.length === 0 ? (
          <div className="hist-say">{tt('Nothing is starred.')}</div>
        ) : (
          <div className="fav-list">
            {list.map((f) => (
              <div className={'fav-row' + (f.present ? '' : ' gone')} key={f.body}>
                <span className="fav-name">{f.name || f.host || f.body}</span>
                <span className="fav-where">
                  {f.present ? f.tab : tt('in no tab')}
                  {f.present && f.host ? ' · ' + f.host : ''}
                </span>
                <button
                  className="btn ghost sm"
                  title={tt('Copy the configuration')}
                  onClick={() => copy(f)}
                >
                  {copied === f.body ? tt('copied') : tt('copy')}
                </button>
                <button
                  className="btn ghost sm"
                  title={tt('Remove from favorites')}
                  onClick={() => remove([f.body])}
                >
                  ✕
                </button>
              </div>
            ))}
          </div>
        )}

        <div className="modal-btns">
          {list && list.length > 0 ? (
            <button className="btn ghost" onClick={copyAll}>
              {copied === '*' ? tt('copied') : tt('Copy all')}
            </button>
          ) : null}
          {missing.length > 0 ? (
            <button className="btn ghost" onClick={() => remove(missing.map((f) => f.body))}>
              {tt('Remove the')} {missing.length} {tt('not found')}
            </button>
          ) : null}
          <button className="btn ghost" onClick={onClose}>
            {tt('Close')}
          </button>
        </div>
      </div>
    </div>
  )
}
