// Package kraken adapts the Kraken WebSocket v2 book channel to the canonical transport
// port. Kraken has no per-update sequence id; integrity is a CRC32 checksum over the top
// 10 levels (ADR-0007), so this package computes that checksum in Kraken's exact format
// for the engine to verify (the engine owns when/what to check, this owns the format —
// ADR-0007). Verified against the Kraken v2 book and checksum guide
// (docs.kraken.com/api/docs/guides/spot-ws-book-v2, 2026-06-28; re-verified 2026-07-05).
package kraken

import (
	"hash/crc32"
	"strconv"
	"strings"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// Checksum computes Kraken's v2 book checksum over the engine's top-10 view: for the
// top 10 asks (low to high) then the top 10 bids (high to low), each level contributes
// its price then its quantity as the venue's decimal string with the point and leading
// zeros removed; the concatenation is CRC32'd (IEEE) to an unsigned 32-bit value. The
// engine passes its top-10 (bids high-to-low, asks low-to-high), which already matches
// this order. Ready for book.Engine.WithChecksum as-is.
func Checksum(bids, asks []market.Level) uint32 {
	var b strings.Builder
	for _, l := range asks {
		b.WriteString(crcField(int64(l.Price)))
		b.WriteString(crcField(int64(l.Size)))
	}
	for _, l := range bids {
		b.WriteString(crcField(int64(l.Price)))
		b.WriteString(crcField(int64(l.Size)))
	}
	return crc32.ChecksumIEEE([]byte(b.String()))
}

// crcField renders one scaled value to Kraken's checksum form. Removing the decimal
// point and the leading zeros from the wire string leaves exactly the decimal digits
// of the scaled integer — "45285.2" (scale 1) and "0.00452852" (scale 8) both reduce
// to "452852" — so the field is the integer's digits and needs no scale at all: the
// venue's trailing zeros are already inside the scaled value.
func crcField(v int64) string {
	if v == 0 {
		return "0" // a zero strips to empty; Kraken book levels are never zero, but be safe
	}
	return strconv.FormatInt(v, 10)
}
