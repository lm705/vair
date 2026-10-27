// The warning that has to be read before the block is allowed to outlive the
// program.
//
// It used to be a paragraph under the setting, in the same grey as every other
// paragraph, and it was the one description whose consequence lands on a
// machine rather than on a session: turn this on, close Vair, and the computer
// has no internet at all until someone gives it back. Nobody reads a grey
// paragraph while flipping a switch. So it is a window now, in front of the
// dialog, with a close button that will not work for the first few seconds —
// long enough that the eyes have to pass over the text on the way to it.
//
// The delay is charged once. Someone who has read it and turns the setting off
// and on again gets the same window and can close it immediately: the point was
// to make them read it, not to punish them for using the switch. "Do not show
// again" ends it for good, and is deliberately the smaller of the two buttons.
import { useEffect, useState } from 'react'
import { t10 } from './i18n'

const SUPPRESS = 'killblock.notice.off'
const READ = 'killblock.notice.read'
const DELAY = 8

// keepBlockNoticeWanted answers whether turning the switch on should raise the
// window at all. Read by the settings dialog, so the state lives in one place.
export function keepBlockNoticeWanted(): boolean {
  return localStorage.getItem(SUPPRESS) !== '1'
}

// service: the Windows helper service is installed, and takes the block down
// for an ordinary user — no administrator needed for the command line either.
export default function KeepBlockNotice({
  lang,
  service,
  onClose,
}: {
  lang: string
  service?: boolean
  onClose: () => void
}) {
  const [cur, setCur] = useState(lang === 'ru' ? 'ru' : 'en')
  // Read once already? Then the text is not new and the wait is not owed.
  const [left, setLeft] = useState(() => (localStorage.getItem(READ) === '1' ? 0 : DELAY))
  useEffect(() => {
    if (left <= 0) {
      localStorage.setItem(READ, '1')
      return
    }
    const id = setTimeout(() => setLeft((n) => n - 1), 1000)
    return () => clearTimeout(id)
  }, [left])
  // No Escape hatch on purpose while the delay runs — a keypress is exactly the
  // reflex this is here to interrupt.
  useEffect(() => {
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && left <= 0) onClose()
    }
    window.addEventListener('keydown', esc)
    return () => window.removeEventListener('keydown', esc)
  }, [left, onClose])

  const tt = (s: string) => t10(cur, s)
  const ready = left <= 0
  return (
    <div className="kb-veil">
      <div className="kb-box" role="alertdialog" aria-modal="true">
        <div className="kb-head">
          <span className="kb-title">{tt('Persistent kill-switch')}</span>
          <span className="bgroup">
            <button className={'hint-lang' + (cur === 'en' ? ' on' : '')} onClick={() => setCur('en')}>
              EN
            </button>
            <button className={'hint-lang' + (cur === 'ru' ? ' on' : '')} onClick={() => setCur('ru')}>
              RU
            </button>
          </span>
        </div>
        <div className="kb-body">
          <p>
            {tt(
              'With this setting on, the block does not depend on Vair running. It stays in place after Vair is closed or crashes, and after Windows restarts.',
            )}
          </p>
          <p>
            {tt(
              'While the block is in place, this computer has no internet access except through the VPN tunnel. Nothing opens outside it — not slowly, not partially.',
            )}
          </p>
          <p>
            {tt('Only you can lift it:')}
          </p>
          <ul>
            <li>{tt('disconnect, or turn this setting off;')}</li>
            <li>{tt('press the red "traffic blocked ✕" button on the connection bar;')}</li>
            <li>
              {service
                ? tt('without the Vair window — from its folder:')
                : tt('without the Vair window — from its folder, as administrator:')}{' '}
              <code>Vair.exe --release-block</code>
            </li>
            <li>
              {tt('if Vair has been removed — in Command Prompt, as administrator:')}{' '}
              <code>powershell -ExecutionPolicy Bypass -File "%LOCALAPPDATA%\vair\data\release-block.ps1"</code>
            </li>
          </ul>
          <p className="kb-calm">
            {tt(
              'To have the tunnel come back by itself after a restart, also turn on Launch at system startup and Restore the connection on start: Vair will reconnect under the block. Otherwise the computer stays offline until you connect or lift the block.',
            )}
          </p>
        </div>
        <div className="kb-foot">
          <button
            className="kb-never"
            onClick={() => {
              localStorage.setItem(SUPPRESS, '1')
              localStorage.setItem(READ, '1')
              onClose()
            }}
          >
            {tt('Do not show again')}
          </button>
          <button className="btn red kb-ok" disabled={!ready} onClick={onClose}>
            {ready ? tt('I understand') : tt('I understand') + ' (' + left + ')'}
          </button>
        </div>
      </div>
    </div>
  )
}
