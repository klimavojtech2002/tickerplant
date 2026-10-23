package bench

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/metrics"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// What a published run does, in one place. The sample count does not follow b.N: Go calls a benchmark
// again with a growing b.N until a time budget is spent, which would change the number of events (and
// the tail) from call to call. A run is a fixed number of events; the documented command is
// `-benchtime=1x -count=10 -cpu 4`.
const (
	benchInterval   = 50 * time.Microsecond  // synthetic pipeline: 20,000 events a second, a stress rate
	wireInterval    = 200 * time.Microsecond // wire frames: 5,000 a second; a frame takes tens of microseconds to decode and apply
	benchWarmup     = 2_000                  // run, not recorded
	benchSamples    = 100_000                // recorded per run
	benchMinSamples = 10_000                 // the fewest recorded events a published tail may rest on
	benchMinCPUs    = 4                      // the spin pacer needs a core of its own
)

func benchDriver(interval time.Duration) driver {
	return driver{clock: realClock, interval: interval, warmup: benchWarmup, samples: benchSamples, minSamples: benchMinSamples}
}

// clockTick is the typical step of time.Now: the median of the non-zero steps seen over up to 100 ms
// of readings. (The smallest step would be misleading: a coarse clock now and then shows a small one.)
// On Windows it is about half a millisecond, far too coarse to hold or measure a 50 µs schedule. The
// header program (bench/header) has the same function; keep them equal.
func clockTick() time.Duration {
	const maxSteps, span = 200_000, 100 * time.Millisecond
	steps := make([]time.Duration, 0, maxSteps)
	first := time.Now()
	prev := first
	for len(steps) < maxSteps {
		now := time.Now()
		if d := now.Sub(prev); d > 0 {
			steps = append(steps, d)
			prev = now
		}
		if now.Sub(first) >= span {
			break
		}
	}
	if len(steps) == 0 {
		return span // the clock did not move at all
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i] < steps[j] })
	return steps[len(steps)/2]
}

// requireFineClock fails a benchmark on a host whose clock is coarser than a fifth of the interval:
// the schedule could not be held and the figures would be the clock's, not the system's.
func requireFineClock(b *testing.B, interval time.Duration) {
	b.Helper()
	if tick := clockTick(); tick > interval/5 {
		b.Fatalf("this host's clock advances in steps of %v, too coarse for a %v schedule (it needs at most %v); run the benchmark on Linux, see bench/README.md",
			tick, interval, interval/5)
	}
}

// reportRun reports what the run was, so the log states its own method.
func reportRun(b *testing.B, d driver) {
	b.Helper()
	b.ReportMetric(float64(d.interval.Nanoseconds()), "interval-ns")
	b.ReportMetric(float64(d.warmup), "warmup")
	b.ReportMetric(float64(d.samples), "samples")
	b.ReportMetric(float64(runtime.GOMAXPROCS(0)), "gomaxprocs")
}

// requireCPUs fails a benchmark that would share too few cores: the pacer spins on one of them, and
// -cpu 4 on a smaller machine would set GOMAXPROCS to 4 without giving the program four cores.
func requireCPUs(b *testing.B) {
	b.Helper()
	if g, n := runtime.GOMAXPROCS(0), runtime.NumCPU(); g < benchMinCPUs || n < benchMinCPUs {
		b.Fatalf("needs at least %d CPUs and GOMAXPROCS (run with -cpu %d); have %d CPUs and GOMAXPROCS %d", benchMinCPUs, benchMinCPUs, n, g)
	}
}

// reportHist reports a histogram's quantiles, in nanoseconds, under metric names that carry no space.
func reportHist(b *testing.B, prefix string, h *metrics.Histogram, p999 bool) {
	b.Helper()
	b.ReportMetric(float64(h.Percentile(0.50).Nanoseconds()), prefix+"-p50-ns")
	b.ReportMetric(float64(h.Percentile(0.99).Nanoseconds()), prefix+"-p99-ns")
	if p999 {
		b.ReportMetric(float64(h.Percentile(0.999).Nanoseconds()), prefix+"-p999-ns")
		b.ReportMetric(float64(h.Max().Nanoseconds()), prefix+"-max-ns")
	}
}

