package book

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
	if e.WouldCrosses() != 1 {
		t.Fatalf("a would-cross rejection must be counted: WouldCrosses() = %d, want 1", e.WouldCrosses())
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
	if e.View() != nil {
		t.Fatal("a failed bootstrap must publish nothing: no crossed snapshot may leak to readers")
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
	if e.Resyncs() != 1 {
		t.Fatalf("Resyncs() = %d, want exactly 1 (the disconnect; a trade must not resync)", e.Resyncs())
	}
	if v := e.View(); v == nil || v.Crosses() {
		t.Fatal("view must exist and not cross after the run")
	}
}

// A trade must leave the book and the published view untouched: no mutation, no
// publish, no resync. The view pointer is compared directly — publish always stores a
// fresh *View, so an unchanged pointer proves no publish happened at all.
func TestTradeIsNoOpOnBookAndView(t *testing.T) {
	m := &mockSource{
		snap: market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		events: []source.Event{
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 99, Size: 2}}}},
			{Kind: source.EventTrade, Trade: market.Trade{Price: 100, Size: 1, Side: market.Bid}},
		},
	}
	e := New(m, 5)
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(context.Background()); err != nil { // the delta
		t.Fatal(err)
	}
	after := e.View()
	if after == nil || after.LastSeq != 11 {
		t.Fatalf("after the delta View.LastSeq = %v, want 11", after)
	}
	if _, err := e.Step(context.Background()); err != nil { // the trade
		t.Fatal(err)
	}
	if e.View() != after {
		t.Fatal("a trade must not publish: the view pointer changed")
	}
	if e.Resyncs() != 0 || e.Disconnects() != 0 {
		t.Fatalf("a trade must not resync or count a disconnect: resyncs=%d disconnects=%d", e.Resyncs(), e.Disconnects())
	}
}

// A rejected crossing delta must never become visible, even transiently inside the
// same Step. The resync's snapshot refetch is made to fail, so Step returns before any
// legitimate re-publish could paper over a premature one; publish always stores a
// fresh *View, so an unchanged pointer proves the crossed book was never published.
func TestCrossedStateNeverPublishedEvenTransiently(t *testing.T) {
	m := &mockSource{
		snap:         market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		snapErr:      errors.New("snapshot unavailable"),
		snapErrAfter: 1, // bootstrap's snapshot succeeds; the resync's refetch fails
		events: []source.Event{
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 101, Size: 1}}}}, // bid at the ask: crosses
		},
	}
	e := New(m, 5)
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	vBefore := e.View()
	_, err := e.Step(context.Background())
	if err == nil {
		t.Fatal("the failing resync must surface an error")
	}
	if e.WouldCrosses() != 1 {
		t.Fatalf("WouldCrosses() = %d, want 1 (the delta was applied and rejected)", e.WouldCrosses())
	}
	if e.View() != vBefore {
		t.Fatal("the crossed state leaked: a new view was published between apply and resync")
	}
}

// The metric counters must match injected truth: one gap per scheduled gap fault, one
// disconnect per scheduled disconnect fault (observability that doesn't lie).
func TestEngineMetricCounters(t *testing.T) {
	faults := map[int]source.Fault{
		10: source.FaultGap, 25: source.FaultGap, 40: source.FaultGap,
		15: source.FaultDisconnect, 50: source.FaultDisconnect,
	}
	eng := New(source.New(source.Config{Venue: "v", Symbol: "s", Seed: 5, Steps: 70, Faults: faults}), 5)
	if err := eng.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if eng.Gaps() != 3 {
		t.Errorf("Gaps() = %d, want 3 (one per injected gap fault)", eng.Gaps())
	}
	if eng.Disconnects() != 2 {
		t.Errorf("Disconnects() = %d, want 2 (one per injected disconnect fault)", eng.Disconnects())
	}
	if eng.Resyncs() < 5 {
		t.Errorf("Resyncs() = %d, want >= 5 (each gap and disconnect resyncs)", eng.Resyncs())
	}
}

