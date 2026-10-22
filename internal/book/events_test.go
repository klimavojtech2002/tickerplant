package book

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// faultSource answers Snapshot from a script (the last entry repeats), calls hook before
// each answer, and hands out scripted source events.
type faultSource struct {
	script []snapResult
	calls  int
	events []source.Event
	i      int
	hook   func(call int)
}

var _ source.Source = (*faultSource)(nil)

func (f *faultSource) Next(context.Context) (source.Event, bool) {
	if f.i >= len(f.events) {
		return source.Event{}, false
	}
	ev := f.events[f.i]
	f.i++
	return ev, true
}
func (f *faultSource) Close() error { return nil }
func (f *faultSource) Snapshot(context.Context) (market.Snapshot, error) {
	call := f.calls
	f.calls++
	if f.hook != nil {
		f.hook(call)
	}
	r := f.script[min(call, len(f.script)-1)]
	return r.snap, r.err
}

// ckSnap is a snapshot whose book (bid 100, ask 101) has the checksum sumChecksum gives it,
// unless sum says otherwise.
func ckSnap(id market.Sequence, sum uint32) market.Snapshot {
	return market.Snapshot{LastUpdateID: id, Checksum: sum,
		Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}
}

func delta(first, last market.Sequence, sum uint32, bids ...market.Level) source.Event {
	return source.Event{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: first, LastSeq: last, Bids: bids, Checksum: sum}}
}

// Each of the six kinds, in one scripted run. The fake clock moves 5 ms with every
// Snapshot call, so a resync's duration is 5 ms times its snapshot calls.
func TestEventKindsAndFields(t *testing.T) {
	const start = 1_700_000_000_000 // ms
	clk := &fakeClock{t: time.UnixMilli(start)}
	src := &faultSource{
		script: []snapResult{
			{snap: ckSnap(10, 201)},                  // bootstrap
			{snap: ckSnap(20, 201)},                  // resync after the gap
			{snap: ckSnap(30, 201)},                  // resync after the cross
			{snap: crossedSnap},                      // resync after the disconnect: crossed (id 20)
			{snap: ckSnap(41, 7)},                    // checksum does not match
			{snap: ckSnap(50, 201)},                  // bound
			{err: source.Transient(errors.New("x"))}, // resync after the checksum drift: one transient error
			{snap: ckSnap(60, 201)},
		},
		events: []source.Event{
			delta(25, 25, 0, market.Level{Price: 99, Size: 1}),   // gap: expected 11, got 25
			delta(21, 22, 0, market.Level{Price: 105, Size: 1}),  // crosses the ask at 101
			{Kind: source.EventDisconnected},                     //
			delta(51, 52, 999, market.Level{Price: 99, Size: 2}), // applies, checksum is wrong
		},
		hook: func(int) { clk.advance(5 * time.Millisecond) },
	}
	sleep, _ := recordedSleeps()
	e := mustNew(t, src, 5, WithClock(clk.now), WithChecksum(sumChecksum), WithSnapshotRetry(5, linearBackoff, sleep))
	ctx := context.Background()
	if err := e.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	for i := range src.events {
		if _, err := e.Step(ctx); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	crossedID := crossedSnap.LastUpdateID
	want := []Event{
		{1, KindGap, start + 5, 11, 25, "sequence gap", 5},
		{2, KindCross, start + 10, 0, 22, "delta would cross the book", 5},
		{3, KindDisconnect, start + 15, 0, 0, "source disconnected", 15},
		{4, KindSnapshotReject, start + 20, 0, crossedID, "snapshot crossed", 0},
		{5, KindSnapshotReject, start + 25, 0, 41, "snapshot checksum mismatch", 0},
		{6, KindChecksum, start + 30, 0, 52, "delta checksum mismatch", 10},
		{7, KindSnapshotError, start + 35, 0, 0, "transient snapshot failure", 0},
	}
	got := e.Events()
	if len(got) != len(want) {
		t.Fatalf("%d events, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The counters and the events must agree. Literals on purpose: the scenario above
	// has one gap, one cross, one disconnect, one delta drift, two refused snapshots
	// (one of them with a wrong checksum) and one failed Snapshot call.
	if e.Gaps() != 1 || e.WouldCrosses() != 1 || e.Disconnects() != 1 {
		t.Errorf("gaps/crosses/disconnects = %d/%d/%d, want 1/1/1", e.Gaps(), e.WouldCrosses(), e.Disconnects())
	}
	if e.SnapshotRejects() != 2 || e.SnapshotErrors() != 1 {
		t.Errorf("rejects/errors = %d/%d, want 2/1", e.SnapshotRejects(), e.SnapshotErrors())
	}
	if e.ChecksumMismatches() != 2 { // the delta drift and the refused snapshot with a wrong checksum
		t.Errorf("ChecksumMismatches = %d, want 2", e.ChecksumMismatches())
	}
	if e.Resyncs() != 4 { // gap + cross + disconnect + checksum event
		t.Errorf("Resyncs = %d, want 4", e.Resyncs())
	}
}

func TestEventRingKeepsTheLast16(t *testing.T) {
	e := &Engine{}
	if len(e.Events()) != 0 || e.Events() == nil {
		t.Fatalf("a fresh engine must report an empty, non-nil list, got %#v", e.Events())
	}
	for range 20 {
		e.recordEvent(KindGap, 1, 2, reasonGap, unknownDuration)
	}
	got := e.Events()
	if len(got) != 16 {
		t.Fatalf("%d events kept, want 16", len(got))
	}
	for i, ev := range got {
		if want := uint64(5 + i); ev.ID != want {
			t.Fatalf("event %d has ID %d, want %d (oldest first, contiguous)", i, ev.ID, want)
		}
	}
}

// A slice handed out once never changes, whether the next change is a new event or a
// duration patch.
func TestEventsSliceIsImmutable(t *testing.T) {
	e := &Engine{}
	id := e.recordEvent(KindGap, 1, 2, reasonGap, unknownDuration)
	first := e.Events()
	e.recordEvent(KindCross, 0, 3, reasonCross, unknownDuration)
	if len(first) != 1 || first[0].ID != id {
		t.Fatalf("a held slice changed after a new event: %+v", first)
	}
	second := e.Events()
	e.setEventDuration(id, 42)
	if second[0].DurationMs != unknownDuration {
		t.Fatalf("a held slice changed after a duration patch: %+v", second)
	}
	if e.Events()[0].DurationMs != 42 {
		t.Fatalf("the patch did not reach the current list: %+v", e.Events())
	}
}

func TestEventsConcurrentReaders(t *testing.T) {
	e := &Engine{}
	done := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
					evs := e.Events()
					for i := 1; i < len(evs); i++ {
						if evs[i].ID != evs[i-1].ID+1 {
							t.Error("a reader saw non-contiguous IDs")
							return
						}
					}
				}
			}
		})
	}
	for range 2000 {
		id := e.recordEvent(KindGap, 1, 2, reasonGap, unknownDuration)
		e.setEventDuration(id, 1)
	}
	close(done)
	wg.Wait()
}

