// "Does this server open the thing I need?" — asked of the whole list at once.
//
// A matrix: configurations down the side, services across the top, one glyph
// per answer. That shape is the point — the question is never "does this one
// server work", it is "which of these four hundred do", and only a grid answers
// that at a glance.
//
// The list of services is the user's. The seven that ship are a starting point,
// not the definition: what matters to somebody is their bank, their government
// portal, the streaming service they pay for. Editing one is editing two
// strings — where to ask, and what the answer looks like when the answer is no.
import { useEffect, useState } from 'react'
import { UnlockService as Unlock } from '../bindings/vair/internal/services'
import { t10 } from './i18n'
import { Hint } from './hints'
import Check from './Check'
import DataTable, { type Col } from './DataTable'
import CloseX from './closex'
import BinIcon from './binicon'
import ConfirmDiscard from './confirm'

type Svc = {
  id: string
  name: string
  url: string
  open_if?: string[]
  blocked_if?: string[]
  builtin: boolean
}
type Row = { index: number; raw: string; name: string; state: Record<string, string>; done: boolean }

const PICKED_KEY = 'unlock.services'

export default function UnlockModal({
  onClose,
  lang,
  tabId,
  selected,
  total,
  sort,
  filter,
  proto,
}: {
  onClose: () => void
  lang: string
  tabId: string
  selected: number[] // the rows selected in the table, if any
  total: number // how many the tab is showing
  // The view the main table is in. "Whole tab" means what is on screen — a tab
  // exclude filter, a search or a protocol pill all narrow it, and a check that
  // ignored them would test rows the user has hidden.
  sort: string
  filter: string
  proto: string[]
}) {
  const tt = (en: string) => t10(lang, en)
  const [services, setServices] = useState<Svc[]>([])
  const [picked, setPicked] = useState<string[]>(() => {
    try {
      return JSON.parse(localStorage.getItem(PICKED_KEY) || '["chatgpt","claude","gemini","youtube"]')
    } catch {
      return ['chatgpt', 'claude', 'gemini', 'youtube']
    }
  })
  const [scope, setScope] = useState<'selected' | 'all'>(selected.length > 0 ? 'selected' : 'all')
  const [rows, setRows] = useState<Row[]>([])
  const [running, setRunning] = useState(false)
  const [done, setDone] = useState(0)
  const [count, setCount] = useState(0)
  const [err, setErr] = useState('')
  const [editing, setEditing] = useState(false)
  // Unsaved edits in the editor, and what to do once the user has answered
  // the "leave without saving?" question.
  const [editorDirty, setEditorDirty] = useState(false)
  const [askLeave, setAskLeave] = useState<null | (() => void)>(null)
  // Closing the editor, or the whole dialog, with edits on screen asks first:
  // "done" used to close the editor and drop them without a word.
  const leaveEditor = (then: () => void) => {
    if (editing && editorDirty) setAskLeave(() => then)
    else then()
  }
  const closeDialog = () => leaveEditor(onClose)
  const [query, setQuery] = useState('')

  const loadServices = () => Unlock.Services().then((s: any) => setServices(s || []))

  useEffect(() => {
    loadServices()
    const pull = () =>
      Unlock.State().then((p: any) => {
        setRows(p?.rows || [])
        setRunning(!!p?.running)
        setDone(p?.done || 0)
        setCount(p?.total || 0)
      })
    pull()
    const id = setInterval(pull, 700)
    return () => clearInterval(id)
  }, [])

  const toggle = (id: string) => {
    setPicked((p) => {
      const next = p.includes(id) ? p.filter((x) => x !== id) : [...p, id]
      try {
        localStorage.setItem(PICKED_KEY, JSON.stringify(next))
      } catch {
        /* storage refused; the choice still holds for this session */
      }
      return next
    })
  }

  const start = () => {
    setErr('')
    const idxs = scope === 'selected' ? selected : []
    Unlock.Start(tabId, sort, filter, proto, idxs, picked).then((e: string) => e && setErr(e))
  }

  // Saving the edited list and reading it back: the backend drops entries with
  // no id or no URL, so what comes back is what will actually be checked.
  const saveServices = (next: Svc[]) =>
    Unlock.SetServices(next as any)
      .then(loadServices)
      .then(() => setEditing(false))
  // resetSeq is bumped so the editor re-reads the list even when the list did
  // not change. "Restore defaults" with unsaved edits on screen is exactly that
  // case: the backend forgets a saved list that may have been the defaults
  // already, nothing about `services` differs, and the editor went on showing
  // the edits — which reads as the button doing nothing.
  const [resetSeq, setResetSeq] = useState(0)
  const resetServices = () =>
    Unlock.ResetServices()
      .then(loadServices)
      .then(() => setResetSeq((n) => n + 1))

  const glyph = (st?: string) => {
    if (!st) return <span className="ug pending">·</span>
    if (st === 'open') return <span className="ug open">✓</span>
    if (st === 'blocked') return <span className="ug blocked">✗</span>
    return <span className="ug failed">—</span>
  }

  const shown = services.filter((s) => picked.includes(s.id))

  // One column per service, plus the configuration they were asked from. The
  // widths are shares of the box and must add to one.
  //
  // A service column is as wide as its HEADING — the cell under it holds one
  // glyph — so the headings decide the split and the configuration takes what
  // is left, rather than the other way round. Sized from the longest name in
  // the monospace face (~0.62em a character) plus the cell padding and the sort
  // arrow, at whatever the dialog font size currently is; a fixed share was
  // clipping "ChatGPT" at seven services and clipped everything sooner once the
  // font size could be raised. The floor keeps the configuration column
  // readable when there are many long names — past that the headings shorten
  // instead of the column disappearing.
  const fsBase =
    parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--modal-fs-base')) || 12
  const tableW = Math.min(880, window.innerWidth * 0.94) - 44
  const longest = shown.reduce((n, s) => Math.max(n, s.name.length), 0)
  const svcPx = Math.max(4 * fsBase, longest * (fsBase + 1) * 0.62 + 30)
  const nameFrac = Math.max(0.2, 1 - (svcPx / tableW) * shown.length)
  const cols: Col<Row>[] = [
    {
      id: 'name',
      label: tt('Configuration'),
      frac: nameFrac,
      sort: (r) => r.name.toLowerCase(),
      cell: (r) => r.name,
      title: (r) => r.name,
      cls: (r) => 'dest' + (r.done ? '' : ' busy'),
    },
    ...shown.map((s) => ({
      id: s.id,
      label: s.name,
      frac: shown.length ? (1 - nameFrac) / shown.length : 1 - nameFrac,
      // Sorted by the answer, best first: what opens, then what refused, then
      // what never replied, and the ones nobody has asked yet at the end.
      sort: (r: Row) => ({ open: 0, blocked: 1, failed: 2 }[r.state?.[s.id] || ''] ?? 3),
      cell: (r: Row) => glyph(r.state?.[s.id]),
      cls: () => 'usvc',
    })),
  ]

  const shownRows = rows.filter((r) => {
    const q = query.trim().toLowerCase()
    return !q || r.name.toLowerCase().includes(q)
  })

  return (
    <div className="modal-overlay" onClick={(e) => e.target === e.currentTarget && closeDialog()}>
      <div className="modal-box unlock-box">
        <div className="modal-head">
          <div className="modal-title">{tt('Service check')}</div>
          <CloseX onClick={closeDialog} title={tt('Close')} />
        </div>

        <div className="unlock-picks">
          {services.map((s) => (
            <Check
              key={s.id}
              className={'unlock-pick' + (picked.includes(s.id) ? ' on' : '')}
              checked={picked.includes(s.id)}
              onChange={() => toggle(s.id)}
              title={s.url}
              label={s.name}
            />
          ))}
          <button
            className="btn ghost tiny"
            onClick={() => (editing ? leaveEditor(() => setEditing(false)) : setEditing(true))}
          >
            {editing ? tt('close the editor') : tt('edit list')}
          </button>
        </div>

        {editing && (
          <ServiceEditor
            services={services}
            resetSeq={resetSeq}
            tt={tt}
            onSave={saveServices}
            onReset={resetServices}
            onDirty={setEditorDirty}
          />
        )}

        <div className="unlock-scope">
          <div className="seg-group" role="radiogroup">
            <button
              type="button"
              role="radio"
              aria-checked={scope === 'selected'}
              className={'seg-btn' + (scope === 'selected' ? ' active' : '')}
              disabled={selected.length === 0}
              onClick={() => setScope('selected')}
            >
              {tt('Selected')} ({selected.length})
            </button>
            <button
              type="button"
              role="radio"
              aria-checked={scope === 'all'}
              className={'seg-btn' + (scope === 'all' ? ' active' : '')}
              onClick={() => setScope('all')}
            >
              {tt('Whole tab')} ({total})
            </button>
          </div>
          {running ? (
            <>
              <span className="unlock-prog">
                {done} / {count}
              </span>
              <button className="btn ghost" onClick={() => Unlock.Cancel()}>
                {tt('stop')}
              </button>
            </>
          ) : (
            <button className="btn primary" onClick={start} disabled={shown.length === 0}>
              {tt('check')}
            </button>
          )}
        </div>
        {err && <div className="msg-note" style={{ color: 'var(--red)' }}>{tt(err)}</div>}

        <div className="conns-search">
          <input
            className="modal-input"
            placeholder={tt('search configuration…')}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {query && (
            <button className="btn ghost" onClick={() => setQuery('')}>
              {tt('clear')}
            </button>
          )}
        </div>
        <DataTable
          // v2: the widths are computed from the headings now, so a layout
          // saved against the old fixed shares would hide the change.
          tableId="unlock.v2"
          cols={cols}
          data={shownRows}
          rowKey={(r) => String(r.index)}
          empty={tt('Pick the services and press check')}
        />

        <div className="unlock-legend">
          <span>
            <span className="ug open">✓</span> {tt('opens')}
          </span>
          <span>
            <span className="ug blocked">✗</span> {tt('refused')}
          </span>
          <span>
            <span className="ug failed">—</span> {tt('no answer')}
          </span>
          <span className="unlock-tip">{tt('Filter the table by an answer: unlock:chatgpt')}</span>
        </div>

      </div>
      {askLeave && (
        <ConfirmDiscard
          text={tt('The list has unsaved changes. Leave without saving them?')}
          discardLabel={tt('Leave without saving')}
          keepLabel={tt('Keep editing')}
          onDiscard={() => {
            const then = askLeave
            setAskLeave(null)
            setEditorDirty(false)
            then()
          }}
          onKeep={() => setAskLeave(null)}
        />
      )}
    </div>
  )
}

