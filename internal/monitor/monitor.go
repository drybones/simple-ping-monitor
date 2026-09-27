package monitor

import (
	"context"
	"sync"
	"time"

	"github.com/drybones/simple-ping-monitor/internal/probe"
)

// Update is pushed to subscribers: either a sample whose state changed or
// the once-a-second summary.
type Update struct {
	Sample  *Sample
	Summary *Summary
}

type Monitor struct {
	cfg  Config
	live bool

	mu      sync.RWMutex
	samples [][]Sample // per target, indexed by seq
	pending []int      // per target, index of the oldest possibly-pending sample

	subMu sync.Mutex
	subs  map[chan Update]struct{}

	// OnSample, if set, is called (from the Run goroutine) whenever a sample
	// reaches or changes a final state. Used for the log file.
	OnSample func(Sample)
}

func New(cfg Config) *Monitor {
	return &Monitor{
		cfg:     cfg,
		live:    true,
		samples: make([][]Sample, len(cfg.Targets)),
		pending: make([]int, len(cfg.Targets)),
		subs:    map[chan Update]struct{}{},
	}
}

// NewReplay builds a read-only monitor from a recorded session.
func NewReplay(cfg Config, samples []Sample) *Monitor {
	m := New(cfg)
	m.live = false
	for _, s := range samples {
		if s.Target < 0 || s.Target >= len(m.samples) || s.Seq < 0 {
			continue
		}
		ts := m.samples[s.Target]
		for int64(len(ts)) <= s.Seq {
			seq := int64(len(ts))
			ts = append(ts, Sample{Target: s.Target, Seq: seq, Sent: cfg.Start.UnixMilli() + seq*cfg.Interval.Milliseconds(), Status: Lost})
		}
		ts[s.Seq] = s
		m.samples[s.Target] = ts
	}
	return m
}

func (m *Monitor) Config() Config { return m.cfg }
func (m *Monitor) Live() bool     { return m.live }

// Run consumes probe events until ctx is cancelled or events is closed.
func (m *Monitor) Run(ctx context.Context, events <-chan probe.Event) {
	sweep := time.NewTicker(200 * time.Millisecond)
	defer sweep.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			m.handle(e)
		case now := <-sweep.C:
			m.sweep(now)
		case now := <-tick.C:
			s := m.Summary(now)
			m.broadcast(Update{Summary: &s})
		}
	}
}

func (m *Monitor) handle(e probe.Event) {
	if e.Target < 0 || e.Target >= len(m.samples) {
		return
	}
	m.mu.Lock()
	ts := m.samples[e.Target]
	var changed *Sample
	switch e.Kind {
	case probe.Sent:
		if e.Seq != int64(len(ts)) {
			m.mu.Unlock()
			return
		}
		m.samples[e.Target] = append(ts, Sample{
			Target: e.Target, Seq: e.Seq, Sent: e.Time.UnixMilli(), Status: Pending, sentAt: e.Time,
		})
		s := m.samples[e.Target][e.Seq]
		changed = &s
	case probe.Reply:
		if e.Seq < 0 || e.Seq >= int64(len(ts)) {
			m.mu.Unlock()
			return
		}
		s := &ts[e.Seq]
		if s.replied() {
			s.Dups++
		} else {
			rtt := e.Time.Sub(s.sentAt)
			s.RTT = roundMs(rtt)
			s.Status = OK
			if rtt > m.cfg.LossAfter {
				s.Status = Late
			}
		}
		c := *s
		changed = &c
	}
	m.mu.Unlock()

	if changed.Status != Pending && m.OnSample != nil {
		m.OnSample(*changed)
	}
	m.broadcast(Update{Sample: changed})
}

// sweep gives up on requests older than LossAfter.
func (m *Monitor) sweep(now time.Time) {
	var lost []Sample
	m.mu.Lock()
	for t, ts := range m.samples {
		i := m.pending[t]
		for ; i < len(ts); i++ {
			s := &ts[i]
			if s.Status != Pending {
				continue
			}
			if now.Sub(s.sentAt) <= m.cfg.LossAfter {
				break
			}
			s.Status = Lost
			lost = append(lost, *s)
		}
		m.pending[t] = i
	}
	m.mu.Unlock()

	for i := range lost {
		if m.OnSample != nil {
			m.OnSample(lost[i])
		}
		m.broadcast(Update{Sample: &lost[i]})
	}
}

// Samples returns a copy of every sample, target by target.
func (m *Monitor) Samples() []Sample {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []Sample
	for _, ts := range m.samples {
		all = append(all, ts...)
	}
	return all
}

// Summary analyses the session as of now. For a replay, now is ignored and
// the end of the recording is used instead.
func (m *Monitor) Summary(now time.Time) Summary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.live {
		now = m.end()
	}
	return Analyze(m.cfg, m.samples, now)
}

func (m *Monitor) end() time.Time {
	var last int64
	for _, ts := range m.samples {
		if len(ts) > 0 && ts[len(ts)-1].Sent > last {
			last = ts[len(ts)-1].Sent
		}
	}
	if last == 0 {
		return m.cfg.Start
	}
	return time.UnixMilli(last).Add(m.cfg.Interval)
}

// Subscribe returns a channel of updates. A subscriber that falls too far
// behind is dropped and its channel closed; it should reconnect and reload.
func (m *Monitor) Subscribe() (<-chan Update, func()) {
	ch := make(chan Update, 4096)
	m.subMu.Lock()
	m.subs[ch] = struct{}{}
	m.subMu.Unlock()
	return ch, func() {
		m.subMu.Lock()
		if _, ok := m.subs[ch]; ok {
			delete(m.subs, ch)
			close(ch)
		}
		m.subMu.Unlock()
	}
}

func (m *Monitor) broadcast(u Update) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- u:
		default:
			delete(m.subs, ch)
			close(ch)
		}
	}
}

func roundMs(d time.Duration) float64 {
	return float64(d.Microseconds()/10) / 100
}
