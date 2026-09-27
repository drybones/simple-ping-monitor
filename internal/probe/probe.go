// Package probe sends ICMP echo requests and reports what happens to them.
//
// A Source emits a Sent event for every request just before it goes on the
// wire and a Reply event for every echo reply that comes back, including
// duplicates and replies that arrive very late. It does no interpretation:
// deciding what counts as lost, late or duplicated is the monitor's job.
package probe

import (
	"context"
	"time"
)

type Kind uint8

const (
	Sent Kind = iota
	Reply
)

type Event struct {
	Target int
	Kind   Kind
	Seq    int64
	Time   time.Time
}

// Source produces events for one target until ctx is cancelled. Sequence
// numbers start at 0 and increase by one per request.
type Source interface {
	Run(ctx context.Context, target int, out chan<- Event) error
}
