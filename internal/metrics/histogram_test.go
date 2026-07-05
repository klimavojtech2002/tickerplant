package metrics

import (
	"math"
	"sync"
	"testing"
	"time"
)

// withinRel reports whether got is within tol (fractional) of want.
func withinRel(got, want time.Duration, tol float64) bool {
	if want == 0 {
		return got == 0
	}
	return math.Abs(float64(got-want))/float64(want) <= tol
}

// A known distribution must yield percentiles within the histogram's stated error.
// 1000 samples of 1µs..1000µs (one each): true p50=500µs, p90=900µs, p99=990µs.
func TestPercentileKnownDistribution(t *testing.T) {
	h := NewHistogram()
	for i := 1; i <= 1000; i++ {
		h.Record(time.Duration(i) * time.Microsecond)
	}
	const tol = 0.02 // the histogram's stated bound; the real worst case is under 1%
	cases := []struct {
		p    float64
		want time.Duration
	}{
		{0.50, 500 * time.Microsecond},
		{0.90, 900 * time.Microsecond},
		{0.99, 990 * time.Microsecond},
		{1.00, 1000 * time.Microsecond}, // rank = ceil(1.0*1000) = 1000 -> the maximum sample
	}
	for _, c := range cases {
		if got := h.Percentile(c.p); !withinRel(got, c.want, tol) {
			t.Errorf("Percentile(%.2f) = %v, want ~%v (within %.0f%%)", c.p, got, c.want, tol*100)
		}
	}
	// percentiles must be monotonic
	if h.Percentile(0.5) > h.Percentile(0.99) {
		t.Error("p50 must not exceed p99")
	}
}

// The histogram claims to be safe for concurrent use; this makes that claim
// falsifiable. Under the race detector any unguarded read/write is a report, and even
// without it, 80k concurrent same-bucket increments make a lost update near-certain,
// failing the exact Count below.
func TestHistogramConcurrentRecordAndRead(t *testing.T) {
	const (
		writers   = 8
		perWriter = 10_000
		sample    = 100 * time.Microsecond
	)
	h := NewHistogram()
	start := make(chan struct{})
	done := make(chan struct{})

	var readers sync.WaitGroup
	for range 2 {
		readers.Go(func() {
			<-start
			for {
				select {
				case <-done:
					return
				default:
					_ = h.Percentile(0.99)
					_ = h.Count()
					_ = h.Max()
					_ = h.Mean()
				}
			}
		})
	}

	var writersWG sync.WaitGroup
	for range writers {
		writersWG.Go(func() {
			<-start
			for range perWriter {
				h.Record(sample)
			}
		})
	}
	close(start)
	writersWG.Wait()
	close(done)
	readers.Wait()

	if got, want := h.Count(), uint64(writers*perWriter); got != want {
		t.Fatalf("Count() = %d, want %d (a lost update means the mutex is gone)", got, want)
	}
	if h.Max() != sample {
		t.Fatalf("Max() = %v, want %v (all samples identical)", h.Max(), sample)
	}
	if h.Mean() != sample {
		t.Fatalf("Mean() = %v, want %v (exact: every sample is the same value)", h.Mean(), sample)
	}
	if got := h.Percentile(1.0); !withinRel(got, sample, 0.02) {
		t.Fatalf("Percentile(1.0) = %v, want ~%v", got, sample)
	}
}

func TestEmptyStatsAreZero(t *testing.T) {
	h := NewHistogram()
	if h.Percentile(0.99) != 0 || h.Count() != 0 || h.Max() != 0 || h.Mean() != 0 {
		t.Fatalf("empty histogram stats must all be zero: p99=%v count=%d max=%v mean=%v",
			h.Percentile(0.99), h.Count(), h.Max(), h.Mean())
	}
}