// sumChecksum is a deterministic stand-in for a venue checksum (the real CRC32 lives in
// the adapter): it sums the level prices, so a test can predict the expected value and
// the engine's drift check is exercised over the actual reconstructed book.
func sumChecksum(bids, asks []market.Level) uint32 {
	var s uint32
	for _, l := range bids {
		s += uint32(l.Price)
	}
	for _, l := range asks {
		s += uint32(l.Price)
	}
	return s
}

// A matching checksum on every snapshot and delta must apply cleanly with no resync.
func TestChecksumMatchApplies(t *testing.T) {
	m := &mockSource{
		snap: market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}, Checksum: 201},
		events: []source.Event{
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 99, Size: 1}}, Checksum: 300}}, // 100+99+101
		},
	}
	e := New(m, 5).WithChecksum(sumChecksum)
	if err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.ChecksumMismatches() != 0 || e.Resyncs() != 0 {
		t.Fatalf("matching checksums must not resync: mismatches=%d resyncs=%d", e.ChecksumMismatches(), e.Resyncs())
	}
	if v := e.View(); v.Crosses() || v.LastSeq != 11 {
		t.Fatalf("view = %+v, want uncrossed at seq 11", v)
	}
}

// A wrong checksum on a delta is drift: the engine must detect it and resync.
func TestChecksumMismatchResyncs(t *testing.T) {
	m := &mockSource{
		snap: market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}, Checksum: 201},
		events: []source.Event{
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 99, Size: 1}}, Checksum: 999}}, // wrong
		},
	}
	e := New(m, 5).WithChecksum(sumChecksum)
	if err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.ChecksumMismatches() != 1 {
		t.Fatalf("ChecksumMismatches() = %d, want 1 (drift detected)", e.ChecksumMismatches())
	}
	if e.Resyncs() == 0 {
		t.Fatal("checksum drift must trigger a resync")
	}
}

// A snapshot whose checksum never matches is unusable: bootstrap must fail loudly with
// the checksum sentinel (not the crossed one), counting each rejected attempt.
func TestBootstrapChecksumMismatchFailsLoud(t *testing.T) {
	m := &mockSource{
		snap: market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}, Checksum: 999}, // never matches 201
	}
	e := New(m, 5).WithChecksum(sumChecksum)
	err := e.Bootstrap(context.Background())
	if err == nil || !errors.Is(err, market.ErrChecksumMismatch) {
		t.Fatalf("a checksum-mismatched snapshot must fail with ErrChecksumMismatch, got %v", err)
	}
	if e.ChecksumMismatches() != maxBindAttempts {
		t.Fatalf("ChecksumMismatches() = %d, want %d (one per rejected attempt)", e.ChecksumMismatches(), maxBindAttempts)
	}
	if e.View() != nil {
		t.Fatal("a failed bootstrap must publish nothing: no unverified snapshot may leak to readers")
	}
}

// The checksum covers exactly the top 10 levels per side (Kraken), independent of the
// published depth: a book deeper than 10 binds when its top-10 checksum matches.
func TestChecksumCoversTopTen(t *testing.T) {
	var bids, asks []market.Level
	for i := range 12 { // 12 levels per side; checksum is over the top 10 only
		bids = append(bids, market.Level{Price: market.Price(100 - i), Size: 1})
		asks = append(asks, market.Level{Price: market.Price(200 + i), Size: 1})
	}
	var want uint32
	for i := range 10 { // literal 10 (not the constant under test): top-10 bids 100..91, asks 200..209
		want += uint32(100-i) + uint32(200+i)
	}
	m := &mockSource{snap: market.Snapshot{LastUpdateID: 10, Bids: bids, Asks: asks, Checksum: want}}
	if err := New(m, 5).WithChecksum(sumChecksum).Bootstrap(context.Background()); err != nil {
		t.Fatalf("a top-10 checksum must bind a deeper book (depth-independent), got %v", err)
	}
}

