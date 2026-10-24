package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/metrics"
)

// statusEdge serves the status route behind the real chain.
func statusEdge(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	if cfg.Hub == nil {
		cfg.Hub = newHub(t)
	}
	if cfg.Engine == nil {
		cfg.Engine = &stubEngine{}
	}
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func statusBody(t *testing.T, srv *httptest.Server) (*http.Response, string) {
	t.Helper()
	resp, body, err := get(t, http.MethodGet, srv.URL+"/api/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

// The contract file the dashboard's tests read: the exact HTTP body of one deterministic
// document, newline included. Every counter is a different number, so swapping two of them
// cannot go unnoticed. The latency quantiles come from floating-point bucket arithmetic, so the
// file is guaranteed on the CI architecture; the test also checks each latency field against the
// histogram itself, so drift shows as a clear mismatch rather than a wrong field.
func TestStatusGolden(t *testing.T) {
	var clk clockMs
	clk.ms.Store(1_700_000_000_000)
	hub := delivery.New(1 << 20)
	defer hub.Close()
	c := hub.Subscribe() // one counted consumer
	hub.Publish(view(1))
	c.Take()
	hub.Publish(view(2)) // delivered 2
	hub.Publish(view(3))
	hub.Publish(view(4))
	hub.Publish(view(5)) // three superseded: dropped 3, published 5

	lat := metrics.NewHistogram()
	for _, ns := range []time.Duration{1000, 2000, 50000} { // well inside their buckets
		lat.Record(ns)
	}
	eng := &stubEngine{
		view: view(9), gaps: 1, resyncs: 2, disconnects: 3, crosses: 4, mismatches: 5, rejects: 6, snapErrors: 7,
		lastEventAt: clk.now().Add(-1500 * time.Millisecond),
	}
	srv := statusEdge(t, Config{
		Hub: hub, Engine: eng, Venue: "kraken", Symbol: "BTC/USD", PriceScale: 1, SizeScale: 8,
		Clock: clk.now, Rejected: func() uint64 { return 8 }, Latency: lat,
		Mode: "live", Version: "v1.2.3", StartedAt: time.UnixMilli(1_699_999_000_000), Depth: 10,
	})

	resp, body := statusBody(t, srv)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	const path = "testdata/status.json"
	if *update {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	if body != string(want) {
		t.Fatalf("status body differs from %s (run with -update to rewrite it if the change is intended):\n got %s\nwant %s", path, body, want)
	}

	var doc statusJSON
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	l := doc.Latency
	if l == nil || l.Count != lat.Count() || l.P50Ns != int64(lat.Percentile(0.50)) || l.P99Ns != int64(lat.Percentile(0.99)) ||
		l.P999N != int64(lat.Percentile(0.999)) || l.MaxNs != int64(lat.Max()) {
		t.Fatalf("latency block %+v does not match the histogram (count %d, p50 %v, p99 %v, p99.9 %v, max %v)",
			l, lat.Count(), lat.Percentile(0.50), lat.Percentile(0.99), lat.Percentile(0.999), lat.Max())
	}
}

func TestLastEventAgeMs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, tc := range []struct {
		name string
		last time.Time
		want int64
	}{
		{"no event yet", time.Time{}, -1},
		{"the epoch is an event, not 'none'", time.Unix(0, 0), now.Sub(time.Unix(0, 0)).Milliseconds()},
		{"1500.9 ms rounds down", now.Add(-1500*time.Millisecond - 900*time.Microsecond), 1500},
		{"999.9 ms rounds down", now.Add(-999*time.Millisecond - 900*time.Microsecond), 999},
		{"exactly 1 ms", now.Add(-time.Millisecond), 1},
		{"under 1 ms", now.Add(-999 * time.Microsecond), 0},
		{"now", now, 0},
		{"an event ahead of the clock is 0, not negative", now.Add(2 * time.Second), 0},
	} {
		if got := lastEventAgeMs(now, tc.last); got != tc.want {
			t.Errorf("%s: lastEventAgeMs = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// The age goes through the handler with the injected clock and the engine's stamp.
func TestStatusReportsTheAgeOfTheLastEvent(t *testing.T) {
	var clk clockMs
	clk.ms.Store(5_000)
	eng := &stubEngine{}
	srv := statusEdge(t, Config{Engine: eng, Clock: clk.now})
	age := func() int64 {
		_, body := statusBody(t, srv)
		var doc statusJSON
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		return doc.LastEventAgeMs
	}
	if got := age(); got != -1 {
		t.Fatalf("before any event: %d, want -1", got)
	}
	eng.lastEventAt = time.UnixMilli(3_000)
	srv = statusEdge(t, Config{Engine: eng, Clock: clk.now})
	if got := age(); got != 2000 {
		t.Fatalf("age = %d, want 2000", got)
	}
}

func TestStatusStartedAtIsZeroWhenUnset(t *testing.T) {
	srv := statusEdge(t, Config{})
	_, body := statusBody(t, srv)
	if !strings.Contains(body, `"startedAtUnixMs":0,`) {
		t.Fatalf("body = %s, want startedAtUnixMs 0 for an unset start", body)
	}
	srv = statusEdge(t, Config{StartedAt: time.UnixMilli(123_456)})
	_, body = statusBody(t, srv)
	if !strings.Contains(body, `"startedAtUnixMs":123456,`) {
		t.Fatalf("body = %s, want the start time in milliseconds", body)
	}
}

func TestStatusModeAndVersion(t *testing.T) {
	for mode, want := range map[string]string{"": "unknown", "synthetic": "synthetic", "live": "live"} {
		_, body := statusBody(t, statusEdge(t, Config{Mode: mode, Version: "dev"}))
		if !strings.Contains(body, `"mode":"`+want+`","chaos":false,"version":"dev"`) {
			t.Errorf("mode %q: body = %s, want mode %q", mode, body, want)
		}
	}
	_, body := statusBody(t, statusEdge(t, Config{}))
	if !strings.Contains(body, `"version":""`) {
		t.Errorf("an empty version is reported empty: %s", body)
	}
}

// The chaos flag is reported as set, and is false (not absent) when it is not.
func TestStatusReportsChaos(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chaos bool
		want  string
	}{{"on", true, `"chaos":true`}, {"off", false, `"chaos":false`}} {
		_, body := statusBody(t, statusEdge(t, Config{Mode: "synthetic", Chaos: tc.chaos}))
		if !strings.Contains(body, tc.want) {
			t.Errorf("chaos %s: body = %s, want %s", tc.name, body, tc.want)
		}
	}
}

// Without a histogram the latency key is left out, never zeroed; without a rejected function
// the count is 0.
func TestStatusWithoutLatencyOrRejected(t *testing.T) {
	_, body := statusBody(t, statusEdge(t, Config{}))
	if strings.Contains(body, "latency") {
		t.Fatalf("body = %s, want no latency key without a histogram", body)
	}
	if !strings.Contains(body, `"rejected":0`) {
		t.Fatalf("body = %s, want rejected 0 without a source count", body)
	}
	if !strings.HasPrefix(body, `{"schema":1,"mode":`) {
		t.Fatalf("body = %s, want the schema first", body)
	}
}

func TestStatusHeadersAndMethods(t *testing.T) {
	for _, policy := range []string{"", "https://app.example"} {
		srv := statusEdge(t, Config{CORSOrigin: policy})
		resp, body := statusBody(t, srv)
		hd := resp.Header
		if hd.Get("Content-Type") != "application/json" || hd.Get("Cache-Control") != "no-store" ||
			hd.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("policy %q: headers = %v", policy, hd)
		}
		if n, _ := strconv.Atoi(hd.Get("Content-Length")); n != len(body) {
			t.Errorf("Content-Length %q does not match the %d-byte body", hd.Get("Content-Length"), len(body))
		}
		if !strings.HasSuffix(body, "}\n") {
			t.Errorf("body should be one JSON object and a newline: %q", body)
		}
	}
	srv := statusEdge(t, Config{})
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		resp, _, err := get(t, method, srv.URL+"/api/status", nil)
		if err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s = %v, %v; want 405", method, resp, err)
		}
		if resp.Header.Get("Allow") == "" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: the 405 lacks Allow or the chain's headers: %v", method, resp.Header)
		}
	}
	resp, body, err := get(t, http.MethodHead, srv.URL+"/api/status", nil)
	if err != nil || resp.StatusCode != http.StatusOK || body != "" {
		t.Fatalf("HEAD = %v, %q, %v; want 200 with no body", resp, body, err)
	}
}

func TestStatusHeadersUnderAnotherOrigin(t *testing.T) {
	srv := statusEdge(t, Config{CORSOrigin: "https://app.example"})
	resp, _, err := get(t, http.MethodGet, srv.URL+"/api/status", map[string]string{"Origin": "https://app.example"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "https://app.example" || resp.Header.Get("Vary") != "Origin" {
		t.Fatalf("headers = %v", resp.Header)
	}
}

// The status route is polled, so it logs at debug level like the probes.
func TestStatusRouteIsLoggedAtDebug(t *testing.T) {
	var sink logSink
	hub := newHub(t)
	h, err := New(Config{Hub: hub, Engine: &stubEngine{}, Log: sink.logger(-4)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	statusBody(t, srv)
	sink.waitForLine(t, "route=\"GET /api/status\"", "level=DEBUG")
}
