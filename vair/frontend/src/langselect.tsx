// The language picker for the dialogs that are not Settings — tab settings,
// Sources, AUTO. Settings has had it in its title bar; someone reading a tab's
// settings in the wrong language had to close it, open Settings, switch, and
// come back.
//
// It saves the way the Settings title bar does: the whole settings object with
// the language changed. The save announces itself (settings_update), and every
// window, the detached AUTO panel included, relabels from that.
import { SettingsService } from '../bindings/vair/internal/services'
import { t10 } from './i18n'
import Select from './Select'

export default function LangSelect({ lang }: { lang: string }) {
  const change = async (v: string) => {
    const s: any = await SettingsService.Get()
    if (!s || (s.language || 'en') === v) return
    await SettingsService.Set({ ...s, language: v })
  }
  return (
    <Select
      className="modal-input set-title-sel"
      aria-label={t10(lang, 'Language')}
      title={t10(lang, 'Language')}
      value={lang === 'ru' ? 'ru' : 'en'}
      onChange={(e) => change(e.target.value)}
    >
      <option value="en">English</option>
      <option value="ru">Русский</option>
    </Select>
  )
}
