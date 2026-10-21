package book

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// maxBindAttempts bounds bootstrap retries so a venue that keeps serving a crossed
// snapshot fails loudly instead of wedging the writer (ADR-0004).
const maxBindAttempts = 8

// maxSnapshotRetries is how many times Bootstrap retries a Snapshot call that failed with
// a transient error (source.IsTransient) before it gives up and returns that error. Only
// consecutive failures count; a snapshot that arrives resets the count. It is separate
// from maxBindAttempts, which counts snapshots that arrived but were rejected.
const maxSnapshotRetries = 5

const (
	snapshotBackoffMin = 250 * time.Millisecond
	snapshotBackoffMax = 4 * time.Second
)

// defaultSnapshotBackoff doubles from snapshotBackoffMin up to snapshotBackoffMax. The app
// replaces it with a jittered one so that several instances do not retry in step.
func defaultSnapshotBackoff(attempt int) time.Duration {
	d := snapshotBackoffMin
	for i := 0; i < attempt && d < snapshotBackoffMax; i++ {
		d *= 2
	}
	return min(d, snapshotBackoffMax)
}

// sleepCtx waits for d or until ctx is done, whichever comes first. A delay of zero or less
// returns at once.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

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
	// Gen counts publishes: the first view is 1, every later one is one higher, and a
	// resync does not reset it. LastSeq can repeat or go back across a resync; Gen cannot.
	Gen     uint64
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
	resyncs            atomic.Int64        // metrics; read lock-free from any goroutine (ADR-0008)
	gaps               atomic.Int64        // sequence gaps detected
	disconnects        atomic.Int64        // disconnect events seen
	checksumMismatches atomic.Int64        // checksum drifts detected (checksum venues)
	wouldCrosses       atomic.Int64        // deltas rejected because applying them would cross the book (ADR-0004)
	consecutiveResyncs int                 // writer-goroutine only; reset on progress to bound a livelock
	gen                uint64              // writer-goroutine only; copied into each published View
	resyncing          atomic.Bool         // true from the start of a resync until its new view is published
	checksum           Checksummer         // nil on sequence venues; set via WithChecksum for checksum venues
	maxDepth           int                 // 0 = keep every level; >0 = the feed's subscribed window (WithMaxDepth)
	latencyFn          func(time.Duration) // nil = no latency observation (WithLatencyObserver)

	snapshotErrors atomic.Int64                               // Snapshot calls that failed, other than by the caller's cancellation
	retryMax       int                                        // retries of a transient Snapshot failure (WithSnapshotRetry)
	retryBackoff   func(attempt int) time.Duration            // delay before retry number attempt+1
	retrySleep     func(context.Context, time.Duration) error // waits out a retry delay; ends early on ctx
}

// ErrConfig marks an Engine configuration New refuses.
var ErrConfig = errors.New("engine: invalid configuration")

// Option configures an Engine at construction. Options apply in order, so a later one
// overrides an earlier one.
type Option func(*Engine)

// New creates an Engine publishing a top-N view of the given depth. It refuses a
// configuration that could only fail later: a nil source, a depth below 1, a negative
// feed window, and a checksum venue whose window is narrower than the checksum's own
// depth (it would checksum fewer levels than the venue expects and resync for ever).
func New(src source.Source, depth int, opts ...Option) (*Engine, error) {
	e := &Engine{
		src: src, depth: depth,
		retryMax: maxSnapshotRetries, retryBackoff: defaultSnapshotBackoff, retrySleep: sleepCtx,
	}
	for _, opt := range opts {
		opt(e)
	}
	switch {
	case src == nil:
		return nil, fmt.Errorf("%w: nil source", ErrConfig)
	case depth < 1:
		return nil, fmt.Errorf("%w: depth %d, want 1 or more", ErrConfig, depth)
	case e.maxDepth < 0:
		return nil, fmt.Errorf("%w: max depth %d, want 0 or more", ErrConfig, e.maxDepth)
	case e.checksum != nil && e.maxDepth > 0 && e.maxDepth < checksumDepth:
		return nil, fmt.Errorf("%w: max depth %d is narrower than the checksum depth %d", ErrConfig, e.maxDepth, checksumDepth)
	}
	return e, nil
}

// WithSnapshotBackoff replaces the delay between snapshot retries and keeps the retry
// count and the sleep. The app uses it to add jitter. A nil backoff keeps the default.
func WithSnapshotBackoff(backoff func(attempt int) time.Duration) Option {
	return func(e *Engine) {
		if backoff != nil {
			e.retryBackoff = backoff
		}
	}
}

// WithSnapshotRetry sets how Bootstrap retries a Snapshot call that failed with a transient
// error: at most retries retries (0 or less disables retrying), waiting backoff(attempt) before retry
// attempt+1 through sleep. A nil backoff or sleep keeps the default. Tests inject a sleep
// that records the delays instead of waiting.
func WithSnapshotRetry(retries int, backoff func(attempt int) time.Duration, sleep func(context.Context, time.Duration) error) Option {
	return func(e *Engine) {
		e.retryMax = retries
		if backoff != nil {
			e.retryBackoff = backoff
		}
		if sleep != nil {
			e.retrySleep = sleep
		}
	}
}

