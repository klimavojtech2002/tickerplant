package book

import (
	"errors"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

func noChecksum([]market.Level, []market.Level) uint32 { return 0 }

func TestNewValidates(t *testing.T) {
	src := &mockSource{}
	cases := []struct {
		name  string
		src   source.Source
		depth int
		opts  []Option
		ok    bool
	}{
		{"defaults", src, 10, nil, true},
		{"depth 1", src, 1, nil, true},
		{"depth 0", src, 0, nil, false},
		{"depth negative", src, -1, nil, false},
		{"nil source", nil, 10, nil, false},
		{"max depth 0 keeps every level", src, 10, []Option{WithMaxDepth(0)}, true},
		{"max depth negative", src, 10, []Option{WithMaxDepth(-1)}, false},
		{"narrow window without a checksum", src, 10, []Option{WithMaxDepth(3)}, true},
		{"window one below the checksum depth", src, 10, []Option{WithChecksum(noChecksum), WithMaxDepth(checksumDepth - 1)}, false},
		{"window at the checksum depth", src, 10, []Option{WithChecksum(noChecksum), WithMaxDepth(checksumDepth)}, true},
		{"checksum with no window", src, 10, []Option{WithChecksum(noChecksum)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, err := New(c.src, c.depth, c.opts...)
			if c.ok {
				if err != nil || e == nil {
					t.Fatalf("New = (%v, %v), want a valid engine", e, err)
				}
				return
			}
			if !errors.Is(err, ErrConfig) || e != nil {
				t.Fatalf("New = (%v, %v), want (nil, ErrConfig)", e, err)
			}
		})
	}
}

// Options apply in the order given, so the last one wins; a nil observer means none.
func TestOptionsApplyInOrder(t *testing.T) {
	e := mustNew(t, &mockSource{}, 5, WithMaxDepth(30), WithMaxDepth(12))
	if e.maxDepth != 12 {
		t.Fatalf("maxDepth = %d, want 12 (the later option)", e.maxDepth)
	}
	e = mustNew(t, &mockSource{}, 5, WithLatencyObserver(func(time.Duration) {}), WithLatencyObserver(nil))
	if e.latencyFn != nil {
		t.Fatal("a later nil observer must leave no observer")
	}
	e = mustNew(t, &mockSource{}, 5, WithSnapshotBackoff(nil))
	if e.retryBackoff == nil {
		t.Fatal("a nil backoff must keep the default, not clear it")
	}
}
