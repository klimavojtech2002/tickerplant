package kraken

import (
	"encoding/json"
	"errors"
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

// errRejectedFrame marks a frame that was meant to be book data and cannot be used:
// it does not decode, it is on the book channel with an unknown type or no data, or its
// levels do not parse. The adapter counts these; frames that are simply not book data
// are skipped without an error.
var errRejectedFrame = errors.New("kraken: frame rejected")

// parseBook normalizes one raw Kraken frame. ok is false for frames the caller skips,
// and err says which of them were rejected rather than just not book data:
//   - ok false, err nil: subscription acks, heartbeats, status and other channels.
//   - err wrapping errRejectedFrame: a frame that does not decode, a book frame with an
//     unknown type or no data (these return an empty result, so the caller never takes
//     them for a snapshot), and a book frame whose levels do not parse (the result keeps
//     isSnapshot, so a malformed in-band snapshot still fails loudly in Snapshot, and the
//     error also wraps market.ErrMalformed).
//
// The sequence is synthetic, so a skipped update cannot leave a gap; the checksum on the
// next applied delta catches the divergence. The subscription is book-only, so a frame
// of another shape that fails to decode is not expected and is counted as a reject.
func parseBook(raw []byte, sc scales) (p parsed, ok bool, err error) {
	var m bookMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return parsed{}, false, fmt.Errorf("%w: not decodable", errRejectedFrame)
	}
	if m.Channel != "book" {
		return parsed{}, false, nil // ack/heartbeat/status/other channel: skip
	}
	if len(m.Data) == 0 || (m.Type != "snapshot" && m.Type != "update") {
		return parsed{}, false, fmt.Errorf("%w: book frame of type %q with %d data items", errRejectedFrame, m.Type, len(m.Data))
	}
	d := m.Data[0]
	isSnap := m.Type == "snapshot"
	bids, err := toLevels(d.Bids, sc)
	if err != nil {
		// isSnapshot survives the error so Snapshot can fail loud on a malformed
		// in-band snapshot instead of skipping it and waiting forever.
		return parsed{isSnapshot: isSnap}, false, fmt.Errorf("%w: kraken bids: %w", errRejectedFrame, err)
	}
	asks, err := toLevels(d.Asks, sc)
	if err != nil {
		return parsed{isSnapshot: isSnap}, false, fmt.Errorf("%w: kraken asks: %w", errRejectedFrame, err)
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
