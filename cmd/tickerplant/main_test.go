package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func synthetic(seed int64, steps int) source.Source {
	return source.New(source.Config{Venue: "synthetic", Symbol: "DEMO", Seed: seed, Steps: steps})
}

// The whole pipeline (source -> engine -> fan-out) reconstructs an uncrossed book and
// delivers it.
func TestRunReconstructsUncrossedBook(t *testing.T) {
	v, stats, err := run(context.Background(), quietLog(), synthetic(7, 400), config{depth: 10})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if v == nil {
		t.Fatal("run must return a final view")
	}
	if v.Crosses() {
		t.Fatalf("final view crosses: %v >= %v", v.Bids[0], v.Asks[0])
	}
	if stats.Delivered == 0 {
		t.Fatal("expected the consumer to receive views")
	}
}

// A faulted run still converges to an uncrossed book (the engine resyncs).
func TestRunWithFaultsStaysUncrossed(t *testing.T) {
	v, _, err := run(context.Background(), quietLog(), synthetic(3, 600), config{depth: 10})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if v == nil || v.Crosses() {
		t.Fatal("faulted run must still end uncrossed")
	}
}

// A cancelled context stops the pipeline cleanly, surfacing the error (no hang/leak).
func TestRunContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := run(ctx, quietLog(), synthetic(1, 400), config{depth: 10}); err == nil {
		t.Fatal("a cancelled context must surface an error")
	}
}

// Exercise the paced + per-update logging paths (and logTop on a populated view).
func TestRunPacedWithLogging(t *testing.T) {
	v, _, err := run(context.Background(), quietLog(), synthetic(7, 50), config{depth: 5, every: 1, pace: 1})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if v == nil || v.Crosses() {
		t.Fatal("paced run must end uncrossed")
	}
}

// staleSource bootstraps fine but its snapshot stays behind the stream, so every
// delta is a gap — the engine livelock-bounds and run surfaces the error.
type staleSource struct{ i int }

func (s *staleSource) Next(context.Context) (source.Event, bool) {
	s.i++
	if s.i > 100 {
		return source.Event{}, false
	}
	seq := market.Sequence(100 + s.i)
	return source.Event{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: seq, LastSeq: seq, Bids: []market.Level{{Price: 99, Size: 1}}}}, true
}
func (s *staleSource) Snapshot(context.Context) (market.Snapshot, error) {
	return market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}, nil
}
func (s *staleSource) Close() error { return nil }

func TestRunStepErrorPropagates(t *testing.T) {
	if _, _, err := run(context.Background(), quietLog(), &staleSource{}, config{depth: 5}); err == nil {
		t.Fatal("a livelocking source must surface an error from run")
	}
}

func TestLogTopHandlesEmptyAndPopulated(t *testing.T) {
	logTop(quietLog(), &book.View{}) // empty sides render "-", not a panic
	logTop(quietLog(), &book.View{
		LastSeq: 5,
		Bids:    []market.Level{{Price: 100, Size: 1}},
		Asks:    []market.Level{{Price: 101, Size: 2}},
	})
}
