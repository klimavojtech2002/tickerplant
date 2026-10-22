package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/delivery"
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
		if got := LiveCadence(c.every, c.everySet, c.live); got != c.want {
			t.Errorf("LiveCadence(%d, set=%v, live=%v) = %d, want %d", c.every, c.everySet, c.live, got, c.want)
		}
	}
}

func TestNewSourceSynthetic(t *testing.T) {
	src, vs, err := newSource(context.Background(), quietLog(), Options{Seed: 1, Steps: 10, Pace: 5 * time.Millisecond})
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
	if _, _, err := newSource(context.Background(), quietLog(), Options{Live: true, Venue: "okx", Symbol: "X", Seed: 1, Steps: 10}); err == nil {
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
	if _, err := Run(context.Background(), quietLog(), ct, Config{depth: 5}); err != nil {
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
	if _, err := Run(context.Background(), quietLog(), &scriptSource{snap: snap}, Config{depth: 5, checksum: poisoned}); err == nil {
		t.Fatal("a never-matching checksummer must fail bootstrap: cfg.checksum was not wired into the engine")
	}
	if _, err := Run(context.Background(), quietLog(), &scriptSource{snap: snap}, Config{depth: 5}); err != nil {
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
	res, err := Run(context.Background(), quietLog(), src, Config{depth: 5, checksum: pq, window: 10})
	if err != nil {
		t.Fatalf("windowed run must bind and apply cleanly: %v", err)
	}
	if res.resyncs != 0 {
		t.Fatalf("resyncs = %d, want 0 after a clean windowed run", res.resyncs)
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
	res, err := Run(context.Background(), quietLog(), src, Config{depth: 5})
	if err != nil {
		t.Fatal(err)
	}
	// Published counts the Publish calls: bootstrap(10) + seq 11 + seq 12 = 3. The stale
	// duplicate does not advance the view, so run never Publishes it (not 4).
	if pubs := res.stats.Published; pubs != 3 {
		t.Fatalf("hub.Publish calls = %d, want 3 (a stale duplicate must not be re-broadcast)", pubs)
	}
}

func TestLogTopRendersBidAndAsk(t *testing.T) {
	var buf bytes.Buffer
	logTop(slog.New(slog.NewTextHandler(&buf, nil)), &book.View{
		LastSeq: 5,
		Bids:    []market.Level{{Price: 100, Size: 2}},
		Asks:    []market.Level{{Price: 101, Size: 3}},
	}, [2]int{})
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
	res, err := Run(context.Background(), quietLog(), synthetic(7, 400), Config{depth: 10})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.view == nil {
		t.Fatal("run must return a final view")
	}
	if res.view.Crosses() {
		t.Fatalf("final view crosses: %v >= %v", res.view.Bids[0], res.view.Asks[0])
	}
	// 400 steps and the bootstrap view: every step advances the view, so each is published.
	// The count is read from this deterministic seed-7 run, not derived.
	if res.stats.Published != 401 {
		t.Fatalf("Published = %d, want 401", res.stats.Published)
	}
	// The log reader is internal: with no HTTP viewer, nothing is counted as a consumer.
	if s := res.stats; s.Consumers != 0 || s.Delivered != 0 || s.Dropped != 0 {
		t.Fatalf("stats = %+v, want no counted consumers, deliveries or drops", s)
	}
}

// A faulted run (gap, reorder, duplicate, disconnect injected mid-stream) still ends
// with an uncrossed book, and the engine is observed to have actually resynced through
// the faults — the gap and reorder each surface as a sequence gap, the disconnect as a
// disconnect, and all three force a resync. This exercises the wired pipeline's recovery end
// to end, not merely that a clean stream finishes.
func TestRunWithFaultsStaysUncrossed(t *testing.T) {
	res, err := Run(context.Background(), quietLog(), faultedSynthetic(3, 600), Config{depth: 10})
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
	_, err := Run(ctx, quietLog(), synthetic(1, 400), Config{depth: 10})
	if err == nil {
		t.Fatal("a cancelled context must surface an error")
	}
	// ReportOutcome relies on this specific error surfacing unwrapped: it is what
	// lets Ctrl-C during a live demo exit clean instead of reading as a crash.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run on a cancelled context must surface context.Canceled, got %v", err)
	}
}

// ReportOutcome must treat a cancelled context (Ctrl-C or SIGTERM) as a clean shutdown,
// not a crash — that is the interruption every demo hits.
func TestReportOutcome(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"interrupted", context.Canceled, 0},
		{"interrupted, wrapped", fmt.Errorf("run: %w", context.Canceled), 0},
		{"real failure", errors.New("boom"), 1},
	}
	for _, c := range cases {
		if got := ReportOutcome(quietLog(), c.err); got != c.want {
			t.Errorf("%s: ReportOutcome(%v) = %d, want %d", c.name, c.err, got, c.want)
		}
	}
}

// liveFlagsIgnored catches the plausible typo of setting -venue/-symbol without
// -live: newSource silently falls back to the synthetic demo in that case.
func TestLiveFlagsIgnored(t *testing.T) {
	cases := []struct {
		name          string
		live          bool
		venue, symbol string
		want          bool
	}{
		{"defaults, synthetic", false, DefaultVenue, DefaultSymbol, false},
		{"venue set without -live", false, "kraken", DefaultSymbol, true},
		{"symbol set without -live", false, DefaultVenue, "BTC/USD", true},
		{"live with non-default venue", true, "kraken", "BTC/USD", false},
		{"live with defaults", true, DefaultVenue, DefaultSymbol, false},
	}
	for _, c := range cases {
		if got := liveFlagsIgnored(c.live, c.venue, c.symbol); got != c.want {
			t.Errorf("%s: liveFlagsIgnored(%v, %q, %q) = %v, want %v", c.name, c.live, c.venue, c.symbol, got, c.want)
		}
	}
}

// Exercise the paced + per-update logging paths (and logTop on a populated view).
func TestRunPacedWithLogging(t *testing.T) {
	res, err := Run(context.Background(), quietLog(), synthetic(7, 50), Config{depth: 5, every: 1, pace: 1})
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
	if _, err := Run(context.Background(), quietLog(), &staleSource{}, Config{depth: 5}); err == nil {
		t.Fatal("a livelocking source must surface an error from run")
	}
}

// An empty book (no bids or asks) renders each side as "-", not a panic or a zero level.
func TestLogTopRendersEmptySides(t *testing.T) {
	var buf bytes.Buffer
	logTop(slog.New(slog.NewTextHandler(&buf, nil)), &book.View{}, [2]int{})
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

// firstViewData reads a server-sent event stream and returns the data line of the first
// event named view; telemetry events on the same stream are skipped.
func firstViewData(sc *bufio.Scanner) (string, bool) {
	event := ""
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			event = name
		} else if data, ok := strings.CutPrefix(line, "data: "); ok && event == "view" {
			return data, true
		}
	}
	return "", false
}

