package book

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// A snapshot call result for retrySource: either a snapshot or an error.
type snapResult struct {
	snap market.Snapshot
	err  error
}

// retrySource answers Snapshot from a script; the last entry repeats.
type retrySource struct {
	script []snapResult
	calls  int
}

var _ source.Source = (*retrySource)(nil)

func (r *retrySource) Next(context.Context) (source.Event, bool) { return source.Event{}, false }
func (r *retrySource) Close() error                              { return nil }
func (r *retrySource) Snapshot(context.Context) (market.Snapshot, error) {
	i := min(r.calls, len(r.script)-1)
	r.calls++
	return r.script[i].snap, r.script[i].err
}

var (
	goodSnap    = market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}
	crossedSnap = market.Snapshot{LastUpdateID: 20, Bids: []market.Level{{Price: 105, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}
	errNet      = source.Transient(errors.New("network down"))
)

func fails(n int, err error) []snapResult {
	out := make([]snapResult, n)
	for i := range out {
		out[i] = snapResult{err: err}
	}
	return out
}

// recordedSleeps returns a sleep that records each delay and never waits.
func recordedSleeps() (func(context.Context, time.Duration) error, *[]time.Duration) {
	var got []time.Duration
	return func(_ context.Context, d time.Duration) error {
		got = append(got, d)
		return nil
	}, &got
}

func linearBackoff(attempt int) time.Duration { return time.Duration(attempt+1) * time.Second }

func TestBootstrapRetriesTransientFailures(t *testing.T) {
	for _, k := range []int{0, 1, 3, 5} {
		src := &retrySource{script: append(fails(k, errNet), snapResult{snap: goodSnap})}
		sleep, delays := recordedSleeps()
		e := mustNew(t, src, 5, WithSnapshotRetry(5, linearBackoff, sleep))
		if err := e.Bootstrap(context.Background()); err != nil {
			t.Fatalf("k=%d: %v", k, err)
		}
		if src.calls != k+1 {
			t.Errorf("k=%d: Snapshot calls = %d, want %d", k, src.calls, k+1)
		}
		if got := int(e.SnapshotErrors()); got != k {
			t.Errorf("k=%d: snapshotErrors = %d, want %d", k, got, k)
		}
		if len(*delays) != k {
			t.Fatalf("k=%d: slept %d times, want %d", k, len(*delays), k)
		}
		for i, d := range *delays {
			if want := linearBackoff(i); d != want {
				t.Errorf("k=%d: delay %d = %v, want backoff(%d) = %v", k, i, d, i, want)
			}
		}
		if e.View() == nil || e.Resyncs() != 0 {
			t.Errorf("k=%d: want a published view and no resync counted for transport failures", k)
		}
	}
}

func TestBootstrapGivesUpAfterMaxRetries(t *testing.T) {
	last := source.Transient(errors.New("still down"))
	script := append(fails(5, errNet), snapResult{err: last}) // 6 failures, then more of the same
	src := &retrySource{script: script}
	sleep, delays := recordedSleeps()
	e := mustNew(t, src, 5, WithSnapshotRetry(5, linearBackoff, sleep))
	err := e.Bootstrap(context.Background())
	if err != last {
		t.Fatalf("err = %v, want the last error returned as is", err)
	}
	// Literals on purpose: 1 attempt + 5 retries.
	if src.calls != 6 || e.SnapshotErrors() != 6 || len(*delays) != 5 {
		t.Fatalf("calls = %d, snapshotErrors = %d, sleeps = %d; want 6, 6, 5", src.calls, e.SnapshotErrors(), len(*delays))
	}
	if e.View() != nil {
		t.Fatal("a failed bootstrap must publish nothing")
	}
}

func TestBootstrapNonTransientErrorReturnsAtOnce(t *testing.T) {
	boom := errors.New("not retryable")
	src := &retrySource{script: []snapResult{{err: boom}}}
	sleep, delays := recordedSleeps()
	e := mustNew(t, src, 5, WithSnapshotRetry(5, linearBackoff, sleep))
	if err := e.Bootstrap(context.Background()); err != boom {
		t.Fatalf("err = %v, want the error unchanged", err)
	}
	if src.calls != 1 || len(*delays) != 0 || e.SnapshotErrors() != 1 {
		t.Fatalf("calls = %d, sleeps = %d, snapshotErrors = %d; want 1, 0, 1", src.calls, len(*delays), e.SnapshotErrors())
	}
}

func TestBootstrapRetriesDisabled(t *testing.T) {
	src := &retrySource{script: []snapResult{{err: errNet}}}
	sleep, delays := recordedSleeps()
	e := mustNew(t, src, 5, WithSnapshotRetry(0, linearBackoff, sleep))
	if err := e.Bootstrap(context.Background()); !source.IsTransient(err) {
		t.Fatalf("err = %v, want the transient error returned", err)
	}
	if src.calls != 1 || len(*delays) != 0 {
		t.Fatalf("calls = %d, sleeps = %d; want 1, 0 with retries disabled", src.calls, len(*delays))
	}
}

// Retrying a transport failure must not use up the bind attempts: eight rejected
// snapshots still end Bootstrap with ErrCrossed, however many transport failures were
// interleaved, and a snapshot that arrives resets the run of consecutive failures.
func TestBootstrapRetriesDoNotConsumeBindAttempts(t *testing.T) {
	var script []snapResult
	for i := 0; i < 8; i++ {
		script = append(script, snapResult{err: errNet}, snapResult{snap: crossedSnap})
	}
	src := &retrySource{script: script}
	sleep, _ := recordedSleeps()
	e := mustNew(t, src, 5, WithSnapshotRetry(1, linearBackoff, sleep)) // 1 retry: only works if the count resets
	err := e.Bootstrap(context.Background())
	if !errors.Is(err, market.ErrCrossed) {
		t.Fatalf("err = %v, want ErrCrossed after 8 rejected snapshots", err)
	}
	// Literals on purpose: 8 crossed snapshots, each preceded by one transport failure.
	if src.calls != 16 || e.SnapshotRejects() != 8 || e.SnapshotErrors() != 8 || e.Resyncs() != 0 {
		t.Fatalf("calls = %d, snapshotRejects = %d, snapshotErrors = %d, resyncs = %d; want 16, 8, 8, 0",
			src.calls, e.SnapshotRejects(), e.SnapshotErrors(), e.Resyncs())
	}
}

// The default sleep waits on a timer, and the caller's cancellation ends it at once.
func TestBootstrapCancelDuringBackoff(t *testing.T) {
	src := &retrySource{script: []snapResult{{err: errNet}}}
	e := mustNew(t, src, 5, WithSnapshotRetry(5, func(int) time.Duration { return time.Hour }, nil))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() { done <- e.Bootstrap(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Bootstrap kept waiting out an hour-long backoff after cancellation")
	}
}

// A shutdown is not a snapshot failure: it is neither retried nor counted.
func TestBootstrapCancelledIsNotCountedOrRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := &retrySource{script: []snapResult{{err: source.Transient(context.Canceled)}}}
	sleep, delays := recordedSleeps()
	e := mustNew(t, src, 5, WithSnapshotRetry(5, linearBackoff, sleep))
	if err := e.Bootstrap(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if src.calls != 1 || len(*delays) != 0 || e.SnapshotErrors() != 0 {
		t.Fatalf("calls = %d, sleeps = %d, snapshotErrors = %d; want 1, 0, 0", src.calls, len(*delays), e.SnapshotErrors())
	}
}

func TestDefaultSnapshotBackoff(t *testing.T) {
	want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second}
	for i, w := range want {
		if got := defaultSnapshotBackoff(i); got != w {
			t.Errorf("attempt %d: %v, want %v", i, got, w)
		}
	}
	if got := defaultSnapshotBackoff(10_000); got != 4*time.Second {
		t.Errorf("a huge attempt number: %v, want the cap", got)
	}
}

func TestSleepCtx(t *testing.T) {
	if err := sleepCtx(context.Background(), 0); err != nil {
		t.Fatalf("zero delay: %v", err)
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("short delay: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if err := sleepCtx(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled with zero delay: %v", err)
	}
}

func TestWithSnapshotBackoffReplacesOnlyTheDelay(t *testing.T) {
	src := &retrySource{script: append(fails(2, errNet), snapResult{snap: goodSnap})}
	sleep, delays := recordedSleeps()
	e := mustNew(t, src, 5, WithSnapshotRetry(5, nil, sleep), WithSnapshotBackoff(linearBackoff))
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*delays) != 2 || (*delays)[0] != time.Second || (*delays)[1] != 2*time.Second {
		t.Fatalf("delays = %v, want [1s 2s] from the replaced backoff", *delays)
	}
}
