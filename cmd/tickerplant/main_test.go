package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestLiveCadence(t *testing.T) {
	cases := []struct {
		every          int
		everySet, live bool
		want           int
	}{
		{500, false, false, 500}, // synthetic, default cadence preserved
		{500, false, true, 1},    // live, default -> log every update
		{500, true, true, 500},   // live, explicit -every 500 -> honoured, not overridden
		{7, true, false, 7},      // synthetic, explicit
		{7, true, true, 7},       // live, explicit
	}
	for _, c := range cases {
		if got := liveCadence(c.every, c.everySet, c.live); got != c.want {
			t.Errorf("liveCadence(%d, set=%v, live=%v) = %d, want %d", c.every, c.everySet, c.live, got, c.want)
		}
	}
}

// parseFlags runs on a private FlagSet, so real argv slices drive it directly. The
// third case is the load-bearing one: an explicit -every at the default value must
// still set everySet, or a live run would wrongly override the requested cadence.
func TestParseFlags(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		want        options
		wantCadence int
		wantErr     bool
	}{
		{
			name: "defaults",
			args: nil,
			want: options{seed: 1, steps: 5000, depth: 10, every: 500, everySet: false,
				pace: 200 * time.Microsecond, live: false, venue: "binance", symbol: "BTCUSDT"},
			wantCadence: 500,
		},
		{
			name: "live with default cadence logs every update",
			args: []string{"-live"},
			want: options{seed: 1, steps: 5000, depth: 10, every: 500, everySet: false,
				pace: 200 * time.Microsecond, live: true, venue: "binance", symbol: "BTCUSDT"},
			wantCadence: 1,
		},
		{
			name: "explicit -every at the default value is honoured on a live run",
			args: []string{"-live", "-every", "500"},
			want: options{seed: 1, steps: 5000, depth: 10, every: 500, everySet: true,
				pace: 200 * time.Microsecond, live: true, venue: "binance", symbol: "BTCUSDT"},
			wantCadence: 500,
		},
		{
			name: "explicit -every",
			args: []string{"-every", "7"},
			want: options{seed: 1, steps: 5000, depth: 10, every: 7, everySet: true,
				pace: 200 * time.Microsecond, live: false, venue: "binance", symbol: "BTCUSDT"},
			wantCadence: 7,
		},
		{
			name: "typed values parse",
			args: []string{"-seed", "42", "-steps", "100", "-pace", "1ms", "-depth", "3", "-venue", "binance", "-symbol", "ETHUSDT"},
			want: options{seed: 42, steps: 100, depth: 3, every: 500, everySet: false,
				pace: time.Millisecond, live: false, venue: "binance", symbol: "ETHUSDT"},
			wantCadence: 500,
		},
		{
			name: "http edge address",
			args: []string{"-http", ":8080"},
			want: options{seed: 1, steps: 5000, depth: 10, every: 500, everySet: false,
				pace: 200 * time.Microsecond, live: false, venue: "binance", symbol: "BTCUSDT", httpAddr: ":8080"},
			wantCadence: 500,
		},
		{
			name:    "unknown flag errors",
			args:    []string{"-nope"},
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseFlags(c.args, io.Discard)
			if c.wantErr {
				if err == nil {
					t.Fatal("want a parse error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("parseFlags(%v) = %+v, want %+v", c.args, got, c.want)
			}
			if cad := liveCadence(got.every, got.everySet, got.live); cad != c.wantCadence {
				t.Fatalf("composed cadence = %d, want %d", cad, c.wantCadence)
			}
		})
	}
}

