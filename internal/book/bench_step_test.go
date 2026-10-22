package book

import (
	"context"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// deltaStream hands out contiguous, non-crossing deltas forever without allocating, so a
// benchmark of Step measures the engine and not the source.
type deltaStream struct {
	next market.Sequence
	bids [1]market.Level
}

var _ source.Source = (*deltaStream)(nil)

func (d *deltaStream) Next(context.Context) (source.Event, bool) {
	d.next++
	d.bids[0] = market.Level{Price: 99, Size: market.Size(1 + d.next%7)}
	return source.Event{Kind: source.EventDelta, Delta: market.Delta{
		FirstSeq: d.next, LastSeq: d.next, Bids: d.bids[:],
	}}, true
}
func (d *deltaStream) Close() error { return nil }
func (d *deltaStream) Snapshot(context.Context) (market.Snapshot, error) {
	d.next = 10
	return market.Snapshot{LastUpdateID: 10,
		Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}, nil
}

// BenchmarkStepDelta times one applied delta end to end: apply, top-N view, publish.
func BenchmarkStepDelta(b *testing.B) {
	e, err := New(&deltaStream{}, 10)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if err := e.Bootstrap(ctx); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := e.Step(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkClockStamp times the work an event adds to Step: one clock read and two
// atomic stores.
func BenchmarkClockStamp(b *testing.B) {
	e := &Engine{}
	b.ReportAllocs()
	for range b.N {
		e.stampEvent()
	}
}
