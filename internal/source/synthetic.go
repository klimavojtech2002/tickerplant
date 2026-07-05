package source

import (
	"cmp"
	"context"
	"errors"
	"math/rand"
	"slices"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// errClosed is returned by Snapshot after the source is closed.
var errClosed = errors.New("source closed")

// Fault is a perturbation injected into the emitted stream or a Snapshot. The zero
// value FaultNone means no fault, so a bare Config map entry is never accidentally a
// real fault.
type Fault uint8

const (
	FaultNone       Fault = iota
	FaultGap              // skip emitting one delta (truth still advances) -> the engine sees a sequence gap
	FaultReorder          // emit two adjacent deltas out of order
	FaultDuplicate        // emit one delta twice
	FaultDisconnect       // emit an EventDisconnected
	FaultCross            // emit one illegal delta that would cross the book (stream only; truth stays legal)
)

// crossBidPrice is a bid price above the entire ask band (asks live in [1001,2000] —
// see seedInitialBook and genDelta), so a bid here always makes best bid >= best ask: a
// deterministic cross with no dependency on the current book. Used only by FaultCross.
const crossBidPrice market.Price = 2001

// Config drives a Synthetic source. Given the same Config, every run is identical:
// the only entropy is the seeded PRNG, and no wall clock, global rand, or map
// iteration touches the emitted order (ADR-0005, docs/correctness.md §10).
type Config struct {
	Venue  market.Venue
	Symbol market.Symbol
	Seed   int64
	Steps  int           // number of generation steps to run before the stream ends
	Faults map[int]Fault // step index -> fault to inject on the emitted stream (looked up, never ranged)

	// CrossingSnapshot makes the next Snapshot return a crossed book (best bid >=
	// best ask) — an illegal snapshot the engine must reject and resync from.
	CrossingSnapshot bool
	// StaleSnapshot makes the first Snapshot return a book from before the stream
	// advanced (a low LastUpdateID); the engine must detect it is too old to bind
	// and refetch. The second Snapshot is current, so the refetch succeeds.
	StaleSnapshot bool
}

// Synthetic is a deterministic, seeded order-book feed used to prove the engine
// correct without a live socket. It owns a truthBook (the oracle) and emits deltas
// consistent with it; faults perturb the emitted stream, never the truth. It emits
// deltas and disconnect lifecycle events; trades (EventTrade) are part of the port
// but not generated here yet — the engine slice exercises the book path first.
type Synthetic struct {
	cfg     Config
	rng     *rand.Rand
	truth   *truthBook
	seq     market.Sequence
	step    int
	pending []Event          // events queued by reorder/duplicate, drained first
	stale   *market.Snapshot // captured pre-stream book for StaleSnapshot
	closed  bool
}

var _ Source = (*Synthetic)(nil)

// New builds a Synthetic source and seeds an initial two-sided book.
func New(cfg Config) *Synthetic {
	s := &Synthetic{
		cfg:   cfg,
		rng:   rand.New(rand.NewSource(cfg.Seed)),
		truth: newTruthBook(),
	}
	s.seedInitialBook()
	if cfg.StaleSnapshot {
		snap := s.truth.snapshot(cfg.Venue, cfg.Symbol, s.seq)
		s.stale = &snap
	}
	return s
}

// seedInitialBook lays down a populated, uncrossed book: bids 850..900, asks
// 1100..1150. Stream mutations stay inside [1,1000] (bids) and [1001,2000] (asks),
// so by construction best bid < best ask always — the truth never crosses.
func (s *Synthetic) seedInitialBook() {
	const depth = 6
	for i := range depth {
		s.truth.set(market.Bid, market.Price(900-i*10), market.Size(1+s.rng.Int63n(1_000_000)))
		s.truth.set(market.Ask, market.Price(1100+i*10), market.Size(1+s.rng.Int63n(1_000_000)))
	}
	s.seq = 1000
}

// genDelta mutates the truth book by one legal level change and returns the
// matching single-sequence delta. It always advances the truth and the sequence.
func (s *Synthetic) genDelta() market.Delta {
	s.seq++
	side := market.Bid
	if s.rng.Intn(2) == 0 {
		side = market.Ask
	}
	var price market.Price
	if side == market.Bid {
		price = market.Price(1 + s.rng.Intn(1000)) // [1,1000]
	} else {
		price = market.Price(1001 + s.rng.Intn(1000)) // [1001,2000]
	}
	var size market.Size
	if s.rng.Intn(4) != 0 { // 75% set, 25% delete
		size = market.Size(1 + s.rng.Int63n(1_000_000))
	}
	lvl := market.Level{Price: price, Size: size}
	d := market.Delta{Venue: s.cfg.Venue, Symbol: s.cfg.Symbol, FirstSeq: s.seq, LastSeq: s.seq}
	if side == market.Bid {
		d.Bids = []market.Level{lvl}
	} else {
		d.Asks = []market.Level{lvl}
	}
	s.truth.apply(d) // one application path for the oracle
	return d
}

// Next returns the next event. Faults are applied here, on the emitted stream only.
func (s *Synthetic) Next(ctx context.Context) (Event, bool) {
	if s.closed || ctx.Err() != nil {
		return Event{}, false
	}
	if len(s.pending) > 0 {
		e := s.pending[0]
		s.pending = s.pending[1:]
		e.Received = time.Now() // stamped at emission: generation is this source's "wire"
		return e, true
	}
	// Each step's fault is looked up exactly once, so a scheduled fault is never
	// silently dropped: a gap swallows its own delta and continues to the next step,
	// and a reorder emits the following event ahead of this delta while honoring that
	// step's own fault (via the recursive Next), never dropping it.
	for s.step < s.cfg.Steps {
		fault := s.cfg.Faults[s.step]
		s.step++
		switch fault {
		case FaultDisconnect:
			return Event{Kind: EventDisconnected, Received: time.Now()}, true
		case FaultGap:
			_ = s.genDelta() // generate but do not emit -> the engine sees a hole; on to the next step
			continue
		case FaultDuplicate:
			d := s.genDelta()
			s.pending = append(s.pending, Event{Kind: EventDelta, Delta: d})
			return Event{Kind: EventDelta, Received: time.Now(), Delta: d}, true
		case FaultReorder:
			d1 := s.genDelta()
			next, ok := s.Next(ctx) // the following event, emitted before d1
			if !ok {
				return Event{Kind: EventDelta, Received: time.Now(), Delta: d1}, true
			}
			s.pending = append(s.pending, Event{Kind: EventDelta, Delta: d1})
			return next, true
		case FaultCross:
			// Emit one illegal delta — a bid above the whole ask band — without advancing
			// the truth or the sequence. When the engine is caught up this is contiguous, so
			// the engine applies it, sees the book cross, and resyncs from the still-legal
			// truth; the crossed state is never published. This is the venue-sent lie of
			// ADR-0004, made observable so the simulation proves never-crosses.
			return Event{Kind: EventDelta, Received: time.Now(), Delta: market.Delta{
				Venue: s.cfg.Venue, Symbol: s.cfg.Symbol,
				FirstSeq: s.seq + 1, LastSeq: s.seq + 1,
				Bids: []market.Level{{Price: crossBidPrice, Size: 1}},
			}}, true
		default: // FaultNone
			return Event{Kind: EventDelta, Received: time.Now(), Delta: s.genDelta()}, true
		}
	}
	return Event{}, false
}

// Snapshot returns the current authoritative book, or an injected illegal/old one
// when configured. The returned slices are fresh, so a caller cannot mutate the
// truth through them.
func (s *Synthetic) Snapshot(ctx context.Context) (market.Snapshot, error) {
	if s.closed {
		return market.Snapshot{}, errClosed
	}
	if err := ctx.Err(); err != nil {
		return market.Snapshot{}, err
	}
	if s.stale != nil { // StaleSnapshot: serve the old book once, then current
		snap := *s.stale
		s.stale = nil
		return snap, nil
	}
	snap := s.truth.snapshot(s.cfg.Venue, s.cfg.Symbol, s.seq)
	if s.cfg.CrossingSnapshot {
		if ba, ok := s.truth.bestAsk(); ok {
			snap.Bids = append([]market.Level{{Price: ba, Size: 1}}, snap.Bids...) // a bid at the ask -> crossed
			slices.SortFunc(snap.Bids, func(a, b market.Level) int { return cmp.Compare(b.Price, a.Price) })
		}
	}
	return snap, nil
}

// Close marks the source closed; further Next calls return false.
func (s *Synthetic) Close() error {
	s.closed = true
	return nil
}
