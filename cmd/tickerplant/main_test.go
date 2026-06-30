package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestLiveCadence(t *testing.T) {
	cases := []struct {
		every          int
		everySet, live bool
		want           int
	}{
		{500, false, false, 500}, // synthetic, default cadence preserved
		{500, false, true, 1},    // live, default -> log every update
		{500, true, true, 500},   // live, explicit -every 500 -> honoured, not overridden
		{7, true, false, 7},      // synthetic, explicit
		{7, true, true, 7},       // live, explicit
	}
	for _, c := range cases {
		if got := liveCadence(c.every, c.everySet, c.live); got != c.want {
			t.Errorf("liveCadence(%d, set=%v, live=%v) = %d, want %d", c.every, c.everySet, c.live, got, c.want)
		}
	}
}

func TestNewSourceSynthetic(t *testing.T) {
	src, pace, err := newSource(context.Background(), false, "", "", 1, 10, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if pace != 5*time.Millisecond {
		t.Fatalf("synthetic pace = %v, want it preserved (5ms)", pace)
	}
	if _, ok := src.Next(context.Background()); !ok {
		t.Fatal("synthetic source must yield events")
	}
}

func TestNewSourceUnknownVenue(t *testing.T) {
	if _, _, err := newSource(context.Background(), true, "kraken", "X", 1, 10, 0); err == nil {
		t.Fatal("an unknown live venue must error")
	}
}

type closeTracker struct {
	source.Source
	closed bool
}

func (c *closeTracker) Close() error { c.closed = true; return c.Source.Close() }

// run must release the source on exit (so a live WebSocket is not leaked).
func TestRunClosesSource(t *testing.T) {
	ct := &closeTracker{Source: synthetic(1, 5)}
	if _, err := run(context.Background(), quietLog(), ct, config{depth: 5}); err != nil {
		t.Fatal(err)
	}
	if !ct.closed {
		t.Fatal("run must Close the source on exit")
	}
}

// scriptSource feeds a fixed snapshot and event sequence, for testing run's wiring.
type scriptSource struct {
	snap   market.Snapshot
	events []source.Event
	i      int
}

func (s *scriptSource) Next(context.Context) (source.Event, bool) {
	if s.i >= len(s.events) {
		return source.Event{}, false
	}
	e := s.events[s.i]
	s.i++
	return e, true
}
func (s *scriptSource) Snapshot(context.Context) (market.Snapshot, error) { return s.snap, nil }
func (s *scriptSource) Close() error                                      { return nil }

// run must publish only when the view advances: a stale duplicate (dropped by the
// engine) must not be re-broadcast.
func TestRunPublishesOnlyOnAdvance(t *testing.T) {
	src := &scriptSource{
		snap: market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		events: []source.Event{
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 100, Size: 2}}}}, // advances
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 100, Size: 2}}}}, // duplicate: engine drops, no advance
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 12, LastSeq: 12, Asks: []market.Level{{Price: 102, Size: 3}}}}, // advances
		},
	}
	res, err := run(context.Background(), quietLog(), src, config{depth: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.stats.Delivered != 3 { // bootstrap(10) + seq 11 + seq 12; the duplicate must not re-publish
		t.Fatalf("Delivered = %d, want 3 (a stale duplicate must not be re-broadcast)", res.stats.Delivered)
	}
}

func TestLogTopRendersBidAndAsk(t *testing.T) {
	var buf bytes.Buffer
	logTop(slog.New(slog.NewTextHandler(&buf, nil)), &book.View{
		LastSeq: 5,
		Bids:    []market.Level{{Price: 100, Size: 2}},
		Asks:    []market.Level{{Price: 101, Size: 3}},
	})
	if out := buf.String(); !strings.Contains(out, "100 x 2") || !strings.Contains(out, "101 x 3") {
		t.Fatalf("logTop output %q is missing the rendered bid/ask", out)
	}
}

func synthetic(seed int64, steps int) source.Source {
	return source.New(source.Config{Venue: "synthetic", Symbol: "DEMO", Seed: seed, Steps: steps})
}

// faultedSynthetic injects a gap, reorder, duplicate, and disconnect mid-stream (well
// before the end so the engine can converge), so a run over it genuinely drives the
// wired pipeline through resyncs rather than a clean stream.
func faultedSynthetic(seed int64, steps int) source.Source {
	return source.New(source.Config{
		Venue: "synthetic", Symbol: "DEMO", Seed: seed, Steps: steps,
		Faults: map[int]source.Fault{
			50:  source.FaultGap,
			120: source.FaultReorder,
			200: source.FaultDuplicate,
			260: source.FaultDisconnect,
		},
	})
}

// The whole pipeline (source -> engine -> fan-out) reconstructs an uncrossed book and
// delivers it.
func TestRunReconstructsUncrossedBook(t *testing.T) {
	res, err := run(context.Background(), quietLog(), synthetic(7, 400), config{depth: 10})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.view == nil {
		t.Fatal("run must return a final view")
	}
	if res.view.Crosses() {
		t.Fatalf("final view crosses: %v >= %v", res.view.Bids[0], res.view.Asks[0])
	}
	if res.stats.Delivered == 0 {
		t.Fatal("expected the consumer to receive views")
	}
}

// A faulted run (gap, reorder, duplicate, disconnect injected mid-stream) still ends
// with an uncrossed book, and the engine is observed to have actually resynced through
// the faults — the gap and reorder each surface as a sequence gap, the disconnect as a
// disconnect, and all three force a resync. This proves the wired pipeline recovers end
// to end, not merely that a clean stream finishes.
func TestRunWithFaultsStaysUncrossed(t *testing.T) {
	res, err := run(context.Background(), quietLog(), faultedSynthetic(3, 600), config{depth: 10})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.view == nil || res.view.Crosses() {
		t.Fatal("faulted run must still end uncrossed")
	}
	// Exact counts (deterministic, seed-independent): the gap and reorder each surface as
	// one forward sequence gap; the duplicate is dropped idempotently (no gap, no resync);
	// the disconnect is one disconnect. So 2 gaps + 1 disconnect = 3 resyncs, no more — a
	// stricter "==" also guards against the duplicate wrongly triggering a fourth resync.
	if res.gaps != 2 {
		t.Fatalf("expected exactly 2 sequence gaps (gap + reorder faults), got %d", res.gaps)
	}
	if res.disconnects != 1 {
		t.Fatalf("expected exactly 1 disconnect from the injected fault, got %d", res.disconnects)
	}
	if res.resyncs != 3 {
		t.Fatalf("expected exactly 3 resyncs (gap, reorder, disconnect; duplicate is dropped), got %d", res.resyncs)
	}
}

// A cancelled context stops the pipeline cleanly, surfacing the error (no hang/leak).
func TestRunContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := run(ctx, quietLog(), synthetic(1, 400), config{depth: 10}); err == nil {
		t.Fatal("a cancelled context must surface an error")
	}
}

// Exercise the paced + per-update logging paths (and logTop on a populated view).
func TestRunPacedWithLogging(t *testing.T) {
	res, err := run(context.Background(), quietLog(), synthetic(7, 50), config{depth: 5, every: 1, pace: 1})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.view == nil || res.view.Crosses() {
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
	if _, err := run(context.Background(), quietLog(), &staleSource{}, config{depth: 5}); err == nil {
		t.Fatal("a livelocking source must surface an error from run")
	}
}

// An empty book (no bids or asks) renders each side as "-", not a panic or a zero level.
func TestLogTopRendersEmptySides(t *testing.T) {
	var buf bytes.Buffer
	logTop(slog.New(slog.NewTextHandler(&buf, nil)), &book.View{})
	if out := buf.String(); !strings.Contains(out, "bid=-") || !strings.Contains(out, "ask=-") {
		t.Fatalf("empty view must render bid=- and ask=-, got %q", out)
	}
}
