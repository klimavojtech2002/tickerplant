package book

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// seededEngine returns an engine with a tiny uncrossed book (bid 100, ask 101) at
// lastSeq 10, for white-box applyDelta tests.
func seededEngine() *Engine {
	e := &Engine{book: &book{}, lastSeq: 10, depth: 5}
	e.book.set(market.Bid, 100, 1)
	e.book.set(market.Ask, 101, 1)
	return e
}

func TestApplyDeltaStaleDropped(t *testing.T) {
	e := seededEngine()
	if o := e.applyDelta(market.Delta{FirstSeq: 5, LastSeq: 9}); o != dropped {
		t.Fatalf("older delta: outcome %v, want dropped", o)
	}
	if o := e.applyDelta(market.Delta{FirstSeq: 10, LastSeq: 10}); o != dropped {
		t.Fatalf("delta at lastSeq: outcome %v, want dropped (idempotent)", o)
	}
}

func TestApplyDeltaGapResync(t *testing.T) {
	e := seededEngine()
	o := e.applyDelta(market.Delta{FirstSeq: 12, LastSeq: 12, Bids: []market.Level{{Price: 99, Size: 2}}})
	if o != needResync {
		t.Fatalf("forward gap (12 over lastSeq+1=11): outcome %v, want needResync", o)
	}
}

func TestApplyDeltaExactAndStraddle(t *testing.T) {
	e := seededEngine()
	if o := e.applyDelta(market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 99, Size: 2}}}); o != applied {
		t.Fatalf("exact next delta: outcome %v, want applied", o)
	}
	if e.lastSeq != 11 {
		t.Fatalf("lastSeq = %d, want 11", e.lastSeq)
	}
	// a straddling delta whose range covers lastSeq+1 (12) must apply, not resync
	if o := e.applyDelta(market.Delta{FirstSeq: 9, LastSeq: 13, Asks: []market.Level{{Price: 102, Size: 1}}}); o != applied {
		t.Fatalf("straddling delta: outcome %v, want applied", o)
	}
	if e.lastSeq != 13 {
		t.Fatalf("lastSeq = %d, want 13", e.lastSeq)
	}
}

func TestApplyDeltaWouldCrossResync(t *testing.T) {
	e := seededEngine() // bid 100, ask 101
	o := e.applyDelta(market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 101, Size: 1}}})
	if o != needResync {
		t.Fatalf("a delta that crosses the book must needResync, got %v", o)
	}
}

func TestApplyDeltaIsAbsoluteNotIncrement(t *testing.T) {
	e := seededEngine()
	e.applyDelta(market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 100, Size: 5}}})
	if bb, _ := e.book.bestBid(); bb.Size != 5 {
		t.Fatalf("delta size must be absolute: best bid size = %d, want 5 (not summed)", bb.Size)
	}
}

func TestBootstrapCrossingFailsLoud(t *testing.T) {
	src := source.New(source.Config{Venue: "v", Symbol: "s", Seed: 1, Steps: 1, CrossingSnapshot: true})
	e := New(src, 5)
	err := e.Bootstrap(context.Background())
	if err == nil || !errors.Is(err, market.ErrCrossed) {
		t.Fatalf("crossing bootstrap must fail loudly with ErrCrossed, got %v", err)
	}
	if e.Resyncs() != maxBindAttempts {
		t.Fatalf("bootstrap must retry exactly %d times then fail loudly, got %d", maxBindAttempts, e.Resyncs())
	}
}

// mockSource is a scripted source for engine cases the legal-by-construction
// synthetic generator cannot produce (trades, a permanently-stale snapshot).
type mockSource struct {
	snap         market.Snapshot
	snapErr      error
	snapErrAfter int // once snapErr is set, start erroring after this many OK snapshots
	snapCalls    int
	events       []source.Event
	i            int
}

var _ source.Source = (*mockSource)(nil)

