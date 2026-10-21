// The measurement history of one configuration.
//
// The Stability column has room for a percentage and a hover; this is the same
// record laid out where it can be read. Every figure here is one the ranking
// uses — nothing is measured to open this dialog.
//
// Two rules keep it honest, and they are why the numbers are grouped the way
// they are:
//
//   - the tiles show what the ranking compares (typical delay and its spread,
//     typical speed and its spread), and Detail shows the single number each
//     one collapses to;
//   - delay and speed are counted separately, because they are: a delay check
//     is a ping, a speed measurement is a download, and a speed test produces
//     one of each. The old dialog had a single "checks" and a single "last
//     check" covering both, which answered neither question.
//
// Numbers, not a picture: the kept window is a list read at a glance, and there
// is no longer series to draw — what survives for a month is a running mean per
// server, not a log of samples. Which is also the confusion this dialog now
// heads off: "twenty checks kept" and "forgotten after thirty days" describe
// storage, and neither is the window the ranking actually weighs.
import { useEffect, useState } from 'react'
import { ConfigService } from '../bindings/vair/internal/services'
import { t10 } from './i18n'

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

  const row = (label: string, value: string, hint?: string) => (
    <div className="hist-row">
      <span className="hist-label">{label}</span>
      <span className="hist-value">{value}</span>
      {hint ? <span className="hist-hint">{hint}</span> : null}
    </div>
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
        <div className="modal-title">{tt('Measurement history')}</div>
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
            <div className="hist-tiles">
              <div className="hist-tile">
                <span className="ht-label">{tt('Delay')}</span>
                <span className="ht-value">
                  {h.avg}
                  <span className="ht-unit">ms</span>
                </span>
                <span className="ht-note">±{h.jitter} ms {tt('spread')}</span>
              </div>
              <div className="hist-tile">
                <span className="ht-label">{tt('Answered')}</span>
                <span className={'ht-value' + (h.percent >= 0 && h.percent < 80 ? ' poor' : '')}>
                  {h.percent >= 0 ? h.percent : '—'}
                  {h.percent >= 0 ? <span className="ht-unit">%</span> : null}
                </span>
                <span className="ht-note">
                  {h.trusted
                    ? h.samples + ' ' + tt('delay checks')
                    : tt('needs') + ' ' + h.min_samples + ' ' + tt('checks to judge')}
                </span>
              </div>
              <div className="hist-tile">
                <span className="ht-label">{tt('Speed')}</span>
                <span className="ht-value">
                  {h.speed_seen === 0 ? '—' : h.speed_mbps.toFixed(1)}
                  {h.speed_seen === 0 ? null : <span className="ht-unit">MB/s</span>}
                </span>
                <span className={'ht-note' + (speedStale(h) ? ' stale' : '')}>
                  {h.speed_seen === 0
                    ? tt('never measured')
                    : '±' + h.speed_spread.toFixed(1) + ' MB/s ' + tt('spread')}
                </span>
              </div>
            </div>

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
              {tt('In milliseconds, oldest first — the last')} {h.recent_max}{' '}
              {tt('checks, as they came, with when each round was taken. The figures above are worked out differently: every check folds into a running average at')}{' '}
              {h.newest_weight_pct}%, {tt('so about')} {h.effective_window}{' '}
              {tt('recent ones decide them — which is why the two need not agree.')}
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
                  {tt('speed measurements, oldest first. A speed test is expensive, so these are rare and far apart — the ranking only uses ones under')}{' '}
                  {h.speed_hours} {tt('hours old.')}
                </div>
              </>
            ) : null}

            <div className="hist-cap">{tt('Detail')}</div>
            {row(
              tt('Delay checks'),
              String(h.samples),
              tt('a speed test makes one of these too — it measures the delay before the download'),
            )}
            {row(tt('Last delay check'), ago(tt, h.last_check_age_sec))}
            {row(
              tt('History weight'),
              '≈' + h.effective_window + ' ' + tt('checks'),
              tt('nothing is dropped — each older check simply counts for less, so a server that was bad yesterday is forgiven within a few runs'),
            )}
            {h.trusted
              ? row(
                  tt('Ranked on'),
                  Math.round(h.delay_score) + ' ms',
                  tt('typical delay plus its spread and a charge for unanswered checks — this is what the steady modes compare'),
                )
              : null}
            {row(
              tt('Speed measurements'),
              h.speed_seen === 0 ? tt('none') : String(h.speed_seen),
              h.speed_seen === 0
                ? tt('run SPEED on the row to measure it')
                : speedThin(h)
                  ? tt('the steady mode wants') + ' ' + h.min_samples + ' ' + tt('of them; below that it ranks on the last one')
                  : undefined,
            )}
            {h.speed_seen > 0
              ? row(
                  tt('Last speed measurement'),
                  ago(tt, h.speed_age_sec),
                  speedStale(h)
                    ? tt('older than') + ' ' + h.speed_hours + ' ' + tt('hours, so the ranking no longer uses it')
                    : tt('a speed figure counts for') + ' ' + h.speed_hours + ' ' + tt('hours'),
                )
              : null}
            {h.speed_seen > 0
              ? row(
                  tt('Ranked on'),
                  h.speed_score.toFixed(1) + ' MB/s',
                  tt('typical speed less its spread — what the server can be relied on to deliver'),
                )
              : null}

            <div className="hist-foot">
              {tt('The history belongs to the server, not to the row: it survives a list refresh, and a configuration that appears in two tabs has one history. It is forgotten after')}{' '}
              {h.kept_days} {tt('days without a check.')}
            </div>
          </>
        )}

        <div className="modal-btns">
          <button className="btn ghost" onClick={onClose}>
            {tt('Close')}
          </button>
        </div>
      </div>
    </div>
  )
}