// -h must surface flag.ErrHelp so main can exit 0, matching flag.ExitOnError's UX.
func TestParseFlagsHelpIsErrHelp(t *testing.T) {
	if _, err := parseFlags([]string{"-h"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseFlags(-h) = %v, want flag.ErrHelp", err)
	}
}

func TestNewSourceSynthetic(t *testing.T) {
	src, vs, err := newSource(context.Background(), quietLog(), false, "", "", 1, 10, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if vs.pace != 5*time.Millisecond {
		t.Fatalf("synthetic pace = %v, want it preserved (5ms)", vs.pace)
	}
	if vs.checksum != nil || vs.window != 0 || vs.scales != [2]int{0, 0} {
		t.Fatalf("the synthetic source is a full-book integer venue: setup = %+v, want no checksum, no window, scales 0/0", vs)
	}
	if _, ok := src.Next(context.Background()); !ok {
		t.Fatal("synthetic source must yield events")
	}
}

func TestNewSourceUnknownVenue(t *testing.T) {
	if _, _, err := newSource(context.Background(), quietLog(), true, "okx", "X", 1, 10, 0); err == nil {
		t.Fatal("an unknown live venue must error")
	}
}

type closeTracker struct {
	source.Source
	closed bool
}

func (c *closeTracker) Close() error { c.closed = true; return c.Source.Close() }

// run must release the source on exit (so a live WebSocket is not leaked).
func TestRunClosesSource(t *testing.T) {
	ct := &closeTracker{Source: synthetic(1, 5)}
	if _, err := run(context.Background(), quietLog(), ct, config{depth: 5}); err != nil {
		t.Fatal(err)
	}
	if !ct.closed {
		t.Fatal("run must Close the source on exit")
	}
}

// scriptSource feeds a fixed snapshot and event sequence, for testing run's wiring.
type scriptSource struct {
	snap   market.Snapshot
	events []source.Event
	i      int
}

func (s *scriptSource) Next(context.Context) (source.Event, bool) {
	if s.i >= len(s.events) {
		return source.Event{}, false
	}
	e := s.events[s.i]
	s.i++
	return e, true
}
func (s *scriptSource) Snapshot(context.Context) (market.Snapshot, error) { return s.snap, nil }
func (s *scriptSource) Close() error                                      { return nil }

// run must hand cfg.checksum to the engine. A checksummer that can never match the
// snapshot makes bootstrap fail loudly — if run dropped the wiring, the same script
// would succeed silently and a checksum venue would run with no integrity guard.
func TestRunWiresChecksumIntoEngine(t *testing.T) {
	snap := market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}
	poisoned := func([]market.Level, []market.Level) uint32 { return 1 } // snapshot carries checksum 0
	if _, err := run(context.Background(), quietLog(), &scriptSource{snap: snap}, config{depth: 5, checksum: poisoned}); err == nil {
		t.Fatal("a never-matching checksummer must fail bootstrap: cfg.checksum was not wired into the engine")
	}
	if _, err := run(context.Background(), quietLog(), &scriptSource{snap: snap}, config{depth: 5}); err != nil {
		t.Fatalf("nil checksummer must run checksum-free: %v", err)
	}
}

// run must hand cfg.window to the engine. The script mimics a windowed feed (Kraken):
// checksums cover only the venue's window, so if run dropped the WithMaxDepth wiring
// the engine's deeper book would mismatch every checksum and the run would fail.
// Window 10 (not narrower) because Bootstrap now refuses a checksum venue whose
// maxDepth is narrower than the checksum depth (engine.go's own checksumDepth, 10) —
// 11 levels per side make the truncation still bite at that floor.
func TestRunWiresWindowIntoEngine(t *testing.T) {
	pq := func(bids, asks []market.Level) uint32 {
		var s uint32
		for _, l := range bids {
			s += uint32(l.Price) + uint32(l.Size)
		}
		for _, l := range asks {
			s += uint32(l.Price) + uint32(l.Size)
		}
		return s
	}
	bids := make([]market.Level, 11) // 100..90 descending
	for i := range bids {
		bids[i] = market.Level{Price: market.Price(100 - i), Size: 1}
	}
	asks := make([]market.Level, 11) // 102..112 ascending
	for i := range asks {
		asks[i] = market.Level{Price: market.Price(102 + i), Size: 1}
	}
	src := &scriptSource{
		// window 10: the 11th level on each side (90, 112) never enters the checksum.
		snap: market.Snapshot{LastUpdateID: 10, Bids: bids, Asks: asks, Checksum: pq(bids[:10], asks[:10])},
		events: []source.Event{
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11,
				Bids:     []market.Level{{Price: 101, Size: 2}}, // new best bid pushes 91 out of the window
				Checksum: pq(append([]market.Level{{Price: 101, Size: 2}}, bids[:9]...), asks[:10])}},
		},
	}
	res, err := run(context.Background(), quietLog(), src, config{depth: 5, checksum: pq, window: 10})
	if err != nil {
		t.Fatalf("windowed run must bind and apply cleanly: %v", err)
	}
	if res.resyncs != 0 {
		t.Fatalf("resyncs = %d, want 0 (a dropped window wiring makes every checksum mismatch)", res.resyncs)
	}
}

// run must publish only when the view advances: a stale duplicate (dropped by the
// engine) must not be re-broadcast.
func TestRunPublishesOnlyOnAdvance(t *testing.T) {
	src := &scriptSource{
		snap: market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		events: []source.Event{
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 100, Size: 2}}}}, // advances
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 100, Size: 2}}}}, // duplicate: engine drops, no advance
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 12, LastSeq: 12, Asks: []market.Level{{Price: 102, Size: 3}}}}, // advances
		},
	}
	res, err := run(context.Background(), quietLog(), src, config{depth: 5})
	if err != nil {
		t.Fatal(err)
	}
	// Under latest-wins conflation each hub.Publish is either delivered or superseded, so
	// Delivered+Dropped counts the Publish calls: bootstrap(10) + seq 11 + seq 12 = 3. The
	// stale duplicate does not advance the view, so run never Publishes it (not 4).
	if pubs := res.stats.Delivered + res.stats.Dropped; pubs != 3 {
		t.Fatalf("hub.Publish calls = %d, want 3 (a stale duplicate must not be re-broadcast)", pubs)
	}
}