// A snapshot older than the stream (fetched before buffered deltas) must not strand
// the engine: the first delta is a forward gap, the resync refetches a current
// snapshot, and the run converges. Counts are derived: one gap, one resync.
func TestEngineRecoversFromStaleSnapshot(t *testing.T) {
	ctx := context.Background()
	src := source.New(source.Config{Venue: "v", Symbol: "s", Seed: 11, Steps: 60, StaleSnapshot: true})
	// Advance the stream past the captured snapshot (seq 1000 -> 1030) before the
	// engine binds, the way a buffered live feed outruns a REST snapshot.
	for range 30 {
		if _, ok := src.Next(ctx); !ok {
			t.Fatal("source ended during pre-consume")
		}
	}
	e := New(src, 5)
	if err := e.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if e.Gaps() != 1 || e.Resyncs() != 1 || e.Disconnects() != 0 {
		t.Fatalf("gaps=%d resyncs=%d disconnects=%d, want 1/1/0 (stale bind, one gap, one refetch)",
			e.Gaps(), e.Resyncs(), e.Disconnects())
	}
	truth, err := src.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v := e.View()
	if v == nil || v.LastSeq != truth.LastUpdateID {
		t.Fatalf("view seq = %v, want truth %d", v, truth.LastUpdateID)
	}
	if !levelsEqual(v.Bids, clipLevels(truth.Bids, 5)) || !levelsEqual(v.Asks, clipLevels(truth.Asks, 5)) {
		t.Fatal("after recovery the view must equal the truth's top-5")
	}
}

// The latency observer must fire once per applied-and-published delta, measuring
// from the event's Received stamp — and stay silent for unstamped events, trades,
// and drops, so a scripted source can never smuggle a zero-span sample in.
func TestLatencyObserverReportsAppliedStampedDeltas(t *testing.T) {
	m := &mockSource{
		snap: market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		events: []source.Event{
			{Kind: source.EventDelta, Received: time.Now().Add(-time.Millisecond), Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 99, Size: 2}}}},
			{Kind: source.EventTrade, Received: time.Now(), Trade: market.Trade{Price: 100, Size: 1, Side: market.Bid}},
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 12, LastSeq: 12, Bids: []market.Level{{Price: 98, Size: 1}}}},                       // unstamped: applied, not reported
			{Kind: source.EventDelta, Received: time.Now(), Delta: market.Delta{FirstSeq: 12, LastSeq: 12, Bids: []market.Level{{Price: 98, Size: 9}}}}, // duplicate: dropped, not reported
		},
	}
	var got []time.Duration
	e := New(m, 5).WithLatencyObserver(func(d time.Duration) { got = append(got, d) })
	if err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("observer fired %d times, want exactly 1 (the stamped applied delta)", len(got))
	}
	if got[0] < time.Millisecond {
		t.Fatalf("span = %v, want >= the 1ms the stamp was backdated by", got[0])
	}
	if v := e.View(); v.LastSeq != 12 {
		t.Fatalf("unstamped delta must still apply: LastSeq = %d, want 12", v.LastSeq)
	}
}

// pqChecksum sums prices and sizes, so a level with a stale quantity changes the
// value — the property the windowed-feed ghost regression below depends on.
func pqChecksum(bids, asks []market.Level) uint32 {
	var s uint32
	for _, l := range bids {
		s += uint32(l.Price) + uint32(l.Size)
	}
	for _, l := range asks {
		s += uint32(l.Price) + uint32(l.Size)
	}
	return s
}

