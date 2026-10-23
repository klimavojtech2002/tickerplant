// Command tickerplant runs the order-book engine against a synthetic or live source and fans
// the book out to a log consumer and, with -http, the SSE and metrics edge: source -> engine
// -> delivery. The synthetic source needs no account.
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
	"strconv"
	"syscall"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/app"
	"github.com/klimavojtech2002/tickerplant/internal/httpapi"
	"github.com/klimavojtech2002/tickerplant/internal/version"
)

// shutdownSignals end the run cleanly. SIGTERM is what `docker stop` sends; as PID 1 the
// process would otherwise ignore it and be killed after the grace period.
var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// options is everything main reads from the command line.
type options struct {
	seed        int64
	steps       int
	depth       int
	every       int
	everySet    bool // -every given explicitly, even if equal to the default
	pace        time.Duration
	live        bool
	venue       string
	symbol      string
	httpAddr    string
	corsOrigin  string
	maxStreams  int
	healthcheck bool
	version     bool
	logLevel    string
	logFormat   string
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
	// -pace 0 the source floods, and the hub disconnects any consumer that falls
	// too far behind.
	fs.DurationVar(&o.pace, "pace", 150*time.Millisecond, "delay between updates (mimics a live feed; 0 floods to stress backpressure)")
	fs.BoolVar(&o.live, "live", false, "connect to a live venue instead of the synthetic source")
	fs.StringVar(&o.venue, "venue", app.DefaultVenue, "live venue (with -live): binance|kraken")
	fs.StringVar(&o.symbol, "symbol", app.DefaultSymbol, "live symbol (with -live)")
	fs.StringVar(&o.httpAddr, "http", "", "serve the SSE stream, /api/status and Prometheus /metrics on this address (e.g. :8080); empty = off")
	fs.IntVar(&o.maxStreams, "max-streams", httpapi.DefaultMaxStreams, "most simultaneous stream clients the HTTP edge serves (1 to 10000); more get a 503")
	fs.StringVar(&o.corsOrigin, "cors-origin", "*", "origin allowed to read the HTTP edge from a browser: * or one origin such as https://app.example")
	fs.BoolVar(&o.healthcheck, "healthcheck", false, "ask the HTTP edge on 127.0.0.1 at the -http port whether it is up (GET /healthz) and exit 0 if so, 1 if not")
	fs.BoolVar(&o.version, "version", false, "print the version and exit")
	fs.StringVar(&o.logLevel, "log-level", "info", "log level: debug|info|warn|error")
	fs.StringVar(&o.logFormat, "log-format", "text", "log format: text|json")
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
	if o.version { // nothing else matters when only the version is wanted
		return o, nil
	}
	if err := o.validate(); err != nil {
		fmt.Fprintf(out, "tickerplant: %v\n", err)
		return options{}, err
	}
	return o, nil
}

// maxDepth bounds -depth. A view of 1000 levels per side is already far more than a client
// renders; the bound keeps one SSE frame small and refuses nonsense such as -depth 99999
// instead of clamping it.
const maxDepth = 1000

// maxStreamsLimit bounds -max-streams. The cap exists to keep the edge small; ten thousand
// stream clients is already far beyond what one process of this size should serve.
const maxStreamsLimit = 10000

