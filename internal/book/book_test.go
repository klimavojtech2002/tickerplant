package book

import (
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

func TestSetInsertUpdateDelete(t *testing.T) {
	b := &book{}
	b.set(market.Bid, 100, 5)
	b.set(market.Bid, 99, 3)
	b.set(market.Bid, 101, 1) // inserts at the front (descending)
	if bb, _ := b.bestBid(); bb.Price != 101 {
		t.Fatalf("best bid = %d, want 101", bb.Price)
	}
	b.set(market.Bid, 101, 0) // delete the current best
	if bb, _ := b.bestBid(); bb.Price != 100 {
		t.Fatalf("after deleting best, best bid = %d, want 100", bb.Price)
	}
	b.set(market.Bid, 100, 7) // update size in place
	if bb, _ := b.bestBid(); bb.Size != 7 {
		t.Fatalf("best bid size = %d, want 7", bb.Size)
	}
	b.set(market.Bid, 50, 0) // delete of an absent level: no-op, no panic
	if len(b.bids) != 2 {
		t.Fatalf("bids len = %d, want 2", len(b.bids))
	}

	b.set(market.Ask, 200, 1)
	b.set(market.Ask, 150, 2) // inserts at the front (ascending)
	if ba, _ := b.bestAsk(); ba.Price != 150 {
		t.Fatalf("best ask = %d, want 150", ba.Price)
	}
}

func TestBestOnEmpty(t *testing.T) {
	b := &book{}
	if _, ok := b.bestBid(); ok {
		t.Error("empty bids must report no best")
	}
	if _, ok := b.bestAsk(); ok {
		t.Error("empty asks must report no best")
	}
}

func TestCrosses(t *testing.T) {
	b := &book{}
	if b.crosses() {
		t.Fatal("empty book must not cross")
	}
	b.set(market.Bid, 100, 1)
	if b.crosses() {
		t.Fatal("one-sided book must not cross")
	}
	b.set(market.Ask, 101, 1)
	if b.crosses() {
		t.Fatal("100 < 101 must not cross")
	}
	b.set(market.Ask, 100, 1) // an ask at the bid price
	if !b.crosses() {
		t.Fatal("best bid == best ask must cross")
	}
}

func TestTopNFreshCopy(t *testing.T) {
	b := &book{}
	for p := market.Price(100); p < 110; p++ {
		b.set(market.Bid, p, 1)
		b.set(market.Ask, p+100, 1)
	}
	bids, asks := b.topN(3)
	if len(bids) != 3 || bids[0].Price != 109 {
		t.Fatalf("top-3 bids wrong: %v", bids)
	}
	if len(asks) != 3 || asks[0].Price != 200 {
		t.Fatalf("top-3 asks wrong: %v", asks)
	}
	bids[0].Size = 999 // mutating the view must not touch the book
	if b.bids[0].Size == 999 {
		t.Fatal("topN returned an aliased slice; the book was mutated through the view")
	}
	// a depth deeper than the book returns every level, not one fewer (clip clamp)
	allBids, allAsks := b.topN(50)
	if len(allBids) != 10 || len(allAsks) != 10 {
		t.Fatalf("topN(50) on a 10-level book = %d/%d levels, want 10 each", len(allBids), len(allAsks))
	}
	if allBids[9].Price != 100 || allAsks[9].Price != 209 {
		t.Fatalf("deepest level dropped: bids[9]=%v asks[9]=%v", allBids[9], allAsks[9])
	}
}

// buildFrom resolves a repeated price last-writer-wins (the duplicate-price rule).
func TestBuildFromDeduplicates(t *testing.T) {
	snap := market.Snapshot{
		Bids: []market.Level{{Price: 100, Size: 1}, {Price: 100, Size: 5}, {Price: 99, Size: 2}},
		Asks: []market.Level{{Price: 101, Size: 1}},
	}
	b := buildFrom(snap)
	if bb, _ := b.bestBid(); bb.Price != 100 || bb.Size != 5 {
		t.Fatalf("duplicate price not last-writer-wins: best bid = %d:%d, want 100:5", bb.Price, bb.Size)
	}
	if len(b.bids) != 2 {
		t.Fatalf("dedup failed: bids len = %d, want 2", len(b.bids))
	}
}
