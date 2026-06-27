package venue

import (
	"testing"
	"time"
)

func TestBackoffExponentialCapped(t *testing.T) {
	b := Backoff{Min: 10 * time.Millisecond, Max: time.Second, Factor: 2}
	cases := map[int]time.Duration{
		0:   10 * time.Millisecond, // Min
		1:   20 * time.Millisecond, // Min*2
		2:   40 * time.Millisecond, // Min*4
		100: time.Second,           // 2^100 overflows the cap -> Max, never wraps
	}
	for attempt, want := range cases {
		if got := b.Next(attempt); got != want {
			t.Errorf("Next(%d) = %v, want %v", attempt, got, want)
		}
	}
}

func TestBackoffInjectedJitter(t *testing.T) {
	b := Backoff{Min: 10 * time.Millisecond, Max: time.Second, Factor: 2, Jitter: func(d time.Duration) time.Duration { return d / 2 }}
	if got := b.Next(2); got != 20*time.Millisecond { // 40ms capped value, halved by the jitter
		t.Fatalf("Next(2) with halving jitter = %v, want 20ms", got)
	}
}

func TestFullJitterBounds(t *testing.T) {
	const d = 100 * time.Millisecond
	for range 1000 {
		if j := FullJitter(d); j < 0 || j > d {
			t.Fatalf("FullJitter(%v) = %v, out of [0,%v]", d, j, d)
		}
	}
	if FullJitter(0) != 0 || FullJitter(-5) != 0 {
		t.Fatal("FullJitter of a non-positive duration must be 0")
	}
}
