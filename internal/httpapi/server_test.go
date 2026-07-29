package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/metrics"
)

// TestMain is the goroutine-leak gate (plan 0007 acceptance): after every test and
// with idle HTTP connections dropped, the goroutine count must return to the
// baseline — a wedged SSE handler or an unreleased hub consumer fails the package.
func TestMain(m *testing.M) {
	base := runtime.NumGoroutine()
	code := m.Run()
	if code == 0 {
		http.DefaultClient.CloseIdleConnections()
		deadline := time.Now().Add(5 * time.Second)
		for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
			runtime.Gosched()
		}
		if n := runtime.NumGoroutine(); n > base {
			buf := make([]byte, 1<<16)
			fmt.Fprintf(os.Stderr, "goroutine leak: %d > baseline %d\n%s\n", n, base, buf[:runtime.Stack(buf, true)])
			code = 1
		}
	}
	os.Exit(code)
}

// stubEngine feeds the handler fixed counters and a settable current view.
type stubEngine struct {
	view                                            *book.View
	gaps, resyncs, disconnects, crosses, mismatches int
}

func (e *stubEngine) View() *book.View        { return e.view }
func (e *stubEngine) Gaps() int               { return e.gaps }
func (e *stubEngine) Resyncs() int            { return e.resyncs }
func (e *stubEngine) Disconnects() int        { return e.disconnects }
func (e *stubEngine) WouldCrosses() int       { return e.crosses }
func (e *stubEngine) ChecksumMismatches() int { return e.mismatches }

func view(seq market.Sequence) *book.View {
	return &book.View{
		LastSeq: seq,
		Bids:    []market.Level{{Price: 627217, Size: 135823999}},
		Asks:    []market.Level{{Price: 627218, Size: 114202}},
	}
}

func newEdge(t *testing.T, hub *delivery.Hub, eng Engine, lat *metrics.Histogram, writeTimeout time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(Config{
		Hub: hub, Engine: eng,
		Venue: "kraken", Symbol: "BTC/USD",
		PriceScale: 1, SizeScale: 8,
		Latency: lat, WriteTimeout: writeTimeout,
	}))
	t.Cleanup(srv.Close)
	return srv
}

// openStream connects to /stream and returns a line scanner over the SSE body.
func openStream(t *testing.T, url string) (*http.Response, *bufio.Scanner) {
	t.Helper()
	resp, err := http.Get(url + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if ao := resp.Header.Get("Access-Control-Allow-Origin"); ao != "*" {
		t.Fatalf("Allow-Origin = %q; without it a browser EventSource on another origin cannot connect", ao)
	}
	return resp, bufio.NewScanner(resp.Body)
}

// nextView reads SSE lines until one view event's data payload is decoded.
func nextView(t *testing.T, sc *bufio.Scanner) viewJSON {
	t.Helper()
	for sc.Scan() {
		line := sc.Text()
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var v viewJSON
			if err := json.Unmarshal([]byte(data), &v); err != nil {
				t.Fatalf("bad event payload %q: %v", data, err)
			}
			return v
		}
	}
	t.Fatalf("stream ended before an event arrived: %v", sc.Err())
	return viewJSON{}
}

// The opener must be the engine's current view — the whole (re)connect story under
// latest-wins — and the hub's next publish must follow in order with the venue's
// decimal strings, never a crossed render.
func TestStreamOpenerThenPublishes(t *testing.T) {
	hub := delivery.New(1 << 20)
	defer hub.Close()
	eng := &stubEngine{view: view(5)}
	srv := newEdge(t, hub, eng, nil, 0)

	_, sc := openStream(t, srv.URL)
	first := nextView(t, sc)
	if first.Seq != 5 || first.Venue != "kraken" || first.Symbol != "BTC/USD" {
		t.Fatalf("opener = %+v, want the current view seq 5", first)
	}
	if first.Bids[0] != [2]string{"62721.7", "1.35823999"} || first.Asks[0] != [2]string{"62721.8", "0.00114202"} {
		t.Fatalf("levels = %v / %v, want the venue's decimal strings", first.Bids[0], first.Asks[0])
	}
	if first.Bids[0][0] >= first.Asks[0][0] { // same width, so string compare is numeric here
		t.Fatal("opener renders a crossed book")
	}

	hub.Publish(view(6))
	if second := nextView(t, sc); second.Seq != 6 {
		t.Fatalf("second event seq = %d, want 6", second.Seq)
	}
}

