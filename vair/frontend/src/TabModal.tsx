// Tab settings modal — an exact port of the 1.10 openTabSettings() DOM
// (same classes/sections, live apply debounced 250ms, no Save button).
import { useEffect, useRef, useState } from 'react'
import { Events } from '@wailsio/runtime'
import Check from './Check'
import { TabService } from '../bindings/vair/internal/services'
import { t10 } from './i18n'
import LangSelect from './langselect'
import { IS_REMOTE } from './remote'
import { Hint, opt, whenOn, whenOff } from './hints'
import type { HintOpt } from './hints'
import Toggle from './Toggle'
import Group, { DepPanel, needsOnNote } from './setgroup'
import BinIcon from './binicon'
import {
  ExcludeGroup,
  RefreshGroup,
  SubInfoSection,
  emptyExInput,
  emptyExMap,
  fmtBytes,
  mapToRules,
  rulesToMap,
  type ExInput,
  type ExMap,
} from './tabshared'
import CloseX from './closex'
import { srcErrText } from './reasons'

type UrlRow = { url: string; disabled: boolean }
type FileRow = { name: string; path: string; size: number; mtime: number; disabled: boolean; isNew: boolean; error?: string }

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
  // The description under a setting: the paragraph while descriptions are
  // on, the mark that opens the same text while they are off.
  const hint = (en: string, opts?: HintOpt[]) => <Hint en={en} opts={opts} />
  const [tab, setTab] = useState<any>(null) // full tab (Detail) — subs are read-only here
  const [name, setName] = useState('')
  const [urls, setUrls] = useState<UrlRow[]>([])
  const [files, setFiles] = useState<FileRow[]>([])
  const [ghEnabled, setGhEnabled] = useState(false)
  const [ghOwner, setGhOwner] = useState('')
  const [ghRepo, setGhRepo] = useState('')
  const [ghFile, setGhFile] = useState('')
  // The stored token never comes here (core Tab.MarshalJSON): the field holds
  // only a new one being typed, and ghPatSet says one is stored.
  const [ghPat, setGhPat] = useState('')
  const [ghPatSet, setGhPatSet] = useState(false)
  const [ghPatClear, setGhPatClear] = useState(false)
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
          error: f.error || '',
        })),
      )
      setGhEnabled(!!t.github_enabled)
      setGhOwner(t.github_owner || '')
      setGhRepo(t.github_repo || '')
      setGhFile(t.github_file || '')
      setGhPatSet(!!t.github_pat_set)
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

  // What the fetches started from here found — a source that did not load,
  // and why — arrives with the tab list; the dialog shows it while open.
  useEffect(() => {
    const off = Events.On('tabs_update', (e: any) => {
      const t = ((e?.data?.payload || []) as any[]).find((x) => x?.id === tabId)
      if (!t) return
      setTab((prev: any) => (prev ? { ...prev, subs: t.subs, github_error: t.github_error } : prev))
      setFiles((prev) =>
        prev.map((f) => {
          const m = (t.source_files || []).find((x: any) => x.path === f.path)
          return m ? { ...f, error: m.error || '' } : f
        }),
      )
    })
    return () => {
      off?.()
    }
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
    ghPatClear,
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
      github_pat_clear: s.ghPatClear,
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

  // urlNote is what is wrong with URL row i, if anything: a line that is not a
  // link (it used to fail as "unsupported protocol scheme"), or one already in
  // the list above (it was loaded twice).
  const urlNote = (i: number): string => {
    const v = urls[i].url.trim()
    if (!v) return ''
    if (!/^https?:\/\//i.test(v)) return tt('Not a link — a source address starts with https://')
    if (urls.slice(0, i).some((r) => r.url.trim() === v)) return tt('This link is already in the list above')
    return ''
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
          <span>{tt('Tab Settings')}</span>
          <div className="set-title-controls">
            <LangSelect lang={lang} />
            <CloseX onClick={closeModal} title={tt('Close')} />
          </div>
        </div>

        <Group>
          <div className="modal-row" style={{ display: 'block' }}>
            <span className="modal-row-label stack">{tt('Name')}</span>
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
        </Group>

        <SubInfoSection subs={tab.subs} lang={lang} />

        {/* ── Sources: URLs, files, GitHub ── */}
        <Group title={tt('Config sources')}>
          <div className="modal-row" style={{ display: 'block' }}>
            <span className="modal-row-label stack">{tt('Source URLs (raw links, base64 subscriptions)')}</span>
            {urls.some((r) => /^http:\/\//i.test(r.url.trim())) && (
              <div className="set-note" style={{ margin: '0 0 6px', color: 'var(--warn, #d9a441)' }}>
                {tt(
                  'A source over plain http:// can be changed by anyone between you and the server, and what it returns becomes the servers your traffic goes through. Use https:// where the provider offers it.',
                )}
              </div>
            )}
            <div id="ms-urls">
              {urls.map((r, i) => (
                <div key={i} className={'url-row' + (r.disabled ? ' src-off' : '')}>
                  <Check
                    className="ms-url-on"
                    checked={!r.disabled}
                    title={tt('Enable / disable this source')}
                    onChange={(v) => {
                      setUrls((prev) => prev.map((x, j) => (j === i ? { ...x, disabled: !v } : x)))
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
                    title={tt('QR code')}
                    onClick={() => {
                      const u = r.url.trim()
                      if (u) onShowQR(u)
                    }}
                  >
                    QR
                  </button>
                  <button
                    className="url-rm"
                    title={tt('Remove')}
                    onClick={() => {
                      setUrls((prev) => prev.filter((_, j) => j !== i))
                      scheduleApply()
                    }}
                  >
                    <BinIcon />
                  </button>
                  {urlNote(i) && <div className="src-err">{urlNote(i)}</div>}
                </div>
              ))}
            </div>
            <button
              className="btn ghost"
              style={{ marginTop: 4 }}
              onClick={() => setUrls((prev) => [...prev, { url: '', disabled: false }])}
            >
              {tt('+ add URL')}
            </button>
          </div>
          <div className="modal-row" style={{ display: 'block' }}>
            <span className="modal-row-label stack">{tt('Files (loaded in addition order, after URLs)')}</span>
            <div id="ms-files">
              {files.length === 0 ? (
                <div className="set-note" style={{ margin: '0 0 6px' }}>
                  {IS_REMOTE
                    ? tt('No files added. Files are added on the computer Vair runs on.')
                    : tt('No files added. Use the + add file button below.')}
                </div>
              ) : (
                files.map((f, i) => (
                  <div key={i} className={'file-line' + (f.disabled ? ' src-off' : '')}>
                    <Check
                      className="ms-url-on"
                      checked={!f.disabled}
                      title={tt('Enable / disable this source')}
                      onChange={(v) => {
                        setFiles((prev) => prev.map((x, j) => (j === i ? { ...x, disabled: !v } : x)))
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
                      title={tt('Remove')}
                      onClick={() => {
                        setFiles((prev) => prev.filter((_, j) => j !== i))
                        scheduleApply()
                      }}
                    >
                      <BinIcon />
                    </button>
                    {f.error && !f.disabled && (
                      <div className="src-err">
                        {tt('Failed to load')}: {srcErrText(f.error, tt)}
                      </div>
                    )}
                  </div>
                ))
              )}
            </div>
            {/* The picker opens on the computer, and the paths it returns are
                the computer's — from a phone the button appears to do nothing.
                The files already added stay listed above, and still load. */}
            {!IS_REMOTE && (
              <button className="btn ghost" style={{ marginTop: 4 }} onClick={pickFiles}>
                {tt('+ add file')}
              </button>
            )}
          </div>
          {hint('Files are read from disk on every RELOAD, so edits propagate without re-adding. No size limit — only the path is stored.')}
          <div className="modal-row">
            <span className="modal-row-label">{tt('Import from GitHub (private repo)')}</span>
            <Toggle
              on={ghEnabled}
              onChange={(v) => {
                setGhEnabled(v)
                scheduleApply()
              }}
            />
          </div>
          {hint('A configuration file from a private GitHub repository, pulled through a personal access token. Loaded after URLs and files. The token is kept in the program and is not shown again; type a new one to replace it.', [
            whenOn('the file is pulled on every reload.'),
            whenOff('the repository is not used.', true),
          ])}
          <DepPanel off={!ghEnabled} note={needsOnNote(tt, lang, tt('Import from GitHub (private repo)'))}>
            <div className={'modal-row' + (ghEnabled ? ' row-cont' : '')} id="ms-gh-fields" style={{ display: 'block' }}>
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
                  placeholder={
                    ghPatSet ? tt('a token is saved — type a new one to replace it') : tt('personal access token (PAT)')
                  }
                  value={ghPat}
                  autoComplete="off"
                  spellCheck={false}
                  onChange={(e) => setGhPat(e.target.value)}
                  onBlur={scheduleApply}
                />
                {/* Only for a token being typed: the stored one never comes to
                    this dialog, and over an empty field the button did nothing. */}
                {ghPat && (
                  <button type="button" className="btn ghost gh-view-btn" onClick={() => setGhPatVisible((v) => !v)}>
                    {ghPatVisible ? tt('hide') : tt('view')}
                  </button>
                )}
                {ghPatSet && !ghPat && (
                  <button
                    type="button"
                    className="url-rm"
                    title={tt('Remove the saved token')}
                    onClick={() => {
                      setGhPatSet(false)
                      setGhPatClear(true)
                      scheduleApply()
                    }}
                  >
                    <BinIcon />
                  </button>
                )}
              </div>
              {tab.github_error && ghEnabled && (
                <div className="src-err">
                  {tt('Failed to load')}: {srcErrText(tab.github_error, tt)}
                </div>
              )}
            </div>
          </DepPanel>
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
        >
          <div className="modal-row">
            <span className="modal-row-label">{tt('Deduplicate duplicate configs')}</span>
            <div className="seg-group" id="ms-dedup-seg" role="radiogroup">
              {[
                { v: '', l: tt('Off'), title: tt('No deduplication') },
                { v: 'hide', l: tt('Hide'), title: tt('Hide duplicates from view (reversible)') },
                { v: 'delete', l: tt('Delete'), title: tt('Delete duplicates from the tab') },
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
          {hint('What happens to duplicate configurations. Two configurations count as duplicates when they point at the same server with the same settings; the name is not compared.', [
            opt('Off', 'everything is shown.', true),
            opt('Hide', 'duplicates are filtered out of the view; this can be undone.'),
            opt('Delete', 'duplicates are removed from the tab — the notification that follows can undo it, and later loads of the sources arrive without duplicates.'),
          ])}
        </ExcludeGroup>

        <div className="set-note" style={{ margin: '14px 0 0' }}>
          {tt('Changes apply automatically.')}
        </div>
      </div>
    </div>
  )
}
