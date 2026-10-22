package book

import "github.com/klimavojtech2002/tickerplant/internal/market"

// EventKind names a fault event the engine recorded. Fault events are the engine's own
// log of what went wrong; they are unrelated to the source events of the same name in
// package source.
type EventKind string

const (
	KindGap            EventKind = "gap"             // a delta skipped sequence numbers
	KindDisconnect     EventKind = "disconnect"      // the source reported a lost connection
	KindCross          EventKind = "cross"           // a delta would have crossed the book
	KindChecksum       EventKind = "checksum"        // the book no longer matched the venue's checksum after a delta
	KindSnapshotReject EventKind = "snapshot-reject" // a snapshot was refused: crossed, or its checksum did not match
	KindSnapshotError  EventKind = "snapshot-error"  // a Snapshot call failed
)

// Fixed reasons. Events never carry raw error text: the ring is meant to be shown to
// anyone watching the feed, and error text can hold addresses and venue-controlled
// strings. The detail stays in the error returned to the caller.
const (
	reasonGap               = "sequence gap"
	reasonCross             = "delta would cross the book"
	reasonChecksum          = "delta checksum mismatch"
	reasonDisconnect        = "source disconnected"
	reasonSnapshotCrossed   = "snapshot crossed"
	reasonSnapshotChecksum  = "snapshot checksum mismatch"
	reasonSnapshotTransient = "transient snapshot failure"
	reasonSnapshotFailed    = "snapshot failed"
)

// unknownDuration marks a recovery that has not reported yet, or never will (the
// resync gave up before it started a bootstrap).
const unknownDuration = -1

// eventCap is how many fault events the engine keeps.
const eventCap = 16

// Event is one recorded fault. IDs start at 1, rise by one per event and are never
// reset, so a reader that sees a jump knows how many events it missed.
//
// ExpectedSeq and GotSeq are 0 when they do not apply to the kind; a reader must not
// show 0 as a sequence number. On the Kraken adapter GotSeq of a checksum event is the
// adapter's own running count, not a number from the venue.
//
// DurationMs is -1 until the resync that followed the fault returns, then the whole
// time that resync's Bootstrap took in milliseconds, retry delays included (0 means
// under a millisecond). It stays -1 if the resync gave up before bootstrapping.
// Snapshot events are instants and carry 0. A later copy of an event with the same ID
// replaces an earlier one: only DurationMs can differ.
type Event struct {
	ID          uint64
	Kind        EventKind
	AtUnixMs    int64
	ExpectedSeq market.Sequence
	GotSeq      market.Sequence
	Reason      string
	DurationMs  int64
}

// noEvents is what Events returns before the first fault. It is shared and empty, so
// handing it out is safe.
var noEvents = []Event{}

// Events returns the engine's last fault events, oldest first. The slice is immutable:
// it is replaced, never edited, so a caller may keep it and must not modify it. Safe
// to call from any goroutine.
func (e *Engine) Events() []Event {
	if p := e.events.Load(); p != nil {
		return *p
	}
	return noEvents
}

// recordEvent appends a fault event and returns its ID. Writer goroutine only.
func (e *Engine) recordEvent(kind EventKind, expected, got market.Sequence, reason string, durationMs int64) uint64 {
	e.eventSeq++
	ev := Event{
		ID: e.eventSeq, Kind: kind, AtUnixMs: e.now().UnixMilli(),
		ExpectedSeq: expected, GotSeq: got, Reason: reason, DurationMs: durationMs,
	}
	cur := e.Events()
	keep := cur
	if len(keep) >= eventCap {
		keep = keep[len(keep)-eventCap+1:]
	}
	next := make([]Event, 0, len(keep)+1)
	next = append(next, keep...)
	next = append(next, ev)
	e.events.Store(&next)
	return ev.ID
}

// setEventDuration replaces the duration of the event with the given ID. It looks the
// event up by ID because one bootstrap can record enough events to push its trigger out
// of the ring; when the ID is gone it does nothing and no other event changes.
func (e *Engine) setEventDuration(id uint64, ms int64) {
	cur := e.Events()
	for i := range cur {
		if cur[i].ID != id {
			continue
		}
		next := make([]Event, len(cur))
		copy(next, cur)
		next[i].DurationMs = ms
		e.events.Store(&next)
		return
	}
}
