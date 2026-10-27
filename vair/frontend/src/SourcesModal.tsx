// Sources (main tab) settings modal — an exact port of the 1.10
// openSourcesSettings() DOM: read-only built-in URLs (copy/QR), auto-refresh
// interval + test-after, exclude filter. Live apply debounced 250ms.
import { useEffect, useRef, useState } from 'react'
import { TabService } from '../bindings/vair/internal/services'
import { t10 } from './i18n'
import LangSelect from './langselect'
import Group from './setgroup'
import {
  ExcludeGroup,
  RefreshGroup,
  SubInfoSection,
  emptyExInput,
  emptyExMap,
  mapToRules,
  rulesToMap,
  type ExInput,
  type ExMap,
} from './tabshared'
import CloseX from './closex'
import { copyText } from './clipboard'

export default function SourcesModal({
  onClose,
  onShowQR,
  notify,
  lang,
}: {
  onClose: () => void
  onShowQR: (text: string) => void
  notify: (text: string) => void
  lang: string
}) {
  const tt = (en: string) => t10(lang, en)
  const [tab, setTab] = useState<any>(null)
  const [srcUrls, setSrcUrls] = useState<string[] | null>(null) // null = loading
  const [refreshMin, setRefreshMin] = useState(0)
  const [refreshDisabled, setRefreshDisabled] = useState(false)
  const [refreshTest, setRefreshTest] = useState('')
  const [excludeDisabled, setExcludeDisabled] = useState(false)
  const [ef, setEf] = useState<ExMap>(emptyExMap())
  const [efInput, setEfInput] = useState<ExInput>(emptyExInput())
  const loadedRef = useRef(false)
  const applyTimer = useRef<number>(0)
  const stateRef = useRef<any>(null)
  stateRef.current = { refreshMin, refreshDisabled, refreshTest, excludeDisabled, ef, efInput }

  useEffect(() => {
    TabService.Detail('main').then((t: any) => {
      setTab(t)
      setRefreshMin(t.refresh_min || 0)
      setRefreshDisabled(!!t.refresh_disabled)
      setRefreshTest(t.auto_refresh_test || '')
      setExcludeDisabled(!!t.exclude_disabled)
      setEf(rulesToMap(t.exclude_filter))
      loadedRef.current = true
    })
    TabService.SourcesInfo().then((urls) => setSrcUrls(urls ?? []))
    return () => {
      if (applyTimer.current) window.clearTimeout(applyTimer.current)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const flushApply = () => {
    const s = stateRef.current
    if (!s) return
    // SetTabSettings ignores source fields for the main tab (IsMain guard) —
    // only refresh/exclude/test-after apply, exactly like 1.10's
    // applySourcesSettings payload.
    TabService.SetSettings('main', {
      urls: [],
      disabled_urls: [],
      files: [],
      refresh_min: s.refreshMin || 0,
      exclude_filter: mapToRules(s.ef, s.efInput),
      dedup_mode: '',
      refresh_disabled: s.refreshDisabled,
      exclude_disabled: s.excludeDisabled,
      auto_refresh_test: s.refreshTest,
      github_enabled: false,
      github_owner: '',
      github_repo: '',
      github_file: '',
      github_pat: '',
    } as any)
  }
  const scheduleApply = () => {
    if (!loadedRef.current) return
    if (applyTimer.current) window.clearTimeout(applyTimer.current)
    applyTimer.current = window.setTimeout(() => {
      applyTimer.current = 0
      flushApply()
    }, 250)
  }
  const closeModal = () => {
    if (applyTimer.current) {
      window.clearTimeout(applyTimer.current)
      applyTimer.current = 0
    }
    if (loadedRef.current) flushApply()
    onClose()
  }

  if (!tab) return null

  return (
    <div
      className="modal-overlay"
      id="tab-modal"
      onClick={(e) => {
        if (e.target === e.currentTarget) closeModal()
      }}
    >
      <div className="modal-box cardset" id="tab-modal-box">
        <div className="modal-title set-title">
          <span>{tt('Sources Settings')}</span>
          <div className="set-title-controls">
            <LangSelect lang={lang} />
            <CloseX onClick={closeModal} title={tt('Close')} />
          </div>
        </div>

        <SubInfoSection subs={tab.subs} lang={lang} />

        {/* ── Built-in source URLs (read-only, copy/QR) ── */}
        <Group title={tt('Config sources')}>
          <div className="modal-row" style={{ display: 'block' }}>
            <span className="modal-row-label stack">{tt('Source URL (read-only)')}</span>
            {srcUrls === null ? (
              <div className="set-note" style={{ margin: 0 }}>
                {tt('Loading…')}
              </div>
            ) : srcUrls.length === 0 ? (
              <div className="set-note" style={{ margin: 0 }}>
                {tt('No source URL.')}
              </div>
            ) : (
              <div id="src-url-list">
                {srcUrls.map((u) => (
                  <div key={u} className="src-url-row">
                    <span className="src-url" title={u}>
                      {u}
                    </span>
                    {/* The shared copy, which falls back where the browser has no
                        clipboard API (the web interface over plain HTTP); the
                        button used to do nothing there, and say nothing here. */}
                    <button
                      className="url-qr"
                      onClick={() => {
                        copyText(u)
                        notify(tt('Copied'))
                      }}
                    >
                      {tt('copy')}
                    </button>
                    <button className="url-qr" title={tt('QR code')} onClick={() => onShowQR(u)}>
                      {tt('QR')}
                    </button>
                  </div>
                ))}
              </div>
            )}
          </div>
        </Group>

        <RefreshGroup
          lang={lang}
          min={refreshMin}
          off={refreshDisabled}
          test={refreshTest}
          setMin={setRefreshMin}
          setOff={setRefreshDisabled}
          setTest={setRefreshTest}
          onChanged={scheduleApply}
        />

        <ExcludeGroup
          lang={lang}
          off={excludeDisabled}
          setOff={setExcludeDisabled}
          ef={ef}
          efInput={efInput}
          setEf={setEf}
          setEfInput={setEfInput}
          onChanged={scheduleApply}
        />

        <div className="set-note" style={{ margin: '14px 0 0' }}>
          {tt('Changes apply automatically.')}
        </div>
      </div>
    </div>
  )
}