// run with -http must serve the edge while the pipeline lives and tear it down when
// the run ends: /api/status answers with the configured identity, /stream opens with the
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
		_, err := Run(context.Background(), quietLog(), gs, Config{
			depth: 5, venue: "synthetic", symbol: "DEMO", HTTPListener: ln,
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
		resp, err := http.Get(base + "/api/status")
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
		t.Fatalf("status identity = %s/%s", doc.Venue, doc.Symbol)
	}

	stream, err := http.Get(base + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := firstViewData(bufio.NewScanner(stream.Body))
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

// Prepare must carry each option through to the pipeline Config: a synthetic run keeps
// its pace and is labelled as the synthetic venue, whatever -venue says.
func TestPrepareSynthetic(t *testing.T) {
	src, cfg, err := Prepare(context.Background(), quietLog(), Options{
		Seed: 1, Steps: 10, Depth: 7, Every: 500, Pace: 5 * time.Millisecond, Venue: "kraken", Symbol: "BTC/USD",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if cfg.depth != 7 || cfg.every != 500 || cfg.pace != 5*time.Millisecond ||
		cfg.venue != "synthetic" || cfg.symbol != "DEMO" || cfg.checksum != nil || cfg.window != 0 {
		t.Fatalf("Prepare config = %+v, want depth 7, every 500, pace 5ms, synthetic/DEMO, no checksum or window", cfg)
	}
}

// A failed source setup is logged once by Prepare and returned, so the caller only exits.
func TestPrepareUnknownVenue(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	src, _, err := Prepare(context.Background(), log, Options{Live: true, Venue: "okx", Symbol: "X", Steps: 1})
	if err == nil {
		src.Close()
		t.Fatal("an unknown live venue must error")
	}
	if got := strings.Count(buf.String(), "source setup failed"); got != 1 {
		t.Fatalf("logged %d times, want once:\n%s", got, buf.String())
	}
}

func TestResultLog(t *testing.T) {
	var buf bytes.Buffer
	Result{resyncs: 2, gaps: 3, disconnects: 4, wouldCrosses: 5, snapshotErrors: 6, snapshotRejects: 7,
		stats: delivery.Stats{Published: 8, Delivered: 9, Dropped: 10}}.Log(slog.New(slog.NewTextHandler(&buf, nil)))
	for _, want := range []string{"msg=done", "resyncs=2", "gaps=3", "disconnects=4", "would-cross=5", "snapshot-errors=6", "snapshot-rejects=7",
		"published=8", "delivered=9", "dropped=10"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("done line lacks %q:\n%s", want, buf.String())
		}
	}
}

// With the context already cancelled, a live venue cannot fetch its scales, so setup fails
// without touching the network and the error comes back to the caller.
func TestNewSourceLiveSetupFailsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, venue := range []string{"binance", "kraken"} {
		t.Run(venue, func(t *testing.T) {
			src, _, err := newSource(ctx, quietLog(), Options{Live: true, Venue: venue, Symbol: "BTCUSDT", Seed: 1, Steps: 10})
			if err == nil {
				src.Close()
				t.Fatal("live setup with a cancelled context must error")
			}
		})
	}
}

// An edge that cannot serve (here: its listener is already closed) is logged and does not
// stop the pipeline. The source is gated so the pipeline outlives the Serve call: ending
// first would close the edge, and Serve would then report http.ErrServerClosed, which is
// deliberately not logged.
func TestRunLogsEdgeServeFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	gs := &gatedSource{
		snap:    market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		release: make(chan struct{}),
	}
	t.Cleanup(func() {
		select {
		case <-gs.release:
		default:
			close(gs.release)
		}
	})
	var buf syncBuffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	runDone := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), log, gs, Config{depth: 5, venue: "synthetic", symbol: "DEMO", HTTPListener: ln})
		runDone <- err
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), "http edge failed") {
		if time.Now().After(deadline) {
			t.Fatalf("serve failure not logged: %s", buf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gs.release)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("a failing edge must not fail the pipeline: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after the source ended")
	}
}

