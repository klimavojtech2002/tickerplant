package bench

import (
	"context"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/metrics"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// drive runs n events through source -> engine -> fan-out at a fixed inter-arrival
// interval (open-loop), recording each event's internal processing latency two ways:
//   - naive: service time only (from when we started processing the event), and
//   - corrected: from when the event was due (start + i*interval),
//
// so a stall makes every event queued behind it look late under the corrected
// measure. A one-off stall of `stall` is injected before event `stallAt` (use -1 for
// none) to model a GC pause or lock contention.
func drive(tb testing.TB, n int, interval, stall time.Duration, stallAt int) (naive, corrected *metrics.Histogram) {
	tb.Helper()
	ctx := context.Background()
	eng := book.New(source.New(source.Config{Venue: "v", Symbol: "s", Seed: 1, Steps: n + 1}), 10)
	// Latency is recorded at the publisher (done.Sub(send) below), and conflation keeps
	// Publish O(consumers) and non-blocking, so the drain never perturbs the measurement;
	// the huge maxLag only keeps a momentarily-behind drain from being disconnected.
	hub := delivery.New(1 << 30)
	defer hub.Close()
	c := hub.Subscribe()
	go func() {
		for range c.Ready() { //nolint:revive // intentional drain
			c.Take()
		}
	}()
	if err := eng.Bootstrap(ctx); err != nil {
		tb.Fatal(err)
	}

	naive, corrected = metrics.NewHistogram(), metrics.NewHistogram()
	start := time.Now()
	for i := range n {
		due := start.Add(time.Duration(i) * interval)
		if d := time.Until(due); d > 0 {
			time.Sleep(d) // pace to the schedule; do NOT let slow service push the schedule back
		}
		send := time.Now()
		if i == stallAt && stall > 0 {
			time.Sleep(stall)
		}
		ok, err := eng.Step(ctx)
		if err != nil {
			tb.Fatal(err)
		}
		if !ok {
			break
		}
		hub.Publish(eng.View())
		done := time.Now()
		naive.Record(done.Sub(send))
		corrected.Record(done.Sub(due))
	}
	return naive, corrected
}

// TestCoordinatedOmissionCorrection proves the correction is real, not cosmetic: under
// a single 40ms stall at a 1ms arrival rate, ~40 events queue behind it. The corrected
// tail must capture that backlog; the naive tail (service time) sees only the one slow
// event and stays low. Without coordinated-omission correction the tail would lie.
func TestCoordinatedOmissionCorrection(t *testing.T) {
	naive, corrected := drive(t, 300, time.Millisecond, 40*time.Millisecond, 150)
	nc, cc := naive.Percentile(0.99), corrected.Percentile(0.99)
	if cc <= nc {
		t.Fatalf("CO correction did not raise the tail: corrected p99=%v <= naive p99=%v", cc, nc)
	}
	if cc < 10*time.Millisecond {
		t.Fatalf("CO-corrected p99=%v should reflect the ~40ms stall backlog", cc)
	}
}

// BenchmarkPipelineLatency reports the coordinated-omission-corrected internal latency
// distribution at a fixed arrival rate. Run with `go test -bench . -benchmem ./bench`;
// the numbers are machine-dependent — see README.md for the method and span.
func BenchmarkPipelineLatency(b *testing.B) {
	_, corrected := drive(b, b.N, 50*time.Microsecond, 0, -1)
	b.ReportMetric(float64(corrected.Percentile(0.50).Nanoseconds()), "p50-ns")
	b.ReportMetric(float64(corrected.Percentile(0.99).Nanoseconds()), "p99-ns")
	b.ReportMetric(float64(corrected.Percentile(0.999).Nanoseconds()), "p999-ns")
	b.ReportMetric(float64(corrected.Max().Nanoseconds()), "max-ns")
}