// A hub view no newer than the opener must not be re-sent: the opener/subscription
// seam dedupes on the sequence, so a client never sees a duplicate or a rewind.
func TestStreamDedupesOpenerSeam(t *testing.T) {
	hub := delivery.New(1 << 20)
	defer hub.Close()
	eng := &stubEngine{view: view(5)}
	srv := newEdge(t, hub, eng, nil, 0)

	_, sc := openStream(t, srv.URL)
	if first := nextView(t, sc); first.Seq != 5 {
		t.Fatalf("opener seq = %d, want 5", first.Seq)
	}
	hub.Publish(view(5)) // the same view raced into the slot: must be skipped
	hub.Publish(view(7))
	if got := nextView(t, sc); got.Seq != 7 {
		t.Fatalf("post-opener seq = %d, want 7 (the stale 5 must be deduped)", got.Seq)
	}
}

// While the client does not read, publishes must conflate at the hub (bounded memory,
// freshest wins) — and once the client resumes, the freshest view arrives. ADR-0015
// through a real HTTP connection.
func TestSlowClientConflatesThroughHTTP(t *testing.T) {
	hub := delivery.New(1 << 20) // huge maxLag: conflate, never disconnect
	defer hub.Close()
	eng := &stubEngine{view: view(1)}
	srv := newEdge(t, hub, eng, nil, 5*time.Second)

	_, sc := openStream(t, srv.URL)
	if first := nextView(t, sc); first.Seq != 1 {
		t.Fatalf("opener seq = %d", first.Seq)
	}

	// Publish without reading until the hub starts superseding (Dropped grows) —
	// whether because buffers filled or because a publish landed between the
	// handler's wakeup and Take. Bounded, no sleeps: Publish never blocks by design.
	var last market.Sequence
	deadline := time.After(5 * time.Second)
	for seq := market.Sequence(2); hub.Stats().Dropped == 0; seq++ {
		select {
		case <-deadline:
			t.Fatal("hub never conflated: backpressure did not reach the hub")
		default:
		}
		hub.Publish(view(seq))
		last = seq
		runtime.Gosched()
	}

	// Resume reading: the stream must reach the freshest published view.
	readDeadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(readDeadline) {
			t.Fatalf("freshest view (seq %d) never arrived after conflation", last)
		}
		if v := nextView(t, sc); v.Seq == uint64(last) {
			break
		}
	}
	if hub.Stats().Dropped == 0 {
		t.Fatal("no supersedes counted; the test proved nothing")
	}
}

// A client that never reads must be disconnected by the hub once its supersede streak
// passes maxLag, and the write deadline must unwedge the handler goroutine parked in
// the blocked write — the response terminates instead of leaking.
func TestLaggingClientDisconnectedAndUnwedged(t *testing.T) {
	hub := delivery.New(2) // tiny tolerance
	defer hub.Close()
	eng := &stubEngine{view: view(1)}
	srv := newEdge(t, hub, eng, nil, 200*time.Millisecond)

	resp, _ := openStream(t, srv.URL) // never read from it

	deadline := time.After(5 * time.Second)
	for seq := market.Sequence(2); hub.Stats().Consumers != 0; seq++ {
		select {
		case <-deadline:
			t.Fatalf("hub never disconnected the lagging consumer: %+v", hub.Stats())
		default:
		}
		hub.Publish(view(seq))
		runtime.Gosched()
	}

	// The handler may be parked in a write the client will never drain; the deadline
	// must end the response. Reading now drains what was buffered, then hits the cut.
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("response never terminated: the write deadline did not unwedge the handler")
	}
}

// Closing the hub must end every open stream promptly.
func TestHubCloseEndsStream(t *testing.T) {
	hub := delivery.New(8)
	eng := &stubEngine{view: view(3)}
	srv := newEdge(t, hub, eng, nil, 0)

	resp, sc := openStream(t, srv.URL)
	if first := nextView(t, sc); first.Seq != 3 {
		t.Fatalf("opener seq = %d", first.Seq)
	}
	hub.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sc.Scan() {
		}
		resp.Body.Close()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end after the hub closed")
	}
}

