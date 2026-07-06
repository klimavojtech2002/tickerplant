// Package source defines the transport port the order-book engine reads through,
// and a deterministic synthetic implementation used to prove the engine correct
// without any live socket (ADR-0001, ADR-0005).
//
// The port is pull-based: the engine asks for the next event and, on bootstrap or
// resync, for a fresh snapshot. Pull (rather than a channel) keeps a run
// single-threaded and therefore fully deterministic given a seed — the basis of the
// deterministic simulation that proves the thesis. Live venue adapters (slice 0004)
// satisfy the same port by buffering their push socket behind Next.
package source

import (
	"context"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// EventKind tags the payload carried by an Event.
type EventKind uint8

const (
	// EventDelta carries an incremental book update.
	EventDelta EventKind = iota
	// EventTrade carries an executed fill.
	EventTrade
	// EventDisconnected signals the transport dropped; the engine must re-bootstrap.
	EventDisconnected
)

func (k EventKind) String() string {
	switch k {
	case EventDelta:
		return "delta"
	case EventTrade:
		return "trade"
	case EventDisconnected:
		return "disconnected"
	default:
		return "unknown"
	}
}

// Event is one item from a Source's stream. The field matching Kind is the valid
// one; the others are zero. Received is when the adapter took the raw input this
// event was decoded from off its transport (the synthetic source stamps emission) —
// the start of the internal-latency span the metrics report; zero means unstamped
// and no latency is recorded for the event.
type Event struct {
	Kind     EventKind
	Received time.Time
	Delta    market.Delta
	Trade    market.Trade
}

// Source is the transport port. The synthetic source and the live venue adapters
// implement it identically, so the engine cannot tell them apart.
type Source interface {
	// Next returns the next event and true, or a zero Event and false when the
	// source is exhausted or ctx is cancelled.
	Next(ctx context.Context) (Event, bool)
	// Snapshot returns a fresh full book, used on bootstrap and on resync after a
	// gap, checksum mismatch, or disconnect.
	Snapshot(ctx context.Context) (market.Snapshot, error)
	// Close releases the source. It is safe to call once; further Next calls return
	// false.
	Close() error
}
