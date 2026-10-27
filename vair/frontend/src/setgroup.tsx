// Group is one block of settings: a title and a line on what the block is for,
// then its settings on one card with a rule between them. Settings draws every
// tab with it, and the other dialogs that hold settings — AUTO, a tab's
// settings, Sources, a configuration — use the same blocks, so a setting looks
// the same wherever it is changed. A dialog opts in with the `cardset` class on
// its box (Settings is styled by its id).
//
// The rows stay what they were: a .modal-row, then its description and
// whatever else belongs to it, as siblings. The card draws the rules from that
// shape (a line above every row that is not a continuation of the one before),
// which keeps the description marks, the search and "Show" working as they did.
//
// `bare` leaves the card out, for a block whose content draws its own frames —
// AUTO's status box and candidate list.
import type { ReactNode } from 'react'

// DepPanel is a group of settings waiting on a switch, drawn as Settings draws
// one (its Dependent): a note at its head that names the switch, and the
// settings under it dimmed and out of reach — still there, so what turning the
// switch on would offer is in plain view. They used to disappear instead.
export function DepPanel({ off, note, children }: { off: boolean; note: string; children: ReactNode }) {
  if (!off) return <>{children}</>
  return (
    <div className="dep">
      <div className="dep-note" role="note">
        <span>{note}</span>
      </div>
      <div className="dep-body" aria-disabled="true" inert>
        {children}
      </div>
    </div>
  )
}

// needsOnNote is the note for a group that needs a switch on, worded as
// Settings words it: the switch's name in the language's quotes.
export function needsOnNote(tt: (en: string) => string, lang: string, name: string): string {
  const n = name.charAt(0).toUpperCase() + name.slice(1)
  return tt('Settings in this group work only with this on:') + ' ' + (lang === 'ru' ? '«' + n + '»' : '“' + n + '”')
}

export default function Group({
  title,
  sub,
  id,
  bare,
  children,
}: {
  title?: ReactNode
  sub?: string
  id?: string
  bare?: boolean
  children: ReactNode
}) {
  return (
    <div className="set-group" id={id}>
      {title && (
        <div className="set-group-head">
          <span className="set-group-title">{title}</span>
          {sub && <span className="set-group-sub">{sub}</span>}
        </div>
      )}
      {bare ? (
        children
      ) : (
        <div className="set-card">
          <div className="set-card-in">{children}</div>
        </div>
      )}
    </div>
  )
}