// A client hanging up must release its hub subscription (the request context path).
func TestClientDisconnectUnsubscribes(t *testing.T) {
	hub := delivery.New(8)
	defer hub.Close()
	eng := &stubEngine{view: view(3)}
	srv := newEdge(t, hub, eng, nil, 0)

	resp, sc := openStream(t, srv.URL)
	_ = nextView(t, sc)
	if c := hub.Stats().Consumers; c != 1 {
		t.Fatalf("consumers = %d, want 1 while connected", c)
	}
	resp.Body.Close()
	deadline := time.After(5 * time.Second)
	for hub.Stats().Consumers != 0 {
		select {
		case <-deadline:
			t.Fatal("handler never unsubscribed after the client hung up")
		default:
			hub.Publish(view(4)) // wake the handler so it notices the dead context
			runtime.Gosched()
		}
	}
}

// /metrics must report the exact counters and stats — observability that doesn't lie.
func TestMetricsExact(t *testing.T) {
	hub := delivery.New(8)
	defer hub.Close()
	c := hub.Subscribe()
	_ = c
	hub.Publish(view(1)) // delivered = 1
	hub.Publish(view(2)) // superseded -> dropped = 1

	lat := metrics.NewHistogram()
	for range 100 {
		lat.Record(100 * time.Microsecond)
	}
	eng := &stubEngine{gaps: 1, resyncs: 2, disconnects: 3, crosses: 4, mismatches: 5}
	srv := newEdge(t, hub, eng, lat, 0)

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if ao := resp.Header.Get("Access-Control-Allow-Origin"); ao != "*" {
		t.Fatalf("Allow-Origin = %q; the dashboard polls /metrics cross-origin in dev", ao)
	}
	var doc metricsJSON
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Venue != "kraken" || doc.Symbol != "BTC/USD" {
		t.Fatalf("identity = %s/%s", doc.Venue, doc.Symbol)
	}
	if doc.Delivery.Consumers != 1 || doc.Delivery.Delivered != 1 || doc.Delivery.Dropped != 1 {
		t.Fatalf("delivery = %+v, want 1/1/1", doc.Delivery)
	}
	if doc.Engine != (engineJSON{Gaps: 1, Resyncs: 2, Disconnects: 3, WouldCrosses: 4, ChecksumMismatches: 5}) {
		t.Fatalf("engine = %+v", doc.Engine)
	}
	if doc.Latency == nil || doc.Latency.Count != 100 {
		t.Fatalf("latency = %+v, want count 100", doc.Latency)
	}
	if doc.Latency.Max != "100µs" {
		t.Fatalf("latency max = %q, want the exact recorded sample", doc.Latency.Max)
	}
	for _, q := range []string{doc.Latency.P50, doc.Latency.P99, doc.Latency.P999} {
		d, err := time.ParseDuration(q)
		if err != nil {
			t.Fatalf("quantile %q does not parse: %v", q, err)
		}
		if d < 98*time.Microsecond || d > 102*time.Microsecond {
			t.Fatalf("quantile %v outside the histogram's 2%% band around 100µs", d)
		}
	}
}

// Without a latency histogram the field is omitted, not zeroed — absent means "not
// measured", never "measured as zero".
func TestMetricsOmitsAbsentLatency(t *testing.T) {
	hub := delivery.New(8)
	defer hub.Close()
	srv := newEdge(t, hub, &stubEngine{}, nil, 0)
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["latency"]; present {
		t.Fatal("latency must be omitted when nothing records it")
	}
}

// A nil engine view means "nothing published yet": the stream opens and waits for the
// first publish instead of sending an empty opener.
func TestStreamNoOpenerBeforeFirstPublish(t *testing.T) {
	hub := delivery.New(8)
	defer hub.Close()
	srv := newEdge(t, hub, &stubEngine{view: nil}, nil, 0)
	_, sc := openStream(t, srv.URL)
	// openStream's http.Get returns once the server flushes headers, which happens
	// before the handler calls Hub.Subscribe (server.go's stream, by design: it
	// unblocks EventSource's onopen on a quiet feed even before subscribing). A
	// Publish racing ahead of that Subscribe would never reach this client, and
	// since this test's engine has no opener view, there would be nothing else to
	// wake it — an unrecoverable hang, not a flake. Wait for the subscription first,
	// the same pattern already used below (TestStreamDegradedWriters).
	deadline := time.After(5 * time.Second)
	for hub.Stats().Consumers != 1 {
		select {
		case <-deadline:
			t.Fatal("handler never subscribed")
		default:
			runtime.Gosched()
		}
	}
	hub.Publish(view(9))
	if first := nextView(t, sc); first.Seq != 9 {
		t.Fatalf("first event seq = %d, want 9 (no empty opener)", first.Seq)
	}
}

