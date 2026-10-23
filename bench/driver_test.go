package bench

import (
	"fmt"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/metrics"
)

// The driver paces events open-loop: event k is due at start + k*interval whatever happened to the
// events before it, so a slow event makes every event behind it late instead of slowing the arrival
// rate. A gap to the next due time longer than sleepAbove is slept through up to spinWindow before it;
// the rest is spent spinning on the clock. At a 50 µs interval that is always a pure spin: time.Sleep
// below its own tick oversleeps and then releases a burst of catch-up events, which is exactly the
// error this replaces.
const (
	sleepAbove = 2 * time.Millisecond
	spinWindow = 1500 * time.Microsecond
)

// clock is what the driver needs from time, so tests can run it on a clock they control.
type clock struct {
	now   func() time.Time
	sleep func(time.Duration)
}

var realClock = clock{now: time.Now, sleep: time.Sleep}

// waitUntil returns once the clock reads due or later.
func (c clock) waitUntil(due time.Time) {
	for {
		d := due.Sub(c.now())
		if d <= 0 {
			return
		}
		if d > sleepAbove {
			c.sleep(d - spinWindow)
		}
		// otherwise spin: ask the clock again
	}
}

type driver struct {
	clock    clock
	interval time.Duration
	warmup   int // events run but not recorded
	samples  int // events recorded
	// minSamples is the fewest recorded events the driver accepts: a thin tail is not published.
	minSamples int
}

type driveResult struct {
	start      time.Time
	senderLate int // events that began more than one interval after they were due
}

func (d driver) due(start time.Time, k int) time.Time {
	return start.Add(time.Duration(k) * d.interval)
}

// run waits for each event's due time and calls step(k, due) for k in [0, warmup+samples). Events
// below warmup are warmup. It refuses a run with fewer than minSamples recorded events.
func (d driver) run(step func(k int, due time.Time)) (driveResult, error) {
	return d.runAt(d.clock.now(), step)
}

// runAt is run with the schedule's start chosen by the caller, so another goroutine can compute
// the same due times.
func (d driver) runAt(start time.Time, step func(k int, due time.Time)) (driveResult, error) {
	if d.samples < d.minSamples {
		return driveResult{}, fmt.Errorf("%d recorded events is fewer than the %d a published tail needs", d.samples, d.minSamples)
	}
	res := driveResult{start: start}
	for k := range d.warmup + d.samples {
		due := d.due(res.start, k)
		d.clock.waitUntil(due)
		if d.clock.now().Sub(due) > d.interval {
			res.senderLate++
		}
		step(k, due)
	}
	return res, nil
}

// ---- tests -------------------------------------------------------------------------------------

// fakeClock moves only when asked: every reading advances it by tick, so a spin ends, and a sleep
// moves it by the whole duration.
type fakeClock struct {
	t      time.Time
	tick   time.Duration
	sleeps int
}

func newFakeClock(tick time.Duration) *fakeClock {
	return &fakeClock{t: time.Unix(1_700_000_000, 0), tick: tick}
}

func (f *fakeClock) now() time.Time { f.t = f.t.Add(f.tick); return f.t }
func (f *fakeClock) sleep(d time.Duration) {
	f.sleeps++
	f.t = f.t.Add(d)
}
func (f *fakeClock) clock() clock { return clock{now: f.now, sleep: f.sleep} }

func TestDriverNeverStartsEarly(t *testing.T) {
	fc := newFakeClock(time.Microsecond)
	d := driver{clock: fc.clock(), interval: 50 * time.Microsecond, warmup: 5, samples: 20, minSamples: 1}
	var starts []time.Time
	res, err := d.run(func(k int, due time.Time) { starts = append(starts, fc.now()) })
	if err != nil {
		t.Fatal(err)
	}
	if len(starts) != 25 {
		t.Fatalf("%d events ran, want warmup + samples = 25", len(starts))
	}
	for k, s := range starts {
		if min := res.start.Add(time.Duration(k) * 50 * time.Microsecond); s.Before(min) {
			t.Fatalf("event %d started at %v, before its due time %v", k, s, min)
		}
	}
	if res.senderLate != 0 {
		t.Fatalf("senderLate = %d on a clock that never stalls", res.senderLate)
	}
}