// WithChecksum makes the engine verify each snapshot and applied delta against the
// venue's checksum (for checksum venues like Kraken, ADR-0007); a mismatch is drift and
// triggers a resync.
func WithChecksum(fn Checksummer) Option {
	return func(e *Engine) {
		e.checksum = fn
	}
}

// WithMaxDepth bounds the book to the feed's subscribed window (e.g. Kraken's book
// depth). Such feeds never send deletes for levels that fall out of the window, so a
// level kept beyond it goes stale and corrupts the top-N when it re-enters; the engine
// truncates after every bind and apply instead. Zero (the default) keeps every level —
// correct for full-book feeds like Binance and the synthetic source.
func WithMaxDepth(n int) Option {
	return func(e *Engine) {
		e.maxDepth = n
	}
}

// WithLatencyObserver reports, for every applied-and-published delta, the span from
// the adapter taking the raw frame off its frame channel (Event.Received) to the view
// stored for readers — the internal latency ARCHITECTURE §9 defines, with no
// wire-wait inside it. Events without a Received stamp are not reported.
func WithLatencyObserver(fn func(time.Duration)) Option {
	return func(e *Engine) {
		e.latencyFn = fn
	}
}

// View returns the latest published view, or nil before the first bootstrap. Safe to
// call from any goroutine.
func (e *Engine) View() *View { return e.view.Load() }

// Resyncs returns how many times the engine has resynced (a metric). This counts every
// resync (gap, would-cross delta, checksum drift, disconnect) plus every snapshot Bootstrap rejects
// (crossed or checksum-mismatched), including at cold start, so a string of bind-time rejections
// reads the same as the book having gone live and drifted.
func (e *Engine) Resyncs() int { return int(e.resyncs.Load()) }

// Ready reports whether the book is usable now: false before the first bootstrap and
// while a resync the engine started is in progress. It reads the resync flag before the
// view; a caller that races a resync start may still see true, which is the state just
// before it.
func (e *Engine) Ready() bool { return !e.resyncing.Load() && e.view.Load() != nil }

// Gaps returns how many sequence gaps the engine has detected (a metric).
func (e *Engine) Gaps() int { return int(e.gaps.Load()) }

// Disconnects returns how many disconnect events the engine has seen (a metric).
func (e *Engine) Disconnects() int { return int(e.disconnects.Load()) }

// ChecksumMismatches returns how many checksum drifts the engine has detected (a metric).
func (e *Engine) ChecksumMismatches() int { return int(e.checksumMismatches.Load()) }

// SnapshotErrors returns how many Snapshot calls failed, transient or not. A cancellation
// by the caller is not a failure and is not counted (a metric).
func (e *Engine) SnapshotErrors() int { return int(e.snapshotErrors.Load()) }

// WouldCrosses returns how many deltas the engine rejected because applying them would
// have crossed the book — the never-crosses invariant firing on a bad update (ADR-0004).
func (e *Engine) WouldCrosses() int { return int(e.wouldCrosses.Load()) }

// publish stores a freshly allocated View on every call — never a reused one. Tests
// rely on pointer identity to prove "no publish happened"; reusing the allocation
// would silently void them.
func (e *Engine) publish() {
	bids, asks := e.book.topN(e.depth)
	e.gen++
	e.view.Store(&View{Gen: e.gen, LastSeq: e.lastSeq, Bids: bids, Asks: asks})
}

// Bootstrap binds the book from a fresh snapshot, retrying a bounded number of times
// if the snapshot is crossed; it surfaces a loud error rather than wedging.
func (e *Engine) Bootstrap(ctx context.Context) error {
	lastReason := market.ErrCrossed // why the most recent attempt was rejected
	for range maxBindAttempts {
		snap, err := e.snapshotWithRetry(ctx)
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
		e.resyncing.Store(false) // after publish: cleared first, Ready would be true over the old view
		return nil
	}
	return fmt.Errorf("bootstrap failed after %d unusable snapshots: %w", maxBindAttempts, lastReason)
}

// snapshotWithRetry fetches a snapshot, retrying while the failure is transient. The caller's
// cancellation ends it at once, also while waiting out a delay. A failure that is not
// transient is returned immediately, exactly as before retrying existed.
func (e *Engine) snapshotWithRetry(ctx context.Context) (market.Snapshot, error) {
	for attempt := 0; ; attempt++ {
		snap, err := e.src.Snapshot(ctx)
		if err == nil {
			return snap, nil
		}
		if ctx.Err() != nil { // shutting down: neither a snapshot failure nor worth a retry
			return market.Snapshot{}, err
		}
		e.snapshotErrors.Add(1)
		if !source.IsTransient(err) || attempt >= e.retryMax {
			return market.Snapshot{}, err
		}
		if serr := e.retrySleep(ctx, e.retryBackoff(attempt)); serr != nil {
			return market.Snapshot{}, serr
		}
	}
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
		// trades are not part of the book; the engine ignores them
	}
	return true, nil
}

func (e *Engine) resync(ctx context.Context) error {
	e.resyncing.Store(true)
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