// writeView must refuse writers that cannot take a deadline (an httptest recorder):
// without the deadline the dead-client unwedge guarantee would silently not exist.
func TestWriteViewRequiresDeadlineSupport(t *testing.T) {
	s := &server{cfg: Config{WriteTimeout: time.Second, PriceScale: 1, SizeScale: 8}}
	if s.writeView(httptest.NewRecorder(), view(1)) {
		t.Fatal("a writer without deadline support must be rejected")
	}
}

// The encode-failure log path: a writer that fails mid-encode must not panic and must
// be loggable (the error branch is real on a dead connection).
type failingWriter struct{ hdr http.Header }

func (f *failingWriter) Header() http.Header       { return f.hdr }
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }
func (f *failingWriter) WriteHeader(int)           {}

func TestMetricsEncodeFailureIsHandled(t *testing.T) {
	hub := delivery.New(8)
	defer hub.Close()
	s := &server{cfg: Config{Hub: hub, Engine: &stubEngine{}, Log: quietLog()}}
	s.metrics(&failingWriter{hdr: http.Header{}}, nil) // must not panic
}

func quietLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// A cancelled request context must end the handler even when no publish ever wakes
// it — the quiet-feed hang-up path.
func TestStreamEndsOnContextCancel(t *testing.T) {
	hub := delivery.New(8)
	defer hub.Close()
	s := &server{cfg: Config{Hub: hub, Engine: &stubEngine{}, WriteTimeout: time.Second, Log: quietLog()}}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.stream(httptest.NewRecorder(), req)
	}()
	deadline := time.After(5 * time.Second)
	for hub.Stats().Consumers != 1 {
		select {
		case <-deadline:
			t.Fatal("handler never subscribed")
		default:
			runtime.Gosched()
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not end on context cancellation")
	}
}

// deadlineWriter supports flush and write deadlines but fails every write — the shape
// of a connection that died after the response opened.
type deadlineWriter struct{ hdr http.Header }

func (d *deadlineWriter) Header() http.Header              { return d.hdr }
func (d *deadlineWriter) Write([]byte) (int, error)        { return 0, errors.New("client gone") }
func (d *deadlineWriter) WriteHeader(int)                  {}
func (d *deadlineWriter) FlushError() error                { return nil }
func (d *deadlineWriter) SetWriteDeadline(time.Time) error { return nil }

// The degraded-writer paths must all end the handler cleanly, never panic or hang:
// no flush support (headers cannot open the stream), no deadline support (the
// unwedge guarantee would silently not exist), and a write that fails mid-event.
func TestStreamDegradedWriters(t *testing.T) {
	hub := delivery.New(8)
	defer hub.Close()
	s := &server{cfg: Config{Hub: hub, Engine: &stubEngine{view: view(1)}, WriteTimeout: time.Second, Log: quietLog()}}
	req := httptest.NewRequest(http.MethodGet, "/stream", nil)

	// no Flusher at all: the opening flush fails before any subscription
	s.stream(&failingWriter{hdr: http.Header{}}, req)
	if c := hub.Stats().Consumers; c != 0 {
		t.Fatalf("consumers = %d after a failed open, want 0", c)
	}

	// flushes but no deadline support: the opener write must be refused
	s.stream(httptest.NewRecorder(), req)
	if c := hub.Stats().Consumers; c != 0 {
		t.Fatalf("consumers = %d after a deadline-less writer, want 0 (handler returned and unsubscribed)", c)
	}

	// deadline-capable writer whose write fails: the in-loop write path returns
	s2 := &server{cfg: Config{Hub: hub, Engine: &stubEngine{}, WriteTimeout: time.Second, Log: quietLog()}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s2.stream(&deadlineWriter{hdr: http.Header{}}, req)
	}()
	deadline := time.After(5 * time.Second)
	for hub.Stats().Consumers != 1 { // wait for the handler's subscription
		select {
		case <-deadline:
			t.Fatal("handler never subscribed")
		default:
			runtime.Gosched()
		}
	}
	hub.Publish(view(2)) // wakes the handler; its write fails; it must exit
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not exit after a failed event write")
	}
}
