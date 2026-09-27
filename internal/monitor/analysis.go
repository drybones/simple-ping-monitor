package monitor

import (
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	// RecentWindow is the span the live status is judged over.
	RecentWindow = 30 * time.Second
	// StallAfter is how long without any reply before the status goes bad
	// without waiting for LossAfter to confirm the losses.
	StallAfter = 3 * time.Second
	// MergeGap is how many good samples may separate two bad runs before they
	// count as separate incidents.
	MergeGap = 3
	// OutageRun is the number of consecutive lost or late replies that makes
	// an incident an outage.
	OutageRun = 3
)

type Stats struct {
	Sent     int `json:"sent"`
	Pending  int `json:"pending"`
	Received int `json:"received"` // including late replies
	Lost     int `json:"lost"`
	Late     int `json:"late"`
	Dups     int `json:"dups"` // extra replies

	LossPct float64 `json:"loss_pct"` // lost / (sent - pending)
	LatePct float64 `json:"late_pct"`

	MinMs    float64 `json:"min_ms"`
	MeanMs   float64 `json:"mean_ms"`
	P50Ms    float64 `json:"p50_ms"`
	P95Ms    float64 `json:"p95_ms"`
	P99Ms    float64 `json:"p99_ms"`
	MaxMs    float64 `json:"max_ms"`
	JitterMs float64 `json:"jitter_ms"` // mean |Δrtt| between consecutive replies

	// Replies (not late) by latency class.
	Good   int `json:"good"`
	Fair   int `json:"fair"`
	High   int `json:"high"`
	Severe int `json:"severe"`

	LongestOutageS float64 `json:"longest_outage_s"`
}

type TargetSummary struct {
	Target
	Status string `json:"status"` // waiting, good, degraded, bad
	Reason string `json:"reason,omitempty"`

	LastRttMs      *float64 `json:"last_rtt_ms"`
	SinceReplyMs   int64    `json:"since_reply_ms"`   // since the last reply arrived; -1 if never
	UnansweredForS float64  `json:"unanswered_for_s"` // age of the oldest request after the last reply

	Recent Stats `json:"recent"`
	Total  Stats `json:"total"`
}

type Incident struct {
	Target      int     `json:"tgt"`
	StartSeq    int64   `json:"start_seq"`
	Kind        string  `json:"kind"`
	Start       int64   `json:"start"` // unix ms
	End         int64   `json:"end"`
	DurationS   float64 `json:"duration_s"`
	Bad         int     `json:"bad"` // samples that were slow, lost or late
	Lost        int     `json:"lost"`
	Late        int     `json:"late"`
	Dups        int     `json:"dups"`
	MaxRttMs    float64 `json:"max_rtt_ms"`
	Ongoing     bool    `json:"ongoing"`
	GatewayAlso *bool   `json:"gateway_also,omitempty"` // set on non-gateway targets when a gateway is monitored
}

func (i Incident) Describe(targets []Target) string {
	s := fmt.Sprintf("%s  %-9s %s, %s", time.UnixMilli(i.Start).Format("15:04:05"), targets[i.Target].Name, i.Kind, fmtDuration(i.DurationS))
	if i.MaxRttMs > 0 {
		s += fmt.Sprintf(", worst %.0f ms", i.MaxRttMs)
	}
	if i.Lost > 0 {
		s += fmt.Sprintf(", %d lost", i.Lost)
	}
	if i.Late > 0 {
		s += fmt.Sprintf(", %d late", i.Late)
	}
	if i.Dups > 0 {
		s += fmt.Sprintf(", %d dup", i.Dups)
	}
	if i.GatewayAlso != nil {
		if *i.GatewayAlso {
			s += " (router also affected)"
		} else {
			s += " (router fine)"
		}
	}
	return s
}

type Summary struct {
	Now       int64           `json:"now"`
	ElapsedS  float64         `json:"elapsed_s"`
	Targets   []TargetSummary `json:"targets"`
	Incidents []Incident      `json:"incidents"` // newest first
}