func (m *mockSource) Next(context.Context) (source.Event, bool) {
	if m.i >= len(m.events) {
		return source.Event{}, false
	}
	e := m.events[m.i]
	m.i++
	return e, true
}
func (m *mockSource) Snapshot(context.Context) (market.Snapshot, error) {
	m.snapCalls++
	if m.snapErr != nil && m.snapCalls > m.snapErrAfter {
		return market.Snapshot{}, m.snapErr
	}
	return m.snap, nil
}
func (m *mockSource) Close() error { return nil }

func TestRunHandlesTradeAndDisconnect(t *testing.T) {
	m := &mockSource{
		snap: market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		events: []source.Event{
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 99, Size: 2}}}},
			{Kind: source.EventTrade, Trade: market.Trade{Price: 100, Size: 1, Side: market.Bid}},
			{Kind: source.EventDisconnected},
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Asks: []market.Level{{Price: 102, Size: 1}}}},
		},
	}
	e := New(m, 5)
	if err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.Resyncs() == 0 {
		t.Fatal("disconnect must trigger a resync")
	}
	if v := e.View(); v == nil || v.Crosses() {
		t.Fatal("view must exist and not cross after the run")
	}
}

// staleSource returns a mock whose snapshot is permanently behind the stream, so each
// of n deltas is an unbridgeable gap — a no-progress resync.
func staleSource(n int) *mockSource {
	events := make([]source.Event, n)
	for i := range events {
		seq := market.Sequence(100 + i)
		events[i] = source.Event{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: seq, LastSeq: seq, Bids: []market.Level{{Price: 99, Size: 1}}}}
	}
	return &mockSource{
		snap:   market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		events: events,
	}
}

// A pathological no-progress feed must fail loudly after exactly the bound, never
// livelock — and not one resync sooner (pins the > vs >= boundary).
func TestResyncLivelockBoundary(t *testing.T) {
	if err := New(staleSource(maxConsecutiveResyncs), 5).Run(context.Background()); err != nil {
		t.Fatalf("at the bound (%d no-progress resyncs) it must not yet fail: %v", maxConsecutiveResyncs, err)
	}
	if err := New(staleSource(maxConsecutiveResyncs+1), 5).Run(context.Background()); !errors.Is(err, market.ErrStaleSnapshot) {
		t.Fatalf("one past the bound must fail with ErrStaleSnapshot, got %v", err)
	}
}

// Resyncs that make progress reset the guard: many disconnects spread across a healthy
// feed produce far more than maxConsecutiveResyncs total resyncs without tripping it.
func TestResyncResetsOnProgress(t *testing.T) {
	faults := map[int]source.Fault{}
	for s := 10; s <= 200; s += 10 {
		faults[s] = source.FaultDisconnect
	}
	e := New(source.New(source.Config{Venue: "v", Symbol: "s", Seed: 7, Steps: 250, Faults: faults}), 5)
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("spread-out disconnects on a healthy feed must not trip the livelock guard: %v", err)
	}
	if e.Resyncs() <= maxConsecutiveResyncs {
		t.Fatalf("expected > %d total resyncs (each reset by progress), got %d", maxConsecutiveResyncs, e.Resyncs())
	}
}

func TestBootstrapPublishesUncrossedView(t *testing.T) {
	src := source.New(source.Config{Venue: "v", Symbol: "s", Seed: 1, Steps: 0})
	e := New(src, 5)
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := e.View()
	if v == nil {
		t.Fatal("bootstrap must publish a view")
	}
	if v.Crosses() {
		t.Fatal("bootstrapped view must not cross")
	}
}

func TestViewCrossesEdgeCases(t *testing.T) {
	var nilV *View
	if nilV.Crosses() {
		t.Error("nil view must not cross")
	}
	if (&View{Bids: []market.Level{{Price: 100, Size: 1}}}).Crosses() {
		t.Error("one-sided view (bids only) must not cross")
	}
	if (&View{Asks: []market.Level{{Price: 100, Size: 1}}}).Crosses() {
		t.Error("one-sided view (asks only) must not cross")
	}
}

