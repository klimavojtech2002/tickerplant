package delivery

import (
	"sync"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/market"
)

func view(seq market.Sequence) *book.View {
	return &book.View{
		LastSeq: seq,
		Bids:    []market.Level{{Price: 100, Size: 1}},
		Asks:    []market.Level{{Price: 101, Size: 1}},
	}
}

func TestSubscribeReceive(t *testing.T) {
	h := New(8)
	c := h.Subscribe()
	h.Publish(view(1))
	<-c.Ready()
	if got := c.Take(); got == nil || got.LastSeq != 1 {
		t.Fatalf("received %v, want LastSeq 1", got)
	}
}

func TestFanOut(t *testing.T) {
	h := New(8)
	a := h.Subscribe()
	b := h.Subscribe()
	h.Publish(view(7))
	<-a.Ready()
	<-b.Ready()
	if va, vb := a.Take(), b.Take(); va == nil || va.LastSeq != 7 || vb == nil || vb.LastSeq != 7 {
		t.Fatalf("both consumers must receive the view, got %v and %v", va, vb)
	}
	if s := h.Stats(); s.Consumers != 2 || s.Delivered != 2 {
		t.Fatalf("stats = %+v, want 2 consumers, 2 delivered", s)
	}
}

// A consumer that has not taken v1 when v2 arrives receives v2 (the latest), never the
// stale v1 — the defining property of the fan-out.
func TestLatestWinsSupersedes(t *testing.T) {
	h := New(8)
	c := h.Subscribe()
	h.Publish(view(1)) // slot: v1
	h.Publish(view(2)) // v1 unread -> superseded by v2
	<-c.Ready()
	if got := c.Take(); got == nil || got.LastSeq != 2 {
		t.Fatalf("latest-wins: got %v, want the freshest LastSeq 2, never the stale 1", got)
	}
	if s := h.Stats(); s.Delivered != 1 || s.Dropped != 1 {
		t.Fatalf("stats = %+v, want 1 delivered, 1 dropped (the superseded view)", s)
	}
}

// A consumer that never reads must not block Publish — the engine is never stalled.
func TestPublishNeverBlocks(t *testing.T) {
	h := New(1 << 20) // high maxLag so the never-reading consumer stays subscribed through the burst
	_ = h.Subscribe() // never reads
	for i := range 1000 {
		h.Publish(view(market.Sequence(i))) // hangs the test (via timeout) if Publish ever blocks
	}
	if s := h.Stats(); s.Consumers != 1 || s.Delivered != 1 {
		t.Fatalf("stats = %+v, want 1 consumer still subscribed, 1 delivered (rest superseded)", s)
	}
}

// Delivered counts clean hand-offs; Dropped counts superseded views; both are exact.
func TestStatsExactAfterSupersede(t *testing.T) {
	h := New(8)
	c := h.Subscribe()
	h.Publish(view(1)) // delivered = 1
	h.Publish(view(2)) // dropped = 1
	h.Publish(view(3)) // dropped = 2
	<-c.Ready()
	if got := c.Take(); got == nil || got.LastSeq != 3 {
		t.Fatalf("Take = %v, want the latest LastSeq 3", got)
	}
	h.Publish(view(4)) // slot empty again -> delivered = 2
	if s := h.Stats(); s.Delivered != 2 || s.Dropped != 2 || s.Consumers != 1 {
		t.Fatalf("stats = %+v, want delivered 2, dropped 2, consumers 1", s)
	}
}

func TestDisconnectAfterMaxLagSupersedes(t *testing.T) {
	h := New(2) // disconnect after 2 consecutive supersedes
	c := h.Subscribe()
	h.Publish(view(1)) // delivered
	h.Publish(view(2)) // supersede #1 (streak 1 < 2)
	h.Publish(view(3)) // supersede #2 (streak 2 >= 2) -> disconnect
	if s := h.Stats(); s.Consumers != 0 || s.Dropped != 2 {
		t.Fatalf("a persistently slow consumer must be disconnected: stats = %+v, want 0 consumers, 2 dropped", s)
	}
	// The doorbell is closed (the range terminates); any wakeup still buffered from before
	// the disconnect yields nil via Take, never a stale view.
	for range c.Ready() {
		if v := c.Take(); v != nil {
			t.Fatalf("disconnected consumer must Take nil, got LastSeq %d", v.LastSeq)
		}
	}
}

func TestStreakResetsOnCatchUp(t *testing.T) {
	h := New(2)
	c := h.Subscribe()
	h.Publish(view(1)) // delivered
	h.Publish(view(2)) // supersede #1
	<-c.Ready()
	_ = c.Take()       // catch up -> the next publish finds the slot empty and resets the streak
	h.Publish(view(3)) // delivered, streak reset to 0
	h.Publish(view(4)) // supersede #1 again (not #2), so no disconnect
	if s := h.Stats(); s.Consumers != 1 {
		t.Fatalf("consumer disconnected too early: %+v", s)
	}
}