// Analyze summarises samples (per target, indexed by seq) as of now.
func Analyze(cfg Config, samples [][]Sample, now time.Time) Summary {
	sum := Summary{Now: now.UnixMilli(), ElapsedS: now.Sub(cfg.Start).Seconds()}
	recentFrom := now.Add(-RecentWindow).UnixMilli()

	incidents := make([][]Incident, len(samples))
	for t, ts := range samples {
		first := sort.Search(len(ts), func(i int) bool { return ts[i].Sent >= recentFrom })
		recent := computeStats(cfg, ts[first:])
		tsum := TargetSummary{
			Target: cfg.Targets[t],
			Recent: recent,
			Total:  computeStats(cfg, ts),
		}
		tsum.SinceReplyMs, tsum.UnansweredForS, tsum.LastRttMs = replyState(ts, now)
		tsum.Status, tsum.Reason = judge(cfg, recent, tsum.UnansweredForS)
		sum.Targets = append(sum.Targets, tsum)
		incidents[t] = findIncidents(cfg, t, ts, false)
	}

	var gatewayRuns []Incident
	for t, tgt := range cfg.Targets {
		if tgt.Gateway {
			gatewayRuns = append(gatewayRuns, findIncidents(cfg, t, samples[t], true)...)
		}
	}
	for t, list := range incidents {
		for _, inc := range list {
			if !cfg.Targets[t].Gateway && len(gatewayRuns) > 0 {
				also := overlaps(inc, gatewayRuns, cfg.Interval.Milliseconds())
				inc.GatewayAlso = &also
			}
			sum.Incidents = append(sum.Incidents, inc)
		}
	}
	sort.Slice(sum.Incidents, func(i, j int) bool { return sum.Incidents[i].Start > sum.Incidents[j].Start })
	if sum.Incidents == nil {
		sum.Incidents = []Incident{}
	}
	return sum
}

func (c Config) class(rtt float64) int {
	switch {
	case rtt < c.Thresholds.GoodMs:
		return 0
	case rtt < c.Thresholds.WarnMs:
		return 1
	case rtt < c.Thresholds.SevereMs:
		return 2
	}
	return 3
}