func TestLogTopRendersBidAndAsk(t *testing.T) {
	var buf bytes.Buffer
	logTop(slog.New(slog.NewTextHandler(&buf, nil)), &book.View{
		LastSeq: 5,
		Bids:    []market.Level{{Price: 100, Size: 2}},
		Asks:    []market.Level{{Price: 101, Size: 3}},
	})
	if out := buf.String(); !strings.Contains(out, "100 x 2") || !strings.Contains(out, "101 x 3") {
		t.Fatalf("logTop output %q is missing the rendered bid/ask", out)
	}
}

func synthetic(seed int64, steps int) source.Source {
	return source.New(source.Config{Venue: "synthetic", Symbol: "DEMO", Seed: seed, Steps: steps})
}

// faultedSynthetic injects a gap, reorder, duplicate, and disconnect mid-stream (well
// before the end so the engine can converge), so a run over it genuinely drives the
// wired pipeline through resyncs rather than a clean stream.
func faultedSynthetic(seed int64, steps int) source.Source {
	return source.New(source.Config{
		Venue: "synthetic", Symbol: "DEMO", Seed: seed, Steps: steps,
		Faults: map[int]source.Fault{
			50:  source.FaultGap,
			120: source.FaultReorder,
			200: source.FaultDuplicate,
			260: source.FaultDisconnect,
		},
	})
}

// The whole pipeline (source -> engine -> fan-out) reconstructs an uncrossed book and
// delivers it.
func TestRunReconstructsUncrossedBook(t *testing.T) {
	res, err := run(context.Background(), quietLog(), synthetic(7, 400), config{depth: 10})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.view == nil {
		t.Fatal("run must return a final view")
	}
	if res.view.Crosses() {
		t.Fatalf("final view crosses: %v >= %v", res.view.Bids[0], res.view.Asks[0])
	}
	if res.stats.Delivered == 0 {
		t.Fatal("expected the consumer to receive views")
	}
}

// A faulted run (gap, reorder, duplicate, disconnect injected mid-stream) still ends
// with an uncrossed book, and the engine is observed to have actually resynced through
// the faults — the gap and reorder each surface as a sequence gap, the disconnect as a
// disconnect, and all three force a resync. This proves the wired pipeline recovers end
// to end, not merely that a clean stream finishes.
func TestRunWithFaultsStaysUncrossed(t *testing.T) {
	res, err := run(context.Background(), quietLog(), faultedSynthetic(3, 600), config{depth: 10})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.view == nil || res.view.Crosses() {
		t.Fatal("faulted run must still end uncrossed")
	}
	// Exact counts (deterministic, seed-independent): the gap and reorder each surface as
	// one forward sequence gap; the duplicate is dropped idempotently (no gap, no resync);
	// the disconnect is one disconnect. So 2 gaps + 1 disconnect = 3 resyncs, no more — a
	// stricter "==" also guards against the duplicate wrongly triggering a fourth resync.
	if res.gaps != 2 {
		t.Fatalf("expected exactly 2 sequence gaps (gap + reorder faults), got %d", res.gaps)
	}
	if res.disconnects != 1 {
		t.Fatalf("expected exactly 1 disconnect from the injected fault, got %d", res.disconnects)
	}
	if res.resyncs != 3 {
		t.Fatalf("expected exactly 3 resyncs (gap, reorder, disconnect; duplicate is dropped), got %d", res.resyncs)
	}
	if res.wouldCrosses != 0 {
		t.Fatalf("this faulted run injects no crossing delta, so WouldCrosses must be 0, got %d", res.wouldCrosses)
	}
}

// A cancelled context stops the pipeline cleanly, surfacing the error (no hang/leak).
func TestRunContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := run(ctx, quietLog(), synthetic(1, 400), config{depth: 10}); err == nil {
		t.Fatal("a cancelled context must surface an error")
	}
}

// Exercise the paced + per-update logging paths (and logTop on a populated view).
func TestRunPacedWithLogging(t *testing.T) {
	res, err := run(context.Background(), quietLog(), synthetic(7, 50), config{depth: 5, every: 1, pace: 1})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.view == nil || res.view.Crosses() {
		t.Fatal("paced run must end uncrossed")
	}
}

