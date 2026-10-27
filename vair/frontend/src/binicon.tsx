// The bin on a row that can be removed from a list — a source URL, a file, a
// favourite. It was an "x" or a "✕", which is also what closes the dialog the
// list is in, so the two were one glyph for two different things.
export default function BinIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path
        d="M2.5 4.5h11M6.3 4.5V3.2a1 1 0 0 1 1-1h1.4a1 1 0 0 1 1 1v1.3M4 4.5l.7 8.4a1.2 1.2 0 0 0 1.2 1.1h4.2a1.2 1.2 0 0 0 1.2-1.1L12 4.5M6.7 7v4.3M9.3 7v4.3"
        stroke="currentColor"
        strokeWidth="1.3"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}
