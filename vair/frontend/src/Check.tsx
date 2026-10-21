// One checkbox, drawn the same way everywhere.
//
// There were four different ones before this: the browser's own in the log
// bars, the browser's own with accent-color in the source lists, a bare one in
// the config form, and a pill in the service check. The switch (Toggle, in
// SettingsModal) stays what it is — a switch is for turning a feature on, a
// checkbox is for picking items out of a list, and they should not look alike.
export default function Check({
  checked,
  onChange,
  disabled,
  label,
  title,
  className,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  disabled?: boolean
  label?: React.ReactNode
  title?: string
  className?: string
}) {
  return (
    <label
      className={'chk' + (disabled ? ' chk-off' : '') + (className ? ' ' + className : '')}
      title={title}
      onClick={(e) => e.stopPropagation()}
    >
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="chk-box" />
      {label !== undefined && <span className="chk-label">{label}</span>}
    </label>
  )
}
