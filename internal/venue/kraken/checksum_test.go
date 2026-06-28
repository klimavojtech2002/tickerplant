package kraken

import (
	"hash/crc32"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// crcField must match the exact transforms in the Kraken checksum guide.
func TestCRCField(t *testing.T) {
	cases := []struct {
		v     int64
		scale int
		want  string
	}{
		{452852, 1, "452852"}, // 45285.2 -> remove "." -> "452852"
		{100000, 8, "100000"}, // 0.00100000 -> "000100000" -> strip leading zeros -> "100000"
		{5, 1, "5"},           // 0.5 -> "05" -> "5"
		{0, 8, "0"},           // defensive: a zero strips to empty, kept as "0"
	}
	for _, c := range cases {
		if got := crcField(c.v, c.scale); got != c.want {
			t.Errorf("crcField(%d, %d) = %q, want %q", c.v, c.scale, got, c.want)
		}
	}
}

// Golden vector from the Kraken v2 book checksum guide (docs.kraken.com, 2026-06-28):
// the documented top-10 asks/bids must produce Kraken's published checksum, pinning the
// CRC32 polynomial, level order, and field format against the real venue.
func TestChecksumGoldenKrakenVector(t *testing.T) {
	asks := []market.Level{ // price scale 1, qty scale 8
		{Price: 452852, Size: 100000}, {Price: 452864, Size: 154571953}, {Price: 452866, Size: 154571109},
		{Price: 452896, Size: 154560911}, {Price: 452902, Size: 15890660}, {Price: 452918, Size: 154553491},
		{Price: 452947, Size: 4454749}, {Price: 452961, Size: 35380000}, {Price: 452975, Size: 9945542},
		{Price: 452995, Size: 18772827},
	}
	bids := []market.Level{
		{Price: 452835, Size: 10000000}, {Price: 452834, Size: 154582015}, {Price: 452821, Size: 10000000},
		{Price: 452810, Size: 10000000}, {Price: 452803, Size: 154592586}, {Price: 452790, Size: 7990000},
		{Price: 452776, Size: 3310103}, {Price: 452775, Size: 30000000}, {Price: 452773, Size: 154602737},
		{Price: 452766, Size: 15445238},
	}
	if got := Checksum(1, 8)(bids, asks); got != 3310070434 {
		t.Fatalf("Kraken golden checksum = %d, want 3310070434", got)
	}
}

// The checksum concatenates the top asks then the top bids, each price+qty stripped, and
// CRC32s the result. (The golden vector above pins the value against real Kraken; this
// pins the order/format mechanics.)
func TestChecksumOrderAndFormat(t *testing.T) {
	asks := []market.Level{{Price: 452852, Size: 100000}} // 45285.2, 0.001
	bids := []market.Level{{Price: 452840, Size: 200000}} // 45284.0, 0.002
	cs := Checksum(1, 8)

	want := crc32.ChecksumIEEE([]byte("452852" + "100000" + "452840" + "200000")) // asks, then bids
	if got := cs(bids, asks); got != want {
		t.Fatalf("checksum = %d, want %d (asks then bids, decimal/leading-zeros stripped)", cs(bids, asks), want)
	}
	// the asks-then-bids order is load-bearing: feeding them swapped must change the result
	if cs(asks, bids) == cs(bids, asks) {
		t.Fatal("bid/ask order must affect the checksum")
	}
}
