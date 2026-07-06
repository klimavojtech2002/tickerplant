package source

import (
	"context"
	"fmt"
	"hash/fnv"
	"slices"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

func cfg(seed int64, steps int) Config {
	return Config{Venue: "synthetic", Symbol: "BTC-USD", Seed: seed, Steps: steps}
}

func drain(s *Synthetic) []Event {
	ctx := context.Background()
	var out []Event
	for {
		e, ok := s.Next(ctx)
		if !ok {
			break
		}
		out = append(out, e)
	}
	return out
}

// hashEvents serializes a stream deterministically (no maps, fields in fixed order)
// and hashes it, so a stable hash proves the stream is reproducible.
func hashEvents(evs []Event) uint64 {
	h := fnv.New64a()
	for _, e := range evs {
		fmt.Fprintf(h, "k%d|", e.Kind)
		if e.Kind == EventDelta {
			fmt.Fprintf(h, "f%d-l%d|", e.Delta.FirstSeq, e.Delta.LastSeq)
			for _, lv := range e.Delta.Bids {
				fmt.Fprintf(h, "b%d:%d|", lv.Price, lv.Size)
			}
			for _, lv := range e.Delta.Asks {
				fmt.Fprintf(h, "a%d:%d|", lv.Price, lv.Size)
			}
		}
	}
	return h.Sum64()
}

func crossedSnap(s market.Snapshot) bool {
	if len(s.Bids) == 0 || len(s.Asks) == 0 {
		return false
	}
	return s.Bids[0].Price >= s.Asks[0].Price
}

func TestDeterministic(t *testing.T) {
	a := hashEvents(drain(New(cfg(42, 500))))
	b := hashEvents(drain(New(cfg(42, 500))))
	if a != b {
		t.Fatalf("same seed produced different streams: %#x vs %#x", a, b)
	}
	if c := hashEvents(drain(New(cfg(43, 500)))); a == c {
		t.Fatal("different seeds produced identical streams")
	}
}

// A committed golden hash catches determinism drift across runs and machines:
// math/rand (seeded) and fnv are platform-stable, so this value must not change
// except on a deliberate change to the generator.
func TestGoldenHash(t *testing.T) {
	const want = uint64(0x25398cd343c33bd1)
	got := hashEvents(drain(New(cfg(1, 200))))
	if got != want {
		t.Fatalf("golden hash = %#x (set want to this; it must change only with a deliberate generator change)", got)
	}
}

// A second golden over a faulted scenario locks fault-stream determinism, not just
// the clean path — a regression in gap/reorder/duplicate/disconnect generation is
// caught loudly, not merely "a fault is still present".
func TestGoldenHashFaulted(t *testing.T) {
	c := cfg(2, 200)
	c.Faults = map[int]Fault{15: FaultGap, 40: FaultReorder, 75: FaultDuplicate, 110: FaultDisconnect}
	const want = uint64(0x18be535d8095ae94)
	got := hashEvents(drain(New(c)))
	if got != want {
		t.Fatalf("faulted golden hash = %#x (set want to this on a deliberate generator change)", got)
	}
}

// A third golden locks the FaultCross stream: the crossing delta's fixed shape (one bid
// at crossBidPrice) and its placement must not drift silently, just like the other faults.
func TestGoldenHashCrossStream(t *testing.T) {
	c := cfg(2, 200)
	c.Faults = map[int]Fault{15: FaultGap, 40: FaultReorder, 75: FaultDuplicate, 110: FaultCross}
	const want = uint64(0x94082b767239db15)
	got := hashEvents(drain(New(c)))
	if got != want {
		t.Fatalf("cross-stream golden hash = %#x (set want to this on a deliberate generator change)", got)
	}
}

// The clean stream only ever drives the truth through legal, uncrossed states.
func TestCleanRunNeverCrosses(t *testing.T) {
	s := New(cfg(7, 1000))
	ctx := context.Background()
	for {
		if _, ok := s.Next(ctx); !ok {
			break
		}
		snap, err := s.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if crossedSnap(snap) {
			t.Fatalf("truth crossed: best bid %d >= best ask %d", snap.Bids[0].Price, snap.Asks[0].Price)
		}
	}
}

// Faults perturb the emitted stream, never the truth oracle: even under a mix of
// faults — including the illegal crossing delta — the truth book stays legal
// (uncrossed) throughout.
func TestTruthStaysLegalUnderFaults(t *testing.T) {
	c := cfg(11, 300)
	c.Faults = map[int]Fault{30: FaultGap, 60: FaultReorder, 90: FaultDuplicate, 120: FaultDisconnect, 150: FaultCross}
	s := New(c)
	ctx := context.Background()
	for {
		if _, ok := s.Next(ctx); !ok {
			break
		}
		snap, err := s.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if crossedSnap(snap) {
			t.Fatal("a fault corrupted the truth oracle: it crossed")
		}
	}
}

// FaultCross injects one illegal crossing delta on the emitted stream only: the emitted
// delta is a bid above the whole ask band (applying it would cross), while the truth
// oracle and its sequence are left untouched, so the engine can resync back to a legal
// book. This proves the injection is stream-only — the property the simulation relies on.
func TestFaultCrossEmitsCrossingDelta(t *testing.T) {
	c := cfg(5, 50)
	c.Faults = map[int]Fault{0: FaultCross}
	s := New(c)
	ctx := context.Background()

	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := s.Next(ctx)
	if !ok || ev.Kind != EventDelta {
		t.Fatalf("FaultCross must emit a delta event, got kind %d ok=%v", ev.Kind, ok)
	}
	if len(ev.Delta.Asks) != 0 || len(ev.Delta.Bids) != 1 || ev.Delta.Bids[0].Price != crossBidPrice {
		t.Fatalf("cross delta must be exactly one bid at %d, got %+v", crossBidPrice, ev.Delta)
	}
	// Stream-only: the truth's content and its sequence are unchanged across the fault.
	after, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastUpdateID != before.LastUpdateID {
		t.Fatalf("FaultCross must not advance the truth sequence: before %d, after %d", before.LastUpdateID, after.LastUpdateID)
	}
	if !slices.Equal(before.Bids, after.Bids) || !slices.Equal(before.Asks, after.Asks) {
		t.Fatal("FaultCross must not mutate the truth oracle")
	}
}

func deltas(evs []Event) []market.Delta {
	var ds []market.Delta
	for _, e := range evs {
		if e.Kind == EventDelta {
			ds = append(ds, e.Delta)
		}
	}
	return ds
}

func TestFaultGapProducesHole(t *testing.T) {
	c := cfg(5, 60)
	c.Faults = map[int]Fault{20: FaultGap}
	ds := deltas(drain(New(c)))
	hole := false
	for i := 1; i < len(ds); i++ {
		if ds[i].FirstSeq != ds[i-1].LastSeq+1 {
			hole = true
		}
	}
	if !hole {
		t.Fatal("gap fault did not produce a sequence hole")
	}
}

func TestFaultDuplicate(t *testing.T) {
	c := cfg(5, 60)
	c.Faults = map[int]Fault{20: FaultDuplicate}
	ds := deltas(drain(New(c)))
	seen := make(map[market.Sequence]int)
	for _, d := range ds {
		seen[d.LastSeq]++
	}
	repeated := false
	for _, n := range seen {
		if n > 1 {
			repeated = true
		}
	}
	if !repeated {
		t.Fatal("duplicate fault did not repeat a sequence")
	}
}

func TestFaultReorderOutOfOrder(t *testing.T) {
	c := cfg(5, 60)
	c.Faults = map[int]Fault{20: FaultReorder}
	ds := deltas(drain(New(c)))
	backwards := false
	for i := 1; i < len(ds); i++ {
		if ds[i].FirstSeq < ds[i-1].FirstSeq {
			backwards = true
		}
	}
	if !backwards {
		t.Fatal("reorder fault did not produce an out-of-order sequence")
	}
}

func TestFaultDisconnect(t *testing.T) {
	c := cfg(5, 60)
	c.Faults = map[int]Fault{20: FaultDisconnect}
	found := false
	for _, e := range drain(New(c)) {
		if e.Kind == EventDisconnected {
			found = true
		}
	}
	if !found {
		t.Fatal("disconnect fault did not emit EventDisconnected")
	}
}

func TestCrossingSnapshot(t *testing.T) {
	c := cfg(9, 10)
	c.CrossingSnapshot = true
	snap, err := New(c).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !crossedSnap(snap) {
		t.Fatal("CrossingSnapshot did not return a crossed book")
	}
}

// StaleSnapshot models correctness.md §3 step 5: the stream has already advanced
// past the captured snapshot, so the first Snapshot is too old to bind (a hole over
// the consumed sequences), and the refetch returns a current one.
func TestStaleSnapshotThenFresh(t *testing.T) {
	c := cfg(9, 60)
	c.StaleSnapshot = true
	s := New(c)
	ctx := context.Background()
	for range 30 { // advance the stream well past the captured (seq 1000) snapshot
		s.Next(ctx)
	}
	stale, _ := s.Snapshot(ctx) // old: seq 1000, but the stream is now far ahead
	fresh, _ := s.Snapshot(ctx) // refetch: current
	if stale.LastUpdateID >= fresh.LastUpdateID {
		t.Fatalf("stale snapshot not behind the advanced stream: stale=%d fresh=%d", stale.LastUpdateID, fresh.LastUpdateID)
	}
}

// The seeded initial book has the documented composition (correctness.md §3): bids
// 850..900 and asks 1100..1150 at depth 6, populated and uncrossed. This pins
// seedInitialBook's prices — the book the engine binds its first snapshot to.
func TestSeededInitialBook(t *testing.T) {
	snap, err := New(cfg(1, 0)).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Bids) != 6 || len(snap.Asks) != 6 {
		t.Fatalf("depth = %d bids / %d asks, want 6 each", len(snap.Bids), len(snap.Asks))
	}
	wantBids := []market.Price{900, 890, 880, 870, 860, 850}
	wantAsks := []market.Price{1100, 1110, 1120, 1130, 1140, 1150}
	for i, p := range wantBids {
		if snap.Bids[i].Price != p || snap.Bids[i].Size < 1 {
			t.Errorf("bid %d = %+v, want price %d and a populated size", i, snap.Bids[i], p)
		}
	}
	for i, p := range wantAsks {
		if snap.Asks[i].Price != p || snap.Asks[i].Size < 1 {
			t.Errorf("ask %d = %+v, want price %d and a populated size", i, snap.Asks[i], p)
		}
	}
	if snap.LastUpdateID != 1000 {
		t.Errorf("LastUpdateID = %d, want 1000", snap.LastUpdateID)
	}
}

