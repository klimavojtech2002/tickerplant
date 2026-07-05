package kraken

import (
	"encoding/json"
	"fmt"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// scales are the per-symbol decimal scales for price and quantity, matching Kraken's
// display precision so a stored value renders back to the exact wire string (needed for
// the checksum, checksum.go).
type scales struct{ price, qty int }

// Kraken v2 book message. price/qty arrive as JSON numbers with the pair's full wire
// precision (e.g. "qty":0.00005100 — trailing zeros on the wire, verified against the
// live feed 2026-07-05). json.Number keeps that literal token, so parsing stays
// integer-exact with no float (ADR-0003) and the checksum can reproduce the exact wire
// digits. A book message carries one symbol's data; type is "snapshot" or "update".
type bookMessage struct {
	Channel string     `json:"channel"`
	Type    string     `json:"type"`
	Data    []bookData `json:"data"`
}

type bookData struct {
	Symbol   string      `json:"symbol"`
	Bids     []bookLevel `json:"bids"`
	Asks     []bookLevel `json:"asks"`
	Checksum uint32      `json:"checksum"`
}

type bookLevel struct {
	Price json.Number `json:"price"`
	Qty   json.Number `json:"qty"`
}

// parsed is one normalized Kraken book message: levels (integer-exact) and the venue
// checksum. Kraken has no sequence id; the source assigns synthetic monotonic ids so the
// engine's continuity check passes and the checksum does the real integrity work.
type parsed struct {
	isSnapshot bool
	bids       []market.Level
	asks       []market.Level
	checksum   uint32
}

// parseBook normalizes one raw Kraken frame. ok is false for frames the caller skips —
// subscription acks, heartbeats, status, and anything that does not decode as a book
// message (the sequence is synthetic, so a wrongly skipped update cannot gap; the
// checksum on the next applied delta catches the divergence). A book frame whose
// levels do not parse is a loud error.
func parseBook(raw []byte, sc scales) (p parsed, ok bool, err error) {
	var m bookMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return parsed{}, false, nil // not JSON we model: skip
	}
	if m.Channel != "book" || len(m.Data) == 0 || (m.Type != "snapshot" && m.Type != "update") {
		return parsed{}, false, nil // ack/heartbeat/status: skip
	}
	d := m.Data[0]
	isSnap := m.Type == "snapshot"
	bids, err := toLevels(d.Bids, sc)
	if err != nil {
		// isSnapshot survives the error so Snapshot can fail loud on a malformed
		// in-band snapshot instead of skipping it and waiting forever.
		return parsed{isSnapshot: isSnap}, false, fmt.Errorf("kraken bids: %w", err)
	}
	asks, err := toLevels(d.Asks, sc)
	if err != nil {
		return parsed{isSnapshot: isSnap}, false, fmt.Errorf("kraken asks: %w", err)
	}
	return parsed{isSnapshot: isSnap, bids: bids, asks: asks, checksum: d.Checksum}, true, nil
}

// toLevels normalizes Kraken's [{price,qty}] into canonical levels, integer-exact. A
// quantity of zero removes the level (Level.IsDelete), per the model.
func toLevels(raw []bookLevel, sc scales) ([]market.Level, error) {
	out := make([]market.Level, len(raw))
	for i, l := range raw {
		p, err := market.ParsePrice(l.Price.String(), sc.price)
		if err != nil {
			return nil, fmt.Errorf("level %d price %q: %w", i, l.Price, err)
		}
		q, err := market.ParseSize(l.Qty.String(), sc.qty)
		if err != nil {
			return nil, fmt.Errorf("level %d qty %q: %w", i, l.Qty, err)
		}
		out[i] = market.Level{Price: p, Size: q}
	}
	return out, nil
}
