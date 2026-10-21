package core

import (
	"math"
	"time"
)

// The measurement history of one configuration, in full.
//
// The table has room for a percentage and a hover; this is the same record laid
// out where it can be read — what was measured, how long ago, and how much of it
// there is. Everything here already existed in node_stats: nothing new is
// recorded to answer this, and nothing is computed that ranking does not
// already compute.
//
// Deliberately numbers and not a picture. The kept window is a list a person
// reads in one glance; a chart of it would be decoration around the same
// numbers, and a chart of a month does not exist to draw — what is kept for a
// month is the running mean, not a log of samples.

// HistoryDTO is one configuration's history as the dialog shows it.
type HistoryDTO struct {
	// Who this is about.
	Name string `json:"name"`
	Host string `json:"host"`

	// Has is false when nothing has ever been measured — the dialog then says so
	// instead of showing a screen of dashes.
	Has bool `json:"has"`

	// Delay, in milliseconds, as ranking sees it: the running mean, its spread,
	// and how many checks are behind them.
	Avg     int   `json:"avg"`
	Jitter  int   `json:"jitter"`
	Samples int64 `json:"samples"`
	// Percent of checks answered, or -1 while there are too few to judge.
	Percent int `json:"percent"`
	// Trusted is whether ranking is allowed to use this history yet, and
	// MinSamples is how many checks that takes.
	Trusted    bool `json:"trusted"`
	MinSamples int  `json:"min_samples"`
	// Recent is the last checks, oldest first; -1 marks one that went unanswered.
	// RecentAgeSec is how long ago each of them was, parallel to it; -1 where a
	// row written by an older build has the value and not the time.
	Recent       []int16 `json:"recent"`
	RecentAgeSec []int64 `json:"recent_age_sec"`
	// RecentMax is how many of them are kept.
	RecentMax int `json:"recent_max"`
	// NewestWeightPct and EffectiveWindow describe how the averages above are
	// built, because the two numbers people compare — "twelve checks kept" and
	// "thirty days of history" — describe neither. Every measurement folds into a
	// running mean at NewestWeightPct per cent, so nothing is dropped and the
	// newest results decide: EffectiveWindow is how many of them carry it in
	// practice.
	NewestWeightPct int `json:"newest_weight_pct"`
	EffectiveWindow int `json:"effective_window"`
	// LastCheckAgeSec is how long ago the last DELAY check was, or -1 if never.
	// A speed test produces one too, so this moves with either kind of test.
	LastCheckAgeSec int64 `json:"last_check_age_sec"`
	// DelayScore is what the "steady" delay ranking actually compares: the
	// typical delay plus what its spread and its failures are worth. Shown so
	// the order in the AUTO panel can be read off the numbers rather than
	// guessed at. Zero when there is not enough history to rank on.
	DelayScore float64 `json:"delay_score"`
	// KeptDays is how long an unused server's history survives.
	KeptDays int `json:"kept_days"`

	// Speed, in MB/s, and how old the figure is. Speed ages out of ranking much
	// sooner than delay does, so its age is part of the answer rather than a
	// footnote.
	SpeedMBps     float64 `json:"speed_mbps"`
	SpeedSeen     int64   `json:"speed_seen"`
	SpeedSpread   float64 `json:"speed_spread"`
	SpeedAgeSec   int64   `json:"speed_age_sec"` // -1 when never measured
	SpeedTrusted  bool    `json:"speed_trusted"`
	SpeedHours    int     `json:"speed_hours"` // how long a speed figure counts for
	SpeedScoreVal float64 `json:"speed_score"` // what ranking uses: typical less spread
	// The kept speed measurements themselves, oldest first, with their ages —
	// the one place the shape of an evening is visible rather than averaged.
	// SpeedRecentMax is how many of them are kept, the counterpart of RecentMax.
	SpeedRecent       []float64 `json:"speed_recent"`
	SpeedRecentAgeSec []int64   `json:"speed_recent_age_sec"`
	SpeedRecentMax    int       `json:"speed_recent_max"`
}

// ConfigHistory returns everything known about one configuration's measurements.
// idx is a row of the displayed table tab, the same identifier every other row
// action takes.
func ConfigHistory(idx int) *HistoryDTO {
	entry, ok := memEntry(TableTab(), idx)
	if !ok {
		return nil
	}
	return historyOf(entry)
}

