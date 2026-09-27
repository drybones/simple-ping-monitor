// Package monitor turns raw probe events into classified samples, keeps the
// whole session in memory, and summarises it.
package monitor

import (
	"encoding/json"
	"fmt"
	"time"
)

type Target struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Gateway bool   `json:"gateway,omitempty"`
}

// Thresholds are RTT boundaries in milliseconds: below Good is good, below
// Warn is fair, below Severe is high, and anything above is severe.
type Thresholds struct {
	GoodMs   float64 `json:"good_ms"`
	WarnMs   float64 `json:"warn_ms"`
	SevereMs float64 `json:"severe_ms"`
}

var DefaultThresholds = Thresholds{GoodMs: 60, WarnMs: 150, SevereMs: 1000}

type Config struct {
	Start      time.Time
	Interval   time.Duration
	LossAfter  time.Duration // a request with no reply after this long is lost
	Thresholds Thresholds
	Targets    []Target
}

// Session is the JSON form of Config, shared by the log file header and the
// web API.
type Session struct {
	Start       int64      `json:"start"` // unix ms
	IntervalMs  int64      `json:"interval_ms"`
	LossAfterMs int64      `json:"loss_after_ms"`
	Thresholds  Thresholds `json:"thresholds"`
	Targets     []Target   `json:"targets"`
}

func (c Config) Session() Session {
	return Session{
		Start:       c.Start.UnixMilli(),
		IntervalMs:  c.Interval.Milliseconds(),
		LossAfterMs: c.LossAfter.Milliseconds(),
		Thresholds:  c.Thresholds,
		Targets:     c.Targets,
	}
}

func (s Session) Config() Config {
	return Config{
		Start:      time.UnixMilli(s.Start),
		Interval:   time.Duration(s.IntervalMs) * time.Millisecond,
		LossAfter:  time.Duration(s.LossAfterMs) * time.Millisecond,
		Thresholds: s.Thresholds,
		Targets:    s.Targets,
	}
}

type Status uint8

const (
	Pending Status = iota // sent, no reply yet, not yet given up on
	OK                    // replied within LossAfter
	Lost                  // no reply within LossAfter (so far)
	Late                  // replied, but only after LossAfter
)

var statusNames = [...]string{"pending", "ok", "lost", "late"}

func (s Status) String() string { return statusNames[s] }

func (s Status) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

func (s *Status) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err != nil {
		return err
	}
	for i, n := range statusNames {
		if n == name {
			*s = Status(i)
			return nil
		}
	}
	return fmt.Errorf("unknown status %q", name)
}

// Sample is one echo request and everything we know about its fate.
type Sample struct {
	Target int     `json:"tgt"`
	Seq    int64   `json:"seq"`
	Sent   int64   `json:"t"`   // unix ms
	RTT    float64 `json:"rtt"` // ms, meaningful for OK and Late
	Status Status  `json:"st"`
	Dups   int     `json:"dup,omitempty"` // extra replies beyond the first

	sentAt time.Time
}

func (s *Sample) replied() bool { return s.Status == OK || s.Status == Late }
