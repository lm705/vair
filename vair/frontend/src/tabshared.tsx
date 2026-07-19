// Shared pieces of the Tab-settings and Sources-settings modals (both are
// exact ports of the corresponding 1.10 modals and render identical blocks).
import React from 'react'
import { t10 } from './i18n'
import { useChipSelect } from './chipselect'

export const EXCLUDE_COLS = ['name', 'type', 'host', 'transport', 'security'] as const
export type ExCol = (typeof EXCLUDE_COLS)[number]
export const EXCLUDE_PLACEHOLDERS: Record<ExCol, string> = {
  name: 'e.g. Russia',
  type: 'e.g. vless',
  host: 'e.g. example.com',
  transport: 'e.g. tcp',
  security: 'e.g. tls',
}
export type ExMap = Record<ExCol, string[]>
export type ExInput = Record<ExCol, string>
export const emptyExMap = (): ExMap => ({ name: [], type: [], host: [], transport: [], security: [] })
export const emptyExInput = (): ExInput => ({ name: '', type: '', host: '', transport: '', security: '' })

export function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  if (n < 1024) return n + ' B'
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  const s = v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2)
  return s + ' ' + units[i]
}
export function fmtDateDM(unixSec: number): string {
  const d = new Date(unixSec * 1000)
  const p = (n: number) => (n < 10 ? '0' : '') + n
  return p(d.getDate()) + '.' + p(d.getMonth() + 1) + '.' + d.getFullYear()
}
export function subDaysLeft(unixSec: number): number {
  return Math.ceil((unixSec * 1000 - Date.now()) / 86400000)
}
// parseExcludeRule mirrors the 1.10 parser: "col:value" → {column, value};
// a bare value (legacy) lands in the Name column.
export function parseExcludeRule(s: string): { column: ExCol; value: string } {
  const i = (s || '').indexOf(':')
  if (i > 0) {
    const col = s.slice(0, i).trim().toLowerCase()
    if ((EXCLUDE_COLS as readonly string[]).includes(col)) {
      return { column: col as ExCol, value: s.slice(i + 1).trim() }
    }
  }
  return { column: 'name', value: (s || '').trim() }
}
// rulesToMap / mapToRules convert between stored "col:value" rules and the
// per-column chip lists (plus any value the user forgot to Enter).
export function rulesToMap(rules: string[] | null | undefined): ExMap {
  const byCol = emptyExMap()
  for (const r of rules || []) {
    const p = parseExcludeRule(r)
    if (p.value) byCol[p.column].push(p.value)
  }
  return byCol
}
export function mapToRules(ef: ExMap, efInput: ExInput): string[] {
  const rules: string[] = []
  const seen: Record<string, 1> = {}
  for (const col of EXCLUDE_COLS) {
    const push = (v: string) => {
      v = (v || '').trim()
      if (!v) return
      const stored = col + ':' + v
      if (seen[stored]) return
      seen[stored] = 1
      rules.push(stored)
    }
    for (const v of ef[col]) push(v)
    push(efInput[col])
  }
  return rules
}

// SubInfoSection — the read-only "Subscription info" block (1.10 subInfoHtml /
// subRows / subLabel / subHasInfo). Renders nothing when no sub carries info.
export function SubInfoSection({ subs, lang }: { subs: any[] | null | undefined; lang: string }) {
  const tt = (en: string) => t10(lang, en)
  const subHasInfo = (s: any) =>
    !!(s && (s.url || s.title || s.total > 0 || s.expire > 0 || s.count > 0 || s.updated || s.update_interval || s.error || (s.notes && s.notes.length)))
  const withInfo = ((subs || []) as any[]).filter(subHasInfo)
  if (withInfo.length === 0) return null
  const block = (sub: any, i: number) => {
    const rows: React.ReactNode[] = []
    if (sub.title)
      rows.push(
        <div key="t" className="si-row">
          <b>{sub.title}</b>
        </div>,
      )
    if (sub.url)
      rows.push(
        <div key="u" className="si-row si-dim si-url" title={sub.url}>
          {sub.url}
        </div>,
      )
    if (sub.error) {
      rows.push(
        <div key="e" className="si-row si-warn">
          {tt('Failed to load')}: {tt(sub.error)}
        </div>,
      )
    } else {
      if (sub.total > 0) {
        const used = (sub.upload || 0) + (sub.download || 0)
        const pct = Math.min(100, Math.round((used / sub.total) * 100))
        rows.push(
          <div key="tr" className="si-row">
            {tt('Traffic')}: {fmtBytes(used)} / {fmtBytes(sub.total)} ({Math.max(0, 100 - pct)}% {tt('left')})
            <div className="si-bar">
              <div className="si-fill" style={{ width: pct + '%' }} />
            </div>
          </div>,
        )
      }
      if (sub.expire > 0) {
        const dl = subDaysLeft(sub.expire)
        rows.push(
          <div key="x" className={'si-row' + (dl <= 7 ? ' si-warn' : '')}>
            {tt('Expires')}: {fmtDateDM(sub.expire)} ({dl} {tt('days')})
          </div>,
        )
      }
      if (sub.count > 0)
        rows.push(
          <div key="c" className="si-row si-dim">
            {tt('Configs')}: {sub.count}
          </div>,
        )
      if (sub.updated)
        rows.push(
          <div key="up" className="si-row si-dim">
            {tt('Updated')}: {sub.updated}
          </div>,
        )
      if (sub.update_interval)
        rows.push(
          <div key="ui" className="si-row si-dim">
            {tt('Update interval')}: {sub.update_interval}
          </div>,
        )
      for (let k = 0; k < (sub.notes || []).length; k++)
        rows.push(
          <div key={'n' + k} className="si-row si-dim si-note">
            {sub.notes[k]}
          </div>,
        )
    }
    return (
      <div key={i} className={'si-block' + (i === 0 ? '' : ' si-sep')}>
        {rows}
      </div>
    )
  }
  return (
    <div className="settings-section">
      <div className="section-header">
        {tt('Subscription info')}
        {withInfo.length > 1 ? ' (' + withInfo.length + ')' : ''}
      </div>
      {withInfo.map((s, i) => block(s, i))}
    </div>
  )
}