// maxLag 0 means zero tolerance: the first supersede disconnects the consumer.
func TestZeroMaxLagDisconnectsOnFirstSupersede(t *testing.T) {
	h := New(0)
	c := h.Subscribe()
	h.Publish(view(1)) // delivered (slot was empty)
	h.Publish(view(2)) // first supersede -> streak(1) >= maxLag(0) -> disconnect
	if s := h.Stats(); s.Consumers != 0 {
		t.Fatalf("maxLag 0 must disconnect on the first supersede: %+v", s)
	}
	for range c.Ready() { // closed; drains any buffered wakeup then terminates
		if v := c.Take(); v != nil {
			t.Fatalf("disconnected consumer must Take nil, got LastSeq %d", v.LastSeq)
		}
	}
}

// maxLag 1 behaves exactly like 0: the streak is incremented before the compare, so
// the first supersede reads streak(1) >= maxLag(1) and disconnects.
func TestMaxLagOneDisconnectsOnFirstSupersede(t *testing.T) {
	h := New(1)
	c := h.Subscribe()
	h.Publish(view(1)) // delivered (slot was empty)
	h.Publish(view(2)) // first supersede -> streak(1) >= maxLag(1) -> disconnect
	if s := h.Stats(); s.Consumers != 0 || s.Dropped != 1 {
		t.Fatalf("maxLag 1 must disconnect on the first supersede: %+v", s)
	}
	if v := c.Take(); v != nil {
		t.Fatalf("disconnected consumer must Take nil, got LastSeq %d", v.LastSeq)
	}
}

// While a producer floods the hub, a live consumer must end on the freshest view — a
// lost doorbell wakeup or a stale latest slot would strand the final view and time
// this out. The hub stays open during the wait, so nothing here rides on Close.
func TestConcurrentPublishConsumerSeesFinalView(t *testing.T) {
	const n = 1000
	h := New(1 << 20) // high maxLag: a lagging consumer is conflated, never disconnected
	c := h.Subscribe()
	sawFinal := make(chan struct{})

	var wg sync.WaitGroup
	wg.Go(func() {
		seen := false
		for range c.Ready() {
			v := c.Take()
			if v != nil && v.LastSeq == n && !seen {
				seen = true
				close(sawFinal)
			}
		}
	})

	for i := 1; i <= n; i++ {
		h.Publish(view(market.Sequence(i)))
	}

	select {
	case <-sawFinal:
	case <-time.After(2 * time.Second):
		t.Fatal("consumer never observed the final view: lost wakeup or stale latest slot")
	}
	h.Close()
	wg.Wait()
}

// A disconnected consumer gets no stale final view: Take returns nil, so it must resync.
func TestDisconnectGivesNoFinalView(t *testing.T) {
	h := New(0)
	c := h.Subscribe()
	h.Publish(view(1)) // delivered, slot holds v1
	h.Publish(view(2)) // first supersede -> disconnect, record deleted
	if v := c.Take(); v != nil {
		t.Fatalf("a disconnected consumer must Take nil, got LastSeq %d", v.LastSeq)
	}
}

func TestUnsubscribeIdempotent(t *testing.T) {
	h := New(8)
	c := h.Subscribe()
	h.Unsubscribe(c.ID())
	if _, ok := <-c.Ready(); ok {
		t.Fatal("unsubscribe must close the doorbell")
	}
	h.Unsubscribe(c.ID()) // again: no panic
	h.Unsubscribe(999)    // unknown id: no panic
}

func TestCloseDisconnectsAll(t *testing.T) {
	h := New(8)
	a := h.Subscribe()
	b := h.Subscribe()
	h.Close()
	if _, ok := <-a.Ready(); ok {
		t.Fatal("close must close consumer a")
	}
	if _, ok := <-b.Ready(); ok {
		t.Fatal("close must close consumer b")
	}
	h.Close() // idempotent
	if h.Stats().Consumers != 0 {
		t.Fatal("no consumers after close")
	}
}

func TestSubscribeOnClosedHub(t *testing.T) {
	h := New(8)
	h.Close()
	c := h.Subscribe()
	if _, ok := <-c.Ready(); ok {
		t.Fatal("subscribing to a closed hub must return an already-closed doorbell")
	}
	if v := c.Take(); v != nil {
		t.Fatal("a closed-hub consumer must Take nil")
	}
}

// Close racing Publish with a live reader must be race-free and shut down cleanly
// (run under -race in CI).
func TestConcurrentCloseVsPublish(t *testing.T) {
	h := New(100)
	c := h.Subscribe()
	var wg sync.WaitGroup
	wg.Go(func() {
		for range c.Ready() {
			_ = c.Take()
		}
	})
	wg.Go(func() {
		for i := range 2000 {
			h.Publish(view(market.Sequence(i)))
		}
	})
	wg.Go(h.Close)
	wg.Wait()
}

// Concurrent publish + subscribe/unsubscribe churn must be race-free (run under -race).
func TestConcurrentPublishSubscribe(t *testing.T) {
	h := New(100)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 2000 {
			h.Publish(view(market.Sequence(i)))
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 300 {
				c := h.Subscribe()
				select {
				case <-c.Ready():
					_ = c.Take()
				default:
				}
				h.Unsubscribe(c.ID())
			}
		})
	}
	wg.Wait()
	h.Close()
}
