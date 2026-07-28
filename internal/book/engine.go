package book

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// maxBindAttempts bounds bootstrap retries so a venue that keeps serving a crossed
// snapshot fails loudly instead of wedging the writer (ADR-0004).
const maxBindAttempts = 8

// maxConsecutiveResyncs bounds resyncs that make no progress (e.g. a snapshot that
// stays behind the stream), so a pathological feed fails loudly rather than livelocking.
const maxConsecutiveResyncs = 16

// checksumDepth is how many levels per side a checksum covers (Kraken uses the top 10).
const checksumDepth = 10

// Checksummer renders the top of book to a venue's integrity checksum (e.g. Kraken's
// CRC32, ADR-0007). The engine compares it against the value the venue published, to
// catch drift the never-crosses check cannot — a level wrong deep in the book leaves
// best bid below best ask yet no longer matches the venue (correctness.md §6).
type Checksummer func(bids, asks []market.Level) uint32

// View is an immutable top-N snapshot of the book, published atomically for readers.
type View struct {
	LastSeq market.Sequence
	Bids    []market.Level
	Asks    []market.Level
}

// Crosses reports whether the published view is crossed. It must never be true.
func (v *View) Crosses() bool {
	if v == nil || len(v.Bids) == 0 || len(v.Asks) == 0 {
		return false
	}
	return v.Bids[0].Price >= v.Asks[0].Price
}

// Engine reconstructs one symbol's L2 book from a Source. It is single-writer: only
// the goroutine calling Bootstrap/Step/Run mutates the book, and readers see a
// consistent state through an atomically published top-N View, lock-free (ADR-0008).
type Engine struct {
	src                source.Source
	depth              int
	book               *book
	lastSeq            market.Sequence
	view               atomic.Pointer[View]
	resyncs            atomic.Int64        // metrics; read lock-free from any goroutine (ADR-0008, slice 0006)
	gaps               atomic.Int64        // sequence gaps detected
	disconnects        atomic.Int64        // disconnect events seen
	checksumMismatches atomic.Int64        // checksum drifts detected (checksum venues)
	wouldCrosses       atomic.Int64        // deltas rejected because applying them would cross the book (ADR-0004)
	consecutiveResyncs int                 // writer-goroutine only; reset on progress to bound a livelock
	checksum           Checksummer         // nil on sequence venues; set via WithChecksum for checksum venues
	maxDepth           int                 // 0 = keep every level; >0 = the feed's subscribed window (WithMaxDepth)
	latencyFn          func(time.Duration) // nil = no latency observation (WithLatencyObserver)
}

// New creates an Engine publishing a top-N view of the given depth.
func New(src source.Source, depth int) *Engine {
	return &Engine{src: src, depth: depth}
}

// WithChecksum makes the engine verify each snapshot and applied delta against the
// venue's checksum (for checksum venues like Kraken, ADR-0007); a mismatch is drift and
// triggers a resync. Call before Bootstrap/Run. Returns the engine for chaining.
func (e *Engine) WithChecksum(fn Checksummer) *Engine {
	e.checksum = fn
	return e
}

// WithMaxDepth bounds the book to the feed's subscribed window (e.g. Kraken's book
// depth). Such feeds never send deletes for levels that fall out of the window, so a
// level kept beyond it goes stale and corrupts the top-N when it re-enters; the engine
// truncates after every bind and apply instead. Zero (the default) keeps every level —
// correct for full-book feeds like Binance and the synthetic source. Call before
// Bootstrap/Run. Returns the engine for chaining.
func (e *Engine) WithMaxDepth(n int) *Engine {
	e.maxDepth = n
	return e
}

// WithLatencyObserver reports, for every applied-and-published delta, the span from
// the adapter taking the raw message off its transport (Event.Received) to the view
// leaving for the fan-out — the internal latency ARCHITECTURE §9 defines, with no
// wire-wait inside it. Events without a Received stamp are not reported. Call before
// Bootstrap/Run. Returns the engine for chaining.
func (e *Engine) WithLatencyObserver(fn func(time.Duration)) *Engine {
	e.latencyFn = fn
	return e
}

// View returns the latest published view, or nil before the first bootstrap. Safe to
// call from any goroutine.
func (e *Engine) View() *View { return e.view.Load() }

// Resyncs returns how many times the engine has resynced (a metric, slice 0006). This
// counts cold-start bootstrap-retry attempts (an unusable first snapshot) together
// with genuine post-bind resyncs — both are Bootstrap calls that discarded a rejected
// snapshot — so a string of bind-time rejections reads the same as the book having
// gone live and drifted.
func (e *Engine) Resyncs() int { return int(e.resyncs.Load()) }

// Gaps returns how many sequence gaps the engine has detected (a metric).
func (e *Engine) Gaps() int { return int(e.gaps.Load()) }

// Disconnects returns how many disconnect events the engine has seen (a metric).
func (e *Engine) Disconnects() int { return int(e.disconnects.Load()) }

// ChecksumMismatches returns how many checksum drifts the engine has detected (a metric).
func (e *Engine) ChecksumMismatches() int { return int(e.checksumMismatches.Load()) }

// WouldCrosses returns how many deltas the engine rejected because applying them would
// have crossed the book — the never-crosses invariant firing on a bad update (ADR-0004).
func (e *Engine) WouldCrosses() int { return int(e.wouldCrosses.Load()) }

