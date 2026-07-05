// Command tickerplant runs the order-book engine against the deterministic synthetic
// source and fans the live book out to a consumer — a runnable, no-account demo of
// the whole pipeline: source -> engine -> delivery.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
	"github.com/klimavojtech2002/tickerplant/internal/venue/binance"
)

type config struct {
	depth int
	every int
	pace  time.Duration
}

// options is everything main reads from the command line.
type options struct {
	seed     int64
	steps    int
	depth    int
	every    int
	everySet bool // -every given explicitly, even if equal to the default
	pace     time.Duration
	live     bool
	venue    string
	symbol   string
}

// parseFlags parses argv (without the program name) on a private FlagSet, so tests can
// drive it with real argv slices instead of mutating the global flag state. Usage and
// errors go to out (stderr in main, discarded in tests).
func parseFlags(args []string, out io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("tickerplant", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Int64Var(&o.seed, "seed", 1, "synthetic source seed")
	fs.IntVar(&o.steps, "steps", 5000, "number of source steps to run")
	fs.IntVar(&o.depth, "depth", 10, "published book depth")
	fs.IntVar(&o.every, "every", 500, "log the top of book every N updates")
	fs.DurationVar(&o.pace, "pace", 200*time.Microsecond, "delay between updates (mimics a live feed; 0 floods to stress backpressure)")
	fs.BoolVar(&o.live, "live", false, "connect to a live venue instead of the synthetic source")
	fs.StringVar(&o.venue, "venue", "binance", "live venue (with -live): binance")
	fs.StringVar(&o.symbol, "symbol", "BTCUSDT", "live symbol (with -live)")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	// An explicit -every must be honoured even on a live run, where the default is
	// overridden to log every update (liveCadence).
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "every" {
			o.everySet = true
		}
	})
	return o, nil
}

func main() {
	opts, err := parseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0) // -h: usage was printed, same exit as flag.ExitOnError
		}
		os.Exit(2) // the FlagSet already printed the error and usage
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	src, pacing, err := newSource(ctx, opts.live, opts.venue, opts.symbol, opts.seed, opts.steps, opts.pace)
	if err != nil {
		log.Error("source setup failed", "err", err)
		os.Exit(1)
	}

	res, err := run(ctx, log, src, config{depth: opts.depth, every: liveCadence(opts.every, opts.everySet, opts.live), pace: pacing})
	if err != nil {
		log.Error("engine stopped", "err", err)
		os.Exit(1)
	}
	log.Info("done",
		"delivered", res.stats.Delivered, "dropped", res.stats.Dropped,
		"resyncs", res.resyncs, "gaps", res.gaps, "disconnects", res.disconnects,
		"would-cross", res.wouldCrosses)
}

// newSource builds the synthetic source, or a live venue adapter when -live is set. A
// live feed paces itself, so the artificial inter-update delay is dropped for it.
func newSource(ctx context.Context, live bool, venueName, symbol string, seed int64, steps int, pace time.Duration) (source.Source, time.Duration, error) {
	if !live {
		return source.New(source.Config{Venue: "synthetic", Symbol: "DEMO", Seed: seed, Steps: steps}), pace, nil
	}
	switch venueName {
	case "binance":
		s, err := binance.Live(ctx, binance.LiveConfig{Symbol: symbol})
		if err != nil {
			return nil, 0, err
		}
		return s, 0, nil
	default:
		return nil, 0, fmt.Errorf("unknown venue %q", venueName)
	}
}

// runResult is the read-only outcome of a finished pipeline run: the final published
// view, the fan-out delivery stats, and the engine's resync/gap/disconnect counters. It
// is a value snapshot taken at return, so a caller gets exactly what it needs to report
// and cannot accidentally drive the engine further — by then the source is already closed.
type runResult struct {
	view         *book.View
	stats        delivery.Stats
	resyncs      int
	gaps         int
	disconnects  int
	wouldCrosses int
}

// resultOf snapshots the finished engine and hub into a runResult.
func resultOf(eng *book.Engine, hub *delivery.Hub) runResult {
	return runResult{
		view:         eng.View(),
		stats:        hub.Stats(),
		resyncs:      eng.Resyncs(),
		gaps:         eng.Gaps(),
		disconnects:  eng.Disconnects(),
		wouldCrosses: eng.WouldCrosses(),
	}
}

// run wires the pipeline (source -> engine -> fan-out), drives it to completion or
// cancellation, and returns a runResult: the final view, the fan-out stats, and the
// resync/gap/disconnect counters. It takes the Source as a parameter so it is
// source-agnostic (synthetic now, a live adapter later) and testable end to end with a
// scripted source.
func run(ctx context.Context, log *slog.Logger, src source.Source, cfg config) (runResult, error) {
	defer src.Close() // release the source (e.g. a live WebSocket) on exit
	eng := book.New(src, cfg.depth)
	hub := delivery.New(1024)

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
					logTop(log, v)
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
			time.Sleep(cfg.pace)
		}
	}
}

// liveCadence picks the top-of-book log cadence. A live feed trickles (~1/s) where the
// synthetic source floods (thousands/s), so when -every is left at its default a live
// run logs every update rather than every 500th. An explicit -every is always honoured.
func liveCadence(every int, everySet, live bool) int {
	if live && !everySet {
		return 1
	}
	return every
}

func logTop(log *slog.Logger, v *book.View) {
	bid, ask := "-", "-"
	if len(v.Bids) > 0 {
		bid = fmt.Sprintf("%d x %d", v.Bids[0].Price, v.Bids[0].Size)
	}
	if len(v.Asks) > 0 {
		ask = fmt.Sprintf("%d x %d", v.Asks[0].Price, v.Asks[0].Size)
	}
	log.Info("top of book", "seq", uint64(v.LastSeq), "bid", bid, "ask", ask)
}
