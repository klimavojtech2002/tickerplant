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
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/httpapi"
	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/metrics"
	"github.com/klimavojtech2002/tickerplant/internal/source"
	"github.com/klimavojtech2002/tickerplant/internal/venue/binance"
	"github.com/klimavojtech2002/tickerplant/internal/venue/kraken"
)

// defaultVenue/defaultSymbol are the -venue/-symbol flag defaults, named so the
// startup warning below can detect a likely -live typo without duplicating the
// literal strings the flags themselves declare.
const (
	defaultVenue  = "binance"
	defaultSymbol = "BTCUSDT"
)

type config struct {
	depth    int
	every    int
	pace     time.Duration
	checksum book.Checksummer // nil except for checksum venues (Kraken)
	window   int              // feed's subscribed book window; 0 = full-book feed
	venue    string
	symbol   string
	scales   [2]int       // price/size decimal scales for the HTTP edge's rendering
	httpLn   net.Listener // serve the HTTP edge on this listener; nil = edge off
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
	httpAddr string
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
	fs.StringVar(&o.venue, "venue", defaultVenue, "live venue (with -live): binance|kraken")
	fs.StringVar(&o.symbol, "symbol", defaultSymbol, "live symbol (with -live)")
	fs.StringVar(&o.httpAddr, "http", "", "serve the SSE stream and /metrics on this address (e.g. :8080); empty = off")
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

	src, vs, err := newSource(ctx, log, opts.live, opts.venue, opts.symbol, opts.seed, opts.steps, opts.pace)
	if err != nil {
		log.Error("source setup failed", "err", err)
		os.Exit(1)
	}

	cfg := config{
		depth: opts.depth, every: liveCadence(opts.every, opts.everySet, opts.live),
		pace: vs.pace, checksum: vs.checksum, window: vs.window,
		venue: venueName(opts.live, opts.venue), symbol: symbolName(opts.live, opts.symbol), scales: vs.scales,
	}
	log.Info("mode", "live", opts.live, "venue", cfg.venue, "symbol", cfg.symbol)
	if liveFlagsIgnored(opts.live, opts.venue, opts.symbol) {
		log.Warn("live venue/symbol flags ignored without -live", "venue", opts.venue, "symbol", opts.symbol)
	}
	if opts.httpAddr != "" {
		ln, err := net.Listen("tcp", opts.httpAddr)
		if err != nil {
			log.Error("http listen failed", "addr", opts.httpAddr, "err", err)
			os.Exit(1)
		}
		log.Info("http edge listening", "addr", ln.Addr().String())
		cfg.httpLn = ln
	}

	res, err := run(ctx, log, src, cfg)
	if code := reportOutcome(log, err); code != 0 {
		os.Exit(code)
	}
	log.Info("done",
		"delivered", res.stats.Delivered, "dropped", res.stats.Dropped,
		"resyncs", res.resyncs, "gaps", res.gaps, "disconnects", res.disconnects,
		"would-cross", res.wouldCrosses)
}

// reportOutcome logs the pipeline's result and returns the process exit code. A
// context-cancellation error (Ctrl-C, the only signal wired via signal.NotifyContext)
// is a clean shutdown, not a failure — it must not read as a crash on the one
// interruption every demo hits.
func reportOutcome(log *slog.Logger, err error) int {
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
	return !live && (venue != defaultVenue || symbol != defaultSymbol)
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
func newSource(ctx context.Context, log *slog.Logger, live bool, venueName, symbol string, seed int64, steps int, pace time.Duration) (source.Source, venueSetup, error) {
	if !live {
		return source.New(source.Config{Venue: "synthetic", Symbol: "DEMO", Seed: seed, Steps: steps}), venueSetup{pace: pace}, nil
	}
	switch venueName {
	case "binance":
		s, err := binance.Live(ctx, binance.LiveConfig{Symbol: symbol, Logger: log})
		if err != nil {
			return nil, venueSetup{}, err
		}
		p, sz := s.Scales()
		return s, venueSetup{scales: [2]int{p, sz}}, nil
	case "kraken":
		s, err := kraken.Live(ctx, kraken.LiveConfig{Symbol: symbol, Logger: log})
		if err != nil {
			return nil, venueSetup{}, err
		}
		p, q := s.Scales()
		return s, venueSetup{checksum: kraken.Checksum, window: kraken.BookDepth, scales: [2]int{p, q}}, nil
	default:
		return nil, venueSetup{}, fmt.Errorf("unknown venue %q", venueName)
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
	// Internal latency per ARCHITECTURE §9: adapter frame-dequeue -> view published,
	// observed by the engine so no wire-wait is ever inside a sample.
	lat := metrics.NewHistogram()
	eng := book.New(src, cfg.depth).WithChecksum(cfg.checksum).WithMaxDepth(cfg.window).WithLatencyObserver(lat.Record)
	hub := delivery.New(1024)

	if cfg.httpLn != nil {
		// ReadHeaderTimeout bounds a client that connects and never finishes its
		// request; response-side stalls are the handler's write deadline's job.
		edge := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: httpapi.New(httpapi.Config{
			Hub: hub, Engine: eng,
			Venue: market.Venue(cfg.venue), Symbol: market.Symbol(cfg.symbol),
			PriceScale: cfg.scales[0], SizeScale: cfg.scales[1],
			Latency: lat, Log: log,
		})}
		go func() {
			if err := edge.Serve(cfg.httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("http edge failed", "err", err)
			}
		}()
		// Close, not Shutdown: open SSE streams never drain on their own, and by the
		// time this runs the hub is closed, so every handler is already exiting.
		defer func() { _ = edge.Close() }()
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
