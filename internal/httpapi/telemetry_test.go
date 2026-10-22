package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/market"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// liveEngine is an Engine whose values a test changes while a stream handler is reading them.
type liveEngine struct {
	mu                                                             sync.Mutex
	view                                                           *book.View
	gaps, resyncs, disconnects, crosses, mismatches, rejects, errs int
	events                                                         []book.Event
}

func (e *liveEngine) set(f func(*liveEngine)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f(e)
}

func (e *liveEngine) get(f func(*liveEngine) int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return f(e)
}

func (e *liveEngine) View() *book.View {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.view
}
func (e *liveEngine) Gaps() int    { return e.get(func(e *liveEngine) int { return e.gaps }) }
func (e *liveEngine) Resyncs() int { return e.get(func(e *liveEngine) int { return e.resyncs }) }
func (e *liveEngine) Disconnects() int {
	return e.get(func(e *liveEngine) int { return e.disconnects })
}
func (e *liveEngine) WouldCrosses() int { return e.get(func(e *liveEngine) int { return e.crosses }) }
func (e *liveEngine) ChecksumMismatches() int {
	return e.get(func(e *liveEngine) int { return e.mismatches })
}
func (e *liveEngine) SnapshotRejects() int {
	return e.get(func(e *liveEngine) int { return e.rejects })
}
func (e *liveEngine) SnapshotErrors() int { return e.get(func(e *liveEngine) int { return e.errs }) }
func (e *liveEngine) Ready() bool         { return true }
func (e *liveEngine) Events() []book.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.events
}

// tickSource stands in for the per-connection ticker: the test sends the ticks and counts the stops.
type tickSource struct {
	ch     chan time.Time
	opened atomic.Int32 // tickers started
	stops  atomic.Int32
}

func newTickSource() *tickSource { return &tickSource{ch: make(chan time.Time, 1)} }

func (ts *tickSource) factory(time.Duration) (<-chan time.Time, func()) {
	ts.opened.Add(1)
	return ts.ch, func() { ts.stops.Add(1) }
}

func (ts *tickSource) tick() { ts.ch <- time.Now() }

// clockMs is a settable clock.
type clockMs struct{ ms atomic.Int64 }

func (c *clockMs) now() time.Time { return time.UnixMilli(c.ms.Load()) }

