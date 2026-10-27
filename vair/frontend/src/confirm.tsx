// A question in front of a dialog: "you have changes — leave without saving?".
//
// Used where closing a dialog throws away what was typed into it: the form for
// a configuration (a click beside it closed it, and every field went with it)
// and the editor of the service check's list. Nested above the dialog it
// guards; Escape and a click beside it keep the dialog, the answer the user
// did not have to think about.
import { useEffect } from 'react'

export default function ConfirmDiscard({
  text,
  discardLabel,
  keepLabel,
  onDiscard,
  onKeep,
}: {
  text: string
  discardLabel: string
  keepLabel: string
  onDiscard: () => void
  onKeep: () => void
}) {
  useEffect(() => {
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onKeep()
      }
    }
    window.addEventListener('keydown', esc, true)
    return () => window.removeEventListener('keydown', esc, true)
  }, [onKeep])
  return (
    <div
      className="modal-overlay modal-nested"
      onClick={(e) => {
        e.stopPropagation()
        if (e.target === e.currentTarget) onKeep()
      }}
    >
      <div className="modal-box confirm-box" role="alertdialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
        <div className="confirm-text">{text}</div>
        <div className="confirm-btns">
          <button className="btn ghost" onClick={onDiscard}>
            {discardLabel}
          </button>
          <button className="btn primary" autoFocus onClick={onKeep}>
            {keepLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
