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

// One logical resync whose fresh snapshot is rejected counts as one resync, and each
// rejected snapshot counts once as a snapshot reject: a single disconnect followed by
// eight crossed snapshots reads 1 and 8. This test records that with literal numbers, so
// a change to either count or to maxBindAttempts has to change the test too.
func TestResyncCountsOnceAndRejectedSnapshotsSeparately(t *testing.T) {
	good := market.Snapshot{LastUpdateID: 10,
		Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}
	crossed := market.Snapshot{LastUpdateID: 20,
		Bids: []market.Level{{Price: 105, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}
	src := &scriptedSnapshots{
		snaps:  []market.Snapshot{good, crossed}, // the first bind is fine, every re-snapshot is crossed
		events: []source.Event{{Kind: source.EventDisconnected}},
	}
	e := mustNew(t, src, 5)
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
	if e.Resyncs() != 1 || e.SnapshotRejects() != 8 {
		t.Fatalf("Resyncs() = %d, SnapshotRejects() = %d, want 1 and 8", e.Resyncs(), e.SnapshotRejects())
	}
	if src.calls != 9 {
		t.Fatalf("Snapshot calls = %d, want 9 (1 bind + 8 attempts)", src.calls)
	}
}

// Characterization of the failure policy for a snapshot error that is not marked
// transient: it ends Bootstrap, and with it Step, at once, with no retry and no backoff.
// Transient failures are retried; see engine_retry_test.go. This test records the
// non-transient case; change it together with the policy.
func TestSnapshotErrorIsFatalToday(t *testing.T) {
	boom := errors.New("snapshot unavailable")
	m := &mockSource{
		snap:         market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		snapErr:      boom,
		snapErrAfter: 1, // the first bind succeeds, the resync's snapshot fails
		events:       []source.Event{{Kind: source.EventDisconnected}},
	}
	e := mustNew(t, m, 5)
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
// checksum mismatch and one snapshot reject, no resync, and the next matching snapshot binds. The synthetic
// source and the golden never exercise a checksum, so this pins the path on its own.
func TestChecksumMismatchedSnapshotIsRefetched(t *testing.T) {
	levels := func(id market.Sequence, sum uint32) market.Snapshot {
		return market.Snapshot{LastUpdateID: id, Checksum: sum,
			Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}
	}
	const want = 7
	src := &scriptedSnapshots{snaps: []market.Snapshot{levels(10, 1), levels(11, want)}} // first one disagrees
	e := mustNew(t, src, 5, WithChecksum(func([]market.Level, []market.Level) uint32 { return want }))
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.ChecksumMismatches() != 1 {
		t.Fatalf("ChecksumMismatches() = %d, want 1", e.ChecksumMismatches())
	}
	if e.SnapshotRejects() != 1 || e.Resyncs() != 0 {
		t.Fatalf("SnapshotRejects() = %d, Resyncs() = %d, want 1 and 0 (a refused bind snapshot is not a resync)", e.SnapshotRejects(), e.Resyncs())
	}
	if src.calls != 2 {
		t.Fatalf("Snapshot calls = %d, want 2 (the mismatching one, then the matching one)", src.calls)
	}
	if v := e.View(); v == nil || v.LastSeq != 11 {
		t.Fatalf("view = %+v, want the second snapshot (LastSeq 11) published", v)
	}
}