// validate refuses values the pipeline cannot use. Each message names the flag, the value
// and what is allowed, and main prints it and exits 2, like any other bad command line.
func (o options) validate() error {
	switch {
	case o.depth < 1 || o.depth > maxDepth:
		return fmt.Errorf("-depth %d is out of range: want 1 to %d", o.depth, maxDepth)
	case o.steps < 0:
		return fmt.Errorf("-steps %d is negative: want 0 or more", o.steps)
	case o.every < 0:
		return fmt.Errorf("-every %d is negative: want 0 (never) or more", o.every)
	case o.pace < 0:
		return fmt.Errorf("-pace %v is negative: want 0 or more", o.pace)
	}
	if o.live && o.venue != "binance" && o.venue != "kraken" {
		return fmt.Errorf("-venue %q is not one of binance, kraken", o.venue)
	}
	if _, err := parseLevel(o.logLevel); err != nil {
		return err
	}
	if o.maxStreams < 1 || o.maxStreams > maxStreamsLimit {
		return fmt.Errorf("-max-streams %d is out of range: want 1 to %d", o.maxStreams, maxStreamsLimit)
	}
	if err := httpapi.ValidateCORSOrigin(o.corsOrigin); err != nil {
		return fmt.Errorf("-cors-origin: %w", err)
	}
	if o.healthcheck {
		if o.httpAddr == "" {
			return errors.New("-healthcheck needs -http: the address of the edge to check")
		}
		if _, err := healthcheckPort(o.httpAddr); err != nil {
			return err
		}
	}
	if o.logFormat != "text" && o.logFormat != "json" {
		return fmt.Errorf("-log-format %q is not one of text, json", o.logFormat)
	}
	return nil
}

func parseLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("-log-level %q is not one of debug, info, warn, error", s)
}

// newLogger builds the process logger. level and format were checked by validate, so an
// unknown value falls back to the defaults rather than failing here.
func newLogger(level, format string, w io.Writer) *slog.Logger {
	lv, err := parseLevel(level)
	if err != nil {
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// healthTimeout bounds one health check, connect and answer together. It is a variable so
// a test can shorten it.
var healthTimeout = 2 * time.Second

// healthcheckPort returns the port of the -http address. The check always goes to the
// loopback address, so only the port matters: ":8080", "0.0.0.0:8080", "[::]:8080" and
// "host:8080" all give 8080. A missing, non-numeric or out-of-range port is refused.
func healthcheckPort(addr string) (int, error) {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("-http %q has no usable port: %v", addr, err)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("-http %q: the port must be a number from 1 to 65535", addr)
	}
	return n, nil
}

// runHealthcheck asks the edge on the loopback address whether it is up and returns the
// process exit code: 0 for a 200 from /healthz, 1 for anything else (nothing listening, a
// timeout, another status, a redirect), with one line on out saying which. It runs inside
// the container it checks, so it never goes anywhere but 127.0.0.1.
func runHealthcheck(port int, out io.Writer) int {
	client := &http.Client{
		Timeout:       healthTimeout,
		Transport:     &http.Transport{DisableKeepAlives: true}, // no proxy: the check is local
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/healthz"
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(out, "tickerplant: healthcheck failed: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(out, "tickerplant: healthcheck failed: %s answered %d\n", url, resp.StatusCode)
		return 1
	}
	return 0
}

func main() {
	opts, err := parseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0) // -h: usage was printed, same exit as flag.ExitOnError
		}
		os.Exit(2) // the FlagSet already printed the error and usage
	}

	if opts.version {
		fmt.Println("tickerplant", version.String())
		return
	}
	if opts.healthcheck { // before the logger, the signal handler and any source: it only asks
		port, _ := healthcheckPort(opts.httpAddr) // already checked by validate
		os.Exit(runHealthcheck(port, os.Stderr))
	}
	log := newLogger(opts.logLevel, opts.logFormat, os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer stop()
	go func() { // after the first signal, restore the default so a second one kills the process
		<-ctx.Done()
		stop()
	}()

	src, cfg, err := app.Prepare(ctx, log, app.Options{
		Seed: opts.seed, Steps: opts.steps, Depth: opts.depth, Every: opts.every, EverySet: opts.everySet,
		Pace: opts.pace, Live: opts.live, Venue: opts.venue, Symbol: opts.symbol, CORSOrigin: opts.corsOrigin, MaxStreams: opts.maxStreams,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(app.ReportOutcome(log, err)) // interrupted during setup: a clean shutdown
		}
		os.Exit(1) // Prepare already logged the cause
	}
	if opts.httpAddr != "" {
		ln, err := net.Listen("tcp", opts.httpAddr)
		if err != nil {
			log.Error("http listen failed", "addr", opts.httpAddr, "err", err)
			_ = src.Close() // os.Exit skips defers; release the source (a live WebSocket) first
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