// telemetryEdge serves the stream behind a real server with the injected clock, ticker and
// rejected counter.
func telemetryEdge(t *testing.T, hub *delivery.Hub, eng Engine, ts *tickSource, clk *clockMs, rejected func() uint64) *httptest.Server {
	t.Helper()
	h, err := New(Config{
		Hub: hub, Engine: eng, PriceScale: 1, SizeScale: 8,
		Clock: clk.now, Rejected: rejected, ticker: ts.factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// readFrame reads one SSE frame (up to the blank line) and returns its event name and data.
func readFrame(t *testing.T, sc *bufio.Scanner) (name, data string) {
	t.Helper()
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if name != "" {
				return name, data
			}
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatalf("stream ended before a frame completed: %v", sc.Err())
	return "", ""
}

func decodeTelemetry(t *testing.T, data string) telemetryJSON {
	t.Helper()
	var tj telemetryJSON
	if err := json.Unmarshal([]byte(data), &tj); err != nil {
		t.Fatalf("bad telemetry payload %q: %v", data, err)
	}
	return tj
}

func TestTelemetryComesFirstThenTheOpener(t *testing.T) {
	var clk clockMs
	clk.ms.Store(1_700_000_000_000)
	for name, eng := range map[string]*liveEngine{
		"with a view":    {view: view(5)},
		"without a view": {},
	} {
		t.Run(name, func(t *testing.T) {
			hub := delivery.New(8)
			defer hub.Close()
			ts := newTickSource()
			srv := telemetryEdge(t, hub, eng, ts, &clk, nil)
			_, sc := openStream(t, srv.URL)

			ev, data := readFrame(t, sc)
			if ev != "telemetry" {
				t.Fatalf("first frame is %q, want telemetry", ev)
			}
			tj := decodeTelemetry(t, data)
			if tj.T != 1_700_000_000_000 || tj.Consumers != 1 {
				t.Fatalf("telemetry = %+v, want t from the clock and one consumer", tj)
			}
			if eng.view != nil {
				if tj.Seq != 5 {
					t.Fatalf("seq = %d, want the view's 5", tj.Seq)
				}
				if ev, _ := readFrame(t, sc); ev != "view" {
					t.Fatalf("second frame is %q, want the opener view", ev)
				}
			} else if tj.Seq != 0 {
				t.Fatalf("seq = %d with no view, want 0", tj.Seq)
			}
		})
	}
}

func TestTelemetryOnEveryTick(t *testing.T) {
	var clk clockMs
	clk.ms.Store(1_000)
	var rejected atomic.Uint64
	eng := &liveEngine{view: view(5)}
	hub := delivery.New(8)
	defer hub.Close()
	ts := newTickSource()
	srv := telemetryEdge(t, hub, eng, ts, &clk, rejected.Load)
	_, sc := openStream(t, srv.URL)

	ev, data := readFrame(t, sc) // the frame sent on connect: an empty log must be [], not null
	if ev != "telemetry" || !strings.Contains(data, `"events":[]`) {
		t.Fatalf("connect frame = %q %s, want a telemetry frame with an empty events list", ev, data)
	}
	if ev, _ := readFrame(t, sc); ev != "view" { // the opener
		t.Fatalf("second frame is %q, want the opener", ev)
	}

	// The fakes change between ticks; each frame reflects them at that moment.
	for round := 1; round <= 3; round++ {
		clk.ms.Store(1_000 + int64(round)*1_000)
		rejected.Store(uint64(10 * round))
		eng.set(func(e *liveEngine) {
			e.gaps, e.resyncs, e.disconnects = round, round+1, round+2
			e.crosses, e.mismatches, e.rejects, e.errs = round+3, round+4, round+5, round+6
			e.events = []book.Event{{ID: uint64(round), Kind: book.KindGap, AtUnixMs: 7, ExpectedSeq: 11, GotSeq: 15, Reason: "sequence gap", DurationMs: -1}}
			e.view = view(market.Sequence(5 + round))
		})
		ts.tick()
		ev, data := readFrame(t, sc)
		if ev != "telemetry" {
			t.Fatalf("round %d: frame is %q, want telemetry", round, ev)
		}
		tj := decodeTelemetry(t, data)
		want := telemetryJSON{
			T: 1_000 + int64(round)*1_000, Seq: uint64(5 + round),
			Gaps: round, Resyncs: round + 1, Disconnects: round + 2, WouldCrosses: round + 3,
			ChecksumMismatches: round + 4, SnapshotRejects: round + 5, SnapshotErrors: round + 6,
			Rejected: uint64(10 * round), Consumers: 1,
			Events: []eventJSON{{ID: uint64(round), Kind: "gap", AtUnixMs: 7, ExpectedSeq: 11, GotSeq: 15, Reason: "sequence gap", DurationMs: -1}},
		}
		if !reflect.DeepEqual(tj, want) {
			t.Fatalf("round %d: telemetry = %+v, want %+v", round, tj, want)
		}
		// One tick is one telemetry frame: the next frame is the view published now, not a second telemetry.
		hub.Publish(view(market.Sequence(100 + round)))
		if ev, _ := readFrame(t, sc); ev != "view" {
			t.Fatalf("round %d: the frame after a tick's telemetry is %q, want the published view", round, ev)
		}
	}
}

// Telemetry never goes through the hub and never counts as a view sent.
func TestTelemetryLeavesTheHubAndTheViewDedupeAlone(t *testing.T) {
	var clk clockMs
	hub := delivery.New(1 << 20)
	defer hub.Close()
	ts := newTickSource()
	srv := telemetryEdge(t, hub, &liveEngine{view: view(5)}, ts, &clk, nil)
	_, sc := openStream(t, srv.URL)
	readFrame(t, sc) // telemetry
	readFrame(t, sc) // opener
	before := hub.Stats()
	for range 5 {
		ts.tick()
		if ev, _ := readFrame(t, sc); ev != "telemetry" {
			t.Fatalf("frame %q, want telemetry", ev)
		}
	}
	if after := hub.Stats(); after != before {
		t.Fatalf("hub stats changed from %+v to %+v: telemetry must not touch the hub", before, after)
	}
	// A view with the opener's generation is still suppressed after the ticks: telemetry did not
	// move the last-sent generation. The next view the client sees is the newer one.
	hub.Publish(view(5))
	settle(t, hub, view(5), 2)
	hub.Publish(view(6))
	if got := nextView(t, sc); got.Seq != 6 {
		t.Fatalf("client saw seq %d, want 6", got.Seq)
	}
}

// With no events the list is [] and never null; a nil log from the engine renders the same way.
func TestTelemetryEmptyLogIsAnEmptyList(t *testing.T) {
	hub := delivery.New(8)
	defer hub.Close()
	s := &server{cfg: Config{Hub: hub, Engine: &stubEngine{}}}
	if got := string(s.telemetryPayload(nil)); !strings.Contains(got, `"events":[]`) || strings.Contains(got, "null") {
		t.Fatalf("payload = %s, want an empty events list and no null", got)
	}
}

// The contract file the dashboard's tests read: the exact payload of one deterministic frame.
func TestTelemetryGolden(t *testing.T) {
	var clk clockMs
	clk.ms.Store(1_700_000_000_000)
	hub := delivery.New(8)
	defer hub.Close()
	hub.Publish(view(1)) // Delivered/Published differ from Consumers, so a wrong figure shows
	ts := newTickSource()
	eng := &liveEngine{
		view: view(4182), gaps: 1, resyncs: 2, disconnects: 3, crosses: 4, mismatches: 5, rejects: 6, errs: 7,
		events: []book.Event{
			{ID: 1, Kind: book.KindGap, AtUnixMs: 1_699_999_990_000, ExpectedSeq: 4182, GotSeq: 4185, Reason: "sequence gap", DurationMs: 38},
			{ID: 2, Kind: book.KindDisconnect, AtUnixMs: 1_699_999_995_000, ExpectedSeq: 0, GotSeq: 0, Reason: "source disconnected", DurationMs: -1},
		},
	}
	srv := telemetryEdge(t, hub, eng, ts, &clk, func() uint64 { return 9 })
	_, sc := openStream(t, srv.URL)
	ev, data := readFrame(t, sc)
	if ev != "telemetry" {
		t.Fatalf("first frame is %q", ev)
	}
	const path = "testdata/telemetry.json"
	if *update {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	if data != string(want) {
		t.Fatalf("telemetry payload differs from %s (run with -update to rewrite it if the change is intended):\n got %s\nwant %s", path, data, want)
	}
}

// opsWriter records the order of write deadlines and writes.
type opsWriter struct {
	*httptest.ResponseRecorder
	mu  sync.Mutex
	ops []string
}

func (o *opsWriter) SetWriteDeadline(time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ops = append(o.ops, "deadline")
	return nil
}

func (o *opsWriter) Write(p []byte) (int, error) {
	o.mu.Lock()
	o.ops = append(o.ops, "write")
	o.mu.Unlock()
	return o.ResponseRecorder.Write(p)
}

func (o *opsWriter) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.ops...)
}

// Every write, of a view and of telemetry, is preceded by a fresh write deadline, so a client
// that stops reading ends the handler on either kind.
func TestEveryWriteIsPrecededByAWriteDeadline(t *testing.T) {
	hub := delivery.New(8)
	defer hub.Close()
	ts := newTickSource()
	s := &server{cfg: Config{Hub: hub, Engine: &stubEngine{view: view(1)}, WriteTimeout: time.Second, Log: quietLog(), ticker: ts.factory}}
	w := &opsWriter{ResponseRecorder: httptest.NewRecorder()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.stream(w, httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx))
	}()
	waitWrites := func(n int) {
		deadline := time.After(5 * time.Second)
		for countOf(w.snapshot(), "write") < n {
			select {
			case <-deadline:
				t.Fatalf("handler did not reach %d writes: %v", n, w.snapshot())
			default:
				runtime.Gosched()
			}
		}
	}
	waitWrites(2) // telemetry, then the opener view
	ts.tick()
	waitWrites(3) // a later telemetry frame
	hub.Publish(view(2))
	waitWrites(4) // a later view
	cancel()
	<-done

	ops := w.snapshot()
	for i, op := range ops {
		if op == "write" && (i == 0 || ops[i-1] != "deadline") {
			t.Fatalf("write %d was not preceded by a write deadline: %v", i, ops)
		}
	}
}

func countOf(ops []string, op string) int {
	n := 0
	for _, o := range ops {
		if o == op {
			n++
		}
	}
	return n
}

// The ticker is stopped on every way out of the handler.
func TestTickerIsStoppedOnEveryExit(t *testing.T) {
	newServer := func(hub *delivery.Hub, ts *tickSource) *server {
		return &server{cfg: Config{Hub: hub, Engine: &stubEngine{view: view(1)}, WriteTimeout: time.Second, Log: quietLog(), ticker: ts.factory}}
	}
	wait := func(t *testing.T, done chan struct{}) {
		t.Helper()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("handler did not exit")
		}
	}
	t.Run("context cancelled", func(t *testing.T) {
		hub := delivery.New(8)
		defer hub.Close()
		ts := newTickSource()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			newServer(hub, ts).stream(&deadlineRecorder{ResponseRecorder: httptest.NewRecorder()},
				httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx))
		}()
		waitForConsumer(t, hub)
		cancel()
		wait(t, done)
		if n := ts.stops.Load(); n != 1 {
			t.Fatalf("ticker stopped %d times, want 1", n)
		}
	})
	t.Run("hub closed", func(t *testing.T) {
		hub := delivery.New(8)
		ts := newTickSource()
		done := make(chan struct{})
		go func() {
			defer close(done)
			newServer(hub, ts).stream(&deadlineRecorder{ResponseRecorder: httptest.NewRecorder()},
				httptest.NewRequest(http.MethodGet, "/stream", nil))
		}()
		waitForConsumer(t, hub)
		hub.Close()
		wait(t, done)
		if n := ts.stops.Load(); n != 1 {
			t.Fatalf("ticker stopped %d times, want 1", n)
		}
	})
	t.Run("a write fails", func(t *testing.T) {
		hub := delivery.New(8)
		defer hub.Close()
		ts := newTickSource()
		done := make(chan struct{})
		go func() {
			defer close(done)
			newServer(hub, ts).stream(&deadlineWriter{hdr: http.Header{}}, httptest.NewRequest(http.MethodGet, "/stream", nil))
		}()
		wait(t, done)
		if n := ts.stops.Load(); n != 1 {
			t.Fatalf("ticker stopped %d times, want 1", n)
		}
	})
}