// syncBuffer is a bytes.Buffer safe to write from the edge goroutine while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// An interrupt during source setup is a shutdown, not a failure: the cause is returned for
// the caller to treat as a clean exit, and Prepare does not log it as an error.
func TestPrepareCancelledIsNotLoggedAsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	_, _, err := Prepare(ctx, log, Options{Live: true, Venue: "binance", Symbol: "BTCUSDT", Steps: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prepare error = %v, want it to wrap context.Canceled", err)
	}
	if strings.Contains(buf.String(), "source setup failed") {
		t.Fatalf("an interrupt was logged as a setup failure:\n%s", buf.String())
	}
}

// The pace wait must end when the context is cancelled, not after the whole pause.
func TestRunPaceStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, quietLog(), synthetic(1, 100), Config{depth: 5, pace: time.Hour})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // let it reach the first pace wait
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop while waiting out the pace")
	}
}

// A connection that is mid-request keeps Shutdown waiting. After the (shortened) bound the
// edge must fall back to Close, which drops that connection, instead of leaving it open.
func TestRunClosesEdgeWhenShutdownTimesOut(t *testing.T) {
	old := edgeShutdownTimeout
	edgeShutdownTimeout = 100 * time.Millisecond
	t.Cleanup(func() { edgeShutdownTimeout = old })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	gs := &gatedSource{
		snap:    market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		release: make(chan struct{}),
	}
	t.Cleanup(func() {
		select {
		case <-gs.release:
		default:
			close(gs.release)
		}
	})
	runDone := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), quietLog(), gs, Config{depth: 5, venue: "synthetic", symbol: "DEMO", HTTPListener: ln})
		runDone <- err
	}()
	if _, err := conn.Write([]byte("GET /metrics HTTP/1.1\r\nHost: x\r\n")); err != nil { // headers never finished
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // let the edge accept the connection and read the partial request
	close(gs.release)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("read succeeded on a connection that should have been closed")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("the stalled connection was still open 2s after Run returned: Close was not used after Shutdown timed out")
	}
}

