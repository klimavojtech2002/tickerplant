// Package app is the composition root: it picks the source, wires source -> engine ->
// fan-out (and, with a listener, the HTTP edge), drives the run, and reports the outcome.
// cmd/tickerplant only parses flags and owns the process (signals, exit code).
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/httpapi"
	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/metrics"
	"github.com/klimavojtech2002/tickerplant/internal/source"
	"github.com/klimavojtech2002/tickerplant/internal/venue"
	"github.com/klimavojtech2002/tickerplant/internal/venue/binance"
	"github.com/klimavojtech2002/tickerplant/internal/venue/kraken"
)

// DefaultVenue/DefaultSymbol are the -venue/-symbol flag defaults, named so the
// startup warning below can detect a likely -live typo without duplicating the
// literal strings the flags themselves declare.
const (
	DefaultVenue  = "binance"
	DefaultSymbol = "BTCUSDT"
)

// snapshotBackoff spaces the engine's snapshot retries: exponential from 500 ms to 8 s with
// full jitter, so several instances recovering from one outage do not retry in step.
var snapshotBackoff = venue.Backoff{Min: 500 * time.Millisecond, Max: 8 * time.Second, Factor: 2, Jitter: venue.FullJitter}.Next

// Limits on the HTTP edge. edgeShutdownTimeout is a variable so a test can shorten it.
var edgeShutdownTimeout = 5 * time.Second

const (
	edgeReadHeaderTimeout = 10 * time.Second
	edgeIdleTimeout       = 2 * time.Minute
	edgeMaxHeaderBytes    = 64 << 10
)

// Config is everything Run needs once the source is chosen. Only HTTPListener is set by the
// caller; Prepare fills in the rest.
type Config struct {
	depth        int
	every        int
	pace         time.Duration
	checksum     book.Checksummer // nil except for checksum venues (Kraken)
	window       int              // feed's subscribed book window; 0 = full-book feed
	venue        string
	symbol       string
	scales       [2]int       // price/size decimal scales for the HTTP edge's rendering
	HTTPListener net.Listener // serve the HTTP edge on this listener; nil = edge off
}

// Options is the caller's choice of source and pipeline settings, before a source exists.
type Options struct {
	Seed     int64
	Steps    int
	Depth    int
	Every    int
	EverySet bool // -every given explicitly, even if equal to the default
	Pace     time.Duration
	Live     bool
	Venue    string
	Symbol   string
}

// Prepare builds the source for o and the pipeline Config that source implies, and logs the
// mode. The caller adds HTTPListener if it wants the HTTP edge, then calls Run.
func Prepare(ctx context.Context, log *slog.Logger, o Options) (source.Source, Config, error) {
	src, vs, err := newSource(ctx, log, o)
	if err != nil {
		if !errors.Is(err, context.Canceled) { // an interrupt during setup is a shutdown, not a failure
			log.Error("source setup failed", "err", err)
		}
		return nil, Config{}, err
	}
	cfg := Config{
		depth: o.Depth, every: LiveCadence(o.Every, o.EverySet, o.Live),
		pace: vs.pace, checksum: vs.checksum, window: vs.window,
		venue: venueName(o.Live, o.Venue), symbol: symbolName(o.Live, o.Symbol), scales: vs.scales,
	}
	log.Info("mode", "live", o.Live, "venue", cfg.venue, "symbol", cfg.symbol)
	if liveFlagsIgnored(o.Live, o.Venue, o.Symbol) {
		log.Warn("live venue/symbol flags ignored without -live", "venue", o.Venue, "symbol", o.Symbol)
	}
	return src, cfg, nil
}

// ReportOutcome logs the pipeline's result and returns the process exit code. A
// context-cancellation error (Ctrl-C or SIGTERM, wired via signal.NotifyContext)
// is a clean shutdown, not a failure — it must not read as a crash on the one
// interruption every demo hits.
func ReportOutcome(log *slog.Logger, err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, context.Canceled):
		log.Info("interrupted, shutting down")
		return 0
	default:
		log.Error("engine stopped", "err", err)
		return 1
	}
}