// A server built without New has no clock or ticker configured and still works.
func TestServerDefaultsClockAndTicker(t *testing.T) {
	s := &server{cfg: Config{}}
	if d := time.Since(s.now()); d < 0 || d > time.Minute {
		t.Fatalf("default clock is off by %v", d)
	}
	c, stop := s.newTicker()
	defer stop()
	if c == nil {
		t.Fatal("default ticker has no channel")
	}
}

// A telemetry write that fails on a tick ends the handler, like a failed view write.
func TestStreamEndsWhenATelemetryWriteFailsOnATick(t *testing.T) {
	hub := delivery.New(8)
	defer hub.Close()
	ts := newTickSource()
	s := &server{cfg: Config{Hub: hub, Engine: &stubEngine{view: view(1)}, WriteTimeout: time.Second, Log: quietLog(), ticker: ts.factory}}
	w := &flakyWriter{deadlineRecorder: &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}, okWrites: 2}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.stream(w, httptest.NewRequest(http.MethodGet, "/stream", nil))
	}()
	waitForConsumer(t, hub) // telemetry and the opener are written by now or about to be
	ts.tick()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not exit after a failed telemetry write on a tick")
	}
	if n := ts.stops.Load(); n != 1 {
		t.Fatalf("ticker stopped %d times, want 1", n)
	}
}
