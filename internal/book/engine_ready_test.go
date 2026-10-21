package book

import (
	"context"
	"errors"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

var readySnap = market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}

func TestViewGenStartsAtOneAndCountsPublishesOnly(t *testing.T) {
	m := &mockSource{
		snap: readySnap,
		events: []source.Event{
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 99, Size: 2}}}},
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 99, Size: 3}}}}, // duplicate: dropped
			{Kind: source.EventTrade, Trade: market.Trade{Price: 100, Size: 1, Side: market.Bid}},
			{Kind: source.EventDisconnected}, // resync rebinds to a snapshot older than LastSeq 11
		},
	}
	e := mustNew(t, m, 5)
	if e.View() != nil {
		t.Fatal("a view before the first bootstrap")
	}
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		gen uint64
		seq market.Sequence
	}{
		{1, 10}, // bootstrap
		{2, 11}, // applied delta
		{2, 11}, // duplicate delta: no publish
		{2, 11}, // trade: no publish
		{3, 10}, // resync: Gen goes up although LastSeq went back
	}
	for i, w := range want {
		if i > 0 {
			if _, err := e.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		if v := e.View(); v.Gen != w.gen || v.LastSeq != w.seq {
			t.Fatalf("step %d: Gen=%d LastSeq=%d, want Gen=%d LastSeq=%d", i, v.Gen, v.LastSeq, w.gen, w.seq)
		}
	}
}

// resyncGate answers the first Snapshot at once and blocks the second until released,
// so a test can look at the engine while a resync is in flight.
type resyncGate struct {
	mockSource
	entered chan struct{}
	release chan struct{}
	failure error // returned by the second Snapshot when set
}

func (g *resyncGate) Snapshot(ctx context.Context) (market.Snapshot, error) {
	g.snapCalls++
	if g.snapCalls == 1 {
		return g.snap, nil
	}
	close(g.entered)
	<-g.release
	if g.failure != nil {
		return market.Snapshot{}, g.failure
	}
	return g.snap, nil
}

func TestReadyFollowsTheResyncLifecycle(t *testing.T) {
	g := &resyncGate{
		mockSource: mockSource{snap: readySnap, events: []source.Event{{Kind: source.EventDisconnected}}},
		entered:    make(chan struct{}), release: make(chan struct{}),
	}
	e := mustNew(t, g, 5)
	if e.Ready() {
		t.Fatal("ready before the first bootstrap")
	}
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !e.Ready() {
		t.Fatal("not ready after a successful bootstrap")
	}
	done := make(chan error, 1)
	go func() { _, err := e.Step(context.Background()); done <- err }()
	<-g.entered
	if e.Ready() {
		t.Fatal("ready while a resync is waiting for its snapshot")
	}
	if e.View() == nil {
		t.Fatal("the old view should still be published during the resync")
	}
	close(g.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !e.Ready() {
		t.Fatal("not ready after the resync published its view")
	}
}

func TestReadyStaysFalseWhenAResyncFails(t *testing.T) {
	boom := errors.New("snapshot refused")
	g := &resyncGate{
		mockSource: mockSource{snap: readySnap, events: []source.Event{{Kind: source.EventDisconnected}}},
		entered:    make(chan struct{}), release: make(chan struct{}), failure: boom,
	}
	close(g.release)
	e := mustNew(t, g, 5)
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	genBefore := e.View().Gen
	if _, err := e.Step(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the snapshot error", err)
	}
	if e.Ready() {
		t.Fatal("ready after a resync that failed")
	}
	if got := e.View().Gen; got != genBefore {
		t.Fatalf("Gen moved from %d to %d without a publish", genBefore, got)
	}
}

func TestReadyIsFalseAfterColdStartFailure(t *testing.T) {
	m := &mockSource{snapErr: errors.New("down")}
	e := mustNew(t, m, 5)
	if err := e.Bootstrap(context.Background()); err == nil {
		t.Fatal("want an error")
	}
	if e.Ready() || e.View() != nil {
		t.Fatal("a failed cold start must publish nothing and not be ready")
	}
}

// A feed that never makes progress ends in the livelock error; the engine counted every
// resync it started, including the one that tripped the guard, and is not ready.
func TestLivelockExitCountsEveryResyncAndIsNotReady(t *testing.T) {
	e := mustNew(t, staleSource(maxConsecutiveResyncs+1), 5)
	err := e.Run(context.Background())
	if !errors.Is(err, market.ErrStaleSnapshot) {
		t.Fatalf("err = %v, want ErrStaleSnapshot", err)
	}
	if got := e.Resyncs(); got != maxConsecutiveResyncs+1 {
		t.Fatalf("Resyncs = %d, want %d", got, maxConsecutiveResyncs+1)
	}
	if e.Ready() {
		t.Fatal("ready after the livelock exit")
	}
}
