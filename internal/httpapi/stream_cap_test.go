package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/delivery"
)

// capEdge serves the stream with the given cap behind the real chain.
func capEdge(t *testing.T, hub *delivery.Hub, maxStreams int, log *slog.Logger, ts *tickSource) *httptest.Server {
	t.Helper()
	cfg := Config{Hub: hub, Engine: &stubEngine{view: view(1)}, PriceScale: 1, SizeScale: 8, MaxStreams: maxStreams, Log: log}
	if ts != nil {
		cfg.ticker = ts.factory
	}
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// waitConsumers waits until the hub counts exactly n consumers.
func waitConsumers(t *testing.T, hub *delivery.Hub, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for hub.Stats().Consumers != n {
		select {
		case <-deadline:
			t.Fatalf("hub counts %d consumers, want %d", hub.Stats().Consumers, n)
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// open starts a stream request and returns the response; the caller closes the body.
func open(t *testing.T, url string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url+"/stream", nil)
	req.Header.Set("Origin", "https://app.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// A full edge refuses the next stream with a response a browser can read, and starts nothing.
func TestStreamCapRefusesWith503(t *testing.T) {
	var sink logSink
	hub := newHub(t)
	ts := newTickSource()
	srv := capEdge(t, hub, 2, sink.logger(slog.LevelInfo), ts)
	for range 2 {
		resp := open(t, srv.URL)
		t.Cleanup(func() { resp.Body.Close() })
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("a stream under the cap got %d, want 200", resp.StatusCode)
		}
	}
	waitConsumers(t, hub, 2)
	// A handler subscribes first and starts its ticker after, so a consumer count of 2 does not yet
	// mean both tickers exist. Wait for them, or the count below could include a late one.
	for deadline := time.Now().Add(5 * time.Second); ts.opened.Load() < 2; {
		if time.Now().After(deadline) {
			t.Fatalf("%d tickers started for 2 streams", ts.opened.Load())
		}
		time.Sleep(2 * time.Millisecond)
	}
	opened := ts.opened.Load()

	resp := open(t, srv.URL)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	for k, want := range map[string]string{
		"Retry-After":                 "5",
		"Cache-Control":               "no-store",
		"Access-Control-Allow-Origin": "*",
		"X-Content-Type-Options":      "nosniff",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "" {
		t.Errorf("X-Accel-Buffering = %q on a refusal, want none", got)
	}
	if string(body) != "too many streams" || strings.HasPrefix(string(body), "event:") {
		t.Errorf("body = %q, want the refusal phrase and no event", body)
	}
	if n := hub.Stats().Consumers; n != 2 {
		t.Errorf("the hub counts %d consumers after a refusal, want the 2 that were already there", n)
	}
	if ts.opened.Load() != opened {
		t.Error("a ticker was started for a refused stream")
	}
	sink.waitForLine(t, "route=\"GET /stream\"", "status=503")
	if n := len(sink.lines("route=\"GET /stream\"", "status=503")); n != 1 {
		t.Errorf("%d access lines for the refusal, want 1", n)
	}
}

// A slot is free again once the handler of a closed stream has unsubscribed.
func TestStreamCapFreesASlot(t *testing.T) {
	hub := newHub(t)
	srv := capEdge(t, hub, 1, nil, nil)
	first := open(t, srv.URL)
	waitConsumers(t, hub, 1)
	if resp := open(t, srv.URL); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a second stream got %d, want 503", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	first.Body.Close()
	waitConsumers(t, hub, 0) // the slot is freed when the handler unsubscribes, not when the client closes
	second := open(t, srv.URL)
	defer second.Body.Close()
	if second.StatusCode != http.StatusOK {
		t.Fatalf("after the slot was freed a stream got %d, want 200", second.StatusCode)
	}
}

// Exactly the cap is served however many arrive at once.
func TestStreamCapUnderConcurrency(t *testing.T) {
	hub := newHub(t)
	srv := capEdge(t, hub, 5, nil, nil)
	results := make(chan *http.Response, 20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { results <- open(t, srv.URL) })
	}
	wg.Wait()
	close(results)
	ok, refused := 0, 0
	for resp := range results {
		switch resp.StatusCode {
		case http.StatusOK:
			ok++
			t.Cleanup(func() { resp.Body.Close() }) // held open until the assertions are done
		case http.StatusServiceUnavailable:
			refused++
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		default:
			t.Errorf("unexpected status %d", resp.StatusCode)
		}
	}
	if ok != 5 || refused != 15 {
		t.Fatalf("%d served and %d refused, want 5 and 15", ok, refused)
	}
	if n := hub.Stats().Consumers; n != 5 {
		t.Fatalf("hub counts %d consumers, want 5", n)
	}
}

// With no cap configured the limit is 256: with 255 consumers already there a stream is served, with
// 256 it is refused. The numbers are literals on purpose: built from the constant, the test could
// not notice the constant changing.
func TestStreamCapDefault(t *testing.T) {
	for _, tc := range []struct {
		prefilled int
		want      int
	}{{255, http.StatusOK}, {256, http.StatusServiceUnavailable}} {
		hub := newHub(t)
		for range tc.prefilled {
			hub.Subscribe()
		}
		srv := capEdge(t, hub, 0, nil, nil)
		resp := open(t, srv.URL)
		if resp.StatusCode != tc.want {
			t.Errorf("%d consumers already: status %d, want %d", tc.prefilled, resp.StatusCode, tc.want)
		}
		resp.Body.Close()
	}
}

// The log reader is not a viewer and uses up no slot.
func TestStreamCapIgnoresInternalConsumers(t *testing.T) {
	hub := newHub(t)
	hub.SubscribeInternal()
	srv := capEdge(t, hub, 1, nil, nil)
	resp := open(t, srv.URL)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: an internal consumer must not count", resp.StatusCode)
	}
}

// A closed hub means the process is stopping: 503, with no hint to try again.
func TestStreamOnAClosedHubIs503WithoutRetryAfter(t *testing.T) {
	hub := delivery.New(8)
	srv := capEdge(t, hub, 0, nil, nil)
	hub.Close()
	resp := open(t, srv.URL)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable || string(body) != "shutting down" {
		t.Fatalf("got %d %q, want 503 shutting down", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q on a stopping edge, want none", got)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("the refusal must carry the cross-origin header")
	}
}