// ConfigHistoryRaw is the same record addressed by the configuration itself.
//
// The AUTO panel ranks candidates drawn from every candidate tab, so a row of
// it usually belongs to some other tab than the one on screen and has no index
// here — the configuration is the only identifier it carries. The history is
// keyed on the server rather than on the row anyway (a configuration listed in
// two tabs has one history), so this is the more direct address of the two.
func ConfigHistoryRaw(raw string) *HistoryDTO {
	if raw == "" {
		return nil
	}
	entry, _, ok := memEntryByRaw(TableTab(), raw)
	if !ok {
		return nil
	}
	return historyOf(entry)
}

func historyOf(entry *ConfigEntry) *HistoryDTO {
	es := entry.snap()
	d := &HistoryDTO{
		Name:            es.Name,
		Host:            hostPort(es.Host, es.Port),
		Percent:         -1,
		MinSamples:      statsMinSamples,
		RecentMax:       statsRecentMax,
		SpeedRecentMax:  speedRecentMax,
		NewestWeightPct: int(math.Round(statsAlpha * 100)),
		EffectiveWindow: int(math.Round(1 / statsAlpha)),
		KeptDays:        int(statsMaxAge / (24 * time.Hour)),
		SpeedHours:      int(speedMaxAge / time.Hour),
		LastCheckAgeSec: -1,
		SpeedAgeSec:     -1,
	}
	s, has := statsFor(hashBody(es.Raw))
	if !has || s.Samples == 0 {
		return d
	}
	d.Has = true
	d.Avg = int(s.PingAvg + 0.5)
	d.Jitter = int(s.Jitter() + 0.5)
	d.Samples = s.Samples
	d.Trusted = s.Trusted()
	if d.Trusted {
		d.Percent = int(s.OK*100 + 0.5)
	}
	d.Recent = append([]int16(nil), s.Recent...)
	d.RecentAgeSec = agesOf(s.RecentAt, len(s.Recent))
	if !s.Updated.IsZero() {
		d.LastCheckAgeSec = int64(time.Since(s.Updated).Seconds())
	}
	if d.Trusted {
		d.DelayScore = s.PingAvg + jitterWeight*s.Jitter() + (1-s.OK)*unreliablePenaltyMs
	}
	if len(s.SpeedRecent) > 0 {
		d.SpeedRecent = make([]float64, len(s.SpeedRecent))
		for i, v := range s.SpeedRecent {
			d.SpeedRecent[i] = float64(v) / 100
		}
		d.SpeedRecentAgeSec = agesOf(s.SpeedRecentAt, len(s.SpeedRecent))
	}
	if s.SpeedSeen > 0 {
		d.SpeedMBps = s.SpeedAvg
		d.SpeedSeen = s.SpeedSeen
		d.SpeedSpread = s.SpeedSpread()
		d.SpeedScoreVal = s.SpeedScore()
		d.SpeedTrusted = s.SpeedTrusted()
		if !s.SpeedAt.IsZero() {
			d.SpeedAgeSec = int64(time.Since(s.SpeedAt).Seconds())
		}
	}
	return d
}

// agesOf turns the stored minute stamps into seconds ago, one per value. A zero
// stamp is a value from a row written before times were kept: it reports -1,
// and the dialog says nothing rather than "just now".
//
// The age is measured from the middle of the recorded minute, not its start.
// The stamp says only which minute the check fell in, so measuring from the
// start of it adds up to a full minute to every age — which the dialog showed
// as a check taken "1 min ago" on the line above "Last delay check: just now",
// the same check reported twice and disagreeing. From the middle the error is
// half a minute either way and neither line is systematically behind.
func agesOf(at []int32, n int) []int64 {
	if n == 0 {
		return nil
	}
	out := make([]int64, n)
	now := time.Now().Unix()
	for i := 0; i < n; i++ {
		out[i] = -1
		if i < len(at) && at[i] > 0 {
			if age := now - int64(at[i])*60 - 30; age >= 0 {
				out[i] = age
			} else if age > -30 {
				out[i] = 0 // inside the recorded minute: just now
			}
		}
	}
	return out
}