// windowSnap builds a 10-level-per-side snapshot (bids 100..91, asks 200..209, size 1)
// with its pqChecksum — the shape of a Kraken depth-10 subscription.
func windowSnap() market.Snapshot {
	var snap market.Snapshot
	for i := range 10 {
		snap.Bids = append(snap.Bids, market.Level{Price: market.Price(100 - i), Size: 1})
		snap.Asks = append(snap.Asks, market.Level{Price: market.Price(200 + i), Size: 1})
	}
	snap.LastUpdateID = 10
	snap.Checksum = pqChecksum(snap.Bids, snap.Asks)
	return snap
}

// Regression for the live-Kraken checksum drift found on 2026-07-05: a windowed feed
// never deletes levels that fall out of its window, so a kept level becomes a stale
// ghost. Here bid 91 falls out when 101 arrives and is then deleted venue-side —
// unseen. When 101 is deleted, the venue's window re-admits 90 (resent fresh), not
// the dead 91. With WithMaxDepth the engine's book matches the venue's window exactly
// and both updates apply cleanly; without truncation the ghost 91 sits in the top-10,
// the checksum mismatches, and this test fails on the exact counters.
func TestMaxDepthDropsOutOfWindowGhosts(t *testing.T) {
	snap := windowSnap()
	up1Bids := []market.Level{{Price: 101, Size: 2}} // pushes 91 out of the window
	window1 := append([]market.Level{{Price: 101, Size: 2}}, snap.Bids[:9]...)
	up2Bids := []market.Level{{Price: 101, Size: 0}, {Price: 90, Size: 4}} // 101 gone; 90 re-enters fresh (91 died out of sight)
	window2 := append(append([]market.Level{}, snap.Bids[:9]...), market.Level{Price: 90, Size: 4})

	m := &mockSource{
		snap: snap,
		events: []source.Event{
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: up1Bids, Checksum: pqChecksum(window1, snap.Asks)}},
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 12, LastSeq: 12, Bids: up2Bids, Checksum: pqChecksum(window2, snap.Asks)}},
		},
	}
	e := New(m, 10).WithChecksum(pqChecksum).WithMaxDepth(10)
	if err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.ChecksumMismatches() != 0 || e.Resyncs() != 0 {
		t.Fatalf("mismatches=%d resyncs=%d, want 0/0 (the out-of-window ghost must be truncated away)",
			e.ChecksumMismatches(), e.Resyncs())
	}
	v := e.View()
	if got := v.Bids[len(v.Bids)-1]; got != (market.Level{Price: 90, Size: 4}) {
		t.Fatalf("10th bid = %+v, want the re-admitted fresh {90,4}, never the ghost 91", got)
	}
}

// A snapshot deeper than the window must be truncated at bind, or it seeds the same
// ghosts bootstrap-first — on either side.
func TestMaxDepthTruncatesBootstrapSnapshot(t *testing.T) {
	snap := windowSnap()
	snap.Bids = append(snap.Bids, market.Level{Price: 89, Size: 1})  // an 11th bid
	snap.Asks = append(snap.Asks, market.Level{Price: 210, Size: 1}) // an 11th ask
	snap.Checksum = 0
	e := New(&mockSource{snap: snap}, 10).WithMaxDepth(10)
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if nb, na := len(e.book.bids), len(e.book.asks); nb != 10 || na != 10 {
		t.Fatalf("book holds %d bids / %d asks after a windowed bootstrap, want 10/10", nb, na)
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
	// The cross at 320 is deterministic: the gap's resync rebinds to current truth and
	// 252..319 are clean, so the engine is caught up and must apply-and-reject exactly
	// one crossing delta while the readers race it.
	src := source.New(source.Config{Venue: "v", Symbol: "s", Seed: 3, Steps: 400,
		Faults: map[int]source.Fault{100: source.FaultDisconnect, 250: source.FaultGap, 320: source.FaultCross}})
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
					_ = e.WouldCrosses()
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
	if e.WouldCrosses() != 1 {
		t.Fatalf("WouldCrosses() = %d, want 1 (the scheduled cross was applied and rejected)", e.WouldCrosses())
	}
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
