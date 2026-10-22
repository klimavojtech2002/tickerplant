package book

import (
	"context"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// fakeClock returns a time the test sets; it is not safe for concurrent use, which is
// allowed because the engine reads its clock only on the writer goroutine.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestLastEventAtFollowsEveryProcessedEvent(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	m := &mockSource{
		snap: readySnap,
		events: []source.Event{
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 99, Size: 2}}}},
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 99, Size: 3}}}}, // duplicate: dropped
			{Kind: source.EventTrade, Trade: market.Trade{Price: 100, Size: 1, Side: market.Bid}},
			{Kind: source.EventDisconnected},
		},
	}
	e := mustNew(t, m, 5, WithClock(clk.now))
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.LastEventAt(); !got.IsZero() {
		t.Fatalf("LastEventAt = %v before any event, want the zero time", got)
	}
	for i, name := range []string{"delta", "dropped duplicate", "trade", "disconnect"} {
		clk.advance(time.Second)
		if _, err := e.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := e.LastEventAt(); !got.Equal(clk.t) {
			t.Fatalf("after %s (event %d): LastEventAt = %v, want %v", name, i, got, clk.t)
		}
	}
	if e.LastEventAt().Location() != time.UTC {
		t.Fatalf("LastEventAt must be in UTC, got %v", e.LastEventAt().Location())
	}
	// The source is exhausted now: Step returns false and must not stamp.
	before := e.LastEventAt()
	clk.advance(time.Hour)
	if ok, err := e.Step(context.Background()); ok || err != nil {
		t.Fatalf("exhausted source: ok=%v err=%v", ok, err)
	}
	if got := e.LastEventAt(); !got.Equal(before) {
		t.Fatalf("an exhausted source moved LastEventAt from %v to %v", before, got)
	}
}

// A clock reading of exactly the Unix epoch is a real event, not "no event yet".
func TestLastEventAtAtEpochZeroIsSet(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	m := &mockSource{snap: readySnap, events: []source.Event{{Kind: source.EventTrade}}}
	e := mustNew(t, m, 5, WithClock(clk.now))
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := e.LastEventAt()
	if !got.Equal(time.Unix(0, 0)) {
		t.Fatalf("LastEventAt = %v, want the epoch", got)
	}
	if got.IsZero() {
		t.Fatal("an event at the Unix epoch must not read as the zero time")
	}
}

func TestWithClockNilKeepsTheDefaultAndLastOptionWins(t *testing.T) {
	e := mustNew(t, &mockSource{snap: readySnap}, 5, WithClock(nil))
	if e.clock != nil {
		t.Fatal("WithClock(nil) must leave the default (no clock set)")
	}
	before := time.Now()
	if got := e.now(); got.Before(before) || got.After(time.Now()) {
		t.Fatalf("default clock read %v, want about now", got)
	}
	first, second := &fakeClock{t: time.Unix(1, 0)}, &fakeClock{t: time.Unix(2, 0)}
	e = mustNew(t, &mockSource{snap: readySnap}, 5, WithClock(first.now), WithClock(second.now))
	if got := e.now(); !got.Equal(second.t) {
		t.Fatalf("now = %v, want the later option's clock %v", got, second.t)
	}
	if got := (&Engine{}).now(); got.IsZero() {
		t.Fatal("an Engine literal without a clock must fall back to time.Now")
	}
}
