package source

import (
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

func TestTruthBookApplyAndDelete(t *testing.T) {
	b := newTruthBook()
	b.apply(market.Delta{
		Bids: []market.Level{{Price: 100, Size: 5}, {Price: 99, Size: 3}},
		Asks: []market.Level{{Price: 101, Size: 2}},
	})
	if bb, ok := b.bestBid(); !ok || bb != 100 {
		t.Fatalf("bestBid = %d, %v; want 100", bb, ok)
	}
	if ba, ok := b.bestAsk(); !ok || ba != 101 {
		t.Fatalf("bestAsk = %d, %v; want 101", ba, ok)
	}
	b.apply(market.Delta{Bids: []market.Level{{Price: 100, Size: 0}}}) // zero size deletes
	if bb, _ := b.bestBid(); bb != 99 {
		t.Fatalf("after delete bestBid = %d, want 99", bb)
	}
}

func TestTruthBookSnapshotSorted(t *testing.T) {
	b := newTruthBook()
	b.apply(market.Delta{
		Bids: []market.Level{{Price: 98, Size: 1}, {Price: 100, Size: 1}, {Price: 99, Size: 1}},
		Asks: []market.Level{{Price: 103, Size: 1}, {Price: 101, Size: 1}, {Price: 102, Size: 1}},
	})
	snap := b.snapshot("V", "S", 7)
	if snap.Bids[0].Price != 100 || snap.Bids[2].Price != 98 {
		t.Fatalf("bids not high-to-low: %+v", snap.Bids)
	}
	if snap.Asks[0].Price != 101 || snap.Asks[2].Price != 103 {
		t.Fatalf("asks not low-to-high: %+v", snap.Asks)
	}
	if snap.LastUpdateID != 7 {
		t.Fatalf("LastUpdateID = %d, want 7", snap.LastUpdateID)
	}
}

func TestTruthBookCrosses(t *testing.T) {
	b := newTruthBook()
	if b.crosses() {
		t.Fatal("empty book must not cross")
	}
	b.apply(market.Delta{Bids: []market.Level{{Price: 100, Size: 1}}})
	if b.crosses() {
		t.Fatal("one-sided book must not cross")
	}
	b.apply(market.Delta{Asks: []market.Level{{Price: 101, Size: 1}}})
	if b.crosses() {
		t.Fatal("100 < 101 must not cross")
	}
	b.apply(market.Delta{Asks: []market.Level{{Price: 100, Size: 1}}}) // an ask at the bid price
	if !b.crosses() {
		t.Fatal("best bid == best ask must cross")
	}
}