func TestPercentileClampsP(t *testing.T) {
	h := NewHistogram()
	for i := 1; i <= 100; i++ {
		h.Record(time.Duration(i) * time.Microsecond)
	}
	// p>1 must clamp to 1.0 and p<0 to 0.0 — not fall through to the empty top bucket.
	if got, want := h.Percentile(2.0), h.Percentile(1.0); got != want {
		t.Fatalf("p>1 must clamp to p=1.0: got %v, want %v", got, want)
	}
	if got, want := h.Percentile(-1.0), h.Percentile(0.0); got != want {
		t.Fatalf("p<0 must clamp to p=0.0: got %v, want %v", got, want)
	}
	// p=0 returns the minimum sample, not the bottom bucket — pins the rank>=1 guard
	if got := h.Percentile(0.0); !withinRel(got, 1*time.Microsecond, 0.03) {
		t.Fatalf("p0 = %v, want ~1µs (the minimum recorded sample)", got)
	}
}

// rank uses ceil: for {10,20,30}µs at p=0.4, ceil(1.2)=rank 2 = 20µs, while round(1.2)
// or floor(1.2) = rank 1 = 10µs — so this pins ceil specifically, not just not-floor.
func TestPercentileRankIsCeil(t *testing.T) {
	h := NewHistogram()
	for _, d := range []time.Duration{10 * time.Microsecond, 20 * time.Microsecond, 30 * time.Microsecond} {
		h.Record(d)
	}
	if got := h.Percentile(0.4); !withinRel(got, 20*time.Microsecond, 0.02) {
		t.Fatalf("p40 of {10,20,30}µs = %v, want ~20µs (rank must use ceil)", got)
	}
}

// the estimate is the bucket's geometric midpoint: a sample placed exactly at bucket
// i's midpoint reads back as itself, which the lower-bound (+0) or upper-bound (+1)
// estimators would miss by ~1%.
func TestPercentileIsBucketMidpoint(t *testing.T) {
	h := NewHistogram()
	const i = 697
	mid := math.Pow(1+relErr, float64(i)+0.5) // ~1ms, sitting at bucket i's geometric center
	h.Record(time.Duration(int64(mid)))
	if got, want := h.Percentile(0.5), time.Duration(int64(mid)); !withinRel(got, want, 0.003) {
		t.Fatalf("p50 = %v, want the bucket midpoint %v (pins the +0.5 centering)", got, want)
	}
}

func TestRecordSubNanosecondLandsInBucketZero(t *testing.T) {
	h := NewHistogram()
	h.Record(0)
	h.Record(1 * time.Nanosecond)
	if h.Count() != 2 {
		t.Fatalf("sub-nanosecond samples not counted: %d", h.Count())
	}
}

func TestRecordIgnoresNegative(t *testing.T) {
	h := NewHistogram()
	h.Record(-5 * time.Microsecond)
	if h.Count() != 0 {
		t.Fatalf("negative sample was counted: %d", h.Count())
	}
}

func TestRecordClampsAboveMax(t *testing.T) {
	h := NewHistogram()
	h.Record(2 * time.Hour) // above the 1h ceiling
	if h.Count() != 1 {
		t.Fatalf("over-max sample not counted: %d", h.Count())
	}
	if h.Max() > time.Hour || h.Max() < time.Hour-time.Hour/50 {
		t.Fatalf("over-max sample not clamped near 1h: %v", h.Max())
	}
}

func TestMeanCountMaxExact(t *testing.T) {
	h := NewHistogram()
	for _, d := range []time.Duration{10 * time.Microsecond, 20 * time.Microsecond, 30 * time.Microsecond} {
		h.Record(d)
	}
	if h.Count() != 3 {
		t.Fatalf("count = %d, want 3", h.Count())
	}
	if h.Mean() != 20*time.Microsecond { // mean is exact (from the running sum)
		t.Fatalf("mean = %v, want 20µs", h.Mean())
	}
	if h.Max() != 30*time.Microsecond {
		t.Fatalf("max = %v, want 30µs", h.Max())
	}
}