// View.Crosses must return true for a genuinely crossed view — the simulation's
// safety assertion routes through it, so a regression here would silently pass.
func TestViewCrossesPositive(t *testing.T) {
	if !(&View{Bids: []market.Level{{Price: 101, Size: 1}}, Asks: []market.Level{{Price: 100, Size: 1}}}).Crosses() {
		t.Error("best bid 101 > best ask 100 must cross")
	}
	if !(&View{Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 100, Size: 1}}}).Crosses() {
		t.Error("best bid == best ask must cross")
	}
}

// The published view carries the last applied sequence, from bootstrap and each delta.
func TestViewReflectsLastSeq(t *testing.T) {
	m := &mockSource{
		snap:   market.Snapshot{LastUpdateID: 42, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		events: []source.Event{{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 43, LastSeq: 43, Bids: []market.Level{{Price: 99, Size: 1}}}}},
	}
	e := New(m, 5)
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.View().LastSeq; got != 42 {
		t.Fatalf("after bootstrap View.LastSeq = %d, want 42", got)
	}
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.View().LastSeq; got != 43 {
		t.Fatalf("after delta View.LastSeq = %d, want 43", got)
	}
}

// Readers hammering View()/Resyncs() while the writer runs must be race-free
// (ADR-0008 lock-free reads). Run under -race in CI to exercise the contract.
func TestConcurrentReaders(t *testing.T) {
	src := source.New(source.Config{Venue: "v", Symbol: "s", Seed: 3, Steps: 400,
		Faults: map[int]source.Fault{100: source.FaultDisconnect, 250: source.FaultGap}})
	e := New(src, 10)
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
					if v := e.View(); v != nil && v.Crosses() {
						t.Error("reader observed a crossed view")
					}
					_ = e.Resyncs()
				}
			}
		})
	}
	for {
		ok, err := e.Step(context.Background())
		if err != nil {
			t.Error(err)
			break
		}
		if !ok {
			break
		}
	}
	close(done)
	wg.Wait()
}

func TestCrossesSnapshotDefensiveUnsorted(t *testing.T) {
	// Levels out of order: the real best bid (100) and best ask (60) are not first.
	s := market.Snapshot{
		Bids: []market.Level{{Price: 50, Size: 1}, {Price: 100, Size: 1}},
		Asks: []market.Level{{Price: 200, Size: 1}, {Price: 60, Size: 1}},
	}
	if !crossesSnapshot(s) {
		t.Fatal("unsorted crossed snapshot must be detected (best bid 100 >= best ask 60)")
	}
	if crossesSnapshot(market.Snapshot{Bids: []market.Level{{Price: 50, Size: 1}}}) {
		t.Fatal("one-sided snapshot must not be crossed")
	}
}

func TestBootstrapAndRunPropagateSnapshotError(t *testing.T) {
	boom := errors.New("snapshot failed")
	if err := New(&mockSource{snapErr: boom}, 5).Bootstrap(context.Background()); err == nil {
		t.Fatal("Bootstrap must propagate a Snapshot error")
	}
	if err := New(&mockSource{snapErr: boom}, 5).Run(context.Background()); err == nil {
		t.Fatal("Run must propagate a bootstrap error")
	}
}

func TestDisconnectResyncErrorPropagates(t *testing.T) {
	m := &mockSource{
		snap:         market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		snapErr:      errors.New("snapshot failed on resync"),
		snapErrAfter: 1, // bootstrap succeeds; the disconnect's resync snapshot fails
		events:       []source.Event{{Kind: source.EventDisconnected}},
	}
	if err := New(m, 5).Run(context.Background()); err == nil {
		t.Fatal("a disconnect whose resync snapshot fails must surface the error")
	}
}

func TestStepContextCancelled(t *testing.T) {
	src := source.New(source.Config{Venue: "v", Symbol: "s", Seed: 1, Steps: 50})
	e := New(src, 5)
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ok, _ := e.Step(ctx); ok {
		t.Fatal("a cancelled context must stop Step")
	}
}
