package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/drybones/simple-ping-monitor/internal/probe"
)

var t0 = time.Date(2026, 9, 27, 10, 30, 0, 0, time.UTC)

func testConfig(targets ...Target) Config {
	if len(targets) == 0 {
		targets = []Target{{Name: "internet", Host: "8.8.8.8"}}
	}
	return Config{Start: t0, Interval: time.Second, LossAfter: 5 * time.Second, Thresholds: DefaultThresholds, Targets: targets}
}

// build makes samples for one target from RTTs in ms; -1 means lost and a
// value above LossAfter is late.
func build(target int, rtts ...float64) []Sample {
	out := make([]Sample, len(rtts))
	for i, r := range rtts {
		s := Sample{Target: target, Seq: int64(i), Sent: t0.Add(time.Duration(i) * time.Second).UnixMilli()}
		switch {
		case r < 0:
			s.Status = Lost
		case r > 5000:
			s.Status, s.RTT = Late, r
		default:
			s.Status, s.RTT = OK, r
		}
		out[i] = s
	}
	return out
}

func TestMonitorClassifiesReplies(t *testing.T) {
	m := New(testConfig())
	var logged []Sample
	m.OnSample = func(s Sample) { logged = append(logged, s) }

	sent := func(seq int64, at time.Duration) {
		m.handle(probe.Event{Kind: probe.Sent, Seq: seq, Time: t0.Add(at)})
	}
	reply := func(seq int64, at time.Duration) {
		m.handle(probe.Event{Kind: probe.Reply, Seq: seq, Time: t0.Add(at)})
	}

	sent(0, 0)
	reply(0, 20*time.Millisecond)
	reply(0, 25*time.Millisecond) // duplicate
	sent(1, time.Second)
	sent(2, 2*time.Second)
	reply(2, 2*time.Second+300*time.Millisecond)
	m.sweep(t0.Add(6500 * time.Millisecond)) // seq 1 is now lost
	reply(1, 7*time.Second)                  // ...and then turns up late
	reply(9, 7*time.Second)                  // unknown seq: ignored

	got := m.Samples()
	want := []struct {
		st   Status
		rtt  float64
		dups int
	}{{OK, 20, 1}, {Late, 6000, 0}, {OK, 300, 0}}
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Status != w.st || g.RTT != w.rtt || g.Dups != w.dups {
			t.Errorf("seq %d: got %v %.2f dups=%d, want %v %.2f dups=%d", i, g.Status, g.RTT, g.Dups, w.st, w.rtt, w.dups)
		}
	}
	// ok, dup, ok, lost, late
	if len(logged) != 5 {
		t.Errorf("logged %d updates, want 5", len(logged))
	}
}

func TestMonitorRunWithSim(t *testing.T) {
	cfg := testConfig(Target{Name: "router", Gateway: true}, Target{Name: "internet"})
	cfg.Interval = 100 * time.Millisecond
	cfg.Start = time.Now().Add(-10 * time.Minute)
	m := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	events := make(chan probe.Event, 64)
	for i := range cfg.Targets {
		sim := &probe.Sim{Epoch: time.Now(), Interval: cfg.Interval, BaseMs: 20, Backfill: 10 * time.Minute, Seed: 1}
		go sim.Run(ctx, i, events)
	}
	m.Run(ctx, events)
	sum := m.Summary(time.Now())
	for _, ts := range sum.Targets {
		if ts.Total.Sent < 5000 {
			t.Errorf("%s: only %d samples after backfill", ts.Name, ts.Total.Sent)
		}
		if ts.Total.Received == 0 {
			t.Errorf("%s: no replies", ts.Name)
		}
	}
}

func TestStats(t *testing.T) {
	cfg := testConfig()
	st := computeStats(cfg, build(0, 20, 30, -1, 200, -1, -1, 6000, 40, 1500))
	if st.Sent != 9 || st.Lost != 3 || st.Late != 1 || st.Received != 6 {
		t.Fatalf("counts: %+v", st)
	}
	if st.Good != 3 || st.High != 1 || st.Severe != 1 {
		t.Errorf("classes: good=%d high=%d severe=%d", st.Good, st.High, st.Severe)
	}
	if st.LongestOutageS != 3 {
		t.Errorf("longest outage %.0f s, want 3 (two lost then a late reply)", st.LongestOutageS)
	}
	if st.P50Ms != 40 || st.MaxMs != 6000 || st.MinMs != 20 {
		t.Errorf("percentiles: p50=%v min=%v max=%v", st.P50Ms, st.MinMs, st.MaxMs)
	}
	// |30-20| + |200-30| + |6000-200| + |40-6000| + |1500-40| over 5
	if want := (10.0 + 170 + 5800 + 5960 + 1460) / 5; st.JitterMs != want {
		t.Errorf("jitter %v, want %v", st.JitterMs, want)
	}
}

