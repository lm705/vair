import { useCallback, useEffect, useRef, useState } from 'react'

// useChipSelect gives a chips-wrap a full selection model over its amber chips:
//   • click a chip           → select just it
//   • Ctrl/⌘+click           → toggle it in a multi-selection
//   • Shift+click            → range from the last-clicked chip to this one
//   • drag                   → select the reading-order range from the chip the
//                              drag started on to the one under the pointer
//                              (contiguous, in order — not a 2-D column band);
//                              works even when the drag starts on the input
//   • Ctrl/⌘+A               → select all chips (even while the input is focused)
//   • Ctrl/⌘+C               → copy the selected values, one per line
//   • Delete / Backspace     → remove the selected chips (when the input is empty)
// A plain click on the empty area / input clears the selection and focuses the
// input; clicking outside the field clears it too. After any selection the input
// keeps focus, so the app's global Ctrl+A/Ctrl+C/Delete (which skip when an input
// is focused) don't fight this field's shortcuts. Native text selection is off on
// the wrap (CSS) so nothing else gets caught.
//
// Returns the ref for the .chips-wrap, its onMouseDown handler, and isSel(i).
// `items` is the ordered chip values; `onRemove` deletes the given indices.
export function useChipSelect(items: string[], onRemove?: (indices: number[]) => void) {
  const wrapRef = useRef<HTMLDivElement>(null)
  const [sel, setSel] = useState<Set<number>>(new Set())
  const selRef = useRef(sel)
  selRef.current = sel
  const anchorRef = useRef(-1) // last-clicked chip, for Shift-range
  const itemsRef = useRef(items)
  itemsRef.current = items
  const removeRef = useRef(onRemove)
  removeRef.current = onRemove

  // Drop indices that no longer exist after an add/remove.
  useEffect(() => {
    setSel((prev) => {
      if (prev.size === 0) return prev
      const next = new Set<number>()
      prev.forEach((i) => {
        if (i < items.length) next.add(i)
      })
      return next.size === prev.size ? prev : next
    })
  }, [items.length])

  const chipEls = () => Array.from(wrapRef.current?.querySelectorAll('.chip') ?? [])
  const focusInput = () =>
    (wrapRef.current?.querySelector('.chip-input') as HTMLInputElement | null)?.focus()
  const inputEmpty = () =>
    !(wrapRef.current?.querySelector('.chip-input') as HTMLInputElement | null)?.value

  // plateIndexAt returns the chip index at point (x,y) in READING ORDER (not by
  // straight-line distance): the chip the point is inside, else the last chip
  // that comes at-or-before the point row-major (rows above, then left-to-right
  // on the point's row). So a point in the trailing area / on the input maps to
  // the LAST chip, and a drag anchors at the end and walks back in order —
  // rather than jumping to whichever chip's centre happens to be nearest (which
  // picked a middle-row chip when the drag started on the input).
  const plateIndexAt = (x: number, y: number, els: Element[]) => {
    let best = -1
    for (let i = 0; i < els.length; i++) {
      const r = els[i].getBoundingClientRect()
      if (x >= r.left && x <= r.right && y >= r.top && y <= r.bottom) return i
      if (r.bottom <= y) {
        best = i // chip is on a row above the point → before it
      } else if (r.top <= y && r.left <= x) {
        best = i // chip shares the point's row and is at/left of it → before it
      }
    }
    return best < 0 ? 0 : best
  }

  const onMouseDown = useCallback((e: React.MouseEvent) => {
    if ((e.target as HTMLElement).closest('.chip-x')) return // delete button
    const sx = e.clientX
    const sy = e.clientY
    const shift = e.shiftKey
    const ctrl = e.ctrlKey || e.metaKey
    let dragging = false
    let dragAnchor = -1

    const onMove = (ev: MouseEvent) => {
      if (!dragging) {
        if (Math.abs(ev.clientX - sx) + Math.abs(ev.clientY - sy) < 5) return
        dragging = true
        dragAnchor = plateIndexAt(sx, sy, chipEls())
      }
      ev.preventDefault() // suppress native text selection (incl. inside the input)
      const els = chipEls()
      const cur = plateIndexAt(ev.clientX, ev.clientY, els)
      if (dragAnchor < 0 || cur < 0) return
      const lo = Math.min(dragAnchor, cur)
      const hi = Math.max(dragAnchor, cur)
      const next = new Set<number>()
      for (let k = lo; k <= hi; k++) next.add(k)
      setSel(next)
    }
    const onUp = () => {
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
      if (dragging) {
        anchorRef.current = dragAnchor
        focusInput() // keep the field "active" for Ctrl+A / Ctrl+C / Delete
        return
      }
      // A plain click (no drag).
      const els = chipEls()
      const chipEl = (e.target as HTMLElement).closest('.chip')
      const i = chipEl ? els.indexOf(chipEl) : -1
      if (i >= 0) {
        setSel((prev) => {
          if (shift && anchorRef.current >= 0) {
            const lo = Math.min(anchorRef.current, i)
            const hi = Math.max(anchorRef.current, i)
            const s = new Set<number>()
            for (let k = lo; k <= hi; k++) s.add(k)
            return s
          }
          if (ctrl) {
            const s = new Set(prev)
            if (s.has(i)) s.delete(i)
            else s.add(i)
            anchorRef.current = i
            return s
          }
          anchorRef.current = i
          return new Set([i])
        })
        focusInput()
      } else {
        // Empty area / input → clear and focus the input for typing.
        setSel(new Set())
        anchorRef.current = -1
        focusInput()
      }
    }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }, [])

  // Field-scoped keyboard + copy + click-outside-clear. Registered once; reads
  // live state via refs. The keydown is a capture listener so it can claim
  // Ctrl+A / Delete before the app's global handler (which anyway skips when an
  // input is focused — and this field keeps its input focused).
  useEffect(() => {
    const active = () =>
      !!wrapRef.current &&
      (wrapRef.current.contains(document.activeElement) || selRef.current.size > 0)
    const onKey = (e: KeyboardEvent) => {
      if (!active()) return
      const mod = e.ctrlKey || e.metaKey
      if (mod && (e.code === 'KeyA' || e.key.toLowerCase() === 'a')) {
        e.preventDefault()
        e.stopImmediatePropagation()
        setSel(new Set(itemsRef.current.map((_, i) => i)))
      } else if (
        (e.key === 'Delete' || e.key === 'Backspace') &&
        selRef.current.size > 0 &&
        inputEmpty()
      ) {
        e.preventDefault()
        e.stopImmediatePropagation()
        const idxs = Array.from(selRef.current).sort((a, b) => a - b)
        setSel(new Set())
        anchorRef.current = -1
        removeRef.current?.(idxs)
      } else if (e.key === 'Escape' && selRef.current.size > 0) {
        setSel(new Set())
        anchorRef.current = -1
      }
    }
    const onCopy = (e: ClipboardEvent) => {
      if (selRef.current.size === 0) return
      if (window.getSelection()?.toString()) return
      const ae = document.activeElement as HTMLInputElement | null
      if (ae && ae.tagName === 'INPUT' && ae.selectionStart !== ae.selectionEnd) return
      const vals = itemsRef.current.filter((_, i) => selRef.current.has(i))
      if (!vals.length) return
      e.clipboardData?.setData('text/plain', vals.join('\n'))
      e.preventDefault()
    }
    const onDocDown = (e: MouseEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) {
        if (selRef.current.size > 0) setSel(new Set())
        anchorRef.current = -1
      }
    }
    document.addEventListener('keydown', onKey, true)
    document.addEventListener('copy', onCopy)
    document.addEventListener('mousedown', onDocDown)
    return () => {
      document.removeEventListener('keydown', onKey, true)
      document.removeEventListener('copy', onCopy)
      document.removeEventListener('mousedown', onDocDown)
    }
  }, [])

  return { wrapRef, onMouseDown, isSel: (i: number) => sel.has(i) }
}