// ExcludeFields — the five labelled per-column chip boxes (1.10
// renderExcludeFields). Parent owns the chip lists + pending inputs.
export function ExcludeFields({
  ef,
  efInput,
  disabled,
  setEf,
  setEfInput,
  onChanged,
}: {
  ef: ExMap
  efInput: ExInput
  disabled: boolean
  setEf: React.Dispatch<React.SetStateAction<ExMap>>
  setEfInput: React.Dispatch<React.SetStateAction<ExInput>>
  onChanged: () => void
}) {
  const add = (col: ExCol, raw: string) => {
    const v = (raw || '').trim()
    if (!v) return
    setEf((prev) => {
      if (prev[col].some((x) => x.toLowerCase() === v.toLowerCase())) return prev
      return { ...prev, [col]: [...prev[col], v] }
    })
    onChanged()
  }
  return (
    <div className="ef-fields" id="tab-filter-fields" style={{ opacity: disabled ? 0.45 : 1 }}>
      {EXCLUDE_COLS.map((col) => (
        <ExcludeChipField
          key={col}
          col={col}
          ef={ef}
          efInput={efInput}
          setEf={setEf}
          setEfInput={setEfInput}
          onChanged={onChanged}
          add={add}
        />
      ))}
    </div>
  )
}

// ExcludeChipField is one labelled column. Split out so useChipDragSelect (a
// hook) is called once per column rather than inside a map.
function ExcludeChipField({
  col,
  ef,
  efInput,
  setEf,
  setEfInput,
  onChanged,
  add,
}: {
  col: ExCol
  ef: ExMap
  efInput: ExInput
  setEf: React.Dispatch<React.SetStateAction<ExMap>>
  setEfInput: React.Dispatch<React.SetStateAction<ExInput>>
  onChanged: () => void
  add: (col: ExCol, raw: string) => void
}) {
  const { wrapRef, onMouseDown, isSel } = useChipSelect(ef[col], (idxs) => {
    const drop = new Set(idxs)
    setEf((prev) => ({ ...prev, [col]: prev[col].filter((_, i) => !drop.has(i)) }))
    onChanged()
  })
  return (
    <div className="ef-field" data-col={col}>
      <div className="ef-field-tag">{col.charAt(0).toUpperCase() + col.slice(1)}</div>
      <div ref={wrapRef} className="chips-wrap" data-col={col} onMouseDown={onMouseDown}>
        {ef[col].map((v, i) => (
          <span key={v} className={'chip' + (isSel(i) ? ' sel' : '')} data-v={v}>
            {v}
            <span
              className="chip-x"
              onClick={() => {
                setEf((prev) => ({ ...prev, [col]: prev[col].filter((x) => x !== v) }))
                onChanged()
              }}
            >
              x
            </span>
          </span>
        ))}
        <input
          className="chip-input ef-chip-input"
          placeholder={EXCLUDE_PLACEHOLDERS[col]}
          value={efInput[col]}
          onChange={(e) => setEfInput((prev) => ({ ...prev, [col]: e.target.value }))}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              add(col, efInput[col])
              setEfInput((prev) => ({ ...prev, [col]: '' }))
            } else if (e.key === 'Backspace' && efInput[col] === '' && ef[col].length) {
              // Empty input + Backspace removes the last chip (same as its ✕).
              setEf((prev) => ({ ...prev, [col]: prev[col].slice(0, -1) }))
              onChanged()
            }
          }}
          onPaste={(e) => {
            e.preventDefault()
            const text = e.clipboardData.getData('text')
            for (const part of text.split(/[\n,;]+/)) add(col, part)
            setEfInput((prev) => ({ ...prev, [col]: '' }))
          }}
        />
        <span className="chip-fill" aria-hidden="true" />
      </div>
    </div>
  )
}
