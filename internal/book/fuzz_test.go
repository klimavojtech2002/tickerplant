package book

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// FuzzEngineSteps drives the engine with scripts decoded from the input: legal changes mixed with
// gaps, reorders, duplicates, coalesced deltas, disconnects, crossing and locking deltas, crossed
// and locked and old snapshots, and stale deltas. After every step the published book must not
// cross and its sides must be strictly ordered; after a fault-free tail the engine must equal an
// independent oracle of the venue's book. A failing input is saved by the fuzzer under
// testdata/fuzz/FuzzEngineSteps and replayed by a plain `go test`.
//
// Run it for a while with:
//
//	go test -run=^$ -fuzz=FuzzEngineSteps -fuzztime=10m ./internal/book
func FuzzEngineSteps(f *testing.F) {
	for _, s := range fuzzSeedList() {
		f.Add(s.data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if _, _, err := runFuzz(data, nil); err != nil {
			t.Fatal(err)
		}
	})
}

// checkView checks the two invariants of a published view directly, without the code under test:
// the best bid is below the best ask, and each side is strictly ordered (bids falling, asks rising).
func checkView(v *View) error {
	if len(v.Bids) > 0 && len(v.Asks) > 0 && v.Bids[0].Price >= v.Asks[0].Price {
		return fmt.Errorf("view crosses or locks: best bid %d, best ask %d", v.Bids[0].Price, v.Asks[0].Price)
	}
	for i := 1; i < len(v.Bids); i++ {
		if v.Bids[i].Price >= v.Bids[i-1].Price {
			return fmt.Errorf("bids not strictly descending at %d: %v then %v", i, v.Bids[i-1], v.Bids[i])
		}
	}
	for i := 1; i < len(v.Asks); i++ {
		if v.Asks[i].Price <= v.Asks[i-1].Price {
			return fmt.Errorf("asks not strictly ascending at %d: %v then %v", i, v.Asks[i-1], v.Asks[i])
		}
	}
	return nil
}

// acceptableError reports whether the script itself could have caused err. Both gates are necessary
// conditions only: they exist so the fuzz does not fail on an error the input asked for, and no
// mutant is expected to be caught by them (the bound tests below pin the exact limits).
func acceptableError(err error, sc *fuzzScript) bool {
	switch {
	case errors.Is(err, market.ErrStaleSnapshot):
		return sc.faults+sc.oldSnapshots >= 17 // the livelock guard needs 17 resyncs in a row
	case errors.Is(err, market.ErrCrossed):
		return sc.armedBad >= 8 // a bind gives up after eight rejected snapshots
	}
	return false
}

// runFuzz builds the script for data, runs the engine over it, and returns the engine and the script
// with the first violation found, if any. tweak, when set, changes the source before the run.
func runFuzz(data []byte, tweak func(*fuzzSource)) (*Engine, *fuzzScript, error) {
	sc := fuzzBuild(data)
	src := newFuzzSource(sc)
	if tweak != nil {
		tweak(src)
	}
	e, err := New(src, sc.depth)
	if err != nil {
		return nil, sc, err
	}
	ctx := context.Background()
	if err := e.Bootstrap(ctx); err != nil {
		return e, sc, fmt.Errorf("the first bootstrap must succeed: %w", err)
	}
	var lastView *View
	var lastGen uint64
	check := func(when string) error {
		v := e.View()
		if v == nil {
			return fmt.Errorf("%s: no published view", when)
		}
		if err := checkView(v); err != nil {
			return fmt.Errorf("%s: %w", when, err)
		}
		if v != lastView {
			if v.Gen <= lastGen {
				return fmt.Errorf("%s: view generation %d did not rise above %d", when, v.Gen, lastGen)
			}
			lastView, lastGen = v, v.Gen
		}
		return nil
	}
	if err := check("after bootstrap"); err != nil {
		return e, sc, err
	}
	stopped := false
	for step := 1; ; step++ {
		ok, err := e.Step(ctx)
		if err != nil {
			if !acceptableError(err, sc) {
				return e, sc, fmt.Errorf("step %d: unexpected error: %w", step, err)
			}
			stopped = true
			if cerr := check(fmt.Sprintf("after step %d (accepted error)", step)); cerr != nil {
				return e, sc, cerr
			}
			break
		}
		if !ok {
			break
		}
		if !e.Ready() {
			return e, sc, fmt.Errorf("step %d: engine not ready after a successful step", step)
		}
		if err := check(fmt.Sprintf("after step %d", step)); err != nil {
			return e, sc, err
		}
	}
	// A spurious resync is a bug: each one must be explained by a fault the script contains.
	switch {
	case e.Resyncs() > sc.faults+sc.oldSnapshots:
		return e, sc, fmt.Errorf("Resyncs = %d, more than the %d faults and %d old snapshots explain", e.Resyncs(), sc.faults, sc.oldSnapshots)
	case e.Gaps() > sc.gapOps+sc.reorderOps+sc.oldSnapshots:
		return e, sc, fmt.Errorf("Gaps = %d, more than the script explains", e.Gaps())
	case e.Disconnects() > sc.disconnectOps:
		return e, sc, fmt.Errorf("Disconnects = %d, more than the %d in the script", e.Disconnects(), sc.disconnectOps)
	case e.WouldCrosses() > sc.crossOps:
		return e, sc, fmt.Errorf("WouldCrosses = %d, more than the %d crossing deltas in the script", e.WouldCrosses(), sc.crossOps)
	}
	if stopped {
		return e, sc, nil
	}
	return e, sc, converged(e, sc)
}