// logTop prints prices and sizes at the feed's scale.
func TestLogTopScaled(t *testing.T) {
	var buf bytes.Buffer
	v := &book.View{LastSeq: 7,
		Bids: []market.Level{{Price: 6400050, Size: 1}},
		Asks: []market.Level{{Price: 6400100, Size: 25}}}
	logTop(slog.New(slog.NewTextHandler(&buf, nil)), v, [2]int{2, 3})
	for _, want := range []string{"bid=\"64000.50 x 0.001\"", "ask=\"64001.00 x 0.025\""} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log lacks %s:\n%s", want, buf.String())
		}
	}
}

// A real setup failure that happens to coincide with a cancelled context is still logged:
// only context.Canceled itself is treated as an interrupt.
func TestPrepareLogsRealFailureEvenIfCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buf bytes.Buffer
	_, _, err := Prepare(ctx, slog.New(slog.NewTextHandler(&buf, nil)), Options{Live: true, Venue: "okx", Symbol: "X", Steps: 1})
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("Prepare error = %v, want the unknown-venue error", err)
	}
	if !strings.Contains(buf.String(), "source setup failed") {
		t.Fatalf("a real failure was not logged: %s", buf.String())
	}
}

// The app's retry delay is jittered and capped: every value falls in [0, 8s].
func TestSnapshotBackoffIsBounded(t *testing.T) {
	for attempt := 0; attempt < 40; attempt++ {
		for i := 0; i < 50; i++ {
			if d := snapshotBackoff(attempt); d < 0 || d > 8*time.Second {
				t.Fatalf("snapshotBackoff(%d) = %v, want within [0, 8s]", attempt, d)
			}
		}
	}
	if d := snapshotBackoff(0); d > 500*time.Millisecond {
		t.Fatalf("first retry delay %v exceeds the 500ms minimum bound", d)
	}
}

// A configuration the engine refuses comes back from Run as that error, and the source is
// still released.
func TestRunRefusesInvalidEngineConfig(t *testing.T) {
	ct := &closeTracker{Source: synthetic(1, 5)}
	_, err := Run(context.Background(), quietLog(), ct, Config{depth: 0})
	if !errors.Is(err, book.ErrConfig) {
		t.Fatalf("Run error = %v, want book.ErrConfig", err)
	}
	if !ct.closed {
		t.Fatal("the source must be closed when the engine cannot be built")
	}
}

// The log reader is an internal consumer, so it must still receive views even though
// the hub does not count it. The gate keeps Run alive until the top-of-book line shows,
// because closing the hub empties the slots and an unread view would never be logged.
func TestRunLogConsumerReceivesViews(t *testing.T) {
	gs := &gatedSource{
		snap:    market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		release: make(chan struct{}),
	}
	release := func() {
		select {
		case <-gs.release:
		default:
			close(gs.release)
		}
	}
	t.Cleanup(release)
	var buf syncBuffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	runDone := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), log, gs, Config{depth: 5, every: 1, venue: "synthetic", symbol: "DEMO"})
		runDone <- err
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), "top of book") {
		if time.Now().After(deadline) {
			t.Fatalf("the log consumer never logged a view: %s", buf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	release()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after the source ended")
	}
}