func computeStats(cfg Config, ts []Sample) Stats {
	var st Stats
	var rtts []float64
	var jitterSum float64
	var jitterN int
	prev := math.NaN()
	run, longest := 0, 0
	for i := range ts {
		s := &ts[i]
		st.Sent++
		st.Dups += s.Dups
		switch s.Status {
		case Pending:
			st.Pending++
			continue
		case Lost:
			st.Lost++
		case Late:
			st.Late++
		case OK:
			switch cfg.class(s.RTT) {
			case 0:
				st.Good++
			case 1:
				st.Fair++
			case 2:
				st.High++
			default:
				st.Severe++
			}
		}
		if s.Status == Lost || s.Status == Late {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
		if s.replied() {
			st.Received++
			rtts = append(rtts, s.RTT)
			if !math.IsNaN(prev) {
				jitterSum += math.Abs(s.RTT - prev)
				jitterN++
			}
			prev = s.RTT
		}
	}
	if done := st.Sent - st.Pending; done > 0 {
		st.LossPct = 100 * float64(st.Lost) / float64(done)
		st.LatePct = 100 * float64(st.Late) / float64(done)
	}
	if jitterN > 0 {
		st.JitterMs = round2(jitterSum / float64(jitterN))
	}
	st.LongestOutageS = float64(longest) * cfg.Interval.Seconds()
	if len(rtts) > 0 {
		sort.Float64s(rtts)
		var total float64
		for _, r := range rtts {
			total += r
		}
		st.MinMs = rtts[0]
		st.MaxMs = rtts[len(rtts)-1]
		st.MeanMs = round2(total / float64(len(rtts)))
		st.P50Ms = percentile(rtts, 50)
		st.P95Ms = percentile(rtts, 95)
		st.P99Ms = percentile(rtts, 99)
	}
	return st
}

// percentile uses the nearest-rank method on sorted values.
func percentile(sorted []float64, p float64) float64 {
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	return sorted[max(rank, 1)-1]
}

func replyState(ts []Sample, now time.Time) (sinceReply int64, unansweredFor float64, lastRtt *float64) {
	sinceReply = -1
	last := -1
	for i := len(ts) - 1; i >= 0; i-- {
		if ts[i].replied() {
			last = i
			break
		}
	}
	if last >= 0 {
		r := ts[last].RTT
		lastRtt = &r
		sinceReply = max(0, now.UnixMilli()-(ts[last].Sent+int64(r)))
	}
	if last+1 < len(ts) {
		unansweredFor = max(0, float64(now.UnixMilli()-ts[last+1].Sent)/1000)
	}
	return
}

func judge(cfg Config, r Stats, unansweredFor float64) (status, reason string) {
	lostish := r.Lost + r.Late
	switch {
	case r.Sent == 0:
		return "waiting", "no pings sent yet"
	case unansweredFor >= StallAfter.Seconds():
		return "bad", fmt.Sprintf("no reply for %.0f s", unansweredFor)
	case r.Received == 0 && r.Pending == r.Sent:
		return "waiting", "waiting for first reply"
	case lostish >= 3:
		return "bad", fmt.Sprintf("%d lost in last %.0f s", lostish, RecentWindow.Seconds())
	case r.P95Ms >= cfg.Thresholds.SevereMs:
		return "bad", fmt.Sprintf("p95 %.0f ms", r.P95Ms)
	case lostish > 0:
		return "degraded", fmt.Sprintf("%d lost in last %.0f s", lostish, RecentWindow.Seconds())
	case r.P95Ms >= cfg.Thresholds.WarnMs:
		return "degraded", fmt.Sprintf("p95 %.0f ms", r.P95Ms)
	case r.Dups > 0:
		return "degraded", fmt.Sprintf("%d duplicate replies", r.Dups)
	}
	return "good", ""
}

// findIncidents groups bad samples (slow, lost or late) into incidents,
// merging runs separated by at most MergeGap good samples. Unless all is
// set, trivial runs (fewer than three slow samples and nothing lost, late or
// severe) are dropped.
func findIncidents(cfg Config, target int, ts []Sample, all bool) []Incident {
	// Only consider samples up to the first one still in flight, so an
	// incident is not closed early by a gap that may yet turn out to be lost.
	n := 0
	for n < len(ts) && ts[n].Status != Pending {
		n++
	}
	bad := func(s *Sample) bool {
		return s.Status == Lost || s.Status == Late || (s.Status == OK && s.RTT >= cfg.Thresholds.WarnMs)
	}

	var out []Incident
	var cur *Incident
	startIdx, lastBad, run, longestRun := 0, -1, 0, 0
	flush := func() {
		if cur == nil {
			return
		}
		cur.Dups = dupsBetween(ts, startIdx, lastBad+1)
		cur.End = ts[lastBad].Sent + cfg.Interval.Milliseconds()
		cur.DurationS = float64(cur.End-cur.Start) / 1000
		cur.Ongoing = n-1-lastBad <= MergeGap
		switch {
		case longestRun >= OutageRun:
			cur.Kind = "outage"
		case cur.Lost+cur.Late > 0:
			cur.Kind = "packet loss"
		case cur.MaxRttMs >= cfg.Thresholds.SevereMs:
			cur.Kind = "severe latency"
		default:
			cur.Kind = "high latency"
		}
		if all || cur.Bad >= 3 || cur.Lost+cur.Late > 0 || cur.MaxRttMs >= cfg.Thresholds.SevereMs {
			out = append(out, *cur)
		}
		cur = nil
	}

	for i := 0; i < n; i++ {
		s := &ts[i]
		if !bad(s) {
			run = 0
			if cur != nil && i-lastBad > MergeGap {
				flush()
			}
			continue
		}
		if cur == nil {
			cur = &Incident{Target: target, StartSeq: s.Seq, Start: s.Sent}
			startIdx, longestRun = i, 0
		}
		cur.Bad++
		switch s.Status {
		case Lost:
			cur.Lost++
			run++
		case Late:
			cur.Late++
			run++
		default:
			run = 0
		}
		longestRun = max(longestRun, run)
		if s.replied() {
			cur.MaxRttMs = max(cur.MaxRttMs, s.RTT)
		}
		lastBad = i
	}
	flush()
	return out
}

func dupsBetween(ts []Sample, from, to int) int {
	d := 0
	for i := from; i < to; i++ {
		d += ts[i].Dups
	}
	return d
}

func overlaps(inc Incident, others []Incident, slackMs int64) bool {
	for _, o := range others {
		if o.Start <= inc.End+slackMs && inc.Start <= o.End+slackMs {
			return true
		}
	}
	return false
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func fmtDuration(s float64) string {
	d := time.Duration(s * float64(time.Second)).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