// converged compares the engine with the oracle's final state: top-N prices and sizes, and the sequence.
func converged(e *Engine, sc *fuzzScript) error {
	v := e.View()
	if want := sc.final.top(market.Bid, sc.depth); !levelsEqual(v.Bids, want) {
		return fmt.Errorf("bids differ after the fault-free tail:\n engine: %v\n oracle: %v", v.Bids, want)
	}
	if want := sc.final.top(market.Ask, sc.depth); !levelsEqual(v.Asks, want) {
		return fmt.Errorf("asks differ after the fault-free tail:\n engine: %v\n oracle: %v", v.Asks, want)
	}
	if v.LastSeq != sc.final.seq {
		return fmt.Errorf("LastSeq %d differs from the oracle's %d", v.LastSeq, sc.final.seq)
	}
	return nil
}

// ---- tests ------------------------------------------------------------------------------------

func TestFuzzSeedsPass(t *testing.T) {
	for _, s := range fuzzSeedList() {
		if _, _, err := runFuzz(s.data, nil); err != nil {
			t.Errorf("seed %s: %v", s.name, err)
		}
	}
}

// Each seed contains what it was written to contain, so a change to the encoding cannot quietly
// turn a seed into a different test.
func TestFuzzSeedsMeetTheirIntent(t *testing.T) {
	for _, s := range fuzzSeedList() {
		if sc := fuzzBuild(s.data); !s.check(sc) {
			t.Errorf("seed %s no longer has the shape it was written for: %+v", s.name, *sc)
		}
	}
}

// Every input decodes, within the operation limit.
func TestFuzzDecodeReadsAtMostThreeHundredOperations(t *testing.T) {
	if got := fuzzBuild(nil).ops; got != 0 {
		t.Errorf("an empty input read %d operations, want 0", got)
	}
	if got := fuzzBuild(make([]byte, 13+2*299)).ops; got != 299 {
		t.Errorf("299 operations in the input read %d", got)
	}
	if got := fuzzBuild(make([]byte, 5000)).ops; got != 300 {
		t.Errorf("a long input read %d operations, want exactly 300", got)
	}
}

func TestFuzzDecodeIsTotal(t *testing.T) {
	inputs := [][]byte{nil, {0}, {0xFF}, make([]byte, 1024), []byte(strings.Repeat("\xff", 1024))}
	rng := rand.New(rand.NewSource(1))
	for range 20 {
		b := make([]byte, rng.Intn(2000))
		rng.Read(b)
		inputs = append(inputs, b)
	}
	for _, in := range inputs {
		sc := fuzzBuild(in)
		if ops := sc.faults + sc.legalOps + sc.oldSnapshots + sc.armedBad; ops > 4*fuzzMaxOps {
			t.Errorf("input of %d bytes produced an implausible %d operations", len(in), ops)
		}
		if len(sc.items) > 2*fuzzMaxOps+fuzzTailDeltas+8 {
			t.Errorf("input of %d bytes produced %d items", len(in), len(sc.items))
		}
	}
}

