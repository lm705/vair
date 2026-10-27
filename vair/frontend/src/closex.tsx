// The close control of every panel and dialog: a square ✕ at the right-hand
// end of the title row, the same one the log panel has. It replaces the CLOSE
// buttons that sat at the foot of each dialog, each in a different place.
export default function CloseX({
  onClick,
  title,
  className,
}: {
  onClick: () => void
  title: string
  className?: string
}) {
  return (
    <button
      type="button"
      className={'btn ghost xbtn' + (className ? ' ' + className : '')}
      title={title}
      aria-label={title}
      onClick={onClick}
    >
      ✕
    </button>
  )
}
