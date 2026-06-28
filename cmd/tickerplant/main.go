// Command tickerplant runs the order-book engine against the deterministic synthetic
// source and fans the live book out to a consumer — a runnable, no-account demo of
// the whole pipeline: source -> engine -> delivery.
package main

import (
	"context"
	"flag"
	"fmt"
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

func main() {
	seed := flag.Int64("seed", 1, "synthetic source seed")
	steps := flag.Int("steps", 5000, "number of source steps to run")
	depth := flag.Int("depth", 10, "published book depth")
	every := flag.Int("every", 500, "log the top of book every N updates")
	pace := flag.Duration("pace", 200*time.Microsecond, "delay between updates (mimics a live feed; 0 floods to stress backpressure)")
	live := flag.Bool("live", false, "connect to a live venue instead of the synthetic source")
	venueName := flag.String("venue", "binance", "live venue (with -live): binance")
	symbol := flag.String("symbol", "BTCUSDT", "live symbol (with -live)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	src, pacing, err := newSource(ctx, *live, *venueName, *symbol, *seed, *steps, *pace)
	if err != nil {
		log.Error("source setup failed", "err", err)
		os.Exit(1)
	}

	// A live feed trickles (~1/s) where the synthetic source floods (thousands/s), so
	// the default "every 500th" cadence would leave a live run silent for minutes. When
	// it is left at the default, log every live update instead.
	logEvery := *every
	if *live && logEvery == 500 {
		logEvery = 1
	}

	_, stats, err := run(ctx, log, src, config{depth: *depth, every: logEvery, pace: pacing})
	if err != nil {
		log.Error("engine stopped", "err", err)
		os.Exit(1)
	}
	log.Info("done", "delivered", stats.Delivered, "dropped", stats.Dropped)
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

// run wires the pipeline (source -> engine -> fan-out), drives it to completion or
// cancellation, and returns the final view and fan-out stats. It takes the Source as
// a parameter so it is source-agnostic (synthetic now, a live adapter later) and
// testable end to end with a scripted source.
func run(ctx context.Context, log *slog.Logger, src source.Source, cfg config) (*book.View, delivery.Stats, error) {
	defer src.Close() // release the source (e.g. a live WebSocket) on exit
	eng := book.New(src, cfg.depth)
	hub := delivery.New(256, 1024)

	_, ch := hub.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		n := 0
		for v := range ch {
			if cfg.every > 0 {
				if n++; n%cfg.every == 0 {
					logTop(log, v)
				}
			}
		}
	}()
	defer func() { hub.Close(); <-done }()

	if err := eng.Bootstrap(ctx); err != nil {
		return nil, hub.Stats(), err
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
			return eng.View(), hub.Stats(), err
		}
		if !ok {
			return eng.View(), hub.Stats(), nil
		}
		publish()
		if cfg.pace > 0 {
			time.Sleep(cfg.pace)
		}
	}
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
