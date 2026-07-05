package kraken

import (
	"hash/crc32"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// crcField must match the exact transforms in the Kraken checksum guide. The guide
// says "render at the pair's precision, drop the point, strip leading zeros" — which
// reduces to the decimal digits of the scaled integer, so the field is scale-free:
// the venue's trailing zeros are already inside the scaled value.
func TestCRCField(t *testing.T) {
	cases := []struct {
		v    int64
		want string
	}{
		{452852, "452852"}, // 45285.2 at scale 1 -> remove "." -> "452852"
		{100000, "100000"}, // 0.00100000 at scale 8 -> "000100000" -> strip zeros -> "100000"
		{5, "5"},           // 0.5 at scale 1 -> "05" -> "5"
		{0, "0"},           // defensive: a zero strips to empty, kept as "0"
	}
	for _, c := range cases {
		if got := crcField(c.v); got != c.want {
			t.Errorf("crcField(%d) = %q, want %q", c.v, got, c.want)
		}
	}
}

// goldenBook returns the levels from the Kraken checksum guide's worked example
// (checksum_test.go pins their CRC to the published 3310070434).
func goldenBook() (bids, asks []market.Level) {
	asks = []market.Level{
		{Price: 452852, Size: 100000}, {Price: 452864, Size: 154571953}, {Price: 452866, Size: 154571109},
		{Price: 452896, Size: 154560911}, {Price: 452902, Size: 15890660}, {Price: 452918, Size: 154553491},
		{Price: 452947, Size: 4454749}, {Price: 452961, Size: 35380000}, {Price: 452975, Size: 9945542},
		{Price: 452995, Size: 18772827},
	}
	bids = []market.Level{
		{Price: 452835, Size: 10000000}, {Price: 452834, Size: 154582015}, {Price: 452821, Size: 10000000},
		{Price: 452810, Size: 10000000}, {Price: 452803, Size: 154592586}, {Price: 452790, Size: 7990000},
		{Price: 452776, Size: 3310103}, {Price: 452775, Size: 30000000}, {Price: 452773, Size: 154602737},
		{Price: 452766, Size: 15445238},
	}
	return bids, asks
}

const goldenChecksum = 3310070434

// Golden vector from the Kraken v2 book checksum guide (docs.kraken.com, 2026-06-28,
// re-verified 2026-07-05): the documented top-10 asks/bids must produce Kraken's
// published checksum, pinning the CRC32 polynomial, level order, and field format
// against the real venue.
func TestChecksumGoldenKrakenVector(t *testing.T) {
	bids, asks := goldenBook() // price scale 1, qty scale 8 (BTC/USD)
	if got := Checksum(bids, asks); got != goldenChecksum {
		t.Fatalf("Kraken golden checksum = %d, want %d", got, uint32(goldenChecksum))
	}
}

// The checksum concatenates the top asks then the top bids, each price+qty stripped, and
// CRC32s the result. (The golden vector above pins the value against real Kraken; this
// pins the order/format mechanics.)
func TestChecksumOrderAndFormat(t *testing.T) {
	asks := []market.Level{{Price: 452852, Size: 100000}} // 45285.2, 0.001
	bids := []market.Level{{Price: 452840, Size: 200000}} // 45284.0, 0.002

	want := crc32.ChecksumIEEE([]byte("452852" + "100000" + "452840" + "200000")) // asks, then bids
	if got := Checksum(bids, asks); got != want {
		t.Fatalf("checksum = %d, want %d (asks then bids, decimal/leading-zeros stripped)", got, want)
	}
	// the asks-then-bids order is load-bearing: feeding them swapped must change the result
	if Checksum(asks, bids) == Checksum(bids, asks) {
		t.Fatal("bid/ask order must affect the checksum")
	}
}