// A short gap is spun through and a long one is slept through.
func TestDriverSleepsOnlyThroughLongGaps(t *testing.T) {
	short := newFakeClock(time.Microsecond)
	driver{clock: short.clock(), interval: 50 * time.Microsecond, samples: 20, minSamples: 1}.run(func(int, time.Time) {})
	if short.sleeps != 0 {
		t.Fatalf("a 50 µs interval slept %d times, want a pure spin", short.sleeps)
	}
	long := newFakeClock(time.Microsecond)
	driver{clock: long.clock(), interval: 20 * time.Millisecond, samples: 5, minSamples: 1}.run(func(int, time.Time) {})
	if long.sleeps == 0 {
		t.Fatal("a 20 ms interval never slept")
	}
}

// A stall in one event delays the ones behind it, and the driver counts them as late; the schedule
// itself does not move.
func TestDriverCountsLateEvents(t *testing.T) {
	fc := newFakeClock(time.Microsecond)
	d := driver{clock: fc.clock(), interval: time.Millisecond, samples: 100, minSamples: 1}
	res, err := d.run(func(k int, due time.Time) {
		if k == 10 {
			fc.t = fc.t.Add(5500 * time.Microsecond) // the clock is now about 15.5 ms; events 11 to 14 are over one interval late
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.senderLate != 4 {
		t.Fatalf("senderLate = %d, want 4 (events 11 to 14 begin more than one interval after they were due; event 15 is within one)", res.senderLate)
	}
}

func TestDriverRefusesAThinSample(t *testing.T) {
	_, err := driver{clock: newFakeClock(time.Microsecond).clock(), interval: time.Millisecond, samples: 99, minSamples: 100}.
		run(func(int, time.Time) { t.Fatal("no event may run when the sample is refused") })
	if err == nil {
		t.Fatal("a sample below the minimum was accepted")
	}
}

func TestDriverWarmupIsNotRecorded(t *testing.T) {
	fc := newFakeClock(time.Microsecond)
	d := driver{clock: fc.clock(), interval: time.Millisecond, warmup: 7, samples: 13, minSamples: 1}
	recorded, total := 0, 0
	if _, err := d.run(func(k int, due time.Time) {
		total++
		if k >= d.warmup {
			recorded++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if recorded != 13 || total != 20 {
		t.Fatalf("recorded %d of %d events, want 13 of 20", recorded, total)
	}
}

// Latency measured from when an event was due, not from when it began, is what makes a stall visible
// to every event queued behind it. Without that correction the tail would hide the backlog.
func TestCoordinatedOmissionCorrection(t *testing.T) {
	fc := newFakeClock(time.Microsecond)
	d := driver{clock: fc.clock(), interval: time.Millisecond, samples: 300, minSamples: 1}
	naive, corrected := metrics.NewHistogram(), metrics.NewHistogram()
	if _, err := d.run(func(k int, due time.Time) {
		begin := fc.now()
		if k == 150 {
			fc.t = fc.t.Add(40 * time.Millisecond) // one stall, as a GC pause or a lock would be
		}
		done := fc.now()
		naive.Record(done.Sub(begin))
		corrected.Record(done.Sub(due))
	}); err != nil {
		t.Fatal(err)
	}
	nc, cc := naive.Percentile(0.99), corrected.Percentile(0.99)
	if cc <= nc {
		t.Fatalf("the correction did not raise the tail: corrected p99 %v <= naive p99 %v", cc, nc)
	}
	if cc < 10*time.Millisecond {
		t.Fatalf("corrected p99 %v should reflect the 40 ms stall backlog", cc)
	}
}
