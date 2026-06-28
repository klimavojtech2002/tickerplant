// Package market is the canonical, venue-neutral order-book and trade model.
//
// Every venue's wire format is translated into these types at the adapter edge
// (slice 0004) so the engine and everything downstream speak one vocabulary.
// Price and size are integers in the venue's smallest increment — never floating
// point — so level identity, deletes, the never-crosses comparison, and checksums
// are all exact (ADR-0003, docs/correctness.md §1–§2).
package market

import "time"

// Sequence is a per-venue update id used to order deltas and detect gaps.
type Sequence uint64

// Venue identifies one exchange feed; Symbol an instrument on it.
type (
	Venue  string
	Symbol string
)

// Price and Size are integers in the venue's smallest increment (ticks, lot units),
// parsed from the wire at a per-venue/symbol decimal scale.
//
// Invariant: a Price or Size is meaningful only at its scale; two may be compared or
// combined only at the same scale. A book holds one scale per symbol, so within a book
// all prices share a scale and all sizes share a scale — that is what makes the bare
// integer comparisons on the book path safe.
type (
	Price int64
	Size  int64
)

// Side is the side of the book a level or trade belongs to.
type Side uint8

const (
	Unknown Side = iota // zero value: an unset side, never a valid book side
	Bid                 // buy orders, sorted high to low; best bid is the highest
	Ask                 // sell orders, sorted low to high; best ask is the lowest
)

func (s Side) String() string {
	switch s {
	case Bid:
		return "bid"
	case Ask:
		return "ask"
	default:
		return "unknown"
	}
}

// Level is one aggregated price level: the total size resting at a price.
type Level struct {
	Price Price
	Size  Size
}

// IsDelete reports whether this level, seen in a Delta, removes the price level.
// A delta carries the new absolute size at a level; size zero means remove it.
func (l Level) IsDelete() bool { return l.Size == 0 }

// Snapshot is a full book at a point in time, current as of LastUpdateID.
//
// Snapshot and Delta are raw carriers: they hold levels exactly as the venue sent
// them. If a venue repeats a price on one side, resolution is last-writer-wins, and
// it is applied where the book is built (slice 0003), not here — the model does not
// dedup. Keeping the model a carrier keeps it pure and the dedup in one place.
//
// Treat a constructed value (and its Bids/Asks slices) as immutable: a published
// book view is read by many goroutines without a lock (ADR-0008), so once a value is
// handed on it must not be mutated, and its slices must not be aliased and changed.
type Snapshot struct {
	Venue        Venue
	Symbol       Symbol
	LastUpdateID Sequence
	Bids         []Level
	Asks         []Level
	// Checksum is the venue's integrity checksum over the book, where it publishes one
	// (Kraken, ADR-0007); zero on venues that order by sequence instead. The engine
	// verifies it only when a checksum function is configured.
	Checksum uint32
}

// Delta is an incremental book update covering sequence ids [FirstSeq, LastSeq].
// Each level carries the new absolute size at its price; size zero deletes it.
// Like Snapshot, treat a constructed Delta and its slices as immutable once handed on.
type Delta struct {
	Venue    Venue
	Symbol   Symbol
	FirstSeq Sequence
	LastSeq  Sequence
	Bids     []Level
	Asks     []Level
	// Checksum is the venue's integrity checksum over the book after this update, where
	// it publishes one (Kraken, ADR-0007); zero on sequence-ordered venues. The engine
	// verifies it only when a checksum function is configured.
	Checksum uint32
}

// Trade is an executed fill. Side is the aggressor (taker) side — the side that
// crossed the spread — normalized from each venue's own convention.
type Trade struct {
	Venue  Venue
	Symbol Symbol
	Price  Price
	Size   Size
	Side   Side
	Time   time.Time
	ID     string
}

// ParsePrice parses a wire decimal string into a Price at the given scale.
func ParsePrice(s string, scale int) (Price, error) {
	v, err := ParseScaled(s, scale)
	return Price(v), err
}

// ParseSize parses a wire decimal string into a Size at the given scale.
func ParseSize(s string, scale int) (Size, error) {
	v, err := ParseScaled(s, scale)
	return Size(v), err
}

// Format renders the price back to its wire decimal string at the given scale.
func (p Price) Format(scale int) string { return FormatScaled(int64(p), scale) }

// Format renders the size back to its wire decimal string at the given scale.
func (s Size) Format(scale int) string { return FormatScaled(int64(s), scale) }
