// Shared pieces of the Tab-settings and Sources-settings modals (both are
// exact ports of the corresponding 1.10 modals and render identical blocks).
import React from 'react'
import { t10 } from './i18n'
import { useChipSelect } from './chipselect'
import Group, { DepPanel, needsOnNote } from './setgroup'
import Toggle from './Toggle'
import Select from './Select'
import { Hint, opt, whenOn, whenOff } from './hints'
import { srcErrText } from './reasons'

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
          {tt('Failed to load')}: {srcErrText(sub.error, tt)}
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
            {dl > 0
              ? tt('Expires') + ': ' + fmtDateDM(sub.expire) + ' (' + dl + ' ' + tt('days') + ')'
              : tt('Expired') + ': ' + fmtDateDM(sub.expire)}
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
    <Group title={tt('Subscription info') + (withInfo.length > 1 ? ' (' + withInfo.length + ')' : '')}>
      {withInfo.map((s, i) => block(s, i))}
    </Group>
  )
}

// The interval auto-refresh starts at when it is turned on with none set.
const REFRESH_DEFAULT_MIN = 60

// RefreshGroup — auto-refresh of a tab: a switch, and while it is on, how
// often and what to test afterwards. The switch used to sit beside a number
// that also turned it off at 0, so there were two ways to say "off" and a
// switch that could be on with nothing scheduled. The minutes are kept while
// it is off, and come back when it is turned on again.
export function RefreshGroup({
  lang,
  min,
  off,
  test,
  setMin,
  setOff,
  setTest,
  onChanged,
}: {
  lang: string
  min: number
  off: boolean
  test: string
  setMin: (v: number) => void
  setOff: (v: boolean) => void
  setTest: (v: string) => void
  onChanged: () => void
}) {
  const tt = (en: string) => t10(lang, en)
  const on = !off && min > 0
  // What is typed, apart from the number: an emptied field is not an interval
  // of zero, which would read as "off" and fold the field away mid-edit.
  const [txt, setTxt] = React.useState(min > 0 ? String(min) : '')
  React.useEffect(() => setTxt(min > 0 ? String(min) : ''), [min])
  return (
    <Group title={tt('Auto-refresh')}>
      <div className="modal-row">
        <span className="modal-row-label">{tt('Refresh on a schedule')}</span>
        <Toggle
          on={on}
          onChange={(v) => {
            setOff(!v)
            if (v && min < 1) setMin(REFRESH_DEFAULT_MIN)
            onChanged()
          }}
        />
      </div>
      <Hint
        en={'Refreshes the tab on a set interval.'}
        opts={[
          whenOn('a tab with a source or URL reloads the latest configuration list; a tab of pasted configurations only clears stale ping and speed results so they get re-tested.'),
          whenOff('the tab changes only on RELOAD.', true),
        ]}
      />
      <DepPanel off={!on} note={needsOnNote(tt, lang, tt('Refresh on a schedule'))}>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Interval (minutes)')}</span>
            <input
              className="modal-input num-input"
              type="number"
              min={1}
              placeholder={String(REFRESH_DEFAULT_MIN)}
              value={txt}
              onChange={(e) => {
                setTxt(e.target.value)
                const n = Math.floor(+e.target.value)
                if (n >= 1) setMin(n)
              }}
              onBlur={() => {
                // Emptied or zero: the field says how often, and "never" is the
                // switch above — so it goes back to the last interval rather
                // than turning the refresh off from here.
                setTxt(min > 0 ? String(min) : '')
                onChanged()
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
              }}
            />
          </div>
          <div className="modal-row">
            <span className="modal-row-label">{tt('Test after auto-refresh')}</span>
            <Select
              className="modal-input num-input"
              style={{ width: 'auto', marginBottom: 0 }}
              value={test}
              onChange={(e) => {
                setTest(e.target.value)
                onChanged()
              }}
            >
              <option value="">{tt('off')}</option>
              <option value="ping">{tt('Ping only')}</option>
              <option value="speed">{tt('Speed test')}</option>
            </Select>
          </div>
          <Hint
            en={'A background test of the tab\'s configurations after a scheduled refresh — not after a manual RELOAD.'}
            opts={[
              opt('off', 'no test.', true),
              opt('Ping only', 'each configuration is pinged.'),
              opt('Speed test', 'a full speed test of each configuration.'),
            ]}
          />
      </DepPanel>
    </Group>
  )
}

// ExcludeGroup — the exclude filter of a tab: its switch and the five columns.
export function ExcludeGroup({
  lang,
  off,
  setOff,
  ef,
  efInput,
  setEf,
  setEfInput,
  onChanged,
  children,
}: {
  lang: string
  off: boolean
  setOff: (v: boolean) => void
  ef: ExMap
  efInput: ExInput
  setEf: React.Dispatch<React.SetStateAction<ExMap>>
  setEfInput: React.Dispatch<React.SetStateAction<ExInput>>
  onChanged: () => void
  // Rows that go above the filter in the same block (a tab's deduplication).
  children?: React.ReactNode
}) {
  const tt = (en: string) => t10(lang, en)
  return (
    <Group title={tt('Filtering')}>
      {children}
      <div className="modal-row">
        <span className="modal-row-label">{tt('Exclude filter')}</span>
        <Toggle
          on={!off}
          onChange={(v) => {
            setOff(!v)
            onChanged()
          }}
        />
      </div>
      <Hint
        en={'Hides configurations by the values in the columns below. Type a value and press Enter to add it. Name and Host match a part of the text; Type, Transport and Security match the whole value (vless, ws, tls). Case does not matter. A column left empty excludes nothing.'}
        opts={[
          whenOn('matching configurations are hidden.', true),
          whenOff('every configuration is shown; the values are kept.'),
        ]}
      />
      <DepPanel off={off} note={needsOnNote(tt, lang, tt('Exclude filter'))}>
        <div className={'modal-row' + (off ? '' : ' row-cont')} style={{ display: 'block' }}>
          <ExcludeFields lang={lang} ef={ef} efInput={efInput} setEf={setEf} setEfInput={setEfInput} onChanged={onChanged} />
        </div>
      </DepPanel>
    </Group>
  )
}

// ExcludeFields — the five labelled per-column chip boxes (1.10
// renderExcludeFields). Parent owns the chip lists + pending inputs.
export function ExcludeFields({
  lang,
  ef,
  efInput,
  setEf,
  setEfInput,
  onChanged,
}: {
  lang: string
  ef: ExMap
  efInput: ExInput
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
    <div className="ef-fields" id="tab-filter-fields">
      {EXCLUDE_COLS.map((col) => (
        <ExcludeChipField
          key={col}
          lang={lang}
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
  lang,
  col,
  ef,
  efInput,
  setEf,
  setEfInput,
  onChanged,
  add,
}: {
  lang: string
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
              ×
            </span>
          </span>
        ))}
        {/* The leftover of the last chip row — see the same spacer in the
            settings dialog. After the input, which takes a row of its own, it
            was a third row with nothing in it. */}
        {ef[col].length > 0 && <span className="chip-fill" aria-hidden="true" />}
        <input
          className="chip-input ef-chip-input"
          placeholder={t10(lang, EXCLUDE_PLACEHOLDERS[col])}
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
      </div>
    </div>
  )
}