// liveFlagsIgnored reports whether -venue/-symbol were set to something other than
// their defaults while -live was not given, meaning the flags are silently ignored
// (newSource always builds the synthetic demo when live is false). Worth a startup
// warning: -venue kraken without -live is a very plausible typo for -live -venue
// kraken, and running the synthetic demo instead is otherwise silent.
func liveFlagsIgnored(live bool, venue, symbol string) bool {
	return !live && (venue != DefaultVenue || symbol != DefaultSymbol)
}

// venueSetup is what a source choice implies for the engine: the pacing (a live feed
// paces itself, so the artificial delay is dropped), the checksummer (non-nil only for
// checksum venues — Kraken's feed has no sequence, so running it without the checksum
// wired would mean no integrity guard), and the book window (non-zero only for feeds
// subscribed at a fixed depth, which never delete out-of-window levels).
type venueSetup struct {
	pace     time.Duration
	checksum book.Checksummer
	window   int
	scales   [2]int // price/size decimal scales (synthetic prices are plain ints: 0,0)
}

// newSource builds the synthetic source, or a live venue adapter when -live is set.
func newSource(ctx context.Context, log *slog.Logger, o Options) (source.Source, venueSetup, error) {
	if !o.Live {
		return source.New(source.Config{Venue: "synthetic", Symbol: "DEMO", Seed: o.Seed, Steps: o.Steps}), venueSetup{pace: o.Pace}, nil
	}
	switch o.Venue {
	case "binance":
		s, err := binance.Live(ctx, binance.LiveConfig{Symbol: o.Symbol, Logger: log})
		if err != nil {
			return nil, venueSetup{}, err
		}
		p, sz := s.Scales()
		return s, venueSetup{scales: [2]int{p, sz}}, nil
	case "kraken":
		s, err := kraken.Live(ctx, kraken.LiveConfig{Symbol: o.Symbol, Logger: log})
		if err != nil {
			return nil, venueSetup{}, err
		}
		p, q := s.Scales()
		return s, venueSetup{checksum: kraken.Checksum, window: kraken.BookDepth, scales: [2]int{p, q}}, nil
	default:
		return nil, venueSetup{}, fmt.Errorf("unknown venue %q", o.Venue)
	}
}

// venueName/symbolName label the HTTP edge: the synthetic demo is its own venue.
func venueName(live bool, venue string) string {
	if live {
		return venue
	}
	return "synthetic"
}

func symbolName(live bool, symbol string) string {
	if live {
		return symbol
	}
	return "DEMO"
}

// Result is the read-only outcome of a finished pipeline run: the final published
// view, the fan-out delivery stats, and the engine's resync/gap/disconnect counters. It
// is a value snapshot taken at return, so a caller gets exactly what it needs to report
// and cannot accidentally drive the engine further — by then the source is already closed.
type Result struct {
	view            *book.View
	stats           delivery.Stats
	resyncs         int
	gaps            int
	disconnects     int
	wouldCrosses    int
	snapshotRejects int
	snapshotErrors  int
}

// resultOf snapshots the finished engine and hub into a Result.
func resultOf(eng *book.Engine, hub *delivery.Hub) Result {
	return Result{
		view:            eng.View(),
		stats:           hub.Stats(),
		resyncs:         eng.Resyncs(),
		gaps:            eng.Gaps(),
		disconnects:     eng.Disconnects(),
		wouldCrosses:    eng.WouldCrosses(),
		snapshotRejects: eng.SnapshotRejects(),
		snapshotErrors:  eng.SnapshotErrors(),
	}
}

// Log writes the one-line summary of a finished run.
func (r Result) Log(log *slog.Logger) {
	log.Info("done",
		"delivered", r.stats.Delivered, "dropped", r.stats.Dropped,
		"resyncs", r.resyncs, "gaps", r.gaps, "disconnects", r.disconnects,
		"would-cross", r.wouldCrosses, "snapshot-rejects", r.snapshotRejects, "snapshot-errors", r.snapshotErrors)
}

