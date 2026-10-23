package source

import (
	"flag"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
)

// update rewrites the golden schedule; the other packages' -update flags are separate.
var update = flag.Bool("update", false, "rewrite the golden files")

// faultNames is the test's own name table: Fault has no String method, and adding one to the
// production type would change how it prints everywhere. The golden file depends on the order
// of the Fault constants (the hash mixes the kind), and this table with it.
var faultNames = map[Fault]string{
	FaultNone: "none", FaultGap: "gap", FaultReorder: "reorder", FaultDuplicate: "duplicate",
	FaultDisconnect: "disconnect", FaultCross: "cross", FaultCoalesce: "coalesce",
}

// windows are where each fault may land inside a period: its base and the jitter below 20.
var windows = map[Fault][2]int{
	FaultGap: {40, 59}, FaultReorder: {100, 119}, FaultDuplicate: {150, 169},
	FaultDisconnect: {200, 219}, FaultCross: {240, 259}, FaultCoalesce: {270, 289},
}

func TestChaosScheduleIsDeterministic(t *testing.T) {
	a, b := ChaosSchedule(7), ChaosSchedule(7)
	for step := range 3000 {
		if a(step) != b(step) {
			t.Fatalf("step %d: two schedules of the same seed disagree", step)
		}
	}
	differ := false
	c, d := ChaosSchedule(1), ChaosSchedule(2)
	for step := range 3000 {
		if c(step) != d(step) {
			differ = true
		}
	}
	if !differ {
		t.Fatal("seeds 1 and 2 give the same schedule for 3000 steps")
	}
}

// Every period holds each of the six faults exactly once, inside its window, with room for the
// engine to recover between them, and nothing else.
func TestChaosScheduleStructure(t *testing.T) {
	seeds := []int64{-1, -1000, math.MinInt64, math.MinInt64 + 1, math.MaxInt64, math.MaxInt64 - 1}
	for s := range int64(200) {
		seeds = append(seeds, s)
	}
	for _, seed := range seeds {
		at := ChaosSchedule(seed)
		for cycle := range 20 {
			start := cycle * ChaosPeriod
			found := map[Fault][]int{}
			var steps []int
			for r := range ChaosPeriod {
				if f := at(start + r); f != FaultNone {
					found[f] = append(found[f], r)
					steps = append(steps, r)
				}
			}
			for kind, w := range windows {
				got := found[kind]
				if len(got) != 1 || got[0] < w[0] || got[0] > w[1] {
					t.Fatalf("seed %d cycle %d: %s at %v, want exactly one in [%d,%d]", seed, cycle, faultNames[kind], got, w[0], w[1])
				}
			}
			if len(steps) != 6 {
				t.Fatalf("seed %d cycle %d: %d faults, want 6 (%v)", seed, cycle, len(steps), steps)
			}
			slices.Sort(steps)
			if steps[0] < 40 {
				t.Fatalf("seed %d cycle %d: first fault at %d, before the engine is bound", seed, cycle, steps[0])
			}
			if steps[len(steps)-1] > ChaosPeriod-11 {
				t.Fatalf("seed %d cycle %d: last fault at %d, the period must end clean", seed, cycle, steps[len(steps)-1])
			}
			for i := 1; i < len(steps); i++ {
				if steps[i]-steps[i-1] < 11 {
					t.Fatalf("seed %d cycle %d: faults at %d and %d are %d apart, want at least 11", seed, cycle, steps[i-1], steps[i], steps[i]-steps[i-1])
				}
			}
		}
	}
}

func TestChaosScheduleEdges(t *testing.T) {
	at := ChaosSchedule(1)
	for _, step := range []int{-1, -300, math.MinInt, 0, 39} {
		if f := at(step); f != FaultNone {
			t.Errorf("step %d: %s, want none", step, faultNames[f])
		}
	}
	// A whole period far out and one at the very top of int answer like any other.
	for _, base := range []int{100_000_000 / ChaosPeriod * ChaosPeriod, (math.MaxInt/ChaosPeriod - 1) * ChaosPeriod} {
		n := 0
		for r := range ChaosPeriod {
			if at(base+r) != FaultNone {
				n++
			}
		}
		if n != 6 {
			t.Errorf("period starting at %d has %d faults, want 6", base, n)
		}
	}
}

// The exact schedule of two seeds, so a change to the offsets, the jitter or the hash shows.
func TestChaosScheduleGolden(t *testing.T) {
	var b strings.Builder
	for _, seed := range []int64{1, 2} {
		at := ChaosSchedule(seed)
		for step := range 2 * ChaosPeriod {
			if f := at(step); f != FaultNone {
				fmt.Fprintf(&b, "seed %d step %d %s\n", seed, step, faultNames[f])
			}
		}
	}
	const path = "testdata/chaos_schedule.txt"
	if *update {
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	if b.String() != string(want) {
		t.Fatalf("schedule differs from %s (run with -update to rewrite it if the change is intended):\n got:\n%s\nwant:\n%s", path, b.String(), want)
	}
}

// ---- the schedule inside the source --------------------------------------------------------

// FaultAt is asked once per step, in order, also across a reorder (which pulls the next
// event inside Next).
func TestFaultAtIsAskedOncePerStep(t *testing.T) {
	var asked []int
	at := ChaosSchedule(3)
	c := cfg(3, 700)
	c.FaultAt = func(step int) Fault {
		asked = append(asked, step)
		return at(step)
	}
	drain(New(c))
	want := make([]int, 700)
	for i := range want {
		want[i] = i
	}
	if !slices.Equal(asked, want) {
		t.Fatalf("FaultAt was asked for steps %v, want each of 0..699 once and in order", asked)
	}
}

func TestChaosRunEmitsItsFaults(t *testing.T) {
	disconnects := 0
	for _, e := range drain(New(func() Config { c := cfg(5, ChaosPeriod); c.FaultAt = ChaosSchedule(5); return c }())) {
		if e.Kind == EventDisconnected {
			disconnects++
		}
	}
	if disconnects != 1 {
		t.Fatalf("a chaos run of one period emitted %d disconnects, want 1", disconnects)
	}
}

func TestFaultsMapWinsOverFaultAt(t *testing.T) {
	var asked []int
	c := cfg(1, 10)
	c.Faults = map[int]Fault{3: FaultDisconnect, 4: FaultNone}
	c.FaultAt = func(step int) Fault {
		asked = append(asked, step)
		return FaultNone
	}
	drain(New(c))
	if slices.Contains(asked, 3) {
		t.Error("FaultAt was asked about step 3, which the map already decides")
	}
	if !slices.Contains(asked, 4) {
		t.Error("an explicit FaultNone entry in the map must not suppress FaultAt")
	}
}

// A schedule that never injects anything leaves the stream exactly as it was without one.
func TestFaultAtNoneLeavesTheStreamUnchanged(t *testing.T) {
	plain := hashEvents(drain(New(cfg(9, 600))))
	c := cfg(9, 600)
	c.FaultAt = func(int) Fault { return FaultNone }
	if got := hashEvents(drain(New(c))); got != plain {
		t.Fatalf("a FaultAt that returns none changed the stream: %#x vs %#x", got, plain)
	}
	c.FaultAt = ChaosSchedule(9)
	if got := hashEvents(drain(New(c))); got == plain {
		t.Fatal("the chaos schedule left the stream unchanged")
	}
}