// The script never produces a fault the engine cannot detect: the oracle stays inside its price
// bands and never empties a side, every emitted delta is at most one sequence ahead of the oracle,
// and the one delta that is (a cross) has a non-zero size and a price that crosses or locks.
func TestFuzzScriptNeverCreatesAnUndetectableLie(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for n := range 80 {
		data := make([]byte, 20+rng.Intn(700))
		rng.Read(data)
		sc := fuzzBuild(data)
		for i, it := range sc.items {
			if it.isMarker {
				continue
			}
			o := it.after
			for _, l := range o.top(market.Bid, len(o.bids)) {
				if l.Price < 1 || l.Price > 1000 {
					t.Fatalf("input %d item %d: bid %d outside the bid band", n, i, l.Price)
				}
			}
			for _, l := range o.top(market.Ask, len(o.asks)) {
				if l.Price < 1001 || l.Price > 2000 {
					t.Fatalf("input %d item %d: ask %d outside the ask band", n, i, l.Price)
				}
			}
			if len(o.bids) == 0 || len(o.asks) == 0 {
				t.Fatalf("input %d item %d: a side of the oracle is empty", n, i)
			}
			d := it.ev.Delta
			if it.ev.Kind != source.EventDelta || d.LastSeq <= o.seq || d.LastSeq == 0 {
				continue
			}
			// Ahead of the oracle: only a cross may be, by exactly one, with one sized level that crosses or locks.
			if d.LastSeq != o.seq+1 || len(d.Bids)+len(d.Asks) != 1 {
				t.Fatalf("input %d item %d: delta %+v is ahead of the oracle (seq %d) and is not a single-level cross", n, i, d, o.seq)
			}
			if len(d.Bids) == 1 {
				if p := d.Bids[0].Price; d.Bids[0].Size == 0 || (p <= 2000 && p != o.bestAsk()) {
					t.Fatalf("input %d item %d: cross bid %+v neither crosses nor locks (best ask %d)", n, i, d.Bids[0], o.bestAsk())
				}
			} else if p := d.Asks[0].Price; d.Asks[0].Size == 0 || p > o.bestBid() {
				t.Fatalf("input %d item %d: cross ask %+v neither crosses nor locks (best bid %d)", n, i, d.Asks[0], o.bestBid())
			}
		}
	}
}

func TestFuzzScriptCleanRun(t *testing.T) {
	sc := fuzzBuild(newSeed(6).legal(0, 3, 2).legal(1, 5, 4).legal(0, 9, 1).bytes())
	e := mustNew(t, newFuzzSource(sc), sc.depth)
	ctx := context.Background()
	if err := e.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	for i, it := range sc.items {
		if it.isMarker {
			continue
		}
		if ok, err := e.Step(ctx); err != nil || !ok {
			t.Fatalf("event %d: step = %v, %v", i, ok, err)
		}
		v := e.View()
		if !levelsEqual(v.Bids, it.after.top(market.Bid, sc.depth)) || !levelsEqual(v.Asks, it.after.top(market.Ask, sc.depth)) || v.LastSeq != it.after.seq {
			t.Fatalf("event %d: the engine differs from the oracle on a run without faults", i)
		}
	}
	if e.Resyncs() != 0 || e.Gaps() != 0 || sc.faults != 0 {
		t.Fatalf("a clean script resynced: resyncs %d, gaps %d", e.Resyncs(), e.Gaps())
	}
}

func TestCheckView(t *testing.T) {
	lv := func(ps ...market.Price) []market.Level {
		out := make([]market.Level, len(ps))
		for i, p := range ps {
			out[i] = market.Level{Price: p, Size: 1}
		}
		return out
	}
	cases := []struct {
		name string
		v    View
		want string // substring of the error, empty = fine
	}{
		{"fine", View{Bids: lv(900, 890), Asks: lv(1100, 1110)}, ""},
		{"one-sided", View{Bids: lv(900)}, ""},
		{"empty", View{}, ""},
		{"crossed", View{Bids: lv(1200), Asks: lv(1100)}, "crosses or locks"},
		{"locked", View{Bids: lv(1100), Asks: lv(1100)}, "crosses or locks"},
		{"bids ascending", View{Bids: lv(890, 900), Asks: lv(1100)}, "bids not strictly descending"},
		{"bids repeated", View{Bids: lv(900, 900), Asks: lv(1100)}, "bids not strictly descending"},
		{"asks descending", View{Bids: lv(900), Asks: lv(1110, 1100)}, "asks not strictly ascending"},
		{"asks repeated", View{Bids: lv(900), Asks: lv(1100, 1100)}, "asks not strictly ascending"},
	}
	for _, tc := range cases {
		err := checkView(&tc.v)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: checkView = %v, want %q", tc.name, err, tc.want)
		}
	}
}