// The editor: one row per service, saved as a whole when the button is pressed.
// Edits are held here rather than pushed on every keystroke — a half-typed URL
// is not a service anyone wants saved.
function ServiceEditor({
  services,
  resetSeq,
  tt,
  onSave,
  onReset,
  onDirty,
}: {
  services: Svc[]
  resetSeq: number
  tt: (s: string) => string
  onSave: (next: Svc[]) => void
  onReset: () => void
  onDirty: (dirty: boolean) => void
}) {
  // The markers are held as the text in their boxes and turned into lists only
  // when the list is saved. They used to be split and trimmed on every
  // keystroke, so a space or a new line vanished as it was typed: "not
  // available" could not be entered, nor a second marker.
  type Draft = Svc & { openText: string; blockedText: string }
  const toDraft = (list: Svc[]): Draft[] =>
    list.map((s) => ({ ...s, openText: (s.open_if || []).join('\n'), blockedText: (s.blocked_if || []).join('\n') }))
  const [draft, setDraft] = useState<Draft[]>(() => toDraft(services))
  const [saveErr, setSaveErr] = useState('')
  // The draft is a copy, so that half-typed rows are not saved on every
  // keystroke — but a copy taken once never noticed the list being REPLACED
  // underneath it. "Restore defaults" did exactly that: the backend forgot the
  // saved list, the panel above reloaded, and the editor went on showing the
  // edits it had made from the copy it took when it opened. Re-taken whenever
  // the list identity changes; a keystroke does not change it.
  const identity = services.map((s) => s.id + '\u0000' + s.name + '\u0000' + s.url).join('\u0001')
  useEffect(() => {
    setDraft(toDraft(services))
    setSaveErr('')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identity, resetSeq])
  // Unsaved: the draft says something the saved list does not.
  const parse = (v: string) => v.split('\n').map((s) => s.trim()).filter(Boolean)
  const fromDraft = (d: Draft[]): Svc[] =>
    d.map(({ openText, blockedText, ...s }) => ({ ...s, open_if: parse(openText), blocked_if: parse(blockedText) }))
  const norm = (list: Svc[]) =>
    JSON.stringify(list.map((s) => [s.id, s.name.trim(), s.url.trim(), s.open_if || [], s.blocked_if || []]))
  const dirty = norm(fromDraft(draft)) !== norm(services)
  useEffect(() => {
    onDirty(dirty)
  }, [dirty, onDirty])

  const set = (i: number, patch: Partial<Draft>) =>
    setDraft((d) => d.map((s, j) => (j === i ? { ...s, ...patch } : s)))
  const remove = (i: number) => setDraft((d) => d.filter((_, j) => j !== i))
  // An id nobody has: numbering by the length of the list gave a second "svc9"
  // after a removal, and two services with one id overwrote each other's
  // answers and were picked together.
  const add = () =>
    setDraft((d) => {
      let n = d.length + 1
      while (d.some((s) => s.id === 'svc' + n)) n++
      return [...d, { id: 'svc' + n, name: '', url: 'https://', builtin: false, openText: '', blockedText: '' }]
    })
  // Saved only when every address is a web address: "https://" left as it was
  // gave a service that every configuration failed to answer.
  const save = () => {
    const bad = draft.find((s) => !/^https?:\/\/[^\s/]+\.[^\s/]+/i.test(s.url.trim()))
    if (bad) {
      setSaveErr(tt('Not a web address') + ': ' + (bad.name.trim() || bad.url))
      return
    }
    setSaveErr('')
    onSave(fromDraft(draft))
  }

  return (
    <div className="unlock-editor">
      <div className="unlock-ed-head">
        <span>{tt('Name')}</span>
        <span>{tt('Address to ask')}</span>
        <span>{tt('Open if the answer has')}</span>
        <span>{tt('Refused if the answer has')}</span>
        <span />
      </div>
      {draft.map((s, i) => (
        <div className="unlock-ed-row" key={s.id + i}>
          <input
            className="modal-input"
            value={s.name}
            placeholder={tt('My bank')}
            onChange={(e) => set(i, { name: e.target.value })}
          />
          <input
            className="modal-input"
            value={s.url}
            spellCheck={false}
            placeholder="https://…"
            onChange={(e) => set(i, { url: e.target.value })}
          />
          <textarea
            className="modal-input"
            rows={1}
            value={s.openText}
            placeholder={tt('(any answer)')}
            onChange={(e) => set(i, { openText: e.target.value })}
          />
          <textarea
            className="modal-input"
            rows={1}
            value={s.blockedText}
            placeholder={tt('(only 403)')}
            onChange={(e) => set(i, { blockedText: e.target.value })}
          />
          <button className="url-rm" title={tt('Remove')} onClick={() => remove(i)}>
            <BinIcon />
          </button>
        </div>
      ))}
      <Hint
        en={
          'A service is asked once per configuration. Anything that answers 403, or whose answer contains one of the refusal markers, counts as refused; with an "open if" marker set, only an answer containing it counts as open.'
        }
      />
      <div className="unlock-ed-btns">
        <button className="btn ghost" onClick={add}>
          {tt('add service')}
        </button>
        <button className="btn ghost" onClick={onReset}>
          {tt('restore defaults')}
        </button>
        <button className="btn" onClick={save}>
          {tt('save')}
        </button>
      </div>
      {saveErr && (
        <div className="msg-note" style={{ color: 'var(--red)', marginTop: 6 }}>
          {saveErr}
        </div>
      )}
    </div>
  )
}
