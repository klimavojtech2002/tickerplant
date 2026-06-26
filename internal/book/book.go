// Package book reconstructs a single venue/symbol's L2 order book from a snapshot
// plus a stream of deltas, maintaining the never-crosses invariant and resyncing on
// a gap, checksum mismatch, or disconnect (docs/correctness.md). It is the thesis.
package book

import (
	"cmp"
	"slices"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// book holds one symbol's L2 state as two sorted slices: bids high to low, asks low
// to high. Best is O(1) (index 0) and a top-N view is O(depth). This is the engine's
// own structure, deliberately different from the source's map-based oracle so the two
// cross-check (docs/correctness.md §10). Single-writer: not safe for concurrent
// mutation.
type book struct {
	bids []market.Level
	asks []market.Level
}

// set inserts, updates, or (size 0) deletes a level, keeping the side sorted.
func (b *book) set(s market.Side, price market.Price, size market.Size) {
	if s == market.Bid {
		b.bids = setLevel(b.bids, price, size, true)
	} else {
		b.asks = setLevel(b.asks, price, size, false)
	}
}

// setLevel applies one level to a sorted slice; desc selects bid (high-to-low) order.
// A delta carries the new absolute size; size 0 removes the level, and a delete of an
// absent level is a normal no-op.
func setLevel(levels []market.Level, price market.Price, size market.Size, desc bool) []market.Level {
	idx, found := slices.BinarySearchFunc(levels, price, func(lvl market.Level, p market.Price) int {
		if desc {
			return cmp.Compare(p, lvl.Price) // reversed: the slice is descending
		}
		return cmp.Compare(lvl.Price, p)
	})
	if found {
		if size == 0 {
			return slices.Delete(levels, idx, idx+1)
		}
		levels[idx].Size = size
		return levels
	}
	if size == 0 {
		return levels // delete of an absent level: normal, no-op
	}
	return slices.Insert(levels, idx, market.Level{Price: price, Size: size})
}

func (b *book) bestBid() (market.Level, bool) {
	if len(b.bids) == 0 {
		return market.Level{}, false
	}
	return b.bids[0], true
}

func (b *book) bestAsk() (market.Level, bool) {
	if len(b.asks) == 0 {
		return market.Level{}, false
	}
	return b.asks[0], true
}

// crosses reports best bid >= best ask. A one-sided book never crosses.
func (b *book) crosses() bool {
	if len(b.bids) == 0 || len(b.asks) == 0 {
		return false
	}
	return b.bids[0].Price >= b.asks[0].Price
}

// topN returns fresh copies of the best n levels per side, so a published view can be
// read without a lock and never mutated through (ADR-0008).
func (b *book) topN(n int) (bids, asks []market.Level) {
	return clip(b.bids, n), clip(b.asks, n)
}

func clip(levels []market.Level, n int) []market.Level {
	if n > len(levels) {
		n = len(levels)
	}
	out := make([]market.Level, n)
	copy(out, levels[:n])
	return out
}

// buildFrom reconstructs a book from a snapshot, applying each level through set so
// the result is sorted and a venue that repeats a price resolves last-writer-wins
// (the duplicate-price rule deferred from the model, docs/correctness.md §1).
func buildFrom(snap market.Snapshot) *book {
	b := &book{}
	for _, lvl := range snap.Bids {
		b.set(market.Bid, lvl.Price, lvl.Size)
	}
	for _, lvl := range snap.Asks {
		b.set(market.Ask, lvl.Price, lvl.Size)
	}
	return b
}