// publish stores a freshly allocated View on every call — never a reused one. Tests
// rely on pointer identity to prove "no publish happened"; reusing the allocation
// would silently void them.
func (e *Engine) publish() {
	bids, asks := e.book.topN(e.depth)
	e.view.Store(&View{LastSeq: e.lastSeq, Bids: bids, Asks: asks})
}

// Bootstrap binds the book from a fresh snapshot, retrying a bounded number of times
// if the snapshot is crossed; it surfaces a loud error rather than wedging.
func (e *Engine) Bootstrap(ctx context.Context) error {
	if e.checksum != nil && e.maxDepth > 0 && e.maxDepth < checksumDepth {
		return fmt.Errorf("engine: maxDepth %d narrower than checksum depth %d: misconfigured", e.maxDepth, checksumDepth)
	}
	lastReason := market.ErrCrossed // why the most recent attempt was rejected
	for range maxBindAttempts {
		snap, err := e.src.Snapshot(ctx)
		if err != nil {
			return err
		}
		if crossesSnapshot(snap) {
			e.resyncs.Add(1)
			lastReason = market.ErrCrossed
			continue
		}
		e.book = buildFrom(snap)
		if e.maxDepth > 0 {
			e.book.truncate(e.maxDepth)
		}
		e.lastSeq = snap.LastUpdateID
		if e.checksum != nil {
			bids, asks := e.book.topN(checksumDepth)
			if e.checksum(bids, asks) != snap.Checksum {
				e.checksumMismatches.Add(1)
				e.resyncs.Add(1)
				lastReason = market.ErrChecksumMismatch
				continue // a snapshot we cannot reproduce: refetch a fresh one
			}
		}
		e.publish()
		return nil
	}
	return fmt.Errorf("bootstrap failed after %d unusable snapshots: %w", maxBindAttempts, lastReason)
}

type outcome uint8

const (
	applied    outcome = iota // delta applied, view publishable
	dropped                   // stale/duplicate, ignored
	needResync                // gap or would-cross: discard and re-bootstrap
)

// applyDelta is the single apply routine. Three-way so stale events don't over-resync
// and neither the continuity nor the never-crosses check can be skipped.
func (e *Engine) applyDelta(d market.Delta) outcome {
	if d.LastSeq <= e.lastSeq {
		return dropped // already applied (stale or duplicate) — idempotent
	}
	if d.FirstSeq > e.lastSeq+1 {
		e.gaps.Add(1)
		return needResync // a real forward gap; the missing deltas are unknown
	}
	for _, lvl := range d.Bids {
		e.book.set(market.Bid, lvl.Price, lvl.Size)
	}
	for _, lvl := range d.Asks {
		e.book.set(market.Ask, lvl.Price, lvl.Size)
	}
	if e.maxDepth > 0 {
		e.book.truncate(e.maxDepth) // drop what fell out of the feed's window before any check sees it
	}
	e.lastSeq = d.LastSeq
	if e.book.crosses() {
		e.wouldCrosses.Add(1)
		return needResync // never serve a crossed book
	}
	if e.checksum != nil {
		bids, asks := e.book.topN(checksumDepth)
		if e.checksum(bids, asks) != d.Checksum {
			e.checksumMismatches.Add(1)
			return needResync // checksum drift: the book no longer matches the venue
		}
	}
	return applied
}

// Step processes one source event. It returns false when the source is exhausted or
// the context is cancelled. A gap, would-cross, or disconnect triggers a resync.
func (e *Engine) Step(ctx context.Context) (bool, error) {
	if e.book == nil {
		return false, fmt.Errorf("book: Step called before Bootstrap: %w", market.ErrNotBootstrapped)
	}
	ev, ok := e.src.Next(ctx)
	if !ok {
		return false, ctx.Err()
	}
	switch ev.Kind {
	case source.EventDelta:
		switch e.applyDelta(ev.Delta) {
		case applied:
			e.consecutiveResyncs = 0 // progress made; reset the livelock guard
			e.publish()
			if e.latencyFn != nil && !ev.Received.IsZero() {
				e.latencyFn(time.Since(ev.Received))
			}
		case dropped:
			// stale/duplicate: ignore
		case needResync:
			if err := e.resync(ctx); err != nil {
				return false, err
			}
		}
	case source.EventDisconnected:
		e.disconnects.Add(1)
		if err := e.resync(ctx); err != nil {
			return false, err
		}
	case source.EventTrade:
		// trades are not part of the book; delivery (slice 0005) forwards them
	}
	return true, nil
}

func (e *Engine) resync(ctx context.Context) error {
	e.resyncs.Add(1)
	e.consecutiveResyncs++
	if e.consecutiveResyncs > maxConsecutiveResyncs {
		return fmt.Errorf("resync livelock: %d consecutive resyncs without progress: %w",
			e.consecutiveResyncs, market.ErrStaleSnapshot)
	}
	return e.Bootstrap(ctx)
}

// Run bootstraps, then steps until the source ends or the context is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	if err := e.Bootstrap(ctx); err != nil {
		return err
	}
	for {
		ok, err := e.Step(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
}

// crossesSnapshot reports whether a snapshot is crossed, finding best bid/ask
// defensively in case a venue sends levels unsorted.
func crossesSnapshot(s market.Snapshot) bool {
	if len(s.Bids) == 0 || len(s.Asks) == 0 {
		return false
	}
	bb := s.Bids[0].Price
	for _, l := range s.Bids {
		if l.Price > bb {
			bb = l.Price
		}
	}
	ba := s.Asks[0].Price
	for _, l := range s.Asks {
		if l.Price < ba {
			ba = l.Price
		}
	}
	return bb >= ba
}
