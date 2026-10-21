package book

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// updateGolden rewrites testdata/*.golden instead of comparing. It is never on by
// default: a golden that regenerates itself would let a behaviour change pass silently.
var updateGolden = flag.Bool("update", false, "rewrite the golden files under testdata/")

// TestCharacterizationGolden pins what the engine does today over a fixed set of seeded
// runs: the final published view and every counter. It is a characterization test, not
// a statement that each number is ideal; a change that moves a number must say so in its
// own commit and regenerate the file with -update.
//
// A faulted run is expected to converge to the same book as its clean twin, so the final
// books mostly pin the order and content of the levels, while the counters and lastSeq are
// what tell the two variants apart.
//
// Only engine state is recorded. Hub delivery counts and log lines are left out on
// purpose: they pass through a lossy, scheduler-dependent consumer and differ run to run.
func TestCharacterizationGolden(t *testing.T) {
	var b strings.Builder
	for _, seed := range []int64{1, 7, 42} {
		for _, variant := range []string{"clean", "faulted"} {
			var faults map[int]source.Fault
			if variant == "faulted" {
				faults = scenarioForSeed(seed)
				if len(faults) == 0 {
					t.Fatalf("seed %d: scenarioForSeed returned no faults; the case would not be faulted", seed)
				}
			}
			src := source.New(source.Config{Venue: "char", Symbol: "S", Seed: seed, Steps: 400, Faults: faults})
			e := New(src, simDepth)
			if err := e.Run(context.Background()); err != nil {
				t.Fatalf("seed %d %s: run: %v", seed, variant, err)
			}
			writeCase(&b, seed, variant, e)
		}
	}
	got := b.String()

	path := filepath.Join("testdata", "characterization.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\nrun `go test ./internal/book -run TestCharacterizationGolden -update` once and review the file", err)
	}
	if got != string(want) {
		t.Fatalf("engine state moved from the committed golden.\n--- got ---\n%s\n--- want ---\n%s\nIf the change is intended, say why in the commit and regenerate with -update.", got, want)
	}
}

func writeCase(b *strings.Builder, seed int64, variant string, e *Engine) {
	v := e.View()
	fmt.Fprintf(b, "case seed=%d %s\n", seed, variant)
	fmt.Fprintf(b, "  lastSeq=%d resyncs=%d gaps=%d disconnects=%d wouldCrosses=%d checksumMismatches=%d\n",
		v.LastSeq, e.Resyncs(), e.Gaps(), e.Disconnects(), e.WouldCrosses(), e.ChecksumMismatches())
	fmt.Fprintf(b, "  bids: %s\n", levelsString(v.Bids))
	fmt.Fprintf(b, "  asks: %s\n", levelsString(v.Asks))
}

func levelsString(ls []market.Level) string {
	parts := make([]string, len(ls))
	for i, l := range ls {
		parts[i] = fmt.Sprintf("%d:%d", l.Price, l.Size)
	}
	return strings.Join(parts, " ")
}
