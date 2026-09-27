// Package logfile records a session as JSON Lines so it can be replayed.
//
// The first line describes the session; each later "sample" line is the
// latest state of one request, so a request that was first logged as lost
// and later replied to appears twice and the last line wins. A final
// "summary" line is written on a clean exit, for comparing sessions without
// re-analysing them.
package logfile

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/drybones/simple-ping-monitor/internal/monitor"
)

const Version = 1

type line struct {
	Type    string           `json:"type"`
	Version int              `json:"version,omitempty"`
	Session *monitor.Session `json:"session,omitempty"`
	*monitor.Sample
	Summary *monitor.Summary `json:"summary,omitempty"`
}

type Writer struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

func Create(path string, cfg monitor.Config) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	w := &Writer{f: f, w: bufio.NewWriter(f)}
	s := cfg.Session()
	if err := w.write(line{Type: "session", Version: Version, Session: &s}); err != nil {
		f.Close()
		return nil, err
	}
	return w, w.Flush()
}

func (w *Writer) Sample(s monitor.Sample) error {
	return w.write(line{Type: "sample", Sample: &s})
}

func (w *Writer) Summary(s monitor.Summary) error {
	return w.write(line{Type: "summary", Summary: &s})
}

// Flush pushes buffered lines to disk. Call it regularly so a crash or a
// closed laptop lid loses at most a second or two.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Flush()
}

func (w *Writer) Close() error {
	if err := w.Flush(); err != nil {
		w.f.Close()
		return err
	}
	return w.f.Close()
}

func (w *Writer) write(l line) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.w.Write(b)
	return w.w.WriteByte('\n')
}

// Read loads a recorded session. The last line for each request wins, and a
// truncated final line (from a crash mid-write) is ignored.
func Read(r io.Reader) (monitor.Config, []monitor.Sample, error) {
	var cfg monitor.Config
	var haveSession bool
	type key struct {
		t   int
		seq int64
	}
	idx := map[key]int{}
	var samples []monitor.Sample

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		var l line
		l.Sample = &monitor.Sample{}
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			continue
		}
		switch l.Type {
		case "session":
			if l.Session == nil {
				return cfg, nil, fmt.Errorf("line %d: session line has no session", lineNo)
			}
			if l.Version > Version {
				return cfg, nil, fmt.Errorf("log format version %d is newer than this program understands (%d)", l.Version, Version)
			}
			cfg = l.Session.Config()
			haveSession = true
		case "sample":
			k := key{l.Sample.Target, l.Sample.Seq}
			if i, ok := idx[k]; ok {
				samples[i] = *l.Sample
			} else {
				idx[k] = len(samples)
				samples = append(samples, *l.Sample)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return cfg, nil, err
	}
	if !haveSession {
		return cfg, nil, fmt.Errorf("not a pingmon log: no session line")
	}
	return cfg, samples, nil
}
