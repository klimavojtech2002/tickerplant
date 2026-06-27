package binance

import (
	"errors"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

func TestToDeltaExact(t *testing.T) {
	u := depthUpdate{
		Event: "depthUpdate", First: 101, Final: 103,
		Bids: [][]string{{"100.50", "2.000"}, {"100.40", "0"}}, // second is a delete
		Asks: [][]string{{"101.10", "1.500"}},
	}
	d, err := toDelta("BINANCE", "BTCUSDT", scales{price: 2, size: 3}, u)
	if err != nil {
		t.Fatal(err)
	}
	if d.FirstSeq != 101 || d.LastSeq != 103 {
		t.Fatalf("seq = [%d,%d], want [101,103]", d.FirstSeq, d.LastSeq)
	}
	if d.Bids[0] != (market.Level{Price: 10050, Size: 2000}) {
		t.Fatalf("bid0 = %+v, want {10050,2000}", d.Bids[0])
	}
	if !d.Bids[1].IsDelete() || d.Bids[1].Price != 10040 {
		t.Fatalf("bid1 = %+v, want a delete at 10040", d.Bids[1])
	}
	if d.Asks[0] != (market.Level{Price: 10110, Size: 1500}) {
		t.Fatalf("ask0 = %+v, want {10110,1500}", d.Asks[0])
	}
}

func TestToSnapshotExact(t *testing.T) {
	snap, err := toSnapshot("BINANCE", "BTCUSDT", scales{price: 2, size: 1},
		restDepth{LastUpdateID: 100, Bids: [][]string{{"100.00", "1.0"}}, Asks: [][]string{{"101.00", "2.0"}}})
	if err != nil {
		t.Fatal(err)
	}
	if snap.LastUpdateID != 100 {
		t.Fatalf("LastUpdateID = %d, want 100", snap.LastUpdateID)
	}
	if snap.Bids[0] != (market.Level{Price: 10000, Size: 10}) || snap.Asks[0] != (market.Level{Price: 10100, Size: 20}) {
		t.Fatalf("levels = %+v / %+v", snap.Bids[0], snap.Asks[0])
	}
}

func TestToSnapshotRejectsBadAsk(t *testing.T) {
	_, err := toSnapshot("V", "S", scales{price: 2, size: 2},
		restDepth{Bids: [][]string{{"100.00", "1.00"}}, Asks: [][]string{{"1.x", "1.00"}}})
	if !errors.Is(err, market.ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
}

func TestNormalizeRejectsBadDecimal(t *testing.T) {
	_, err := toDelta("V", "S", scales{price: 2, size: 2}, depthUpdate{Bids: [][]string{{"1.x", "1.0"}}})
	if !errors.Is(err, market.ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
}

func TestNormalizeRejectsShortLevel(t *testing.T) {
	_, err := toDelta("V", "S", scales{price: 2, size: 2}, depthUpdate{Asks: [][]string{{"100.00"}}})
	if !errors.Is(err, market.ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
}

func TestNormalizeRejectsTooManyFractionalDigits(t *testing.T) {
	// "1.234" at size scale 2 has more fractional digits than the scale: a loud error,
	// never a silent round (ADR-0003).
	_, err := toDelta("V", "S", scales{price: 2, size: 2}, depthUpdate{Bids: [][]string{{"100.00", "1.234"}}})
	if !errors.Is(err, market.ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
}