// staleSource bootstraps fine but its snapshot stays behind the stream, so every
// delta is a gap — the engine livelock-bounds and run surfaces the error.
type staleSource struct{ i int }

func (s *staleSource) Next(context.Context) (source.Event, bool) {
	s.i++
	if s.i > 100 {
		return source.Event{}, false
	}
	seq := market.Sequence(100 + s.i)
	return source.Event{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: seq, LastSeq: seq, Bids: []market.Level{{Price: 99, Size: 1}}}}, true
}
func (s *staleSource) Snapshot(context.Context) (market.Snapshot, error) {
	return market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}, nil
}
func (s *staleSource) Close() error { return nil }

func TestRunStepErrorPropagates(t *testing.T) {
	if _, err := run(context.Background(), quietLog(), &staleSource{}, config{depth: 5}); err == nil {
		t.Fatal("a livelocking source must surface an error from run")
	}
}

// An empty book (no bids or asks) renders each side as "-", not a panic or a zero level.
func TestLogTopRendersEmptySides(t *testing.T) {
	var buf bytes.Buffer
	logTop(slog.New(slog.NewTextHandler(&buf, nil)), &book.View{})
	if out := buf.String(); !strings.Contains(out, "bid=-") || !strings.Contains(out, "ask=-") {
		t.Fatalf("empty view must render bid=- and ask=-, got %q", out)
	}
}

// The edge's identity labels: a live run reports the venue flag values, the
// synthetic demo is its own venue.
func TestVenueSymbolNames(t *testing.T) {
	if v, sy := venueName(true, "kraken"), symbolName(true, "BTC/USD"); v != "kraken" || sy != "BTC/USD" {
		t.Fatalf("live identity = %s/%s", v, sy)
	}
	if v, sy := venueName(false, "kraken"), symbolName(false, "BTC/USD"); v != "synthetic" || sy != "DEMO" {
		t.Fatalf("synthetic identity = %s/%s, want synthetic/DEMO regardless of flags", v, sy)
	}
}

// gatedSource serves a snapshot and a few events, then blocks Next until released —
// it keeps run alive while a test talks to the HTTP edge.
type gatedSource struct {
	snap    market.Snapshot
	events  []source.Event
	i       int
	release chan struct{}
}

func (g *gatedSource) Next(ctx context.Context) (source.Event, bool) {
	if g.i < len(g.events) {
		e := g.events[g.i]
		e.Received = time.Now() // real sources stamp at dequeue; the gate mimics them
		g.i++
		return e, true
	}
	select {
	case <-g.release:
		return source.Event{}, false
	case <-ctx.Done():
		return source.Event{}, false
	}
}
func (g *gatedSource) Snapshot(context.Context) (market.Snapshot, error) { return g.snap, nil }
func (g *gatedSource) Close() error                                      { return nil }

// run with -http must serve the edge while the pipeline lives and tear it down when
// the run ends: /metrics answers with the configured identity, /stream opens with the
// bootstrapped view, and after the source ends the port stops answering.
func TestRunServesHTTPEdge(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := &gatedSource{
		snap: market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		events: []source.Event{
			{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 99, Size: 2}}}},
		},
		release: make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() {
		_, err := run(context.Background(), quietLog(), gs, config{
			depth: 5, venue: "synthetic", symbol: "DEMO", httpLn: ln,
		})
		done <- err
	}()

	base := "http://" + ln.Addr().String()
	var doc struct {
		Venue   string `json:"venue"`
		Symbol  string `json:"symbol"`
		Latency *struct {
			Count uint64 `json:"count"`
		} `json:"latency"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for { // poll until the edge answers AND the one scripted delta has been recorded
		resp, err := http.Get(base + "/metrics")
		if err == nil {
			decodeErr := json.NewDecoder(resp.Body).Decode(&doc)
			resp.Body.Close()
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if doc.Latency != nil && doc.Latency.Count == 1 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("edge never reported the recorded step: err=%v doc=%+v", err, doc)
		}
	}
	if doc.Venue != "synthetic" || doc.Symbol != "DEMO" {
		t.Fatalf("metrics identity = %s/%s", doc.Venue, doc.Symbol)
	}

	stream, err := http.Get(base + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(stream.Body)
	got := ""
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data: ") {
			got = line
			break
		}
	}
	stream.Body.Close()
	if !strings.Contains(got, `"seq":11`) {
		t.Fatalf("stream opener = %q, want the current view at seq 11 (snapshot + one applied delta)", got)
	}

	close(gs.release) // end the run; the edge must go down with it
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := http.Get(base + "/metrics"); err == nil {
		t.Fatal("edge still answering after the run ended")
	}
}
