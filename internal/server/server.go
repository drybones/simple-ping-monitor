// Package server serves the web UI and its JSON / Server-Sent Events API.
package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/drybones/simple-ping-monitor/internal/monitor"
	"github.com/drybones/simple-ping-monitor/web"
)

type Server struct {
	Monitor *monitor.Monitor
	Label   string // shown in the page header, e.g. the replayed file name
}

type snapshot struct {
	Live    bool             `json:"live"`
	Label   string           `json:"label,omitempty"`
	Session monitor.Session  `json:"session"`
	Samples []monitor.Sample `json:"samples"`
	Summary monitor.Summary  `json:"summary"`
}

func (s *Server) Handler() http.Handler {
	static, err := fs.Sub(web.Static, "static")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/snapshot", s.snapshot)
	mux.HandleFunc("GET /api/events", s.events)
	return mux
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	m := s.Monitor
	samples := m.Samples()
	if samples == nil {
		samples = []monitor.Sample{}
	}
	snap := snapshot{
		Live:    m.Live(),
		Label:   s.Label,
		Session: m.Config().Session(),
		Samples: samples,
		Summary: m.Summary(time.Now()),
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(snap)
}

// events streams sample changes and summaries. A client should load
// /api/snapshot after (re)connecting; updates it already has are harmless
// because each carries the full state of its sample.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if !s.Monitor.Live() {
		http.Error(w, "replay has no live events", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	updates, cancel := s.Monitor.Subscribe()
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	fmt.Fprint(w, "retry: 1000\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case u, ok := <-updates:
			if !ok {
				return // too slow; the client will reconnect and resync
			}
			event, data := "sample", any(u.Sample)
			if u.Summary != nil {
				event, data = "summary", u.Summary
			}
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
			// Batch whatever else is already queued into one flush.
			if len(updates) == 0 {
				flusher.Flush()
			}
		}
	}
}
