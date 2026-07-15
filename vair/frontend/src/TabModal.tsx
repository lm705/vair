// Tab settings modal — an exact port of the 1.10 openTabSettings() DOM
// (same classes/sections, live apply debounced 250ms, no Save button).
import { useEffect, useRef, useState } from 'react'
import { TabService } from '../bindings/vair'
import { t10 } from './i18n'
import {
  ExcludeFields,
  SubInfoSection,
  emptyExInput,
  emptyExMap,
  fmtBytes,
  mapToRules,
  rulesToMap,
  type ExInput,
  type ExMap,
} from './tabshared'

type UrlRow = { url: string; disabled: boolean }
type FileRow = { name: string; path: string; size: number; mtime: number; disabled: boolean; isNew: boolean }

export default function TabModal({
  tabId,
  onClose,
  onShowQR,
  lang,
}: {
  tabId: string
  onClose: () => void
  onShowQR: (text: string) => void
  lang: string
}) {
  const tt = (en: string) => t10(lang, en)
  const [tab, setTab] = useState<any>(null) // full tab (Detail) — subs are read-only here
  const [name, setName] = useState('')
  const [urls, setUrls] = useState<UrlRow[]>([])
  const [files, setFiles] = useState<FileRow[]>([])
  const [ghEnabled, setGhEnabled] = useState(false)
  const [ghOwner, setGhOwner] = useState('')
  const [ghRepo, setGhRepo] = useState('')
  const [ghFile, setGhFile] = useState('')
  const [ghPat, setGhPat] = useState('')
  const [ghPatVisible, setGhPatVisible] = useState(false)
  const [refreshMin, setRefreshMin] = useState(0)
  const [refreshDisabled, setRefreshDisabled] = useState(false)
  const [refreshTest, setRefreshTest] = useState('')
  const [dedupMode, setDedupMode] = useState('')
  const [excludeDisabled, setExcludeDisabled] = useState(false)
  const [ef, setEf] = useState<ExMap>(emptyExMap())
  const [efInput, setEfInput] = useState<ExInput>(emptyExInput())
  const loadedRef = useRef(false)
  const origNameRef = useRef('')
  const applyTimer = useRef<number>(0)

  // Seed local state from the full tab.
  useEffect(() => {
    TabService.Detail(tabId).then((t: any) => {
      setTab(t)
      setName(t.name || '')
      origNameRef.current = t.name || ''
      let us: string[] = t.source_urls || []
      if (us.length === 0 && t.source_url) us = [t.source_url]
      const dis: string[] = t.source_disabled || []
      const rows = us.map((u) => ({ url: u, disabled: dis.includes(u) }))
      if (rows.length === 0) rows.push({ url: '', disabled: false })
      setUrls(rows)
      setFiles(
        (t.source_files || []).map((f: any) => ({
          name: f.name || 'file.txt',
          path: f.path || '',
          size: typeof f.size === 'number' ? f.size : 0,
          mtime: typeof f.mtime === 'number' ? f.mtime : 0,
          disabled: !!f.disabled,
          isNew: false,
        })),
      )
      setGhEnabled(!!t.github_enabled)
      setGhOwner(t.github_owner || '')
      setGhRepo(t.github_repo || '')
      setGhFile(t.github_file || '')
      setGhPat(t.github_pat || '')
      setRefreshMin(t.refresh_min || 0)
      setRefreshDisabled(!!t.refresh_disabled)
      setRefreshTest(t.auto_refresh_test || '')
      setDedupMode(t.dedup_mode || '')
      setExcludeDisabled(!!t.exclude_disabled)
      setEf(rulesToMap(t.exclude_filter))
      loadedRef.current = true
    })
    return () => {
      if (applyTimer.current) window.clearTimeout(applyTimer.current)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tabId])

  // Live apply (debounced 250ms, as in 1.10 scheduleTabApply). Reads the
  // CURRENT state on fire — the effect below reschedules on every edit.
  const stateRef = useRef<any>(null)
  stateRef.current = {
    name,
    urls,
    files,
    ghEnabled,
    ghOwner,
    ghRepo,
    ghFile,
    ghPat,
    refreshMin,
    refreshDisabled,
    refreshTest,
    dedupMode,
    excludeDisabled,
    ef,
    efInput,
  }
  const flushApply = () => {
    const s = stateRef.current
    if (!s) return
    const nm = (s.name || '').trim()
    if (nm && nm !== origNameRef.current) {
      origNameRef.current = nm
      TabService.Rename(tabId, nm)
    }
    const cleanUrls: string[] = []
    const disabledUrls: string[] = []
    for (const r of s.urls as UrlRow[]) {
      const v = r.url.trim()
      if (!v) continue
      cleanUrls.push(v)
      if (r.disabled) disabledUrls.push(v)
    }
    // Exclude filter: chips + any value the user forgot to Enter.
    const rules = mapToRules(s.ef, s.efInput)
    TabService.SetSettings(tabId, {
      urls: cleanUrls,
      disabled_urls: disabledUrls,
      files: (s.files as FileRow[])
        .filter((f) => !!f.path)
        .map((f) => ({ name: f.name, path: f.path, size: 0, mtime: 0, disabled: !!f.disabled })),
      refresh_min: s.refreshMin || 0,
      exclude_filter: rules,
      dedup_mode: s.dedupMode,
      refresh_disabled: s.refreshDisabled,
      exclude_disabled: s.excludeDisabled,
      auto_refresh_test: s.refreshTest,
      github_enabled: s.ghEnabled,
      github_owner: s.ghOwner,
      github_repo: s.ghRepo,
      github_file: s.ghFile,
      github_pat: s.ghPat,
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

  const pickFiles = () => {
    TabService.PickFiles().then((picked: any) => {
      if (!picked || picked.length === 0) return
      setFiles((prev) => [
        ...prev,
        ...picked
          .filter((f: any) => f && f.path)
          .map((f: any) => ({
            name: f.name || 'file.txt',
            path: f.path,
            size: typeof f.size === 'number' ? f.size : 0,
            mtime: typeof f.mtime === 'number' ? f.mtime : 0,
            disabled: false,
            isNew: true,
          })),
      ])
      scheduleApply()
    })
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
        <div className="modal-title">{tt('Tab Settings')}</div>

        <div className="settings-section">
          <div className="modal-label">{tt('Name')}</div>
          <input
            className="modal-input"
            id="ms-name"
            maxLength={40}
            style={{ marginBottom: 0 }}
            value={name}
            autoFocus
            onFocus={(e) => e.target.select()}
            onChange={(e) => setName(e.target.value)}
            onBlur={scheduleApply}
            onKeyDown={(e) => {
              if (e.key === 'Enter') scheduleApply()
            }}
          />
        </div>

        <SubInfoSection subs={tab.subs} lang={lang} />

        {/* ── Sources: URLs + files ── */}
        <div className="settings-section">
          <div className="modal-label">{tt('Source URLs (raw links, base64 subscriptions)')}</div>
          <div id="ms-urls">
            {urls.map((r, i) => (
              <div key={i} className={'url-row' + (r.disabled ? ' src-off' : '')}>
                <input
                  type="checkbox"
                  className="ms-url-on"
                  checked={!r.disabled}
                  title={tt('Enable / disable this source')}
                  onChange={(e) => {
                    setUrls((prev) => prev.map((x, j) => (j === i ? { ...x, disabled: !e.target.checked } : x)))
                    scheduleApply()
                  }}
                />
                <input
                  className="modal-input ms-url"
                  placeholder="https://raw.githubusercontent.com/..."
                  value={r.url}
                  spellCheck={false}
                  onChange={(e) =>
                    setUrls((prev) => prev.map((x, j) => (j === i ? { ...x, url: e.target.value } : x)))
                  }
                  onBlur={scheduleApply}
                />
                <button
                  className="url-qr"
                  title={tt('QR')}
                  onClick={() => {
                    const u = r.url.trim()
                    if (u) onShowQR(u)
                  }}
                >
                  QR
                </button>
                <button
                  className="url-rm"
                  title="Remove"
                  onClick={() => {
                    setUrls((prev) => prev.filter((_, j) => j !== i))
                    scheduleApply()
                  }}
                >
                  x
                </button>
              </div>
            ))}
          </div>
          <button
            className="btn ghost"
            style={{ fontSize: 9, margin: '4px 0 12px' }}
            onClick={() => setUrls((prev) => [...prev, { url: '', disabled: false }])}
          >
            {tt('+ add URL')}
          </button>
          <div className="modal-label">{tt('Files (loaded in addition order, after URLs)')}</div>
          <div id="ms-files">
            {files.length === 0 ? (
              <div className="modal-hint" style={{ margin: '0 0 6px' }}>
                {tt('No files added. Use the + add file button below.')}
              </div>
            ) : (
              files.map((f, i) => (
                <div key={i} className={'file-line' + (f.disabled ? ' src-off' : '')}>
                  <input
                    type="checkbox"
                    className="ms-url-on"
                    checked={!f.disabled}
                    title={tt('Enable / disable this source')}
                    onChange={(e) => {
                      setFiles((prev) =>
                        prev.map((x, j) => (j === i ? { ...x, disabled: !e.target.checked } : x)),
                      )
                      scheduleApply()
                    }}
                  />
                  <div className={'file-row' + (f.isNew ? ' new' : '')} title={f.path || f.name}>
                    <span className="file-ico">📄</span>
                    <span className="file-name">{f.name}</span>
                    <span className="file-size">{fmtBytes(f.size)}</span>
                  </div>
                  <button
                    className="url-rm"
                    title="Remove"
                    onClick={() => {
                      setFiles((prev) => prev.filter((_, j) => j !== i))
                      scheduleApply()
                    }}
                  >
                    x
                  </button>
                </div>
              ))
            )}
          </div>
          <button className="btn ghost" style={{ fontSize: 9, margin: '4px 0 12px' }} onClick={pickFiles}>
            {tt('+ add file')}
          </button>
          <div className="modal-hint">
            {tt('Files are read from disk on every RELOAD, so edits propagate without re-adding. No size limit — only the path is stored.')}
          </div>
        </div>

        {/* ── GitHub import ── */}
        <div className="settings-section">
          <div className="modal-row" style={{ marginBottom: 6 }}>
            <span className="modal-label" style={{ margin: 0 }}>
              {tt('Import from GitHub (private repo)')}
            </span>
            <label className="toggle">
              <input
                type="checkbox"
                checked={ghEnabled}
                onChange={(e) => {
                  setGhEnabled(e.target.checked)
                  scheduleApply()
                }}
              />
              <span className="toggle-track" />
              <span className="toggle-thumb" />
            </label>
          </div>
          <div className="modal-hint">
            {tt('Pulls a config file from a private GitHub repository on every reload via a personal access token. Loaded after URLs and files.')}
          </div>
          {ghEnabled && (
            <div id="ms-gh-fields">
              <input
                className="modal-input"
                placeholder={tt('owner (user or organization)')}
                value={ghOwner}
                autoComplete="off"
                spellCheck={false}
                onChange={(e) => setGhOwner(e.target.value)}
                onBlur={scheduleApply}
              />
              <input
                className="modal-input"
                placeholder={tt('repository name')}
                value={ghRepo}
                autoComplete="off"
                spellCheck={false}
                onChange={(e) => setGhRepo(e.target.value)}
                onBlur={scheduleApply}
              />
              <input
                className="modal-input"
                placeholder={tt('path to file, e.g. configs.txt')}
                value={ghFile}
                autoComplete="off"
                spellCheck={false}
                onChange={(e) => setGhFile(e.target.value)}
                onBlur={scheduleApply}
              />
              <div className="gh-pat-row">
                <input
                  className="modal-input"
                  type={ghPatVisible ? 'text' : 'password'}
                  placeholder={tt('personal access token (PAT)')}
                  value={ghPat}
                  autoComplete="off"
                  spellCheck={false}
                  onChange={(e) => setGhPat(e.target.value)}
                  onBlur={scheduleApply}
                />
                <button type="button" className="btn ghost gh-view-btn" onClick={() => setGhPatVisible((v) => !v)}>
                  {ghPatVisible ? tt('hide') : tt('view')}
                </button>
              </div>
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

        {/* ── Dedup ── */}
        <div className="settings-section">
          <div className="modal-row" style={{ alignItems: 'flex-end' }}>
            <span className="modal-row-label">{tt('Deduplicate duplicate configs')}</span>
            <div className="seg-group" id="ms-dedup-seg" role="radiogroup">
              {[
                { v: '', l: tt('Off'), title: tt('No deduplication') },
                { v: 'hide', l: tt('Hide'), title: tt('Hide duplicates from view (reversible)') },
                { v: 'delete', l: tt('Delete'), title: tt('Permanently delete duplicates') },
              ].map((m) => (
                <button
                  key={m.v}
                  type="button"
                  className={'seg-btn' + (dedupMode === m.v ? ' active' : '')}
                  title={m.title}
                  onClick={() => {
                    setDedupMode(m.v)
                    scheduleApply()
                  }}
                >
                  {m.l}
                </button>
              ))}
            </div>
          </div>
          <div className="modal-hint">
            {tt('Off: show everything. Hide: filter from view, reversible. Delete: permanently remove duplicate entries. Matching is by vless body (ignores the name).')}
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
