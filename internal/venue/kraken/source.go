package kraken

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

var _ source.Source = (*Source)(nil)

// ErrNoSnapshot is returned when a fresh snapshot is needed but the source has no way
// to obtain one (no snapshot pending and no Redial configured).
var ErrNoSnapshot = errors.New("kraken: no snapshot available")

// Config configures a Kraken adapter. Frames is the stream of raw v2 frames — fed by
// the WebSocket layer in production, by a channel in tests. Kraken serves the book
// snapshot in-band (the first book frame after subscribing), so there is no snapshot
// URL; Redial tears the transport down and returns a fresh frame channel whose
// subscription will deliver a new snapshot — the engine's drift resync uses it when
// the current connection has no snapshot to offer.
type Config struct {
	Venue      market.Venue
	Symbol     market.Symbol
	PriceScale int
	QtyScale   int
	Frames     <-chan []byte
	Redial     func() <-chan []byte
}

// Source is the Kraken transport-port adapter. Kraken's book feed carries no sequence
// numbers — the checksum is the integrity guard (ADR-0007) — so the adapter stamps a
// synthetic monotonic sequence on the events it emits: the engine's continuity check
// stays inert and every real integrity decision flows through WithChecksum.
type Source struct {
	cfg    Config
	scales scales
	frames <-chan []byte

	seq              market.Sequence
	pending          *market.Snapshot // in-band snapshot cached by Next, consumed by Snapshot
	snapshotExpected bool             // the current frames channel still owes us its first snapshot

	done    chan struct{}
	once    sync.Once
	onClose func() // optional teardown (closing the live WebSocket), set by Live
}

// New builds a Kraken adapter from cfg. The initial frames channel is assumed to owe a
// snapshot (Kraken sends one on every subscribe), so the first Snapshot call waits for
// it instead of redialing.
func New(cfg Config) *Source {
	return &Source{
		cfg:              cfg,
		scales:           scales{price: cfg.PriceScale, qty: cfg.QtyScale},
		frames:           cfg.Frames,
		snapshotExpected: true,
		done:             make(chan struct{}),
	}
}

// Next returns the next book event. Updates become deltas with the next synthetic
// sequence. An in-band snapshot arriving mid-stream (the transport reconnected and
// re-subscribed) is cached for Snapshot and surfaced as a disconnect, so the engine
// rebinds instead of applying updates onto a stale book. Non-book frames (acks,
// heartbeats, status) and frames that fail to normalize are skipped — with no wire
// sequence a bad frame cannot leave a detectable hole, so the checksum on the next
// applied delta is what catches any divergence.
func (s *Source) Next(ctx context.Context) (source.Event, bool) {
	for {
		select {
		case <-ctx.Done():
			return source.Event{}, false
		case <-s.done:
			return source.Event{}, false
		case raw, ok := <-s.frames:
			if !ok {
				return source.Event{}, false
			}
			p, ok, err := parseBook(raw, s.scales)
			if !ok || err != nil {
				continue
			}
			if p.isSnapshot {
				s.cache(p)
				return source.Event{Kind: source.EventDisconnected}, true
			}
			s.seq++
			return source.Event{Kind: source.EventDelta, Delta: market.Delta{
				Venue: s.cfg.Venue, Symbol: s.cfg.Symbol,
				FirstSeq: s.seq, LastSeq: s.seq,
				Bids: p.bids, Asks: p.asks,
				Checksum: p.checksum,
			}}, true
		}
	}
}

// Snapshot returns the in-band snapshot: the cached one if Next already saw it,
// otherwise the next one on the wire. When the current connection has already
// delivered its snapshot (a checksum-drift resync, not a reconnect), it redials so the
// fresh subscription serves a new one. Updates read while waiting belong to the book
// state the incoming snapshot supersedes, so they are discarded.
func (s *Source) Snapshot(ctx context.Context) (market.Snapshot, error) {
	if s.pending != nil {
		snap := *s.pending
		s.pending = nil
		return snap, nil
	}
	if !s.snapshotExpected {
		if s.cfg.Redial == nil {
			return market.Snapshot{}, ErrNoSnapshot
		}
		s.frames = s.cfg.Redial()
		s.snapshotExpected = true
	}
	for {
		select {
		case <-ctx.Done():
			return market.Snapshot{}, ctx.Err()
		case <-s.done:
			return market.Snapshot{}, ErrNoSnapshot
		case raw, ok := <-s.frames:
			if !ok {
				return market.Snapshot{}, fmt.Errorf("kraken: frame stream closed: %w", ErrNoSnapshot)
			}
			p, ok, err := parseBook(raw, s.scales)
			if err != nil && p.isSnapshot {
				// The snapshot this wait exists for is malformed; nothing else on this
				// subscription can unblock it, so skipping here would hang forever.
				return market.Snapshot{}, fmt.Errorf("kraken: malformed in-band snapshot: %w", err)
			}
			if !ok || err != nil || !p.isSnapshot {
				continue // acks, heartbeats, and superseded pre-snapshot updates
			}
			s.cache(p)
			snap := *s.pending
			s.pending = nil
			return snap, nil
		}
	}
}

// cache stores an in-band snapshot with the next synthetic sequence, so the deltas
// that follow it on the wire continue contiguously from its LastUpdateID.
func (s *Source) cache(p parsed) {
	s.seq++
	s.snapshotExpected = false
	s.pending = &market.Snapshot{
		Venue: s.cfg.Venue, Symbol: s.cfg.Symbol,
		LastUpdateID: s.seq,
		Bids:         p.bids, Asks: p.asks,
		Checksum: p.checksum,
	}
}

// Close releases the source; further Next calls return false. The live constructor
// hooks the WebSocket teardown here.
func (s *Source) Close() error {
	s.once.Do(func() {
		close(s.done)
		if s.onClose != nil {
			s.onClose()
		}
	})
	return nil
}
