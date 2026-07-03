package book

import (
	"context"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

const simDepth = 20

// scenarioForSeed deterministically derives a fault schedule from the seed: some
// runs are clean, others inject a gap, a reorder, a duplicate, one illegal crossing
// delta, and sometimes a disconnect, all well before the stream ends so the engine can
// converge. The cross sits at 330+seed%30 -> [330,359], disjoint from the gap [50,79],
// reorder [120,150], duplicate [200,229], and disconnect [260,289] windows and before
// Steps (400), so at the cross step the engine is provably caught up (e.lastSeq==s.seq):
// the cross is applied and rejected, never degraded into a gap.
func scenarioForSeed(seed int64) map[int]source.Fault {
	if seed%5 == 0 {
		return nil
	}
	f := map[int]source.Fault{
		50 + int(seed%30):  source.FaultGap,
		120 + int(seed%30): source.FaultReorder,
		200 + int(seed%30): source.FaultDuplicate,
		330 + int(seed%30): source.FaultCross,
	}
	if seed%3 == 0 {
		f[260+int(seed%30)] = source.FaultDisconnect
	}
	return f
}

// TestSimulation drives the engine over many seeds and fault scenarios against the
// source's independent truth oracle, asserting continuously that the published view
// never crosses and, at the quiescent end, that the engine has converged exactly to
// the truth. It exercises gap, reorder, duplicate, disconnect, and crossing-delta
// recovery. The crossing delta (FaultCross) drives the apply-path never-crosses check
// itself: the engine must apply the bad update, detect the cross, reject it, and resync
// without ever publishing the crossed state — proven here, not merely asserted, by the
// exact per-seed WouldCrosses count below. The exact bind boundary stays pinned by the
// white-box tests in engine_test.go.
func TestSimulation(t *testing.T) {
	ctx := context.Background()
	faultedSeeds, faultedWithResync := 0, 0
	for s := range 300 {
		seed := int64(s)
		faults := scenarioForSeed(seed)
		src := source.New(source.Config{Venue: "sim", Symbol: "S", Seed: seed, Steps: 400, Faults: faults})
		e := New(src, simDepth)
		if err := e.Bootstrap(ctx); err != nil {
			t.Fatalf("seed %d: bootstrap: %v", seed, err)
		}
		assertNeverCrosses(t, e, seed)
		for {
			ok, err := e.Step(ctx)
			if err != nil {
				t.Fatalf("seed %d: step: %v", seed, err)
			}
			if !ok {
				break
			}
			assertNeverCrosses(t, e, seed)
		}
		truth, err := src.Snapshot(ctx)
		if err != nil {
			t.Fatalf("seed %d: truth snapshot: %v", seed, err)
		}
		assertConverged(t, e, truth, seed)
		// Every faulted seed schedules exactly one crossing delta; a clean seed schedules
		// none. The engine must apply, reject, and count each cross exactly once. A cross
		// that silently degraded into a gap (engine not caught up) would read 0 here and
		// fail loudly — this exact count is the anti-fake-green guard for the cross path.
		wantCross := 0
		if len(faults) > 0 {
			wantCross = 1
		}
		if got := e.WouldCrosses(); got != wantCross {
			t.Fatalf("seed %d: WouldCrosses() = %d, want %d", seed, got, wantCross)
		}
		if len(faults) > 0 {
			faultedSeeds++
			if e.Resyncs() > 0 {
				faultedWithResync++
			}
		}
	}
	// Guard against a fake-green run: most faulted scenarios must actually have driven
	// the engine through resyncs, or the fault/resync path was barely exercised.
	if faultedSeeds == 0 || faultedWithResync < faultedSeeds/2 {
		t.Fatalf("fault/resync path under-exercised: %d of %d faulted seeds resynced", faultedWithResync, faultedSeeds)
	}
}

func assertNeverCrosses(t *testing.T, e *Engine, seed int64) {
	t.Helper()
	v := e.View()
	if v == nil {
		t.Fatalf("seed %d: no published view", seed)
	}
	if v.Crosses() {
		t.Fatalf("seed %d: published view crosses: %v >= %v", seed, v.Bids[0], v.Asks[0])
	}
}

func assertConverged(t *testing.T, e *Engine, truth market.Snapshot, seed int64) {
	t.Helper()
	v := e.View()
	if want := clipLevels(truth.Bids, simDepth); !levelsEqual(v.Bids, want) {
		t.Fatalf("seed %d: bids did not converge to truth\n engine: %v\n truth:  %v", seed, v.Bids, want)
	}
	if want := clipLevels(truth.Asks, simDepth); !levelsEqual(v.Asks, want) {
		t.Fatalf("seed %d: asks did not converge to truth\n engine: %v\n truth:  %v", seed, v.Asks, want)
	}
}

func levelsEqual(a, b []market.Level) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func clipLevels(levels []market.Level, n int) []market.Level {
	if n > len(levels) {
		n = len(levels)
	}
	return levels[:n]
}