func TestClosedSourceStops(t *testing.T) {
	s := New(cfg(1, 50))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Next(context.Background()); ok {
		t.Fatal("closed source returned an event")
	}
	if _, err := s.Snapshot(context.Background()); err == nil {
		t.Fatal("closed source Snapshot must error")
	}
}

func TestCancelledContextStops(t *testing.T) {
	s := New(cfg(1, 50))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := s.Next(ctx); ok {
		t.Fatal("cancelled context must stop Next")
	}
	if _, err := s.Snapshot(ctx); err == nil {
		t.Fatal("cancelled context must make Snapshot error")
	}
}

func TestEventKindString(t *testing.T) {
	for k, want := range map[EventKind]string{
		EventDelta:        "delta",
		EventTrade:        "trade",
		EventDisconnected: "disconnected",
		EventKind(99):     "unknown",
	} {
		if got := k.String(); got != want {
			t.Errorf("EventKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}

// A fault scheduled at the last step must end the stream cleanly, not hang or panic.
func TestFaultAtStreamEnd(t *testing.T) {
	cg := cfg(3, 2)
	cg.Faults = map[int]Fault{1: FaultGap} // gap with nothing after it
	if ds := deltas(drain(New(cg))); len(ds) != 1 {
		t.Fatalf("gap at end: got %d deltas, want 1", len(ds))
	}
	cr := cfg(3, 1)
	cr.Faults = map[int]Fault{0: FaultReorder} // reorder with nothing to swap with
	if ds := deltas(drain(New(cr))); len(ds) != 1 {
		t.Fatalf("reorder at end: got %d deltas, want 1", len(ds))
	}
}

// Every emitted event carries a Received stamp — the start of the internal-latency
// span; the golden hashes ignore it, so determinism is untouched.
func TestEventsAreStamped(t *testing.T) {
	c := cfg(3, 50)
	c.Faults = map[int]Fault{5: FaultGap, 10: FaultReorder, 15: FaultDuplicate, 20: FaultDisconnect, 25: FaultCross}
	for i, e := range drain(New(c)) {
		if e.Received.IsZero() {
			t.Fatalf("event %d (%v) has a zero Received stamp", i, e.Kind)
		}
	}
	// reorder scheduled on the final step takes the end-of-stream branch: still stamped
	end := cfg(4, 30)
	end.Faults = map[int]Fault{29: FaultReorder}
	for i, e := range drain(New(end)) {
		if e.Received.IsZero() {
			t.Fatalf("end-reorder event %d (%v) has a zero Received stamp", i, e.Kind)
		}
	}
}