// Run wires the pipeline (source -> engine -> fan-out), drives it to completion or
// cancellation, and returns a Result: the final view, the fan-out stats, and the
// resync/gap/disconnect counters. It takes the Source as a parameter so it is source-agnostic
// (synthetic or live) and testable end to end with a scripted source. If the HTTP edge
// configuration is refused, Run returns the error before serving anything and leaves the
// listener to its owner.
func Run(ctx context.Context, log *slog.Logger, src source.Source, cfg Config) (Result, error) {
	defer src.Close() // release the source (e.g. a live WebSocket) on exit
	// Internal latency per ARCHITECTURE §9: adapter frame-dequeue -> view stored for readers,
	// observed by the engine so no wire-wait is ever inside a sample.
	lat := metrics.NewHistogram()
	eng, err := book.New(src, cfg.depth,
		book.WithChecksum(cfg.checksum), book.WithMaxDepth(cfg.window),
		book.WithLatencyObserver(lat.Record), book.WithSnapshotBackoff(snapshotBackoff))
	if err != nil {
		return Result{}, err
	}
	hub := delivery.New(1024)

	if cfg.HTTPListener != nil {
		handler, err := httpapi.New(httpapi.Config{
			Hub: hub, Engine: eng,
			Venue: market.Venue(cfg.venue), Symbol: market.Symbol(cfg.symbol),
			PriceScale: cfg.scales[0], SizeScale: cfg.scales[1],
			Latency: lat, Log: log,
		})
		if err != nil {
			// The listener is not served, and not closed here: the caller owns it.
			return Result{}, fmt.Errorf("http edge: %w", err)
		}
		// ReadHeaderTimeout bounds a client that connects and never finishes its
		// request, IdleTimeout an idle keep-alive connection; response-side stalls are
		// the handler's write deadline's job.
		edge := &http.Server{
			ReadHeaderTimeout: edgeReadHeaderTimeout, IdleTimeout: edgeIdleTimeout, MaxHeaderBytes: edgeMaxHeaderBytes,
			Handler: handler,
		}
		go func() {
			if err := edge.Serve(cfg.HTTPListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("http edge failed", "err", err)
			}
		}()
		// By the time this runs the hub is closed, so every stream handler is already
		// leaving and Shutdown returns quickly. If it does not within the bound, Close
		// drops whatever is left.
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), edgeShutdownTimeout)
			defer cancel()
			if err := edge.Shutdown(sctx); err != nil {
				_ = edge.Close()
			}
		}()
	}

	c := hub.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		n := 0
		for range c.Ready() {
			v := c.Take()
			if v == nil {
				continue
			}
			if cfg.every > 0 {
				if n++; n%cfg.every == 0 {
					logTop(log, v, cfg.scales)
				}
			}
		}
	}()
	defer func() { hub.Close(); <-done }()

	if err := eng.Bootstrap(ctx); err != nil {
		return resultOf(eng, hub), err
	}

	// Publish only when the view advances, so dropped/stale steps don't re-broadcast
	// an identical view.
	var lastSeq market.Sequence
	publish := func() {
		if v := eng.View(); v != nil && v.LastSeq != lastSeq {
			lastSeq = v.LastSeq
			hub.Publish(v)
		}
	}
	publish()

	for {
		ok, err := eng.Step(ctx)
		if err != nil {
			return resultOf(eng, hub), err
		}
		if !ok {
			return resultOf(eng, hub), nil
		}
		publish()
		if cfg.pace > 0 {
			t := time.NewTimer(cfg.pace)
			select {
			case <-t.C:
			case <-ctx.Done(): // the next Step reports the cancellation
				t.Stop()
			}
		}
	}
}

// LiveCadence picks the top-of-book log cadence. When -every is left at its
// default, a live run logs every update rather than every 500th. An explicit -every is
// always honoured.
func LiveCadence(every int, everySet, live bool) int {
	if live && !everySet {
		return 1
	}
	return every
}

// logTop logs the best bid and ask at the feed's decimal scales (price, size).
func logTop(log *slog.Logger, v *book.View, scales [2]int) {
	bid, ask := "-", "-"
	if len(v.Bids) > 0 {
		bid = market.FormatScaled(int64(v.Bids[0].Price), scales[0]) + " x " + market.FormatScaled(int64(v.Bids[0].Size), scales[1])
	}
	if len(v.Asks) > 0 {
		ask = market.FormatScaled(int64(v.Asks[0].Price), scales[0]) + " x " + market.FormatScaled(int64(v.Asks[0].Size), scales[1])
	}
	log.Info("top of book", "seq", uint64(v.LastSeq), "bid", bid, "ask", ask)
}
