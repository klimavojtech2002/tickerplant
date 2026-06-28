// Package delivery fans the order-book engine's published views out to many
// consumers with bounded backpressure: a slow consumer is dropped, then
// disconnected past a bound, so it can never stall the engine or the others
// (ADR-0006, ADR-0013).
package delivery

import (
	"sync"

	"github.com/klimavojtech2002/tickerplant/internal/book"
)

// Hub is an in-process fan-out of *book.View snapshots. Each view is a complete,
// immutable top-N book, so dropping one under backpressure is safe: the next view
// supersedes it (latest-wins). The engine is the single publisher.
type Hub struct {
	buffer int // per-consumer channel capacity
	maxLag int // consecutive drops before a consumer is disconnected

	mu     sync.Mutex
	subs   map[uint64]*sub
	nextID uint64
	closed bool

	delivered uint64 // guarded by mu
	dropped   uint64 // guarded by mu
}

type sub struct {
	ch    chan *book.View
	drops int
}

// Stats is a point-in-time snapshot of fan-out activity, for a /metrics endpoint.
type Stats struct {
	Consumers int    `json:"consumers"`
	Delivered uint64 `json:"delivered"`
	Dropped   uint64 `json:"dropped"`
}

// New returns a Hub. buffer (clamped to >= 1) bounds how far a consumer may lag
// before its views are dropped; maxLag is how many consecutive drops are tolerated
// before the consumer is disconnected — 0 (or less) disconnects on the first drop.
func New(buffer, maxLag int) *Hub {
	if buffer < 1 {
		buffer = 1
	}
	return &Hub{buffer: buffer, maxLag: maxLag, subs: make(map[uint64]*sub)}
}

// Subscribe registers a consumer and returns its id and receive-only channel. The
// channel is closed when the consumer is unsubscribed, disconnected for lagging, or
// the hub is closed. On a closed hub it returns an already-closed channel.
func (h *Hub) Subscribe() (uint64, <-chan *book.View) {
	ch := make(chan *book.View, h.buffer)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(ch)
		return 0, ch
	}
	id := h.nextID
	h.nextID++
	h.subs[id] = &sub{ch: ch}
	return id, ch
}

// Unsubscribe detaches a consumer and closes its channel. It is idempotent.
func (h *Hub) Unsubscribe(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.subs[id]; ok {
		close(s.ch)
		delete(h.subs, id)
	}
}

// Publish broadcasts a view to every consumer without blocking. A consumer whose
// buffer is full has the view dropped (counted); past maxLag consecutive drops it is
// disconnected. A consumer that keeps up has its drop streak reset.
func (h *Hub) Publish(v *book.View) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, s := range h.subs {
		select {
		case s.ch <- v:
			s.drops = 0
			h.delivered++
		default:
			s.drops++
			h.dropped++
			if s.drops >= h.maxLag {
				close(s.ch)
				delete(h.subs, id)
			}
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
		close(s.ch)
		delete(h.subs, id)
	}
}
