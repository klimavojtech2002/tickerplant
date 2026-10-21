package book

import (
	"context"
	"errors"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// scriptedSnapshots serves snapshots in order (the last one repeats forever) and a fixed
// list of events, and counts Snapshot calls. The synthetic source cannot do this: it never
// serves a rejected snapshot after the first bind.
type scriptedSnapshots struct {
	snaps  []market.Snapshot
	events []source.Event
	calls  int
	next   int
}

var _ source.Source = (*scriptedSnapshots)(nil)

func (s *scriptedSnapshots) Next(context.Context) (source.Event, bool) {
	if s.next >= len(s.events) {
		return source.Event{}, false
	}
	e := s.events[s.next]
	s.next++
	return e, true
}

func (s *scriptedSnapshots) Snapshot(context.Context) (market.Snapshot, error) {
	i := s.calls
	if i >= len(s.snaps) {
		i = len(s.snaps) - 1
	}
	s.calls++
	return s.snaps[i], nil
}

func (s *scriptedSnapshots) Close() error { return nil }

// Characterization of how Resyncs counts one logical resync whose fresh snapshot is
// rejected. Today the counter adds one for the resync itself and one more for every
// rejected snapshot inside Bootstrap, so a single disconnect followed by eight crossed
// snapshots reads 9. Bind-time rejections and post-bind resyncs share one number by
// design (see Resyncs). This test records that behaviour with literal numbers, so a
// change to the count or to maxBindAttempts has to change the test too.
func TestResyncsCountOnRejectedResnapshotToday(t *testing.T) {
	good := market.Snapshot{LastUpdateID: 10,
		Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}
	crossed := market.Snapshot{LastUpdateID: 20,
		Bids: []market.Level{{Price: 105, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}
	src := &scriptedSnapshots{
		snaps:  []market.Snapshot{good, crossed}, // the first bind is fine, every re-snapshot is crossed
		events: []source.Event{{Kind: source.EventDisconnected}},
	}
	e := New(src, 5)
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.Resyncs() != 0 {
		t.Fatalf("a clean bind must not count: Resyncs() = %d", e.Resyncs())
	}

	ok, err := e.Step(context.Background())
	if ok {
		t.Fatal("Step must stop when the re-snapshot keeps being rejected")
	}
	if !errors.Is(err, market.ErrCrossed) {
		t.Fatalf("err = %v, want ErrCrossed after %d rejected snapshots", err, maxBindAttempts)
	}
	if e.Disconnects() != 1 {
		t.Fatalf("Disconnects() = %d, want 1", e.Disconnects())
	}
	// Literals on purpose: expectations built from maxBindAttempts would move with it.
	if e.Resyncs() != 9 {
		t.Fatalf("Resyncs() = %d, want 9 (1 resync + 8 rejected snapshots)", e.Resyncs())
	}
	if src.calls != 9 {
		t.Fatalf("Snapshot calls = %d, want 9 (1 bind + 8 attempts)", src.calls)
	}
}

// Characterization of today's failure policy for a snapshot that cannot be fetched: the
// error ends Bootstrap, and with it Step, at once. There is no retry and no backoff, so a
// single transient failure (a rate limit, a timeout) ends the pipeline. This test records
// that behaviour; change it together with the policy.
func TestSnapshotErrorIsFatalToday(t *testing.T) {
	boom := errors.New("snapshot unavailable")
	m := &mockSource{
		snap:         market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		snapErr:      boom,
		snapErrAfter: 1, // the first bind succeeds, the resync's snapshot fails
		events:       []source.Event{{Kind: source.EventDisconnected}},
	}
	e := New(m, 5)
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	ok, err := e.Step(context.Background())
	if ok || !errors.Is(err, boom) {
		t.Fatalf("Step = (%v, %v), want (false, the snapshot error)", ok, err)
	}
	if m.snapCalls != 2 {
		t.Fatalf("Snapshot calls = %d, want 2 (one bind, one failed resync, no retry)", m.snapCalls)
	}
	if e.Resyncs() != 1 {
		t.Fatalf("Resyncs() = %d, want 1", e.Resyncs())
	}
}

// A snapshot whose checksum the engine cannot reproduce is refetched: it counts as one
// checksum mismatch and one resync, and the next matching snapshot binds. The synthetic
// source and the golden never exercise a checksum, so this pins the path on its own.
func TestChecksumMismatchedSnapshotIsRefetched(t *testing.T) {
	levels := func(id market.Sequence, sum uint32) market.Snapshot {
		return market.Snapshot{LastUpdateID: id, Checksum: sum,
			Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}
	}
	const want = 7
	src := &scriptedSnapshots{snaps: []market.Snapshot{levels(10, 1), levels(11, want)}} // first one disagrees
	e := New(src, 5).WithChecksum(func([]market.Level, []market.Level) uint32 { return want })
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.ChecksumMismatches() != 1 {
		t.Fatalf("ChecksumMismatches() = %d, want 1", e.ChecksumMismatches())
	}
	if e.Resyncs() != 1 {
		t.Fatalf("Resyncs() = %d, want 1 (the rejected snapshot counts as a resync)", e.Resyncs())
	}
	if src.calls != 2 {
		t.Fatalf("Snapshot calls = %d, want 2 (the mismatching one, then the matching one)", src.calls)
	}
	if v := e.View(); v == nil || v.LastSeq != 11 {
		t.Fatalf("view = %+v, want the second snapshot (LastSeq 11) published", v)
	}
}