// A resync that fails still reports its duration; so does one the caller cancels.
func TestEventDurationIsStampedWhenTheResyncFails(t *testing.T) {
	boom := errors.New("not retryable")
	for _, tc := range []struct {
		name   string
		cancel bool
		events int
	}{{"snapshot error", false, 2}, {"cancelled", true, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			clk := &fakeClock{t: time.UnixMilli(1000)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			src := &faultSource{
				script: []snapResult{{snap: goodSnap}, {err: boom}},
				events: []source.Event{{Kind: source.EventDisconnected}},
				hook: func(call int) {
					if call > 0 {
						clk.advance(7 * time.Millisecond)
						if tc.cancel {
							cancel()
						}
					}
				},
			}
			e := mustNew(t, src, 5, WithClock(clk.now))
			if err := e.Bootstrap(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Step(ctx); err == nil {
				t.Fatal("want the resync to fail")
			}
			evs := e.Events()
			if len(evs) != tc.events {
				t.Fatalf("%d events, want %d: %+v", len(evs), tc.events, evs)
			}
			if evs[0].Kind != KindDisconnect || evs[0].DurationMs != 7 {
				t.Fatalf("trigger = %+v, want a disconnect with DurationMs 7", evs[0])
			}
		})
	}
}

// The resync that trips the livelock guard never bootstraps: its event keeps the unknown
// duration and the clock is not read.
func TestEventDurationStaysUnknownOnLivelockExit(t *testing.T) {
	reads := 0
	clk := func() time.Time { reads++; return time.UnixMilli(5000) }
	e := mustNew(t, &mockSource{snap: readySnap}, 5, WithClock(clk))
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := e.recordEvent(KindDisconnect, 0, 0, reasonDisconnect, unknownDuration)
	e.consecutiveResyncs = maxConsecutiveResyncs
	before := reads
	if err := e.resync(context.Background(), id); !errors.Is(err, market.ErrStaleSnapshot) {
		t.Fatalf("err = %v, want ErrStaleSnapshot", err)
	}
	if reads != before {
		t.Fatalf("the livelock exit read the clock %d times, want 0", reads-before)
	}
	if got := e.Events()[0].DurationMs; got != unknownDuration {
		t.Fatalf("DurationMs = %d, want %d", got, unknownDuration)
	}
}

// A clock that steps back during a bootstrap must not turn a real duration into the -1
// "unknown" marker.
func TestEventDurationIsNeverNegative(t *testing.T) {
	now := time.UnixMilli(10_000)
	src := &faultSource{
		script: []snapResult{{snap: goodSnap}},
		events: []source.Event{{Kind: source.EventDisconnected}},
		hook: func(call int) {
			if call > 0 {
				now = now.Add(-time.Minute)
			}
		},
	}
	e := mustNew(t, src, 5, WithClock(func() time.Time { return now }))
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.Events()[0].DurationMs; got != 0 {
		t.Fatalf("DurationMs = %d after the clock stepped back, want 0", got)
	}
}

// One bootstrap can record more events than the ring holds, pushing its own trigger
// out. The patch is by ID, so it must then change nothing.
func TestEventPatchIsByIDWhenTheTriggerWasEvicted(t *testing.T) {
	clk := &fakeClock{t: time.UnixMilli(1000)}
	script := []snapResult{{snap: goodSnap}}
	for range maxBindAttempts {
		script = append(script, snapResult{err: errNet}, snapResult{err: errNet}, snapResult{snap: crossedSnap})
	}
	src := &faultSource{
		script: script,
		events: []source.Event{{Kind: source.EventDisconnected}},
		hook:   func(call int) { clk.advance(5 * time.Millisecond) },
	}
	sleep, _ := recordedSleeps()
	e := mustNew(t, src, 5, WithClock(clk.now), WithSnapshotRetry(5, linearBackoff, sleep))
	ctx := context.Background()
	if err := e.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(ctx); !errors.Is(err, market.ErrCrossed) {
		t.Fatalf("err = %v, want ErrCrossed after %d refused snapshots", err, maxBindAttempts)
	}
	// 1 disconnect + 8 x (2 errors + 1 reject) = 25 events; the last 16 are IDs 10..25.
	evs := e.Events()
	if len(evs) != 16 || evs[0].ID != 10 || evs[15].ID != 25 {
		t.Fatalf("ring = %d events, first %d, last %d; want 16, 10, 25", len(evs), evs[0].ID, evs[len(evs)-1].ID)
	}
	for _, ev := range evs {
		if ev.DurationMs != 0 {
			t.Fatalf("event %+v: an evicted trigger's patch changed another entry", ev)
		}
	}
}

// The trigger comes first and the events its bootstrap records come after it.
func TestEventOrderTriggerBeforeItsSnapshotEvents(t *testing.T) {
	src := &faultSource{
		script: []snapResult{{snap: goodSnap}, {err: errNet}, {snap: crossedSnap}, {snap: goodSnap}},
		events: []source.Event{{Kind: source.EventDisconnected}},
	}
	sleep, _ := recordedSleeps()
	e := mustNew(t, src, 5, WithSnapshotRetry(5, linearBackoff, sleep))
	ctx := context.Background()
	if err := e.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(ctx); err != nil {
		t.Fatal(err)
	}
	var kinds []EventKind
	for i, ev := range e.Events() {
		if ev.ID != uint64(i+1) {
			t.Fatalf("event %d has ID %d", i, ev.ID)
		}
		kinds = append(kinds, ev.Kind)
	}
	want := []EventKind{KindDisconnect, KindSnapshotError, KindSnapshotReject}
	if len(kinds) != len(want) || kinds[0] != want[0] || kinds[1] != want[1] || kinds[2] != want[2] {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
}

// An error carries addresses and venue text; the event carries one of two fixed phrases.
func TestSnapshotErrorReasonIsAFixedCategory(t *testing.T) {
	leaky := errors.New(`GET http://10.0.0.7:8080/api/v3/depth?symbol=X: dial tcp 10.0.0.7:8080: connection refused`)
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"transient", source.Transient(leaky), "transient snapshot failure"},
		{"other", leaky, "snapshot failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := mustNew(t, &faultSource{script: []snapResult{{err: tc.err}}}, 5, WithSnapshotRetry(0, nil, nil))
			if err := e.Bootstrap(context.Background()); err == nil {
				t.Fatal("want an error")
			}
			evs := e.Events()
			if len(evs) != 1 || evs[0].Kind != KindSnapshotError || evs[0].Reason != tc.want {
				t.Fatalf("events = %+v, want one snapshot-error with reason %q", evs, tc.want)
			}
			if strings.Contains(evs[0].Reason, "10.0.0.7") || !utf8.ValidString(evs[0].Reason) {
				t.Fatalf("reason leaks error text: %q", evs[0].Reason)
			}
		})
	}
}

// A shutdown is not a snapshot failure, so it leaves no event either.
func TestSnapshotErrorEventNotRecordedForCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := mustNew(t, &faultSource{script: []snapResult{{err: source.Transient(context.Canceled)}}}, 5)
	if err := e.Bootstrap(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := len(e.Events()); n != 0 || e.SnapshotErrors() != 0 {
		t.Fatalf("%d events and %d snapshot errors after a cancellation, want 0 and 0", n, e.SnapshotErrors())
	}
}
