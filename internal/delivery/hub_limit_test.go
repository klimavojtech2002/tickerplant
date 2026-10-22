package delivery

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// doorbellClosed reports whether the consumer's doorbell has been closed. A signal still
// sitting in the buffer is read first and does not count as closed.
func doorbellClosed(c *Consumer) bool {
	for {
		select {
		case _, ok := <-c.Ready():
			if !ok {
				return true
			}
		default:
			return false
		}
	}
}

func TestTrySubscribeLimit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing int
		limit    int
		ok       bool
	}{
		{"limit 0 allows none", 0, 0, false},
		{"negative limit allows none", 0, -3, false},
		{"limit 1, none yet", 0, 1, true},
		{"limit 1, one present", 1, 1, false},
		{"limit 2, one present", 1, 2, true},
		{"limit 2, two present", 2, 2, false},
		{"limit 2, three present", 3, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := New(8)
			for range tc.existing {
				h.Subscribe()
			}
			c, err := h.TrySubscribe(tc.limit)
			if tc.ok {
				if err != nil || c == nil {
					t.Fatalf("TrySubscribe = %v, %v; want a consumer", c, err)
				}
				return
			}
			if !errors.Is(err, ErrFull) || c != nil {
				t.Fatalf("TrySubscribe = %v, %v; want nil and ErrFull", c, err)
			}
		})
	}
}

// A closed hub says ErrClosed even when the limit would also refuse. Close empties the
// hub, so ErrFull can only apply there for a limit below 1, which is what limit 0 checks.
func TestTrySubscribeOnClosedHub(t *testing.T) {
	for _, limit := range []int{0, 5} {
		h := New(8)
		h.Close()
		c, err := h.TrySubscribe(limit)
		if !errors.Is(err, ErrClosed) || c != nil {
			t.Fatalf("limit %d: TrySubscribe = %v, %v; want nil and ErrClosed", limit, c, err)
		}
	}
}

// A refusal uses up no id, and the ids that are handed out are consecutive.
func TestTrySubscribeRefusalKeepsIDs(t *testing.T) {
	h := New(8)
	a := h.Subscribe()
	if c, err := h.TrySubscribe(1); c != nil || !errors.Is(err, ErrFull) {
		t.Fatalf("want a refusal, got %v, %v", c, err)
	}
	b := h.Subscribe()
	if b.ID() != a.ID()+1 {
		t.Fatalf("ids %d then %d: a refused TrySubscribe must not use one up", a.ID(), b.ID())
	}
}

func TestTrySubscribeNeverExceedsTheLimit(t *testing.T) {
	h := New(8)
	var ok atomic.Int64
	var wg sync.WaitGroup
	for range 200 {
		wg.Go(func() {
			if _, err := h.TrySubscribe(5); err == nil {
				ok.Add(1)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 5 || h.Stats().Consumers != 5 {
		t.Fatalf("%d successes and %d consumers, want 5 and 5", ok.Load(), h.Stats().Consumers)
	}
}

func TestUnsubscribingFreesASlot(t *testing.T) {
	h := New(8)
	a, _ := h.TrySubscribe(1)
	h.Unsubscribe(a.ID())
	if _, err := h.TrySubscribe(1); err != nil {
		t.Fatalf("a freed slot must be usable, got %v", err)
	}
}

// An internal consumer is not a viewer: it is in no count and uses up no slot.
func TestInternalConsumerIsNotCounted(t *testing.T) {
	h := New(8)
	in := h.SubscribeInternal()
	if s := h.Stats(); s.Consumers != 0 {
		t.Fatalf("Consumers = %d with only an internal consumer, want 0", s.Consumers)
	}
	viewer, err := h.TrySubscribe(1)
	if err != nil {
		t.Fatalf("an internal consumer must not use up the limit: %v", err)
	}
	if s := h.Stats(); s.Consumers != 1 {
		t.Fatalf("Consumers = %d, want 1 (the viewer only)", s.Consumers)
	}
	h.Publish(view(1))
	if s := h.Stats(); s.Delivered != 1 || s.Dropped != 0 {
		t.Fatalf("stats = %+v, want 1 delivered (the viewer's), 0 dropped", s)
	}
	if got := in.Take(); got == nil || got.LastSeq != 1 {
		t.Fatalf("the internal consumer must still receive views, got %v", got)
	}
	_ = viewer
}

// A consumer that falls behind is disconnected whether or not it is counted; only the
// counts differ. The sequence is fixed: one publish into the empty slot (a delivery),
// then maxLag more without taking (supersedes, ending in the disconnect).
func TestLaggingInternalConsumerIsDisconnectedButNotCounted(t *testing.T) {
	const maxLag = 3
	for _, tc := range []struct {
		name      string
		subscribe func(*Hub) *Consumer
		want      Stats
	}{
		{"internal", func(h *Hub) *Consumer { return h.SubscribeInternal() }, Stats{Published: 1 + maxLag}},
		{"counted (control)", func(h *Hub) *Consumer { return h.Subscribe() },
			Stats{Delivered: 1, Dropped: maxLag, Published: 1 + maxLag}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := New(maxLag)
			c := tc.subscribe(h)
			for i := range 1 + maxLag {
				h.Publish(view(market.Sequence(1 + i)))
			}
			if got := h.Stats(); got != tc.want {
				t.Fatalf("stats = %+v, want %+v", got, tc.want)
			}
			if !doorbellClosed(c) || c.Take() != nil {
				t.Fatal("the lagging consumer must be disconnected: doorbell closed and no view to take")
			}
		})
	}
}

func TestUnsubscribeTwiceLeavesCountsRight(t *testing.T) {
	h := New(8)
	viewer := h.Subscribe()
	in := h.SubscribeInternal()
	for range 2 {
		h.Unsubscribe(in.ID())
		if s := h.Stats(); s.Consumers != 1 {
			t.Fatalf("Consumers = %d after unsubscribing the internal consumer, want 1", s.Consumers)
		}
	}
	for range 2 {
		h.Unsubscribe(viewer.ID())
		if s := h.Stats(); s.Consumers != 0 {
			t.Fatalf("Consumers = %d after unsubscribing the viewer, want 0", s.Consumers)
		}
	}
}

func TestSubscribeInternalOnClosedHub(t *testing.T) {
	h := New(8)
	h.Close()
	c := h.SubscribeInternal()
	if c == nil || !doorbellClosed(c) || c.Take() != nil {
		t.Fatal("want a closed handle whose doorbell is closed and whose Take is nil")
	}
}

func TestPublishedCountsEveryPublish(t *testing.T) {
	h := New(8)
	h.Publish(view(1)) // nobody subscribed
	if got := h.Stats().Published; got != 1 {
		t.Fatalf("Published = %d with no consumers, want 1", got)
	}
	h.Subscribe()
	h.SubscribeInternal()
	h.Publish(view(2))
	h.Publish(view(3))
	if got := h.Stats().Published; got != 3 {
		t.Fatalf("Published = %d, want 3", got)
	}
	h.Close()
	h.Publish(view(4))
	if got := h.Stats().Published; got != 4 {
		t.Fatalf("Published = %d after Close, want 4: a Publish on a closed hub still counts", got)
	}
}
