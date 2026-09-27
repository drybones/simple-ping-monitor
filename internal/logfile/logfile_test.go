package logfile

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/drybones/simple-ping-monitor/internal/monitor"
)

func TestRoundTrip(t *testing.T) {
	cfg := monitor.Config{
		Start:      time.UnixMilli(1790000000000),
		Interval:   time.Second,
		LossAfter:  5 * time.Second,
		Thresholds: monitor.DefaultThresholds,
		Targets:    []monitor.Target{{Name: "router", Host: "192.168.1.1", Gateway: true}, {Name: "internet", Host: "8.8.8.8"}},
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	w, err := Create(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.Sample(monitor.Sample{Target: 1, Seq: 0, Sent: 1790000000000, RTT: 21.5, Status: monitor.OK})
	w.Sample(monitor.Sample{Target: 1, Seq: 1, Sent: 1790000001000, Status: monitor.Lost})
	w.Sample(monitor.Sample{Target: 0, Seq: 0, Sent: 1790000000000, RTT: 1.2, Status: monitor.OK})
	w.Sample(monitor.Sample{Target: 1, Seq: 1, Sent: 1790000001000, RTT: 6100, Status: monitor.Late})
	w.Sample(monitor.Sample{Target: 1, Seq: 1, Sent: 1790000001000, RTT: 6100, Status: monitor.Late, Dups: 1})
	w.Summary(monitor.Summary{})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-line.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"sample","tgt":0,"se`)
	f.Close()

	f, _ = os.Open(path)
	defer f.Close()
	got, samples, err := Read(f)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Start.Equal(cfg.Start) || got.Interval != cfg.Interval || !reflect.DeepEqual(got.Targets, cfg.Targets) {
		t.Errorf("config mismatch: %+v", got)
	}
	if len(samples) != 3 {
		t.Fatalf("got %d samples, want 3", len(samples))
	}
	if s := samples[1]; s.Status != monitor.Late || s.Dups != 1 || s.RTT != 6100 {
		t.Errorf("last write should win: %+v", s)
	}
}

func TestRejectsOtherFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.jsonl")
	os.WriteFile(path, []byte("hello\n"), 0o644)
	f, _ := os.Open(path)
	defer f.Close()
	if _, _, err := Read(f); err == nil {
		t.Error("want an error for a file with no session line")
	}
}
