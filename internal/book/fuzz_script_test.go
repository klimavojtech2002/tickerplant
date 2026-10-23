package book

import (
	"context"
	"sort"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// The pieces of the engine fuzz: an oracle of the venue's true book, a scripted source that
// replays a list of events and snapshots in step with the engine, and a builder that turns a byte
// string into that list. Nothing here uses source.Synthetic, its truth book, or the engine's own
// book, so the fuzz checks the engine against something it shares no code with.

const (
	fuzzInitialSeq = market.Sequence(1000) // above the stale range (0 to 50) of the stale-delta operation
	fuzzMaxOps     = 300                   // operations read from one input
	fuzzTailDeltas = 20                    // fault-free legal deltas appended after the operations
	fuzzHistory    = 4                     // oracle states kept for the old-snapshot operation
)

// fuzzOracle is the venue's true book: a size per price on each side, and the sequence of the last
// change. Bids stay in [1,1000] and asks in [1001,2000], so it never crosses.
type fuzzOracle struct {
	bids, asks map[market.Price]market.Size
	seq        market.Sequence
}

// newFuzzOracle lays down six levels per side: bids from 900 down in steps of 10, asks from 1100 up.
func newFuzzOracle(sizes []market.Size) *fuzzOracle {
	o := &fuzzOracle{bids: map[market.Price]market.Size{}, asks: map[market.Price]market.Size{}, seq: fuzzInitialSeq}
	for i := range 6 {
		o.bids[market.Price(900-10*i)] = sizes[i]
		o.asks[market.Price(1100+10*i)] = sizes[6+i]
	}
	return o
}

func (o *fuzzOracle) clone() *fuzzOracle {
	c := &fuzzOracle{bids: map[market.Price]market.Size{}, asks: map[market.Price]market.Size{}, seq: o.seq}
	for p, s := range o.bids {
		c.bids[p] = s
	}
	for p, s := range o.asks {
		c.asks[p] = s
	}
	return c
}

func (o *fuzzOracle) side(s market.Side) map[market.Price]market.Size {
	if s == market.Bid {
		return o.bids
	}
	return o.asks
}

// apply sets a level; size zero removes it.
func (o *fuzzOracle) apply(s market.Side, p market.Price, size market.Size) {
	if size == 0 {
		delete(o.side(s), p)
		return
	}
	o.side(s)[p] = size
}

// top returns up to n levels, best first: bids high to low, asks low to high.
func (o *fuzzOracle) top(s market.Side, n int) []market.Level {
	m := o.side(s)
	prices := make([]market.Price, 0, len(m))
	for p := range m {
		prices = append(prices, p)
	}
	sort.Slice(prices, func(i, j int) bool {
		if s == market.Bid {
			return prices[i] > prices[j]
		}
		return prices[i] < prices[j]
	})
	if n < len(prices) {
		prices = prices[:n]
	}
	out := make([]market.Level, len(prices))
	for i, p := range prices {
		out[i] = market.Level{Price: p, Size: m[p]}
	}
	return out
}

func (o *fuzzOracle) bestAsk() market.Price { return o.top(market.Ask, 1)[0].Price }
func (o *fuzzOracle) bestBid() market.Price { return o.top(market.Bid, 1)[0].Price }

func (o *fuzzOracle) snapshot() market.Snapshot {
	return market.Snapshot{
		Venue: "fuzz", Symbol: "S", LastUpdateID: o.seq,
		Bids: o.top(market.Bid, len(o.bids)), Asks: o.top(market.Ask, len(o.asks)),
	}
}

// ---- the script ------------------------------------------------------------------------------

// A marker arms the snapshot queue when the engine reaches it; an event is what Next returns.
type fuzzMarkerKind uint8

const (
	markCrossed fuzzMarkerKind = iota // the next n snapshots are crossed
	markLocked                        // the next n snapshots are locked (best bid equals best ask)
	markOld                           // the next snapshot is an honest older state
	markDisarm                        // drop every armed snapshot (before the fault-free tail)
)

type fuzzItem struct {
	isMarker bool
	kind     fuzzMarkerKind
	n        int
	old      *fuzzOracle // for markOld
	ev       source.Event
	after    *fuzzOracle // the oracle after the whole operation that produced the event
}

// fuzzScript is a built script and the counts the assertions need.
type fuzzScript struct {
	items   []fuzzItem
	initial *fuzzOracle
	final   *fuzzOracle
	depth   int

	faults, gapOps, reorderOps, disconnectOps, crossOps int // faults = gap + reorder + disconnect + cross
	oldSnapshots, armedBad                              int // armedBad counts queue entries, not markers
	legalOps                                            int
	ops                                                 int // operations read from the input
	dupOps, coalesceOps, staleOps                       int
	lockedCrosses, askCrosses, deletes                  int // crosses that lock, crosses on the ask side, deletes asked for
}

// fuzzSource replays a fuzzScript in lockstep with the engine. Snapshot returns the oracle as it was
// after the last event Next returned (the initial book before any), unless a marker armed something
// else; so an engine at sequence s always holds the oracle's book at s.
type fuzzSource struct {
	items   []fuzzItem
	pos     int
	current *fuzzOracle
	armed   []fuzzItem
	// shiftSizes makes every snapshot report sizes one too large: a deliberately wrong source, used only
	// to show that the convergence check can fail.
	shiftSizes bool
}

var _ source.Source = (*fuzzSource)(nil)

func newFuzzSource(sc *fuzzScript) *fuzzSource {
	return &fuzzSource{items: sc.items, current: sc.initial}
}

func (s *fuzzSource) Next(context.Context) (source.Event, bool) {
	for s.pos < len(s.items) {
		it := s.items[s.pos]
		s.pos++
		if !it.isMarker {
			s.current = it.after
			return it.ev, true
		}
		if it.kind == markDisarm {
			s.armed = nil
			continue
		}
		n := it.n
		if it.kind == markOld {
			n = 1
		}
		for range n {
			s.armed = append(s.armed, it)
		}
	}
	return source.Event{}, false
}

func (s *fuzzSource) Snapshot(context.Context) (market.Snapshot, error) {
	snap := s.current.snapshot()
	if len(s.armed) > 0 {
		it := s.armed[0]
		s.armed = s.armed[1:]
		switch it.kind {
		case markCrossed:
			snap = s.current.snapshot()
			snap.Bids = append([]market.Level{{Price: s.current.bestAsk() + 10, Size: 1}}, snap.Bids...)
		case markLocked:
			snap = s.current.snapshot()
			snap.Bids = append([]market.Level{{Price: s.current.bestAsk(), Size: 1}}, snap.Bids...)
		case markOld:
			snap = it.old.snapshot()
		}
	}
	if s.shiftSizes {
		for i := range snap.Bids {
			snap.Bids[i].Size++
		}
		for i := range snap.Asks {
			snap.Asks[i].Size++
		}
	}
	return snap, nil
}

func (s *fuzzSource) Close() error { return nil }

// ---- decoding and building ---------------------------------------------------------------------

// fuzzReader reads bytes and returns 0 past the end, so every input decodes.
type fuzzReader struct {
	data []byte
	pos  int
}

func (r *fuzzReader) more() bool { return r.pos < len(r.data) }

func (r *fuzzReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

// Operation numbers, from (b0 & 0x7F) % 12. Operations 0 to 3 and 11 are all legal deltas.
const (
	opGap        = 4
	opReorder    = 5
	opDuplicate  = 6
	opCoalesce   = 7
	opDisconnect = 8
	opCross      = 9
	opMarker     = 10 // kind and count come from b1; kind 3 is a stale-delta event, not a marker
	opLegalHigh  = 11
)

type fuzzBuilder struct {
	o    *fuzzOracle
	hist []*fuzzOracle
	sc   *fuzzScript
}

// record keeps the oracle after each advance, so an old snapshot can be an earlier state.
func (b *fuzzBuilder) record() {
	b.hist = append(b.hist, b.o.clone())
	if len(b.hist) > fuzzHistory {
		b.hist = b.hist[1:]
	}
}

// price maps a byte-derived offset to a price near the spread: 910 - 5*off for a bid, 1090 + 5*off for an ask.
func fuzzPrice(side market.Side, off int) market.Price {
	if side == market.Bid {
		return market.Price(910 - 5*off)
	}
	return market.Price(1090 + 5*off)
}

// advance applies one legal change to the oracle (sequence +1) and returns it as a delta. A delete that
// would empty a side becomes a size-1 set; a delete of an absent level is a legal no-op.
func (b *fuzzBuilder) advance(side market.Side, off, sz int) market.Delta {
	price := fuzzPrice(side, off)
	size := market.Size(sz)
	if size == 0 {
		b.sc.deletes++
		if _, present := b.o.side(side)[price]; present && len(b.o.side(side)) == 1 {
			size = 1
		}
	}
	b.o.seq++
	b.o.apply(side, price, size)
	b.record()
	d := market.Delta{Venue: "fuzz", Symbol: "S", FirstSeq: b.o.seq, LastSeq: b.o.seq}
	lvl := []market.Level{{Price: price, Size: size}}
	if side == market.Bid {
		d.Bids = lvl
	} else {
		d.Asks = lvl
	}
	return d
}

func (b *fuzzBuilder) emit(evs ...source.Event) {
	after := b.o.clone()
	for _, ev := range evs {
		b.sc.items = append(b.sc.items, fuzzItem{ev: ev, after: after})
	}
}

func deltaEvent(d market.Delta) source.Event { return source.Event{Kind: source.EventDelta, Delta: d} }

// op applies one operation read from the input.
func (b *fuzzBuilder) op(b0, b1 byte) {
	sc := b.sc
	kind := int(b0&0x7F) % 12
	side := market.Bid
	if b0>>7 == 1 {
		side = market.Ask
	}
	off, sz := int(b1>>3), int(b1&7)
	off2, sz2 := (off+7)%32, (sz+3)%8
	switch kind {
	case opGap:
		b.advance(side, off, sz) // the oracle moves on; nothing is emitted
		sc.gapOps++
		sc.faults++
	case opReorder:
		d1 := b.advance(side, off, sz)
		d2 := b.advance(side, off2, sz2)
		b.emit(deltaEvent(d2), deltaEvent(d1))
		sc.reorderOps++
		sc.faults++
	case opDuplicate:
		d := b.advance(side, off, sz)
		b.emit(deltaEvent(d), deltaEvent(d))
		sc.dupOps++
		sc.legalOps++
	case opCoalesce:
		d1 := b.advance(side, off, sz)
		d2 := b.advance(side, off2, sz2)
		merged := market.Delta{Venue: "fuzz", Symbol: "S", FirstSeq: d1.FirstSeq, LastSeq: d2.LastSeq,
			Bids: append(append([]market.Level{}, d1.Bids...), d2.Bids...),
			Asks: append(append([]market.Level{}, d1.Asks...), d2.Asks...)}
		b.emit(deltaEvent(merged))
		sc.coalesceOps++
		sc.legalOps++
	case opDisconnect:
		b.emit(source.Event{Kind: source.EventDisconnected})
		sc.disconnectOps++
		sc.faults++
	case opCross:
		// A delta that crosses the book (a bid above the ask band, or an ask below the bid band) or locks it
		// (a bid equal to the best ask, or an ask equal to the best bid), with the oracle's next sequence
		// number, never applied to the oracle. Its size is at least 1: a zero size would be a legal no-op
		// that moves the engine's sequence without crossing, and the next legal delta, which reuses this
		// sequence number, would then be dropped as stale.
		locked := b1&1 == 1
		size := market.Size(max(sz, 1))
		d := market.Delta{Venue: "fuzz", Symbol: "S", FirstSeq: b.o.seq + 1, LastSeq: b.o.seq + 1}
		if side == market.Ask {
			price := market.Price(1 + off)
			if locked {
				price = b.o.bestBid()
			}
			d.Asks = []market.Level{{Price: price, Size: size}}
			sc.askCrosses++
		} else {
			price := market.Price(2001 + off)
			if locked {
				price = b.o.bestAsk()
			}
			d.Bids = []market.Level{{Price: price, Size: size}}
		}
		if locked {
			sc.lockedCrosses++
		}
		b.emit(deltaEvent(d))
		sc.crossOps++
		sc.faults++
	case opMarker:
		k, n := fuzzMarkerKind(b1&3), 1+int((b1>>2)&3)
		switch {
		case b1&3 == 3: // a stale delta: older than any snapshot, so it is always dropped
			s := market.Sequence(int(b1>>2) % 51)
			d := market.Delta{Venue: "fuzz", Symbol: "S", FirstSeq: s, LastSeq: s,
				Bids: []market.Level{{Price: fuzzPrice(market.Bid, off), Size: market.Size(max(sz, 1))}}}
			b.emit(deltaEvent(d))
			sc.staleOps++
		case k == markOld:
			old := b.o.clone()
			if len(b.hist) > 0 {
				old = b.hist[max(len(b.hist)-n, 0)]
			}
			sc.items = append(sc.items, fuzzItem{isMarker: true, kind: markOld, old: old})
			sc.oldSnapshots++
		default: // crossed or locked
			sc.items = append(sc.items, fuzzItem{isMarker: true, kind: k, n: n})
			sc.armedBad += n
		}
	default: // 0 to 3 and 11: a legal delta
		b.emit(deltaEvent(b.advance(side, off, sz)))
		sc.legalOps++
	}
}

// fuzzBuild decodes data into a script. Every input is valid: the reader pads with zeros and at most
// fuzzMaxOps operations are read.
func fuzzBuild(data []byte) *fuzzScript {
	r := &fuzzReader{data: data}
	sc := &fuzzScript{depth: 1 + int(r.next()%8)}
	sizes := make([]market.Size, 12)
	for i := range sizes {
		sizes[i] = market.Size(1 + int(r.next()))
	}
	b := &fuzzBuilder{o: newFuzzOracle(sizes), sc: sc}
	sc.initial = b.o.clone()
	for i := 0; i < fuzzMaxOps && r.more(); i++ {
		b0 := r.next()
		b.op(b0, r.next())
		sc.ops++
	}
	// Disarm what is still queued, then a fault-free tail: whatever the faults left behind must heal.
	sc.items = append(sc.items, fuzzItem{isMarker: true, kind: markDisarm})
	for i := range fuzzTailDeltas {
		side := market.Bid
		if i%2 == 1 {
			side = market.Ask
		}
		b.emit(deltaEvent(b.advance(side, (i*5)%32, 1+i%7)))
	}
	sc.final = b.o
	return sc
}
