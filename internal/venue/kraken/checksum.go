// Package kraken adapts the Kraken WebSocket v2 book channel to the canonical transport
// port. Kraken has no per-update sequence id; integrity is a CRC32 checksum over the top
// 10 levels (ADR-0007), so this package computes that checksum in Kraken's exact format
// for the engine to verify (the engine owns when/what to check, this owns the format —
// ADR-0007). Verified against the Kraken v2 book and checksum guide
// (docs.kraken.com/api/docs/guides/spot-ws-book-v2, 2026-06-28).
package kraken

import (
	"hash/crc32"
	"strings"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// Checksum returns the engine's checksum function in Kraken's v2 format, for a symbol
// whose price and quantity are stored at the given decimal scales. For the top 10 asks
// (low to high) then the top 10 bids (high to low), each level contributes its price
// then its quantity, each rendered at the symbol's scale with the decimal point and
// leading zeros removed; the concatenation is CRC32'd (IEEE) to an unsigned 32-bit
// value. The engine passes its top-10 view (bids high-to-low, asks low-to-high), which
// already matches this order.
func Checksum(priceScale, qtyScale int) func(bids, asks []market.Level) uint32 {
	return func(bids, asks []market.Level) uint32 {
		var b strings.Builder
		for _, l := range asks {
			b.WriteString(crcField(int64(l.Price), priceScale))
			b.WriteString(crcField(int64(l.Size), qtyScale))
		}
		for _, l := range bids {
			b.WriteString(crcField(int64(l.Price), priceScale))
			b.WriteString(crcField(int64(l.Size), qtyScale))
		}
		return crc32.ChecksumIEEE([]byte(b.String()))
	}
}

// crcField renders one scaled value to Kraken's checksum form: the decimal string at the
// venue's scale, with the decimal point removed and leading zeros stripped — "45285.2"
// (scale 1) -> "452852", "0.00100000" (scale 8) -> "100000".
func crcField(v int64, scale int) string {
	s := market.FormatScaled(v, scale)
	s = strings.ReplaceAll(s, ".", "")
	s = strings.TrimLeft(s, "0")
	if s == "" { // a zero value strips to empty; Kraken book levels are never zero, but be safe
		s = "0"
	}
	return s
}