// ---- the synthetic pipeline ---------------------------------------------------------------------

type synthResult struct {
	svc, publish         *metrics.Histogram
	senderLate           int
	resyncs, gaps        int
	published, processed uint64
}

// runSynthetic drives n events through the synthetic source, the engine and the fan-out, one after
// another on this goroutine. The synthetic source builds an event when Step asks for it, so an event's
// "due" time is the moment the driver calls Step: the figures are service time plus the pacer's overshoot
// (svc), not queueing behind a channel as in the wire benchmark.
func runSynthetic(d driver) (synthResult, error) {
	total := d.warmup + d.samples
	ctx := context.Background()
	eng, err := book.New(source.New(source.Config{Venue: "v", Symbol: "s", Seed: 1, Steps: total + 1}), 10)
	if err != nil {
		return synthResult{}, err
	}
	hub := delivery.New(1 << 30)
	defer hub.Close()
	c := hub.Subscribe()
	go func() {
		for range c.Ready() { //nolint:revive // intentional drain
			c.Take()
		}
	}()
	if err := eng.Bootstrap(ctx); err != nil {
		return synthResult{}, err
	}
	res := synthResult{svc: metrics.NewHistogram(), publish: metrics.NewHistogram()}
	var stepErr error
	runtime.GC()
	dr, err := d.run(func(k int, due time.Time) {
		if stepErr != nil {
			return
		}
		ok, err := eng.Step(ctx)
		if err != nil || !ok {
			stepErr = fmt.Errorf("event %d: step ok=%v err=%v", k, ok, err)
			return
		}
		t0 := time.Now()
		hub.Publish(eng.View())
		done := time.Now()
		res.processed++
		if k >= d.warmup {
			res.svc.Record(done.Sub(due))
			res.publish.Record(done.Sub(t0))
		}
	})
	if err != nil {
		return res, err
	}
	if stepErr != nil {
		return res, stepErr
	}
	res.senderLate = dr.senderLate
	res.resyncs, res.gaps = eng.Resyncs(), eng.Gaps()
	res.published = hub.Stats().Published
	return res, nil
}

// BenchmarkPipelineLatency reports the internal latency of the synthetic pipeline at a fixed arrival
// rate, measured from when each event was due (coordinated-omission-corrected). See README.md.
func BenchmarkPipelineLatency(b *testing.B) {
	requireCPUs(b)
	requireFineClock(b, benchInterval)
	d := benchDriver(benchInterval)
	res, err := runSynthetic(d)
	if err != nil {
		b.Fatal(err)
	}
	if res.resyncs != 0 || res.gaps != 0 || res.published != uint64(d.warmup+d.samples) {
		b.Fatalf("the run is not clean: resyncs %d, gaps %d, published %d of %d", res.resyncs, res.gaps, res.published, d.warmup+d.samples)
	}
	b.ReportMetric(0, "ns/op") // the per-op figure is meaningless: b.N does not set the sample count
	reportHist(b, "svc", res.svc, true)
	reportHist(b, "publish", res.publish, false)
	b.ReportMetric(float64(res.senderLate), "sender-late")
	reportRun(b, d)
}

// A short synthetic run completes cleanly and records exactly the samples asked for.
func TestPipelineSmoke(t *testing.T) {
	d := driver{clock: realClock, interval: 200 * time.Microsecond, warmup: 50, samples: 300, minSamples: 1}
	res, err := runSynthetic(d)
	if err != nil {
		t.Fatal(err)
	}
	if res.svc.Count() != 300 || res.publish.Count() != 300 || res.processed != 350 || res.resyncs != 0 || res.gaps != 0 {
		t.Fatalf("svc %d publish %d processed %d resyncs %d gaps %d", res.svc.Count(), res.publish.Count(), res.processed, res.resyncs, res.gaps)
	}
}
