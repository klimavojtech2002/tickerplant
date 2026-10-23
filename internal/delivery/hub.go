// Package delivery fans the order-book engine's published views out to many consumers
// with bounded backpressure. Each view is a complete, immutable top-N book, so a
// consumer only ever needs the freshest one: the hub keeps a per-consumer latest-value
// slot and, under load, the newest view supersedes an unread one (latest-wins
// conflation, ADR-0015). A consumer that keeps falling behind is disconnected past a
// bound and must resync, so it can never stall the engine or the others (ADR-0006).
package delivery

import (
	"errors"
	"sync"

	"github.com/klimavojtech2002/tickerplant/internal/book"
)

// ErrFull is returned by TrySubscribe when the hub already has as many counted consumers
// as the limit allows.
var ErrFull = errors.New("delivery: consumer limit reached")

// ErrClosed is returned by TrySubscribe on a closed hub.
var ErrClosed = errors.New("delivery: hub closed")

// Hub is an in-process fan-out of *book.View snapshots. The run loop is the single
// publisher; each consumer holds a size-1 latest slot plus a doorbell.
type Hub struct {
	maxLag int // the consecutive-supersede count at which a consumer is disconnected

	mu     sync.Mutex
	subs   map[uint64]*sub
	nextID uint64
	closed bool

	delivered uint64 // guarded by mu
	dropped   uint64 // guarded by mu
	published uint64 // guarded by mu
}

type sub struct {
	ready  chan struct{} // cap 1; a doorbell that a fresher view is available; closed on disconnect
	latest *book.View    // guarded by Hub.mu; the freshest undelivered view, nil once taken
	skips  int           // consecutive supersedes; guarded by Hub.mu
	// internal marks a consumer that belongs to the process itself (the log reader), as
	// opposed to a viewer: it is not counted in Stats or against a TrySubscribe limit.
	internal bool
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

// Stats is a point-in-time snapshot of fan-out activity, for the HTTP edge's metrics.
//
// Consumers, Delivered and Dropped count the consumers made by Subscribe and
// TrySubscribe, not the internal one from SubscribeInternal. Published counts every
// Publish call, with or without consumers.
type Stats struct {
	Consumers int
	Delivered uint64
	Dropped   uint64
	Published uint64
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
	c, err := h.subscribe(false, false, 0)
	if err != nil {
		return h.closedHandle()
	}
	return c
}

// SubscribeInternal registers a consumer that belongs to the process itself, such as
// the one that logs the top of book. It receives views and is disconnected for lagging
// like any other, but it is not counted: not in Stats, not against a TrySubscribe limit.
// On a closed hub it returns the same closed handle as Subscribe.
func (h *Hub) SubscribeInternal() *Consumer {
	c, err := h.subscribe(true, false, 0)
	if err != nil {
		return h.closedHandle()
	}
	return c
}

// TrySubscribe registers a consumer unless the hub is closed (ErrClosed) or already has
// limit counted consumers (ErrFull). A limit below 1 allows none. The check and the
// registration happen under one lock, so concurrent callers cannot exceed the limit. On an
// error it returns a nil Consumer and uses up no id.
func (h *Hub) TrySubscribe(limit int) (*Consumer, error) {
	return h.subscribe(false, true, limit)
}

// subscribe is the one place a consumer is added. Closed is checked before the limit, so
// a closed hub always answers ErrClosed.
func (h *Hub) subscribe(internal, limited bool, limit int) (*Consumer, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	if limited && h.counted() >= limit {
		return nil, ErrFull
	}
	ready := make(chan struct{}, 1)
	id := h.nextID
	h.nextID++
	h.subs[id] = &sub{ready: ready, internal: internal}
	return &Consumer{hub: h, id: id, ready: ready}, nil
}

// closedHandle is what Subscribe returns on a closed hub: a doorbell that is already
// closed, so Take returns nil. Its id is left at the zero value, which is safe only
// because Close permanently empties subs and a closed hub never re-adds to it, so that
// id can never collide with a live subscriber's.
func (h *Hub) closedHandle() *Consumer {
	ready := make(chan struct{})
	close(ready)
	return &Consumer{hub: h, ready: ready}
}

// counted returns how many consumers count towards Stats and the limit. The caller
// holds mu. It scans instead of keeping a counter, so it cannot drift from subs.
func (h *Hub) counted() int {
	n := 0
	for _, s := range h.subs {
		if !s.internal {
			n++
		}
	}
	return n
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
	h.published++ // counted even on a closed hub, where no one is left to deliver to
	for id, s := range h.subs {
		if s.latest != nil { // previous view not taken: supersede it
			s.skips++
			if !s.internal {
				h.dropped++
			}
			if s.skips >= h.maxLag {
				close(s.ready)
				delete(h.subs, id)
				continue
			}
		} else {
			s.skips = 0
			if !s.internal {
				h.delivered++
			}
		}
		s.latest = v // the freshest view always wins the slot
		select {
		case s.ready <- struct{}{}: // wake the consumer
		default: // already signalled and unread; the consumer will read the latest
		}
	}
}

// Stats returns a point-in-time snapshot of fan-out activity. Safe to call from any
// goroutine; all fields are read under the lock so they are consistent.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Stats{
		Consumers: h.counted(),
		Delivered: h.delivered,
		Dropped:   h.dropped,
		Published: h.published,
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
