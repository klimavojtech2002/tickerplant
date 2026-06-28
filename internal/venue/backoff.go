// Package venue holds the live-transport pieces shared by the exchange adapters: a
// reconnecting WebSocket stream over the one third-party dependency (ADR-0014) and the
// reconnect backoff. The adapters themselves (binance, kraken, okx) consume a frame
// channel from here, so the WebSocket library is isolated to this package.
package venue

import (
	"math"
	"math/rand"
	"time"
)

// Backoff computes reconnect delays: exponential growth capped at Max, with an optional
// injectable jitter. Jitter is a function so tests can make delays deterministic;
// production uses FullJitter to decorrelate reconnecting clients.
type Backoff struct {
	Min    time.Duration
	Max    time.Duration
	Factor float64
	Jitter func(time.Duration) time.Duration // nil: no jitter (the raw capped delay)
}

// Next returns the delay before the given zero-based reconnect attempt: Min*Factor^attempt,
// capped at Max, then jittered. attempt 0 returns Min (jittered).
func (b Backoff) Next(attempt int) time.Duration {
	d := float64(b.Min) * math.Pow(b.Factor, float64(attempt))
	capped := b.Max
	if !math.IsInf(d, 1) && d < float64(b.Max) {
		capped = time.Duration(d)
	}
	if b.Jitter != nil {
		return b.Jitter(capped)
	}
	return capped
}

// FullJitter returns a uniform random duration in [0, d] — the AWS "full jitter"
// strategy that spreads reconnecting clients so they do not retry in lockstep. It is
// for the live path only, never the deterministic test suite.
func FullJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(d) + 1))
}