// The convergence check can fail: a source whose snapshots report sizes one too large leaves the
// engine wrong after a resync, and the run says so.
func TestFuzzCatchesABrokenEngine(t *testing.T) {
	seed := newSeed(8).legal(0, 3, 2).disconnect().legal(1, 5, 4).bytes()
	if _, _, err := runFuzz(seed, nil); err != nil {
		t.Fatalf("the seed fails on a correct source: %v", err)
	}
	_, _, err := runFuzz(seed, func(s *fuzzSource) { s.shiftSizes = true })
	if err == nil || !strings.Contains(err.Error(), "differ after the fault-free tail") {
		t.Fatalf("a wrong snapshot was not caught: %v", err)
	}
}

// stepAll runs the script to its end and returns the engine and the first error.
func stepAll(t *testing.T, data []byte) (*Engine, error) {
	t.Helper()
	sc := fuzzBuild(data)
	e := mustNew(t, newFuzzSource(sc), sc.depth)
	ctx := context.Background()
	if err := e.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		ok, err := e.Step(ctx)
		if err != nil || !ok {
			return e, err
		}
	}
}

// Sixteen resyncs without an applied delta in between are the most the engine allows; the
// seventeenth gives up with the stale-snapshot error.
func TestResyncLivelockBound(t *testing.T) {
	disconnects := func(n int) []byte {
		s := newSeed(4).legal(0, 3, 2)
		for range n {
			s.disconnect()
		}
		return s.bytes()
	}
	if _, err := stepAll(t, disconnects(16)); err != nil {
		t.Fatalf("16 consecutive disconnects: %v, want none", err)
	}
	if _, err := stepAll(t, disconnects(17)); !errors.Is(err, market.ErrStaleSnapshot) {
		t.Fatalf("17 consecutive disconnects: %v, want %v", err, market.ErrStaleSnapshot)
	}
}

// A delta the engine drops is not progress: stale deltas between disconnects do not reset the count.
func TestStaleDeltasDoNotResetTheLivelockCounter(t *testing.T) {
	s := newSeed(4).legal(0, 3, 2)
	for i := range 17 {
		s.disconnect().marker(3, 1, 5+i)
	}
	if _, err := stepAll(t, s.bytes()); !errors.Is(err, market.ErrStaleSnapshot) {
		t.Fatalf("17 disconnects separated by stale deltas: %v, want %v", err, market.ErrStaleSnapshot)
	}
}

// Faults that are each followed by applied deltas never add up: the counter resets on progress.
func TestSpacedFaultsNeverLivelock(t *testing.T) {
	s := newSeed(4).legal(0, 3, 2)
	for i := range 20 {
		switch i % 4 {
		case 0:
			s.gap(0, i%32, 1+i%7)
		case 1:
			s.reorder(1, i%32, 1+i%7)
		case 2:
			s.cross(i%2, i%3 == 0, i%32, 2)
		default:
			s.disconnect()
		}
		for j := range 3 {
			s.legal(j%2, (i+j)%32, 1+j)
		}
	}
	e, err := stepAll(t, s.bytes())
	if err != nil {
		t.Fatalf("twenty spaced faults: %v, want none", err)
	}
	if e.Resyncs() < 17 {
		t.Fatalf("only %d resyncs: the test no longer exercises the reset", e.Resyncs())
	}
}

// A bind gives up after eight rejected snapshots in a row and keeps going on the ninth chance only
// if the eighth attempt is good: seven bad snapshots recover, eight fail.
func TestBindFailsAfterEightBadSnapshots(t *testing.T) {
	build := func(bad int) []byte {
		s := newSeed(4).legal(0, 3, 2)
		for bad > 0 {
			n := min(bad, 4)
			s.marker(0, n, 0)
			bad -= n
		}
		return s.disconnect().legal(1, 4, 2).bytes()
	}
	if _, err := stepAll(t, build(7)); err != nil {
		t.Fatalf("seven bad snapshots: %v, want recovery", err)
	}
	if _, err := stepAll(t, build(8)); !errors.Is(err, market.ErrCrossed) {
		t.Fatalf("eight bad snapshots: %v, want %v", err, market.ErrCrossed)
	}
}
