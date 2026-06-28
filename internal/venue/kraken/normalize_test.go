package kraken

import (
	"errors"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

func bookFrame(typ string) []byte {
	return []byte(`{"channel":"book","type":"` + typ + `","data":[{"symbol":"BTC/USD",` +
		`"bids":[{"price":"45284.0","qty":"0.00200000"}],` +
		`"asks":[{"price":"45285.2","qty":"0.00100000"}],"checksum":12345}]}`)
}

func TestParseBookSnapshot(t *testing.T) {
	p, ok, err := parseBook(bookFrame("snapshot"), scales{price: 1, qty: 8})
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !p.isSnapshot {
		t.Fatal("type snapshot must set isSnapshot")
	}
	if p.checksum != 12345 {
		t.Fatalf("checksum = %d, want 12345", p.checksum)
	}
	if p.asks[0] != (market.Level{Price: 452852, Size: 100000}) { // 45285.2 / 0.001
		t.Fatalf("ask = %+v, want {452852,100000}", p.asks[0])
	}
	if p.bids[0] != (market.Level{Price: 452840, Size: 200000}) { // 45284.0 / 0.002
		t.Fatalf("bid = %+v, want {452840,200000}", p.bids[0])
	}
}

func TestParseBookUpdate(t *testing.T) {
	p, ok, err := parseBook(bookFrame("update"), scales{price: 1, qty: 8})
	if !ok || err != nil || p.isSnapshot {
		t.Fatalf("update frame: ok=%v err=%v isSnapshot=%v", ok, err, p.isSnapshot)
	}
}

func TestParseBookSkipsNonBook(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"method":"subscribe","success":true}`), // ack
		[]byte(`{"channel":"heartbeat"}`),               // heartbeat
		[]byte(`{"channel":"status","data":[]}`),        // status (no book data)
		[]byte(`not json`),                              // junk
		// each isolates one clause of the book-frame guard (so each clause is load-bearing):
		[]byte(`{"channel":"book","type":"snapshot","data":[]}`),                                     // book, but empty data (would panic on data[0] without the guard)
		[]byte(`{"channel":"book","type":"pong","data":[{"symbol":"X","bids":[],"asks":[]}]}`),       // book + data, but not a snapshot/update type
		[]byte(`{"channel":"ticker","type":"snapshot","data":[{"symbol":"X","bids":[],"asks":[]}]}`), // snapshot type + data, but not the book channel
	} {
		if _, ok, err := parseBook(raw, scales{price: 1, qty: 8}); ok || err != nil {
			t.Fatalf("non-book frame %q: ok=%v err=%v, want skipped", raw, ok, err)
		}
	}
}

func TestParseBookZeroQtyIsDelete(t *testing.T) {
	raw := []byte(`{"channel":"book","type":"update","data":[{"symbol":"BTC/USD",` +
		`"bids":[{"price":"45284.0","qty":"0.00000000"}],"asks":[]}]}`)
	p, ok, err := parseBook(raw, scales{price: 1, qty: 8})
	if !ok || err != nil {
		t.Fatal(err)
	}
	if !p.bids[0].IsDelete() {
		t.Fatalf("zero qty must be a delete, got %+v", p.bids[0])
	}
}

func TestParseBookBadDecimal(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"channel":"book","type":"update","data":[{"bids":[{"price":"45284.x","qty":"0.001"}],"asks":[]}]}`), // bad bid price
		[]byte(`{"channel":"book","type":"update","data":[{"bids":[],"asks":[{"price":"45285.y","qty":"0.001"}]}]}`), // bad ask price
		[]byte(`{"channel":"book","type":"update","data":[{"bids":[{"price":"45284.0","qty":"0.0z"}],"asks":[]}]}`),  // bad qty
	} {
		if _, _, err := parseBook(raw, scales{price: 1, qty: 8}); !errors.Is(err, market.ErrMalformed) {
			t.Fatalf("frame %q: err = %v, want ErrMalformed", raw, err)
		}
	}
}
