package book

import (
	"context"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// The engine must heal from the whole chaos schedule: for many seeds and ten periods the
// published view never crosses and is always ordered, the run ends equal to the truth, and the
// counters are exactly what each period's faults cause.
//
// Per period: the gap and the reorder each read as one sequence gap (a reordered delta arrives
// ahead of what the engine waits for, and its early partner is later dropped as stale), so
// Gaps is 2; gap, reorder, disconnect and cross each start a resync, so Resyncs is 4; the cross
// is applied, found crossing and refused once, and the disconnect is handled once. The duplicate
// and the coalesced delta cost nothing. Nothing is a refused or failed snapshot.
func TestChaosScheduleHeals(t *testing.T) {
	const cycles = 10
	ctx := context.Background()
	for seed := range int64(40) {
		src := source.New(source.Config{
			Venue: "chaos", Symbol: "S", Seed: seed, Steps: cycles * source.ChaosPeriod,
			FaultAt: source.ChaosSchedule(seed),
		})
		e := mustNew(t, src, simDepth)
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
			assertSidesOrdered(t, e, seed)
		}
		truth, err := src.Snapshot(ctx)
		if err != nil {
			t.Fatalf("seed %d: truth snapshot: %v", seed, err)
		}
		assertConverged(t, e, truth, seed)
		for _, c := range []struct {
			name      string
			got, want int
		}{
			{"Gaps", e.Gaps(), 2 * cycles},
			{"Resyncs", e.Resyncs(), 4 * cycles},
			{"WouldCrosses", e.WouldCrosses(), cycles},
			{"Disconnects", e.Disconnects(), cycles},
			{"SnapshotRejects", e.SnapshotRejects(), 0},
			{"SnapshotErrors", e.SnapshotErrors(), 0},
		} {
			if c.got != c.want {
				t.Fatalf("seed %d: %s = %d, want %d", seed, c.name, c.got, c.want)
			}
		}
	}
}
