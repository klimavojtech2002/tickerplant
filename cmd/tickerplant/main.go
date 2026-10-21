// Command tickerplant runs the order-book engine against a synthetic or live source and fans
// the book out to a log consumer and, with -http, the SSE and metrics edge: source -> engine
// -> delivery. The synthetic source needs no account.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/app"
)

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
	// 150ms (~6.7/s) is a modest feed rate; with
	// -pace 0 the source floods, and the hub disconnects any consumer after 1024
	// consecutive supersedes.
	fs.DurationVar(&o.pace, "pace", 150*time.Millisecond, "delay between updates (mimics a live feed; 0 floods to stress backpressure)")
	fs.BoolVar(&o.live, "live", false, "connect to a live venue instead of the synthetic source")
	fs.StringVar(&o.venue, "venue", app.DefaultVenue, "live venue (with -live): binance|kraken")
	fs.StringVar(&o.symbol, "symbol", app.DefaultSymbol, "live symbol (with -live)")
	fs.StringVar(&o.httpAddr, "http", "", "serve the SSE stream and /metrics on this address (e.g. :8080); empty = off")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	// An explicit -every must be honoured even on a live run, where the default is
	// overridden to log every update (app.LiveCadence).
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

	src, cfg, err := app.Prepare(ctx, log, app.Options{
		Seed: opts.seed, Steps: opts.steps, Depth: opts.depth, Every: opts.every, EverySet: opts.everySet,
		Pace: opts.pace, Live: opts.live, Venue: opts.venue, Symbol: opts.symbol,
	})
	if err != nil {
		os.Exit(1) // Prepare already logged the cause
	}
	if opts.httpAddr != "" {
		ln, err := net.Listen("tcp", opts.httpAddr)
		if err != nil {
			log.Error("http listen failed", "addr", opts.httpAddr, "err", err)
			os.Exit(1)
		}
		log.Info("http edge listening", "addr", ln.Addr().String())
		cfg.HTTPListener = ln
	}

	res, err := app.Run(ctx, log, src, cfg)
	if code := app.ReportOutcome(log, err); code != 0 {
		os.Exit(code)
	}
	res.Log(log)
}
