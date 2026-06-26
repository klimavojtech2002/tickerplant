package source

import (
	"cmp"
	"slices"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// truthBook is a naive, obviously-correct L2 book: the oracle the engine (slice
// 0003) is verified against. It is deliberately the simplest possible
// reconstruction — a plain map per side, rebuilt from absolute sizes — and shares
// no code with the engine, so a bug in one cannot hide a bug in the other
// (docs/correctness.md §10). It is single-threaded; the synthetic source owns it.
type truthBook struct {
	bids map[market.Price]market.Size
	asks map[market.Price]market.Size
}

func newTruthBook() *truthBook {
	return &truthBook{
		bids: make(map[market.Price]market.Size),
		asks: make(map[market.Price]market.Size),
	}
}

func (b *truthBook) side(s market.Side) map[market.Price]market.Size {
	if s == market.Bid {
		return b.bids
	}
	return b.asks
}

// set applies one level: a zero size deletes it, any other size replaces it.
func (b *truthBook) set(s market.Side, price market.Price, size market.Size) {
	m := b.side(s)
	if size == 0 {
		delete(m, price)
		return
	}
	m[price] = size
}

// apply applies every level of a delta to the book.
func (b *truthBook) apply(d market.Delta) {
	for _, lvl := range d.Bids {
		b.set(market.Bid, lvl.Price, lvl.Size)
	}
	for _, lvl := range d.Asks {
		b.set(market.Ask, lvl.Price, lvl.Size)
	}
}

// sortedLevels returns the side's levels: bids high to low, asks low to high. The
// iteration is over a sorted key slice, never a map, so the output is deterministic.
func (b *truthBook) sortedLevels(s market.Side) []market.Level {
	m := b.side(s)
	prices := make([]market.Price, 0, len(m))
	for p := range m {
		prices = append(prices, p)
	}
	if s == market.Bid {
		slices.SortFunc(prices, func(a, b market.Price) int { return cmp.Compare(b, a) }) // high to low
	} else {
		slices.Sort(prices) // low to high
	}
	levels := make([]market.Level, len(prices))
	for i, p := range prices {
		levels[i] = market.Level{Price: p, Size: m[p]}
	}
	return levels
}

// snapshot returns the current book as a canonical Snapshot, current as of seq.
func (b *truthBook) snapshot(venue market.Venue, symbol market.Symbol, seq market.Sequence) market.Snapshot {
	return market.Snapshot{
		Venue:        venue,
		Symbol:       symbol,
		LastUpdateID: seq,
		Bids:         b.sortedLevels(market.Bid),
		Asks:         b.sortedLevels(market.Ask),
	}
}

// bestBid returns the highest bid price; ok is false when the side is empty.
func (b *truthBook) bestBid() (market.Price, bool) {
	var best market.Price
	ok := false
	for p := range b.bids {
		if !ok || p > best {
			best, ok = p, true
		}
	}
	return best, ok
}

// bestAsk returns the lowest ask price; ok is false when the side is empty.
func (b *truthBook) bestAsk() (market.Price, bool) {
	var best market.Price
	ok := false
	for p := range b.asks {
		if !ok || p < best {
			best, ok = p, true
		}
	}
	return best, ok
}

// crosses reports whether the book is crossed: best bid at or above best ask. A
// legal book is never crossed (docs/correctness.md §5).
func (b *truthBook) crosses() bool {
	bb, okB := b.bestBid()
	ba, okA := b.bestAsk()
	if !okB || !okA {
		return false // a one-sided book cannot cross
	}
	return bb >= ba
}
