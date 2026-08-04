// Package binance adapts the Binance spot diff-depth WebSocket stream and REST depth
// snapshot to the canonical transport port (internal/source.Source).
//
// Binance's documented maintenance procedure — buffer the stream, take a REST
// snapshot, drop events whose final id u <= the snapshot's lastUpdateId, bind from the
// event straddling lastUpdateId+1, then accept each event so long as it leaves no gap —
// is the engine's generic stale/gap logic (slice 0003). A depthUpdate maps to a
// canonical Delta with FirstSeq=U and LastSeq=u, so the engine drops stale events
// (LastSeq <= last applied) and detects gaps (FirstSeq > last applied + 1) itself; this
// adapter only normalizes and transports. (Binance documents the stricter
// U == prev.u+1; the engine's no-gap form also tolerates a harmless overlap, correct
// because a delta carries absolute sizes, so re-applying an overlapped range yields the
// same book.) Verified against the Binance spot docs (developers.binance.com,
// 2026-06-27): stream <symbol>@depth, event fields e/E/s/U/u/b/a, REST /api/v3/depth
// returning lastUpdateId/bids/asks.
package binance

import (
	"fmt"

	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// depthUpdate is the WS diff-depth event. Each level in b/a is a [price, quantity]
// pair of decimal strings; quantity 0 deletes the level. EventTime is otherwise
// unused, but its field must exist: encoding/json falls back to a case-insensitive
// match when a JSON key has no exact-tag match, so without a field claiming "E" the
// wire's "E" (event time, a number) collides onto Event (tag "e", a string) and every
// real frame fails to unmarshal — never reproduced by a struct literal or a
// hand-built test frame missing "E", only by the venue's actual wire format.
type depthUpdate struct {
	Event     string     `json:"e"`
	EventTime int64      `json:"E"`
	First     uint64     `json:"U"`
	Final     uint64     `json:"u"`
	Bids      [][]string `json:"b"`
	Asks      [][]string `json:"a"`
}

// restDepth is the REST /api/v3/depth snapshot.
type restDepth struct {
	LastUpdateID uint64     `json:"lastUpdateId"`
	Bids         [][]string `json:"bids"`
	Asks         [][]string `json:"asks"`
}

// scales are the per-symbol decimal scales: price in ticks, size in lot units.
type scales struct{ price, size int }

// levels normalizes a venue [price,qty] array into canonical levels, integer-exact
// (no float, ADR-0003). A short or malformed level is a loud error, never skipped.
func levels(raw [][]string, sc scales) ([]market.Level, error) {
	out := make([]market.Level, len(raw))
	for i, pq := range raw {
		if len(pq) < 2 {
			return nil, fmt.Errorf("level %d: want [price,qty], got %v: %w", i, pq, market.ErrMalformed)
		}
		p, err := market.ParsePrice(pq[0], sc.price)
		if err != nil {
			return nil, fmt.Errorf("level %d price: %w", i, err)
		}
		s, err := market.ParseSize(pq[1], sc.size)
		if err != nil {
			return nil, fmt.Errorf("level %d size: %w", i, err)
		}
		out[i] = market.Level{Price: p, Size: s}
	}
	return out, nil
}

// toDelta normalizes one depthUpdate into a canonical Delta. FirstSeq=U, LastSeq=u
// carry Binance's sequence semantics into the engine's gap/stale checks.
func toDelta(venue market.Venue, symbol market.Symbol, sc scales, u depthUpdate) (market.Delta, error) {
	bids, err := levels(u.Bids, sc)
	if err != nil {
		return market.Delta{}, fmt.Errorf("bids: %w", err)
	}
	asks, err := levels(u.Asks, sc)
	if err != nil {
		return market.Delta{}, fmt.Errorf("asks: %w", err)
	}
	return market.Delta{
		Venue:    venue,
		Symbol:   symbol,
		FirstSeq: market.Sequence(u.First),
		LastSeq:  market.Sequence(u.Final),
		Bids:     bids,
		Asks:     asks,
	}, nil
}

// toSnapshot normalizes a REST depth response into a canonical Snapshot, current as of
// lastUpdateId.
func toSnapshot(venue market.Venue, symbol market.Symbol, sc scales, d restDepth) (market.Snapshot, error) {
	bids, err := levels(d.Bids, sc)
	if err != nil {
		return market.Snapshot{}, fmt.Errorf("snapshot bids: %w", err)
	}
	asks, err := levels(d.Asks, sc)
	if err != nil {
		return market.Snapshot{}, fmt.Errorf("snapshot asks: %w", err)
	}
	return market.Snapshot{
		Venue:        venue,
		Symbol:       symbol,
		LastUpdateID: market.Sequence(d.LastUpdateID),
		Bids:         bids,
		Asks:         asks,
	}, nil
}
