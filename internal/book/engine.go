package book

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// maxBindAttempts bounds bootstrap retries so a venue that keeps serving a crossed
// snapshot fails loudly instead of wedging the writer (ADR-0004).
const maxBindAttempts = 8

// maxConsecutiveResyncs bounds resyncs that make no progress (e.g. a snapshot that
// stays behind the stream), so a pathological feed fails loudly rather than livelocking.
const maxConsecutiveResyncs = 16

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
	resyncs            atomic.Int64 // a metric; read lock-free from any goroutine (ADR-0008)
	consecutiveResyncs int          // writer-goroutine only; reset on progress to bound a livelock
}

// New creates an Engine publishing a top-N view of the given depth.
func New(src source.Source, depth int) *Engine {
	return &Engine{src: src, depth: depth}
}

// View returns the latest published view, or nil before the first bootstrap. Safe to
// call from any goroutine.
func (e *Engine) View() *View { return e.view.Load() }

// Resyncs returns how many times the engine has resynced (a metric, slice 0006).
func (e *Engine) Resyncs() int { return int(e.resyncs.Load()) }

func (e *Engine) publish() {
	bids, asks := e.book.topN(e.depth)
	e.view.Store(&View{LastSeq: e.lastSeq, Bids: bids, Asks: asks})
}

// Bootstrap binds the book from a fresh snapshot, retrying a bounded number of times
// if the snapshot is crossed; it surfaces a loud error rather than wedging.
func (e *Engine) Bootstrap(ctx context.Context) error {
	for range maxBindAttempts {
		snap, err := e.src.Snapshot(ctx)
		if err != nil {
			return err
		}
		if crossesSnapshot(snap) {
			e.resyncs.Add(1)
			continue
		}
		e.book = buildFrom(snap)
		e.lastSeq = snap.LastUpdateID
		e.publish()
		return nil
	}
	return fmt.Errorf("bootstrap failed after %d crossed snapshots: %w", maxBindAttempts, market.ErrCrossed)
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
		return needResync // a real forward gap; the missing deltas are unknown
	}
	for _, lvl := range d.Bids {
		e.book.set(market.Bid, lvl.Price, lvl.Size)
	}
	for _, lvl := range d.Asks {
		e.book.set(market.Ask, lvl.Price, lvl.Size)
	}
	e.lastSeq = d.LastSeq
	if e.book.crosses() {
		return needResync // never serve a crossed book
	}
	return applied
}

// Step processes one source event. It returns false when the source is exhausted or
// the context is cancelled. A gap, would-cross, or disconnect triggers a resync.
func (e *Engine) Step(ctx context.Context) (bool, error) {
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
		case dropped:
			// stale/duplicate: ignore
		case needResync:
			if err := e.resync(ctx); err != nil {
				return false, err
			}
		}
	case source.EventDisconnected:
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
