// The on/off switch, one drawing for the whole program.
//
// A switch is not a checkbox and should not look like one: a switch turns a
// feature on, a checkbox picks an item out of a list. Both exist here on
// purpose — see Check.tsx for the other one.
export default function Toggle({ on, onChange }: { on: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="toggle">
      <input type="checkbox" checked={on} onChange={(e) => onChange(e.target.checked)} />
      <span className="toggle-track" />
      <span className="toggle-thumb" />
    </label>
  )
}
