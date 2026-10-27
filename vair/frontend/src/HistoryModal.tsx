// The measurement history of one configuration.
//
// The Stability column has room for a percentage and a hover; this is the same
// record laid out where it can be read. Every figure here is one the ranking
// uses — nothing is measured to open this dialog.
//
// Two panels at the top, one per measurement — delay and speed — laid out the
// same way: three figures in a row (the typical value with its spread, how far
// it can be trusted, what the ranking makes of it), a line of facts under them
// (how recent, how far back it counts), and a status mark in the panel's title
// when the figures are not yet, or no longer, fit to rank on. The measurements
// themselves are the lists below. Delay and speed are counted separately,
// because they are: a delay check is a ping, a speed measurement is a
// download, and a speed test produces one of each.
//
// Numbers, not a picture: the kept window is a list read at a glance, and there
// is no longer series to draw — what survives for a month is a running mean per
// server, not a log of samples. Which is also the confusion this dialog now
// heads off: "twenty checks kept" and "forgotten after thirty days" describe
// storage, and neither is the window the ranking actually weighs.
import { useEffect, useState } from 'react'
import { ConfigService } from '../bindings/vair/internal/services'
import { t10 } from './i18n'
import CloseX from './closex'
import { InfoDot, opt } from './hints'
import type { HintOpt } from './hints'

type History = {
  name: string
  host: string
  has: boolean
  avg: number
  jitter: number
  samples: number
  percent: number
  trusted: boolean
  min_samples: number
  recent: number[] | null
  recent_age_sec: number[] | null
  recent_max: number
  newest_weight_pct: number
  effective_window: number
  last_check_age_sec: number
  delay_score: number
  kept_days: number
  speed_mbps: number
  speed_seen: number
  speed_spread: number
  speed_age_sec: number
  speed_trusted: boolean
  speed_hours: number
  speed_score: number
  speed_recent: number[] | null
  speed_recent_age_sec: number[] | null
  speed_recent_max: number
}

// ago renders a duration the way the panels around it do: the largest unit that
// still says something useful.
function ago(tt: (s: string) => string, sec: number): string {
  if (sec < 0) return '—'
  if (sec < 60) return tt('just now')
  const m = Math.floor(sec / 60)
  if (m < 60) return m + ' ' + tt('min ago')
  const h = Math.floor(m / 60)
  if (h < 48) return h + ' ' + tt('h ago')
  return Math.floor(h / 24) + ' ' + tt('days ago')
}

// What each figure of a panel is, for the panel's description mark: a name the
// panel shows and the definition of it, in the pop-up the settings use (EN/RU).
// opt() rather than bare pairs so the translation checker sees every string.
const delayDefs: HintOpt[] = [
  opt('Mean', 'Typical round-trip delay to the server: a running average in which every new check enters with a fixed weight, so recent checks count most. The spread is how far single checks typically deviate from it.'),
  opt('Response rate', 'Share of delay checks the server answered, over the same running average. A speed test also counts as a delay check: it measures the delay before the download.'),
  opt('Score', 'Mean plus spread plus a penalty for unanswered checks. AUTO ranks by this figure in the "Delay, steady (history)" mode. It appears once there are enough checks to judge.'),
  opt('Averaging window', 'About how many of the latest checks determine the averages. Older checks are not discarded; they simply count for less, so a past failure stops mattering after a few new checks.'),
]
const speedDefs: HintOpt[] = [
  opt('Mean', 'Typical download speed: a running average of the successful measurements. The spread is how far single measurements typically deviate from it.'),
  opt('Samples', 'Number of successful speed measurements; failed tests are not counted, as they usually mean the test host refused. The "Speed, steady (history)" mode needs at least the minimum; with fewer it ranks on the latest measurement.'),
  opt('Score', 'Mean less spread: the speed the server can be relied on to deliver. AUTO ranks by this figure in the "Speed, steady (history)" mode once there are enough measurements and the latest is recent enough.'),
  opt('Counts for ranking', 'How long a speed measurement stays valid for the ranking. An older one is still shown here, but no longer used.'),
]
const defsDesc = (defs: HintOpt[]) => ({
  render: (tt: (s: string) => string) => (
    <div className="hint-opts">
      {defs.map(([k, v]) => (
        <div className="hint-opt" key={k}>
          <span className="hint-opt-k">{tt(k)}</span>
          <span className="hint-opt-v">{tt(v)}</span>
        </div>
      ))}
    </div>
  ),
})

// timedRuns groups kept measurements by when they were taken.
//
// Measurements arrive in bursts — a whole tab is tested at once, and every row
// gets its result in the same minute — so a time written beside every value
// would say "5 min ago" a dozen times over. Consecutive values that share an
// age are one run, and the time is said once for it. Values from a row written
// before times were kept have none: they group under one heading rather than
// claiming an hour.
//
// Both lists in this dialog are built from it. They answer the same question
// about two different measurements, so they are read the same way: when on the
// left, what was measured beside it, oldest at the top.
type Run = { when: string; stale: boolean; vals: number[] }