func TestIncidents(t *testing.T) {
	cfg := testConfig()
	rtts := []float64{
		20, 20, 20,
		300, 400, 20, 500, // seq 3-6: slow, one good in between: one incident
		20, 20, 20, 20, 20,
		200, // seq 12: a single slow ping is not an incident
		20, 20, 20, 20, 20,
		-1, -1, 6000, 20, // seq 18-20: outage (two lost + late)
		20, 20, 20, 20, 20,
		1200, // seq 27: a single severe ping is
		20, 20, 20, 20, 20,
		-1, // seq 33: lone loss, still at the end so ongoing
		20,
	}
	incs := findIncidents(cfg, 0, build(0, rtts...), false)
	want := []struct {
		seq     int64
		kind    string
		ongoing bool
	}{{3, "high latency", false}, {18, "outage", false}, {27, "severe latency", false}, {33, "packet loss", true}}
	if len(incs) != len(want) {
		t.Fatalf("got %d incidents %+v, want %d", len(incs), incs, len(want))
	}
	for i, w := range want {
		if incs[i].StartSeq != w.seq || incs[i].Kind != w.kind || incs[i].Ongoing != w.ongoing {
			t.Errorf("incident %d: got seq %d %q ongoing=%v, want seq %d %q ongoing=%v",
				i, incs[i].StartSeq, incs[i].Kind, incs[i].Ongoing, w.seq, w.kind, w.ongoing)
		}
	}
	if d := incs[0].DurationS; d != 4 {
		t.Errorf("first incident lasted %v s, want 4", d)
	}
}

func TestIncidentsStopAtPending(t *testing.T) {
	cfg := testConfig()
	ts := build(0, 20, 300, 400, 500, 20, 20, 20, 20, 20)
	ts[5].Status = Pending // may still turn out lost, so the incident isn't over
	incs := findIncidents(cfg, 0, ts, false)
	if len(incs) != 1 || !incs[0].Ongoing {
		t.Fatalf("want one ongoing incident, got %+v", incs)
	}
}

func TestGatewayCorrelation(t *testing.T) {
	cfg := testConfig(Target{Name: "router", Gateway: true}, Target{Name: "internet"})
	router := build(0, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2)
	internet := build(1, 20, 300, 400, 500, 20, 20, 20, 20, 20, 20, 20, 20, 300, 400, 500, 20, 20, 20, 20, 20)
	router[13].RTT = 900 // the LAN is struggling during the second spike only
	sum := Analyze(cfg, [][]Sample{router, internet}, t0.Add(30*time.Second))
	if len(sum.Incidents) != 2 {
		t.Fatalf("got %d incidents, want 2", len(sum.Incidents))
	}
	// newest first
	if g := sum.Incidents[0].GatewayAlso; g == nil || !*g {
		t.Errorf("second spike should implicate the router")
	}
	if g := sum.Incidents[1].GatewayAlso; g == nil || *g {
		t.Errorf("first spike should not implicate the router")
	}
}

func TestJudge(t *testing.T) {
	cfg := testConfig()
	cases := []struct {
		name  string
		rtts  []float64
		stall float64
		want  string
	}{
		{"healthy", []float64{20, 22, 21, 25}, 0, "good"},
		{"one lost", []float64{20, -1, 21, 25}, 0, "degraded"},
		{"three lost", []float64{-1, -1, 21, -1}, 0, "bad"},
		{"slow", []float64{200, 220, 210, 250}, 0, "degraded"},
		{"very slow", []float64{2000, 2200, 2100, 2500}, 0, "bad"},
		{"stalled", []float64{20, 22, 21, 25}, 4, "bad"},
	}
	for _, c := range cases {
		got, _ := judge(cfg, computeStats(cfg, build(0, c.rtts...)), c.stall)
		if got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
