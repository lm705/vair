// The startup notice of 2.7.2: the Telegram channel, the coming 3.0.0 test
// build and where to write about it. Shown at every start until "Don't show
// again"; closing it only hides it until the next start. It opens in the
// program's language and has the language picker the other dialogs have.
//
// 2.7.2 only — 3.0.0 does not carry it. Everything it needs is here, plus one
// settings field (core/startup_notice.go) and the few lines in App.tsx that
// render it, so leaving it out is deleting those.
import { t10 } from './i18n'
import LangSelect from './langselect'
import { openLink } from './remote'

const CHANNEL_URL = 'https://t.me/vair705'
const DIRECT_URL = 'https://t.me/mial705'
const CHAT_URL = 'https://t.me/vair705chat'

export default function StartupNotice({
  lang,
  onClose,
  onDismiss,
}: {
  lang: string
  onClose: () => void
  onDismiss: () => void
}) {
  const tt = (en: string) => t10(lang, en)
  const link = (url: string) => (
    <a className="notice-link" onClick={() => openLink(url)}>
      {url.replace('https://', '')}
    </a>
  )
  return (
    <div className="modal-overlay" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal-box startup-notice">
        <div className="modal-title set-title">
          <span>{tt('Vair has a Telegram channel')}</span>
          {/* No ✕ here: the Close button below is the one way out (Esc and a
              click beside the box do the same, see dialogkeys.ts). */}
          <div className="set-title-controls">
            <LangSelect lang={lang} />
          </div>
        </div>
        <div className="notice-body">
          <p className="notice-channel">{link(CHANNEL_URL)}</p>
          <p>{tt('News and new versions come out there. Version 3.0.0 is coming soon — with zapret (DPI bypass) and more.')}</p>
          <p>{tt('3.0.0 needs testers. Its test build will be published in the channel: anyone will be able to download it and try it.')}</p>
          <p className="notice-where">{tt('If you can, write what works and what does not:')}</p>
          <ul className="notice-list">
            <li>
              {tt('in a private message')} — {link(DIRECT_URL)}
            </li>
            <li>
              {tt('in the discussion chat')} — {link(CHAT_URL)}
            </li>
          </ul>
          <p className="notice-optional">
            {tt('Your system, provider and city help to find the cause of a problem, but they are optional — share only what you want to.')}
          </p>
        </div>
        <div className="modal-btns">
          <button className="btn ghost" onClick={onDismiss}>
            {tt("Don't show again")}
          </button>
          <button className="btn primary" onClick={onClose}>
            {tt('Close')}
          </button>
        </div>
      </div>
    </div>
  )
}