// mbps writes a speed figure. Below 10 MB/s with two decimals, as the list of
// measurements does: with one, a spread of 0.05 read "±0.0" beside measurements
// of 1.00 and 1.12.
const mbps = (v: number) => (v < 10 ? v.toFixed(2) : v.toFixed(1))

function timedRuns(values: number[], ages: number[], tt: (s: string) => string, staleSec = 0): Run[] {
  const runs: Run[] = []
  values.forEach((v, i) => {
    const age = (ages || [])[i]
    const when = age >= 0 ? ago(tt, age) : tt('time not kept')
    const stale = staleSec > 0 && age >= 0 && age > staleSec
    const last = runs[runs.length - 1]
    if (last && last.when === when) last.vals.push(v)
    else runs.push({ when, stale, vals: [v] })
  })
  return runs
}

export default function HistoryModal({
  idx,
  raw,
  nested,
  onClose,
  lang,
}: {
  idx?: number
  // Addressed by the configuration rather than by a row: the AUTO panel opens
  // this dialog for a candidate that belongs to another tab, and so has no row
  // in the one on screen.
  raw?: string
  // Opened from another dialog (the AUTO panel), which has to stack above it.
  nested?: boolean
  onClose: () => void
  lang: string
}) {
  const tt = (en: string) => t10(lang, en)
  const [h, setH] = useState<History | null>(null)
  const [missing, setMissing] = useState(false)

  useEffect(() => {
    const p = raw ? ConfigService.HistoryRaw(raw) : ConfigService.History(idx ?? -1)
    p.then((r: any) => (r ? setH(r) : setMissing(true)))
  }, [idx, raw])

  // A figure: its name, the value with its unit, and one line that qualifies
  // it — always present, empty or not, so the three in a row line up.
  const metric = (o: {
    label: string
    value: string
    unit?: string
    caption?: string
    poor?: boolean
    off?: boolean
    title?: string
  }) => (
    <div className="hm" title={o.title}>
      <span className="hm-label">{o.label}</span>
      <span className={'hm-value' + (o.poor ? ' poor' : '') + (o.off ? ' off' : '')}>
        {o.value}
        {o.unit ? <span className="hm-unit">{o.unit}</span> : null}
      </span>
      <span className="hm-cap">{o.caption || '\u00a0'}</span>
    </div>
  )
  // A fact under the figures: a name and its value, on one line.
  const fact = (label: string, value: string, stale?: boolean) => (
    <span className="hf">
      <span className="hf-label">{label}</span>
      <span className={'hf-value' + (stale ? ' stale' : '')}>{value}</span>
    </span>
  )

  // Speed ages out of the ranking on a clock, and needs a few measurements
  // before the steady mode will use it at all. Those are two different things
  // and they used to be reported as one, so a figure measured half an hour ago
  // was labelled too old.
  const speedStale = (h?: History) =>
    !!h && h.speed_seen > 0 && h.speed_age_sec >= 0 && h.speed_age_sec > h.speed_hours * 3600
  const speedThin = (h?: History) => !!h && h.speed_seen > 0 && h.speed_seen < h.min_samples

  return (
    <div
      className={'modal-overlay' + (nested ? ' modal-nested' : '')}
      onMouseDown={(e) => e.target === e.currentTarget && onClose()}
    >
      <div className="modal-box hist-box">
        <div className="modal-head">
          <div className="modal-title">{tt('Measurement history')}</div>
          <CloseX onClick={onClose} title={tt('Close')} />
        </div>
        {h ? (
          <div className="hist-head">
            <span className="hist-name">{h.name || tt('This configuration')}</span>
            <span className="hist-host">{h.host}</span>
          </div>
        ) : null}

        {missing ? (
          <div className="hist-say">{tt('This configuration is no longer in the tab.')}</div>
        ) : !h ? (
          <div className="hist-say">{tt('Loading…')}</div>
        ) : !h.has ? (
          <div className="hist-say">
            {tt('Nothing has been measured yet. Run PING on the row — the history starts with the first check.')}
          </div>
        ) : (
          <>
            <section className="hist-sec">
              <div className="hist-sec-head">
                <span className="hist-sec-title">
                  {tt('Delay')}
                  <InfoDot desc={defsDesc(delayDefs)} />
                </span>
                {!h.trusted ? (
                  <span
                    className="hist-chip"
                    title={tt('needs') + ' ' + h.min_samples + ' ' + tt('checks to judge')}
                  >
                    {tt('Too few checks')} · {h.samples}/{h.min_samples}
                  </span>
                ) : null}
              </div>
              <div className="hist-metrics">
                {metric({
                  label: tt('Mean'),
                  value: String(h.avg),
                  unit: 'ms',
                  caption: tt('spread') + ' ±' + h.jitter + ' ms',
                })}
                {metric({
                  label: tt('Response rate'),
                  value: h.percent >= 0 ? String(h.percent) : '—',
                  unit: h.percent >= 0 ? '%' : '',
                  caption: tt('checks') + ': ' + h.samples,
                  poor: h.percent >= 0 && h.percent < 80,
                })}
                {metric({
                  label: tt('Score'),
                  value: h.trusted ? String(Math.round(h.delay_score)) : '—',
                  unit: h.trusted ? 'ms' : '',
                  caption: h.trusted ? tt('used for AUTO ranking') : tt('too few checks'),
                })}
              </div>
              <div className="hist-facts">
                {fact(tt('Last check'), ago(tt, h.last_check_age_sec))}
                {fact(tt('Averaging window'), '≈' + h.effective_window + ' ' + tt('checks'))}
              </div>
            </section>

            <section className="hist-sec">
              <div className="hist-sec-head">
                <span className="hist-sec-title">
                  {tt('Speed')}
                  <InfoDot desc={defsDesc(speedDefs)} />
                </span>
                {speedStale(h) ? (
                  <span
                    className="hist-chip"
                    title={tt('older than') + ' ' + h.speed_hours + ' ' + tt('hours, so the ranking no longer uses it')}
                  >
                    {tt('Outdated')}
                  </span>
                ) : speedThin(h) ? (
                  <span
                    className="hist-chip muted"
                    title={
                      tt('the steady mode wants') +
                      ' ' +
                      h.min_samples +
                      ' ' +
                      tt('of them; below that it ranks on the last one')
                    }
                  >
                    {tt('Too few measurements')} · {h.speed_seen}/{h.min_samples}
                  </span>
                ) : null}
              </div>
              {h.speed_seen === 0 ? (
                <div className="hist-sec-empty">
                  {tt('Not measured yet. Run SPEED on the row to measure it.')}
                </div>
              ) : (
                <>
                  <div className="hist-metrics">
                    {metric({
                      label: tt('Mean'),
                      value: mbps(h.speed_mbps),
                      unit: 'MB/s',
                      caption: tt('spread') + ' ±' + mbps(h.speed_spread) + ' MB/s',
                    })}
                    {metric({
                      label: tt('Samples'),
                      value: String(h.speed_seen),
                      caption: tt('minimum') + ': ' + h.min_samples,
                    })}
                    {metric({
                      label: tt('Score'),
                      value: mbps(h.speed_score),
                      unit: 'MB/s',
                      caption: speedStale(h) ? tt('not used') : tt('used for AUTO ranking'),
                      off: speedStale(h),
                    })}
                  </div>
                  <div className="hist-facts">
                    {fact(tt('Last measurement'), ago(tt, h.speed_age_sec), speedStale(h))}
                    {fact(tt('Counts for ranking'), h.speed_hours + ' ' + tt('hours'))}
                  </div>
                </>
              )}
            </section>
            <div className="hist-cap">{tt('Last delay checks')}</div>
            {(h.recent || []).length === 0 ? (
              <div className="hist-runs">
                <span className="hist-none">—</span>
              </div>
            ) : (
              <div className="hist-runs">
                {timedRuns(h.recent || [], h.recent_age_sec || [], tt).map((r, i) => (
                  <div className="hist-run" key={i}>
                    <span className="hist-when">{r.when}</span>
                    <span className="hist-recent">
                      {r.vals.map((v, j) => (
                        <span key={j} className={'hist-seq' + (v < 0 ? ' bad' : '')}>
                          {v < 0 ? tt('no answer') : v}
                          {j < r.vals.length - 1 ? <span className="hist-dot">·</span> : null}
                        </span>
                      ))}
                    </span>
                  </div>
                ))}
              </div>
            )}
            <div className="hist-hint hist-para">
              {tt('In milliseconds, oldest first: the last')} {h.recent_max}{' '}
              {tt('checks, each with the time it was taken. The figures above are calculated differently: each check enters a running average with a weight of')}{' '}
              {h.newest_weight_pct}%, {tt('so they reflect roughly the last')} {h.effective_window}{' '}
              {tt('checks. That is why the two can differ.')}
            </div>

            {(h.speed_recent || []).length > 0 ? (
              <>
                <div className="hist-cap">{tt('Last speed measurements')}</div>
                <div className="hist-runs">
                  {timedRuns(
                    h.speed_recent || [],
                    h.speed_recent_age_sec || [],
                    tt,
                    h.speed_hours * 3600,
                  ).map((r, i) => (
                    <div className={'hist-run' + (r.stale ? ' stale' : '')} key={i}>
                      <span className="hist-when">{r.when}</span>
                      <span className="hist-recent">
                        {r.vals.map((v, j) => (
                          <span key={j} className="hist-seq">
                            {v.toFixed(2)} MB/s
                            {j < r.vals.length - 1 ? <span className="hist-dot">·</span> : null}
                          </span>
                        ))}
                      </span>
                    </div>
                  ))}
                </div>
                <div className="hist-hint hist-para">
                  {tt('The last')} {h.speed_recent_max}{' '}
                  {tt('speed measurements, oldest first. Speed tests are costly, so there are few of them; the ranking uses only those less than')}{' '}
                  {h.speed_hours} {tt('hours old.')}
                </div>
              </>
            ) : null}

            <div className="hist-foot">
              {tt('The history is tied to the server, not to the row: it is kept when the list is refreshed, and a configuration in two tabs shares one history. It is deleted after')}{' '}
              {h.kept_days} {tt('days without a check.')}
            </div>
          </>
        )}

      </div>
    </div>
  )
}
