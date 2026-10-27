// "Check IP": the public address the connection leaves from, as the check IP
// service sees it through the tunnel. Shared by the connection bar and the AUTO
// panel, which runs in a window of its own — one wording for both.
import { ConnService } from '../bindings/vair/internal/services'

type TT = (en: string) => string

// What to show: a class for the colour, the line itself, and the details for
// the tooltip.
export type ExitView = { cls: string; label: string; title?: string }

export function flagEmoji(cc: string): string {
  if (!cc || cc.length !== 2) return ''
  cc = cc.toUpperCase()
  if (!/^[A-Z]{2}$/.test(cc)) return ''
  return String.fromCodePoint(0x1f1e6 + (cc.charCodeAt(0) - 65), 0x1f1e6 + (cc.charCodeAt(1) - 65))
}

export function exitChecking(tt: TT): ExitView {
  return { cls: 'testing', label: '🌍 ' + tt('checking…') }
}

// exitView turns the service's answer into the line shown.
export function exitView(d: any, tt: TT): ExitView {
  if (!d || d.error) {
    // The reason in the user's language; the service's own words in the tooltip.
    const reason: Record<string, string> = {
      unreachable: tt('service unreachable'),
      http: tt('the service answered') + ' ' + String(d?.error || '').split(':')[0],
      unreadable: tt('unreadable answer'),
      no_address: tt('no address in the answer'),
      refused: tt('the service refused'),
    }
    return {
      cls: 'failed',
      label: '🌍 ' + ((d?.reason && reason[d.reason]) || d?.error || tt('check failed')) + ' ↻',
      title: (d?.error ? d.error + ' — ' : '') + tt('The check IP service could not tell the address. Click to re-check.'),
    }
  }
  const flag = flagEmoji(d.country_code)
  const loc = [d.city, d.country].filter(Boolean).join(', ')
  return {
    cls: 'ok',
    label: (flag ? flag + ' ' : '') + (loc || d.country_code || '?') + (d.ip ? ' · ' + d.ip : ''),
    title: (d.isp ? d.isp + ' — ' : '') + tt('Exit IP as seen through the tunnel. Click to re-check.'),
  }
}

// checkExit asks the service and never rejects: a failed call is a failed check.
export function checkExit(tt: TT): Promise<ExitView> {
  return Promise.resolve(ConnService.CheckExit())
    .then((d: any) => exitView(d, tt))
    .catch(() => exitView(null, tt))
}
