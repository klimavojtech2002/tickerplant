// Package delivery fans the order-book engine's published views out to many consumers
// with bounded backpressure. Each view is a complete, immutable top-N book, so a
// consumer only ever needs the freshest one: the hub keeps a per-consumer latest-value
// slot and, under load, the newest view supersedes an unread one (latest-wins
// conflation, ADR-0015). A consumer that keeps falling behind is disconnected past a
// bound and must resync, so it can never stall the engine or the others (ADR-0006).
package delivery

import (
	"sync"

	"github.com/klimavojtech2002/tickerplant/internal/book"
)

// Hub is an in-process fan-out of *book.View snapshots. The engine is the single
// publisher; each consumer holds a size-1 latest slot plus a doorbell.
type Hub struct {
	maxLag int // the consecutive-supersede count at which a consumer is disconnected

	mu     sync.Mutex
	subs   map[uint64]*sub
	nextID uint64
	closed bool

	delivered uint64 // guarded by mu
	dropped   uint64 // guarded by mu
}

type sub struct {
	ready  chan struct{} // cap 1; a doorbell that a fresher view is available; closed on disconnect
	latest *book.View    // guarded by Hub.mu; the freshest undelivered view, nil once taken
	skips  int           // consecutive supersedes; guarded by Hub.mu
}

// Consumer is a subscription handle: a doorbell that fires when a fresher view may be
// available, and Take to fetch it. The doorbell carries no payload — the view lives in
// the latest slot — so a coalesced wakeup simply yields the latest view (or nil).
type Consumer struct {
	hub   *Hub
	id    uint64
	ready chan struct{}
}

// ID returns the subscription id, for Unsubscribe.
func (c *Consumer) ID() uint64 { return c.id }

// Ready is the doorbell: it receives once whenever a fresher view may be available, and
// is closed when the consumer is unsubscribed, disconnected for lagging, or the hub is
// closed. Range over it and call Take on each wakeup.
func (c *Consumer) Ready() <-chan struct{} { return c.ready }

// Take returns the latest view and clears the slot, or nil if none is pending or the
// consumer has been disconnected. It looks the record up by id, so after a disconnect it
// returns nil rather than a stale final view.
func (c *Consumer) Take() *book.View {
	h := c.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.subs[c.id]
	if !ok {
		return nil
	}
	v := s.latest
	s.latest = nil
	return v
}

// Stats is a point-in-time snapshot of fan-out activity, for a /metrics endpoint.
type Stats struct {
	Consumers int    `json:"consumers"`
	Delivered uint64 `json:"delivered"`
	Dropped   uint64 `json:"dropped"`
}

// New returns a Hub. maxLag is the consecutive-supersede count at which a consumer is
// disconnected: it tolerates maxLag-1 consecutive supersedes and is dropped on the
// maxLag-th. Values of 1 or less disconnect on the first supersede.
func New(maxLag int) *Hub {
	return &Hub{maxLag: maxLag, subs: make(map[uint64]*sub)}
}

// Subscribe registers a consumer and returns its handle. On a closed hub the handle's
// doorbell is already closed and Take returns nil; its id is left at the zero value,
// which is safe only because Close permanently empties subs and a closed hub never
// re-adds to it, so that id can never collide with a live subscriber's.
func (h *Hub) Subscribe() *Consumer {
	ready := make(chan struct{}, 1)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(ready)
		return &Consumer{hub: h, ready: ready}
	}
	id := h.nextID
	h.nextID++
	h.subs[id] = &sub{ready: ready}
	return &Consumer{hub: h, id: id, ready: ready}
}

// Unsubscribe detaches a consumer and closes its doorbell. It is idempotent.
func (h *Hub) Unsubscribe(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.subs[id]; ok {
		close(s.ready)
		delete(h.subs, id)
	}
}

// Publish hands the view to every consumer without blocking. A consumer that has not yet
// taken its previous view has it superseded by this fresher one (latest-wins) and its
// supersede streak grows; on the maxLag-th consecutive supersede it is disconnected. A
// consumer that kept up (slot empty) has its streak reset. Publish never blocks, so a
// slow consumer can never stall the engine.
func (h *Hub) Publish(v *book.View) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, s := range h.subs {
		if s.latest != nil { // previous view not taken: supersede it
			s.skips++
			h.dropped++
			if s.skips >= h.maxLag {
				close(s.ready)
				delete(h.subs, id)
				continue
			}
		} else {
			s.skips = 0
			h.delivered++
		}
		s.latest = v // the freshest view always wins the slot
		select {
		case s.ready <- struct{}{}: // wake the consumer
		default: // already signalled and unread; the consumer will read the latest
		}
	}
}

// Stats returns a point-in-time snapshot of fan-out activity. Safe to call from any
// goroutine; all three fields are read under the lock so they are consistent.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Stats{
		Consumers: len(h.subs),
		Delivered: h.delivered,
		Dropped:   h.dropped,
	}
}

// Close disconnects every consumer and rejects further subscriptions. Idempotent.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for id, s := range h.subs {
		close(s.ready)
		delete(h.subs, id)
	}
}
