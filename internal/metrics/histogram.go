// Package metrics records the system's behaviour honestly: counters for the engine's
// decisions and a bounded-error latency histogram. The histogram math uses float64,
// which is fine — latency statistics are off the order-book path, where the no-float
// rule applies (ADR-0003, ADR-0009).
package metrics

import (
	"math"
	"sync"
	"time"
)

// relErr is the histogram's bucket relative width: a recorded value lands in a bucket
// covering [v, v*(1+relErr)), so any reported percentile is within relErr of the true
// value. 0.02 = 2%, small enough that a microsecond p99 is trustworthy.
const relErr = 0.02

// maxNanos bounds the range so the bucket count stays fixed (~1461 buckets to 1h at
// relErr resolution). A sample above it is clamped and still counted (inflating the
// tail, never lost).
const maxNanos = int64(time.Hour)

// Histogram is a fixed-memory, log-bucketed latency histogram with bounded relative
// error. It is safe for concurrent use: one writer per record is typical, but reads
// and writes are guarded so a /metrics scrape never races the recorder.
type Histogram struct {
	growth  float64 // ln(1+relErr); bucket i spans [1.02^i, 1.02^(i+1)) ns at relErr=0.02
	mu      sync.Mutex
	buckets []uint64
	total   uint64
	sum     int64
	max     int64
}

// NewHistogram returns an empty histogram sized for [1ns, 1h] at relErr resolution.
func NewHistogram() *Histogram {
	growth := math.Log1p(relErr)
	n := bucketIndex(growth, maxNanos) + 1
	return &Histogram{growth: growth, buckets: make([]uint64, n)}
}

// bucketIndex maps a nanosecond value to its bucket. Values <= 1ns share bucket 0.
func bucketIndex(growth float64, ns int64) int {
	if ns <= 1 {
		return 0
	}
	return int(math.Log(float64(ns)) / growth)
}

// Record adds one latency sample. Negative durations are ignored (a non-monotonic
// clock would be the only source; the measurement path uses a monotonic span).
func (h *Histogram) Record(d time.Duration) {
	ns := int64(d)
	if ns < 0 {
		return
	}
	if ns > maxNanos {
		ns = maxNanos
	}
	i := bucketIndex(h.growth, ns)
	h.mu.Lock()
	h.buckets[i]++
	h.total++
	h.sum += ns
	if ns > h.max {
		h.max = ns
	}
	h.mu.Unlock()
}

// Percentile returns the p-quantile (p in [0,1]) as a duration, using the geometric
// midpoint of the containing bucket so the estimate is centered, not biased low. The
// result is within relErr of the true quantile. An empty histogram returns 0.
func (h *Histogram) Percentile(p float64) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.total == 0 {
		return 0
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	// rank is the 1-based index of the sample at quantile p (ceil), so p=1 selects the
	// last sample and p=0 the first.
	rank := uint64(math.Ceil(p * float64(h.total)))
	if rank == 0 {
		rank = 1
	}
	idx := len(h.buckets) - 1 // fallback to the top bucket; the loop sets it for rank <= total
	var cum uint64
	for i, c := range h.buckets {
		cum += c
		if cum >= rank {
			idx = i
			break
		}
	}
	mid := math.Exp(h.growth * (float64(idx) + 0.5))
	return time.Duration(int64(mid))
}

// Count returns how many samples were recorded.
func (h *Histogram) Count() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.total
}

// Max returns the largest sample, or 0 if none.
func (h *Histogram) Max() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return time.Duration(h.max)
}

// Mean returns the arithmetic mean of the samples, or 0 if none. It is computed from
// the exact running sum, not the buckets, so it carries no bucketing error.
func (h *Histogram) Mean() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.total == 0 {
		return 0
	}
	return time.Duration(h.sum / int64(h.total))
}
