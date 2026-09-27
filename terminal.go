package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/drybones/simple-ping-monitor/internal/monitor"
)

// terminal keeps a one-line live status at the bottom of the terminal and
// prints each incident above it once the incident is over.
type terminal struct {
	w       io.Writer
	cfg     monitor.Config
	tty     bool
	printed map[[2]int64]bool
	lastLog time.Time
}

func newTerminal(w io.Writer, cfg monitor.Config) *terminal {
	tty := false
	if f, ok := w.(*os.File); ok {
		if fi, err := f.Stat(); err == nil {
			tty = fi.Mode()&os.ModeCharDevice != 0
		}
	}
	return &terminal{w: w, cfg: cfg, tty: tty, printed: map[[2]int64]bool{}}
}

func (t *terminal) update(sum monitor.Summary) {
	t.clear()
	// Incidents are newest first; print the finished ones oldest first.
	for i := len(sum.Incidents) - 1; i >= 0; i-- {
		inc := sum.Incidents[i]
		k := [2]int64{int64(inc.Target), inc.StartSeq}
		if inc.Ongoing || t.printed[k] {
			continue
		}
		t.printed[k] = true
		fmt.Fprintln(t.w, t.paint(incidentColour(inc), inc.Describe(t.cfg.Targets)))
	}
	line := t.statusLine(sum)
	if t.tty {
		fmt.Fprint(t.w, line)
	} else if time.Since(t.lastLog) >= time.Minute {
		fmt.Fprintln(t.w, line)
		t.lastLog = time.Now()
	}
}

func (t *terminal) clear() {
	if t.tty {
		fmt.Fprint(t.w, "\r\033[K")
	}
}

func (t *terminal) statusLine(sum monitor.Summary) string {
	var b strings.Builder
	b.WriteString(time.UnixMilli(sum.Now).Format("15:04:05"))
	fmt.Fprintf(&b, "  %s ", fmtElapsed(sum.ElapsedS))
	for _, ts := range sum.Targets {
		rtt := "      -"
		if ts.LastRttMs != nil {
			rtt = fmt.Sprintf("%7s", ms(*ts.LastRttMs))
		}
		status := ts.Status
		if ts.Reason != "" {
			status += " (" + ts.Reason + ")"
		}
		fmt.Fprintf(&b, " │ %s %s %s", ts.Name, rtt, t.paint(statusColour(ts.Status), status))
	}
	return b.String()
}

func (t *terminal) paint(colour, s string) string {
	if !t.tty || colour == "" {
		return s
	}
	return colour + s + "\033[0m"
}

const (
	green  = "\033[32m"
	yellow = "\033[33m"
	red    = "\033[31m"
)

func statusColour(s string) string {
	switch s {
	case "good":
		return green
	case "degraded":
		return yellow
	case "bad":
		return red
	}
	return ""
}

func incidentColour(i monitor.Incident) string {
	if i.Kind == "high latency" {
		return yellow
	}
	return red
}

func printSummary(w io.Writer, cfg monitor.Config, sum monitor.Summary) {
	fmt.Fprintf(w, "Session %s, %s\n", cfg.Start.Format("Mon 2 Jan 2006 15:04"), fmtElapsed(sum.ElapsedS))
	for i, ts := range sum.Targets {
		s := ts.Total
		fmt.Fprintf(w, "\n%s (%s)\n", ts.Name, ts.Host)
		fmt.Fprintf(w, "  pings     %d sent, %d lost (%.2f%%), %d late, %d duplicate replies\n", s.Sent, s.Lost, s.LossPct, s.Late, s.Dups)
		if s.Received > 0 {
			fmt.Fprintf(w, "  latency   median %s, p95 %s, p99 %s, worst %s, jitter %s\n", ms(s.P50Ms), ms(s.P95Ms), ms(s.P99Ms), ms(s.MaxMs), ms(s.JitterMs))
			fmt.Fprintf(w, "  classes   good %s, fair %s, high %s, severe %s\n", pct(s.Good, s), pct(s.Fair, s), pct(s.High, s), pct(s.Severe, s))
		}
		counts := map[string]int{}
		n := 0
		for _, inc := range sum.Incidents {
			if inc.Target == i {
				counts[inc.Kind]++
				n++
			}
		}
		fmt.Fprintf(w, "  incidents %d", n)
		for _, k := range []string{"outage", "packet loss", "severe latency", "high latency"} {
			if counts[k] > 0 {
				fmt.Fprintf(w, ", %d %s", counts[k], k)
			}
		}
		if s.LongestOutageS > 0 {
			fmt.Fprintf(w, "; longest run without replies %.0f s", s.LongestOutageS)
		}
		fmt.Fprintln(w)
	}
}

func pct(n int, s monitor.Stats) string {
	done := s.Sent - s.Pending
	if done == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(done))
}

func fmtElapsed(s float64) string {
	d := time.Duration(s * float64(time.Second))
	return fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}

func ms(v float64) string {
	if v < 10 {
		return fmt.Sprintf("%.1f ms", v)
	}
	return fmt.Sprintf("%.0f ms", v)
}
