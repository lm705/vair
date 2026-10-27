// What a vair:// link wants to add, shown before anything is added.
//
// A link is opened by a web page, a message, a QR code — by someone else. It
// used to be imported the moment it arrived: a page could put its own server
// into the open tab, and if that tab was one AUTO chooses from, the next switch
// could route everything through it. Now the link says what it holds and where
// it would go, and nothing happens until the user agrees.
import { t10 } from './i18n'
import CloseX from './closex'

// summary is what the link holds, in a form a person can check: the
// subscription addresses as they are, or how many configurations and the first
// few names.
function summary(payload: string): { subs: string[]; configs: number; names: string[]; json: boolean } {
  const lines = payload
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean)
  const subs = lines.filter((l) => /^https?:\/\//i.test(l)).map((l) => l.split(/\s+/)[0])
  const nodes = lines.filter((l) => /^[a-z0-9-]+:\/\//i.test(l) && !/^https?:\/\//i.test(l))
  const names = nodes.slice(0, 3).map((l) => {
    const i = l.indexOf('#')
    if (i < 0) return l.split('://')[0] + '://…'
    try {
      return decodeURIComponent(l.slice(i + 1)) || l.split('://')[0] + '://…'
    } catch {
      return l.slice(i + 1)
    }
  })
  return { subs, configs: nodes.length, names, json: /^\s*[[{]/.test(payload) }
}

export default function LinkConfirm({
  payload,
  lang,
  tabName,
  onAdd,
  onCancel,
}: {
  payload: string
  lang: string
  tabName: string
  onAdd: () => void
  onCancel: () => void
}) {
  const tt = (en: string) => t10(lang, en)
  const s = summary(payload)
  return (
    <div className="modal-overlay" onClick={(e) => e.target === e.currentTarget && onCancel()}>
      <div className="modal-box link-confirm">
        <div className="modal-head">
          <div className="modal-title">{tt('Add from a link?')}</div>
          <CloseX onClick={onCancel} title={tt('Cancel')} />
        </div>
        <div className="set-note link-confirm-what">
          {s.subs.length > 0 ? (
            <>
              <div>{tt('A subscription — its configurations will be loaded from:')}</div>
              {s.subs.map((u) => (
                <div key={u} className="link-confirm-url">
                  {u}
                </div>
              ))}
            </>
          ) : s.json ? (
            <div>{tt('A JSON configuration.')}</div>
          ) : (
            <>
              <div>
                {tt('Configurations:')} {s.configs}
              </div>
              {s.names.map((n, i) => (
                <div key={i} className="link-confirm-url">
                  {n}
                </div>
              ))}
            </>
          )}
          <div className="link-confirm-where">
            {tt('They will be added to:')} <b>{tabName}</b>
          </div>
        </div>
        {/* Not a modal-hint: those go away with the descriptions, and this one must not. */}
        <div className="link-confirm-warn">
          {tt('A configuration decides which server your traffic goes through. Add links only from sources you trust: a server you do not know can read and change unencrypted traffic and sees every site you visit.')}
        </div>
        <div className="modal-btns">
          <button className="btn ghost" onClick={onCancel}>
            {tt('Cancel')}
          </button>
          <button className="btn primary" onClick={onAdd}>
            {tt('Add')}
          </button>
        </div>
      </div>
    </div>
  )
}
