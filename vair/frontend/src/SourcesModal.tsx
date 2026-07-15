// Sources (main tab) settings modal — an exact port of the 1.10
// openSourcesSettings() DOM: read-only built-in URLs (copy/QR), auto-refresh
// interval + test-after, exclude filter. Live apply debounced 250ms.
import { useEffect, useRef, useState } from 'react'
import { TabService } from '../bindings/vair'
import { t10 } from './i18n'
import {
  ExcludeFields,
  SubInfoSection,
  emptyExInput,
  emptyExMap,
  mapToRules,
  rulesToMap,
  type ExInput,
  type ExMap,
} from './tabshared'

export default function SourcesModal({
  onClose,
  onShowQR,
  lang,
}: {
  onClose: () => void
  onShowQR: (text: string) => void
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
      <div className="modal-box" id="tab-modal-box">
        <div className="modal-title">{tt('Sources Settings')}</div>

        <SubInfoSection subs={tab.subs} lang={lang} />

        {/* ── Built-in source URLs (read-only, copy/QR) ── */}
        <div className="settings-section">
          <div className="modal-label">{tt('Source URL (read-only)')}</div>
          {srcUrls === null ? (
            <div className="modal-hint" style={{ margin: 0 }}>
              {tt('Loading…')}
            </div>
          ) : srcUrls.length === 0 ? (
            <div className="modal-hint" style={{ margin: 0 }}>
              {tt('No source URL.')}
            </div>
          ) : (
            <div id="src-url-list">
              {srcUrls.map((u) => (
                <div key={u} className="src-url-row">
                  <span className="src-url" title={u}>
                    {u}
                  </span>
                  <button className="url-qr" onClick={() => navigator.clipboard.writeText(u).catch(() => {})}>
                    {tt('copy')}
                  </button>
                  <button className="url-qr" onClick={() => onShowQR(u)}>
                    {tt('QR')}
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>

        {/* ── Auto-refresh ── */}
        <div className="settings-section">
          <div className="modal-row" style={{ marginBottom: 6 }}>
            <span className="modal-label" style={{ margin: 0 }}>
              {tt('Auto-refresh interval (minutes, 0 = off)')}
            </span>
            <label className="toggle">
              <input
                type="checkbox"
                checked={!refreshDisabled}
                onChange={(e) => {
                  setRefreshDisabled(!e.target.checked)
                  scheduleApply()
                }}
              />
              <span className="toggle-track" />
              <span className="toggle-thumb" />
            </label>
          </div>
          <input
            className="modal-input"
            type="number"
            min={0}
            value={refreshMin}
            style={{ width: 80, opacity: refreshDisabled ? 0.45 : 1 }}
            onChange={(e) => setRefreshMin(+e.target.value || 0)}
            onBlur={scheduleApply}
          />
          <div className="modal-hint">
            {tt('On a set interval: tabs with a source/URL reload the latest config list; pasted-only tabs (no source) just clear stale ping/speed results so they get re-tested.')}
          </div>
          <div className="modal-row" style={{ marginTop: 8 }}>
            <span className="modal-row-label">{tt('Test after auto-refresh')}</span>
            <select
              className="modal-input num-input"
              style={{ width: 'auto', marginBottom: 0 }}
              value={refreshTest}
              onChange={(e) => {
                setRefreshTest(e.target.value)
                scheduleApply()
              }}
            >
              <option value="">{tt('off')}</option>
              <option value="ping">{tt('Ping only')}</option>
              <option value="speed">{tt('Speed test')}</option>
            </select>
          </div>
          <div className="modal-hint">
            {tt("After a scheduled auto-refresh (not a manual RELOAD), test the tab's configs in the background — ping only, or a full speed test.")}
          </div>
        </div>

        {/* ── Exclude filter ── */}
        <div className="settings-section">
          <div className="modal-row" style={{ marginBottom: 6 }}>
            <span className="modal-label" style={{ margin: 0 }}>
              {tt('Exclude filter')}
            </span>
            <label className="toggle">
              <input
                type="checkbox"
                checked={!excludeDisabled}
                onChange={(e) => {
                  setExcludeDisabled(!e.target.checked)
                  scheduleApply()
                }}
              />
              <span className="toggle-track" />
              <span className="toggle-thumb" />
            </label>
          </div>
          <div className="modal-hint ef-hint">
            {tt('Configs matching any of these are hidden. Type a value and press Enter to add it; matching is a case-insensitive substring. Leave a column empty to disable it.')}
          </div>
          <ExcludeFields
            ef={ef}
            efInput={efInput}
            disabled={excludeDisabled}
            setEf={setEf}
            setEfInput={setEfInput}
            onChanged={scheduleApply}
          />
        </div>

        <div className="modal-hint" style={{ marginBottom: 8 }}>
          {tt('Changes apply automatically.')}
        </div>
        <div className="modal-btns">
          <button className="btn ghost" onClick={closeModal}>
            {tt('Close')}
          </button>
        </div>
      </div>
    </div>
  )
}
