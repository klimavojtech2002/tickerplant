package delivery

import (
	"sync"
	"testing"

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
	h := New(4, 8)
	_, ch := h.Subscribe()
	h.Publish(view(1))
	if got := <-ch; got.LastSeq != 1 {
		t.Fatalf("received LastSeq %d, want 1", got.LastSeq)
	}
}

func TestFanOut(t *testing.T) {
	h := New(4, 8)
	_, a := h.Subscribe()
	_, b := h.Subscribe()
	h.Publish(view(7))
	if (<-a).LastSeq != 7 || (<-b).LastSeq != 7 {
		t.Fatal("both consumers must receive the view")
	}
	if s := h.Stats(); s.Consumers != 2 || s.Delivered != 2 {
		t.Fatalf("stats = %+v, want 2 consumers, 2 delivered", s)
	}
}

func TestSlowConsumerDropsThenDisconnects(t *testing.T) {
	h := New(1, 2) // buffer 1; disconnect after 2 consecutive drops
	_, slow := h.Subscribe()
	_, fast := h.Subscribe()

	h.Publish(view(1)) // slow buffer 1/1; fast 1/1
	<-fast
	h.Publish(view(2)) // slow full -> drop #1; fast ok
	<-fast
	h.Publish(view(3)) // slow full -> drop #2 -> disconnect; fast ok
	<-fast

	// Assert the disconnect via Stats first (non-blocking), so "never disconnect" fails
	// here rather than hanging on the channel read below.
	if s := h.Stats(); s.Consumers != 1 || s.Dropped < 2 {
		t.Fatalf("a persistently slow consumer must be disconnected: stats = %+v, want 1 consumer, >=2 dropped", s)
	}
	if got := <-slow; got.LastSeq != 1 { // the view buffered before it fell behind
		t.Fatalf("slow buffered LastSeq %d, want 1", got.LastSeq)
	}
	if _, ok := <-slow; ok {
		t.Fatal("disconnected consumer's channel must be closed")
	}
}

func TestSlowConsumerResetsOnCatchUp(t *testing.T) {
	h := New(1, 2)
	_, ch := h.Subscribe()
	h.Publish(view(1)) // buffered
	<-ch
	h.Publish(view(2)) // full? no — drained -> delivered, drops stay 0
	<-ch
	// one drop, then catch up, then another drop: must NOT disconnect (streak reset)
	h.Publish(view(3)) // buffered
	h.Publish(view(4)) // full -> drop #1
	<-ch               // drain (catch up) -> next send resets the streak
	h.Publish(view(5)) // delivered -> drops reset to 0
	<-ch
	h.Publish(view(6)) // buffered
	h.Publish(view(7)) // full -> drop #1 again (not #2)
	if s := h.Stats(); s.Consumers != 1 {
		t.Fatalf("consumer disconnected too early: %+v", s)
	}
}

func TestUnsubscribeIdempotent(t *testing.T) {
	h := New(4, 8)
	id, ch := h.Subscribe()
	h.Unsubscribe(id)
	if _, ok := <-ch; ok {
		t.Fatal("unsubscribe must close the channel")
	}
	h.Unsubscribe(id)  // again: no panic
	h.Unsubscribe(999) // unknown id: no panic
}

func TestCloseDisconnectsAll(t *testing.T) {
	h := New(4, 8)
	_, a := h.Subscribe()
	_, b := h.Subscribe()
	h.Close()
	if _, ok := <-a; ok {
		t.Fatal("close must close consumer a")
	}
	if _, ok := <-b; ok {
		t.Fatal("close must close consumer b")
	}
	h.Close() // idempotent
	if h.Stats().Consumers != 0 {
		t.Fatal("no consumers after close")
	}
}

func TestSubscribeOnClosedHub(t *testing.T) {
	h := New(4, 8)
	h.Close()
	_, ch := h.Subscribe()
	if _, ok := <-ch; ok {
		t.Fatal("subscribing to a closed hub must return an already-closed channel")
	}
}

func TestNewClampsBuffer(t *testing.T) {
	h := New(0, 8) // buffer clamped to 1
	_, ch := h.Subscribe()
	h.Publish(view(1)) // buffer 1 -> fits
	if got := <-ch; got.LastSeq != 1 {
		t.Fatal("clamped buffer must still deliver one view")
	}
}

// maxLag 0 means zero tolerance: the first drop disconnects the consumer.
func TestZeroMaxLagDisconnectsOnFirstDrop(t *testing.T) {
	h := New(1, 0)
	_, ch := h.Subscribe()
	h.Publish(view(1)) // buffered (buffer 1)
	h.Publish(view(2)) // full -> drop #1 -> drops(1) >= maxLag(0) -> disconnect
	if got := <-ch; got.LastSeq != 1 {
		t.Fatalf("buffered view LastSeq %d, want 1", got.LastSeq)
	}
	if _, ok := <-ch; ok {
		t.Fatal("maxLag 0 must disconnect on the first drop")
	}
}

// Concurrent publish + subscribe/unsubscribe must be race-free (run under -race in CI).
func TestConcurrentPublishSubscribe(t *testing.T) {
	h := New(8, 100)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 2000 {
			h.Publish(view(market.Sequence(i)))
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 300 {
				id, ch := h.Subscribe()
				select {
				case <-ch:
				default:
				}
				h.Unsubscribe(id)
			}
		})
	}
	wg.Wait()
	h.Close()
}
