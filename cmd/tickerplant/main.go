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
)

func main() {
	seed := flag.Int64("seed", 1, "synthetic source seed")
	steps := flag.Int("steps", 5000, "number of source steps to run")
	depth := flag.Int("depth", 10, "published book depth")
	every := flag.Int("every", 500, "log the top of book every N updates")
	pace := flag.Duration("pace", 200*time.Microsecond, "delay between updates (mimics a live feed; 0 floods to stress backpressure)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	src := source.New(source.Config{Venue: "synthetic", Symbol: "DEMO", Seed: *seed, Steps: *steps})
	eng := book.New(src, *depth)
	hub := delivery.New(256, 1024)

	_, ch := hub.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		n := 0
		for v := range ch {
			if n++; n%*every == 0 {
				logTop(log, v)
			}
		}
	}()

	if err := eng.Bootstrap(ctx); err != nil {
		log.Error("bootstrap failed", "err", err)
		os.Exit(1)
	}

	// Publish only when the view actually advanced, so dropped/stale steps don't
	// re-broadcast an identical view.
	var lastSeq market.Sequence
	publish := func() {
		if v := eng.View(); v != nil && v.LastSeq != lastSeq {
			lastSeq = v.LastSeq
			hub.Publish(v)
		}
	}
	publish()

	failed := false
	for {
		ok, err := eng.Step(ctx)
		if err != nil {
			log.Error("engine stopped", "err", err)
			failed = true
			break
		}
		if !ok {
			break
		}
		publish()
		if *pace > 0 {
			time.Sleep(*pace)
		}
	}

	hub.Close()
	<-done
	s := hub.Stats()
	log.Info("done", "resyncs", eng.Resyncs(), "delivered", s.Delivered, "dropped", s.Dropped)
	if failed {
		os.Exit(1)
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
