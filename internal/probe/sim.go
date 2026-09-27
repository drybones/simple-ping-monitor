package probe

import (
	"context"
	"hash/fnv"
	"math"
	"math/rand"
	"time"
)

// Sim is a fake network for trying out the UI without real pings. It mimics
// the powerline symptoms: occasional episodes where latency climbs from tens
// of milliseconds into the thousands as buffers fill, duplicate replies, and
// short outages in which some replies are lost and others arrive seconds
// late. Episodes are a function of time, so every Sim sharing an Epoch sees
// the same bad patches (as a router and an internet target behind the same
// adapters would).
type Sim struct {
	Epoch    time.Time
	Interval time.Duration
	BaseMs   float64 // typical RTT when the link is healthy
	// Backfill starts the session this far in the past so there is history
	// to look at straight away.
	Backfill time.Duration
	Seed     int64
}

func (s *Sim) Run(ctx context.Context, target int, out chan<- Event) error {
	rng := rand.New(rand.NewSource(s.Seed + int64(target)))
	start := s.Epoch.Add(-s.Backfill)
	for seq := int64(0); ; seq++ {
		sent := start.Add(time.Duration(seq) * s.Interval)
		if wait := time.Until(sent); wait > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(wait):
			}
		}
		if !emit(ctx, out, Event{Target: target, Kind: Sent, Seq: seq, Time: sent}) {
			return nil
		}
		for _, rtt := range s.replies(rng, sent) {
			at := sent.Add(rtt)
			e := Event{Target: target, Kind: Reply, Seq: seq, Time: at}
			if wait := time.Until(at); wait > 0 {
				go func() {
					select {
					case <-ctx.Done():
					case <-time.After(wait):
						emit(ctx, out, e)
					}
				}()
			} else if !emit(ctx, out, e) {
				return nil
			}
		}
	}
}

// replies returns the RTTs of the replies to a request sent at t: none if
// lost, two or more if duplicated.
func (s *Sim) replies(rng *rand.Rand, t time.Time) []time.Duration {
	sev := severity(t)
	base := s.BaseMs * (1 + 0.15*rng.NormFloat64())
	if base < 0.3 {
		base = 0.3
	}
	if rng.Float64() < 0.02 {
		base *= 2 + 3*rng.Float64() // ordinary background jitter
	}
	ms := base + sev*sev*2500*(0.6+0.4*rng.Float64())

	switch {
	case sev > 0.85 && rng.Float64() < 0.7:
		if rng.Float64() < 0.3 {
			ms = 5200 + 2500*rng.Float64() // arrives after we've given up
		} else {
			return nil
		}
	case rng.Float64() < 0.0005+0.01*sev:
		return nil
	}
	rtts := []time.Duration{dur(ms)}
	if rng.Float64() < 0.001+0.08*sev {
		rtts = append(rtts, dur(ms+5+20*rng.Float64()))
	}
	return rtts
}

// severity is 0 on a healthy link and rises towards 1 inside an episode.
// Time is cut into 4-minute slots; roughly a third of slots contain an
// episode of 10-70 s whose severity ramps up then drops away sharply, as a
// buffer filling and then flushing would.
func severity(t time.Time) float64 {
	const slot = 240
	sec := float64(t.UnixMilli()) / 1000
	n := int64(math.Floor(sec / slot))
	h := fnv.New64a()
	var b [8]byte
	for i := range b {
		b[i] = byte(n >> (8 * i))
	}
	h.Write(b[:])
	r := rand.New(rand.NewSource(int64(h.Sum64())))
	if r.Float64() > 0.35 {
		return 0
	}
	length := 10 + 60*r.Float64()
	offset := r.Float64() * (slot - length)
	peak := 0.4 + 0.6*r.Float64()
	x := (sec - float64(n)*slot - offset) / length
	if x < 0 || x > 1 {
		return 0
	}
	if x < 0.8 {
		return peak * x / 0.8
	}
	return peak * (1 - x) / 0.2
}

func dur(ms float64) time.Duration { return time.Duration(ms * float64(time.Millisecond)) }
