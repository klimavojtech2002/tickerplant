package httpapi

import (
	"bytes"
	"context"
	"errors"
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

func newHub(t *testing.T) *delivery.Hub {
	t.Helper()
	h := delivery.New(8)
	t.Cleanup(h.Close)
	return h
}

// logSink is a log buffer safe to write from the server goroutines while a test reads it.
type logSink struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *logSink) logger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(s, &slog.HandlerOptions{Level: level}))
}

// lines returns the log lines that contain every given part.
func (s *logSink) lines(parts ...string) []string {
	var out []string
	for _, l := range strings.Split(s.String(), "\n") {
		ok := l != ""
		for _, p := range parts {
			ok = ok && strings.Contains(l, p)
		}
		if ok {
			out = append(out, l)
		}
	}
	return out
}

// waitForLine polls until a log line with all the parts exists.
func (s *logSink) waitForLine(t *testing.T, parts ...string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ls := s.lines(parts...); len(ls) > 0 {
			return ls[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no log line with %v:\n%s", parts, s.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// routeAttr is how the route appears in a text log line: slog quotes a value only when it
// has a space.
func routeAttr(route string) string {
	if strings.Contains(route, " ") {
		return "route=\"" + route + "\""
	}
	return "route=" + route
}

// serve runs next behind the edge's chain on a real server, so the behaviour of the
// connection (aborts, flushes) is what a client sees.
func serve(t *testing.T, log *slog.Logger, corsOrigin string, next http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(chain(next, log, corsOrigin))
	t.Cleanup(srv.Close)
	return srv
}

// get sends a request with http.DefaultClient (the package's leak gate closes its idle
// connections) and returns the response and its whole body.
func get(t *testing.T, method, url string, hdr map[string]string) (*http.Response, string, error) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp, string(body), err
}

// Every response carries the same headers, the mux's own 404 and 405 included.
func TestHeadersOnEveryRoute(t *testing.T) {
	hub := newHub(t)
	hub.Publish(view(1))
	for _, policy := range []string{"", "https://app.example"} {
		h, err := New(Config{Hub: hub, Engine: &stubEngine{view: view(1)}, PriceScale: 1, SizeScale: 8, CORSOrigin: policy})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		for _, rt := range []struct {
			name, method, path string
			stream             bool
		}{
			{"stream", http.MethodGet, "/stream", true},
			{"metrics", http.MethodGet, "/metrics", false},
			{"not found", http.MethodGet, "/nope", false},
			{"method not allowed", http.MethodPost, "/metrics", false},
		} {
			for _, origin := range []string{"", "https://app.example", "https://other.example"} {
				name := rt.name + "/policy=" + policy + "/origin=" + origin
				t.Run(name, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					req, _ := http.NewRequestWithContext(ctx, rt.method, srv.URL+rt.path, nil)
					if origin != "" {
						req.Header.Set("Origin", origin)
					}
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					hd := resp.Header
					if hd.Get("X-Content-Type-Options") != "nosniff" {
						t.Errorf("nosniff missing: %v", hd)
					}
					if policy == "" {
						if hd.Get("Access-Control-Allow-Origin") != "*" || len(hd.Values("Vary")) != 0 {
							t.Errorf("default policy: want allow-origin * and no Vary, got %v", hd)
						}
					} else {
						if hd.Get("Vary") != "Origin" {
							t.Errorf("single origin: want Vary: Origin, got %v", hd)
						}
						got, has := hd["Access-Control-Allow-Origin"]
						if origin == policy && (!has || got[0] != policy) {
							t.Errorf("matching origin: want it echoed, got %v", hd)
						}
						if origin != policy && has {
							t.Errorf("origin %q: want no allow-origin header, got %v", origin, hd)
						}
					}
					if got := hd.Get("X-Accel-Buffering"); (got == "no") != rt.stream {
						t.Errorf("X-Accel-Buffering = %q, want it on /stream only", got)
					}
				})
			}
		}
	}
}

func TestRecoverAnswers500AndKeepsServing(t *testing.T) {
	var sink logSink
	calls := 0
	srv := serve(t, sink.logger(slog.LevelInfo), "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			panic("boom")
		}
		io.WriteString(w, "fine")
	}))
	resp, _, err := get(t, http.MethodGet, srv.URL, nil)
	if err != nil || resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("first request = %v, %v; want a 500", resp, err)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("the 500 must carry the edge headers: %v", resp.Header)
	}
	if ls := sink.lines("handler panic", "boom"); len(ls) != 1 {
		t.Fatalf("want one panic line naming the value, got %d:\n%s", len(ls), sink.String())
	}
	if ls := sink.lines("request", "status=500"); len(ls) != 1 {
		t.Fatalf("want one access line with status 500, got %d:\n%s", len(ls), sink.String())
	}
	if resp, body, err := get(t, http.MethodGet, srv.URL, nil); err != nil || body != "fine" || resp.StatusCode != 200 {
		t.Fatalf("second request = %v, %q, %v; the server must keep serving", resp, body, err)
	}
}

// http.ErrAbortHandler is the standard way to cut a response off: it is not an error to log.
func TestRecoverPassesAbortHandlerOn(t *testing.T) {
	var sink logSink
	srv := serve(t, sink.logger(slog.LevelInfo), "", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	if _, _, err := get(t, http.MethodGet, srv.URL, nil); err == nil {
		t.Fatal("an aborted response must reach the client as an error")
	}
	line := sink.waitForLine(t, "request")
	if !strings.Contains(line, "status=0") {
		t.Errorf("no response was sent, so the access line status must be 0: %s", line)
	}
	if strings.Contains(sink.String(), "handler panic") {
		t.Errorf("an abort is not logged as a panic:\n%s", sink.String())
	}
}

// Once bytes are written a 500 cannot be sent, and a body cut short must not look whole:
// the connection is aborted, so the client sees an error and never a clean end.
func TestRecoverAbortsWhenTheResponseHasStarted(t *testing.T) {
	for _, tc := range []struct {
		name         string
		write, flush bool
	}{{"written", true, false}, {"written and flushed", true, true}, {"flushed only", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			var sink logSink
			srv := serve(t, sink.logger(slog.LevelInfo), "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.write {
					io.WriteString(w, "partial")
				}
				if tc.flush {
					_ = http.NewResponseController(w).Flush()
				}
				panic("late")
			}))
			resp, _, err := get(t, http.MethodGet, srv.URL, nil)
			if err == nil {
				t.Fatalf("a response cut short by a panic reached the client as complete (status %d)", resp.StatusCode)
			}
			sink.waitForLine(t, "handler panic", "late")
			line := sink.waitForLine(t, "request")
			if !strings.Contains(line, "status=200") {
				t.Errorf("the access line must keep the status already sent: %s", line)
			}
		})
	}
}

func TestAccessLog(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /explicit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "12345")
	})
	mux.HandleFunc("GET /implicit", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "abc") })
	mux.HandleFunc("GET /silent", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })

	run := func(t *testing.T, level slog.Level) *logSink {
		var sink logSink
		srv := serve(t, sink.logger(level), "", mux)
		for _, p := range []string{"/explicit", "/implicit", "/silent", "/healthz", "/readyz", "/secret-path?token=abc123"} {
			get(t, http.MethodGet, srv.URL+p, nil)
		}
		for _, route := range []string{"GET /explicit", "GET /implicit", "GET /silent", "unmatched"} {
			sink.waitForLine(t, routeAttr(route))
		}
		return &sink
	}

	t.Run("info", func(t *testing.T) {
		sink := run(t, slog.LevelInfo)
		for route, want := range map[string][]string{
			"GET /explicit": {"status=201", "bytes=5", "method=GET"},
			"GET /implicit": {"status=200", "bytes=3"},
			"GET /silent":   {"status=200", "bytes=0"},
			"unmatched":     {"status=404"},
		} {
			ls := sink.lines(routeAttr(route))
			if len(ls) != 1 {
				t.Fatalf("route %s: %d lines, want 1:\n%s", route, len(ls), sink.String())
			}
			for _, w := range want {
				if !strings.Contains(ls[0], w) {
					t.Errorf("route %s: line lacks %q: %s", route, w, ls[0])
				}
			}
			if !strings.Contains(ls[0], "duration=") {
				t.Errorf("route %s: line has no duration: %s", route, ls[0])
			}
		}
		if len(sink.lines("healthz")) != 0 || len(sink.lines("readyz")) != 0 {
			t.Errorf("health polls must not be logged at Info:\n%s", sink.String())
		}
		for _, leak := range []string{"secret-path", "token", "abc123", "127.0.0.1"} {
			if strings.Contains(sink.String(), leak) {
				t.Errorf("the log leaks %q:\n%s", leak, sink.String())
			}
		}
	})
	t.Run("debug", func(t *testing.T) {
		sink := run(t, slog.LevelDebug)
		sink.waitForLine(t, "route=\"GET /healthz\"", "level=DEBUG")
		sink.waitForLine(t, "route=\"GET /readyz\"", "level=DEBUG")
		if ls := sink.lines("route=\"GET /explicit\""); len(ls) != 1 || !strings.Contains(ls[0], "level=INFO") {
			t.Errorf("other routes log at Info: %v", ls)
		}
	})
}

// A stream that lasts logs one line, when it ends.
func TestAccessLogWritesOneLineForAStreamWhenItEnds(t *testing.T) {
	var sink logSink
	hub := newHub(t)
	h, err := New(Config{Hub: hub, Engine: &stubEngine{view: view(1)}, PriceScale: 1, SizeScale: 8, Log: sink.logger(slog.LevelInfo)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	if ls := sink.lines("route=\"GET /stream\""); len(ls) != 0 {
		t.Fatalf("the stream is still open, so nothing is logged yet:\n%s", sink.String())
	}
	cancel()
	resp.Body.Close()
	sink.waitForLine(t, "route=\"GET /stream\"", "status=200")
	time.Sleep(50 * time.Millisecond)
	if n := len(sink.lines("route=\"GET /stream\"")); n != 1 {
		t.Fatalf("%d lines for the stream, want 1:\n%s", n, sink.String())
	}
}

// failingFlusher is a ResponseWriter that cannot flush.
type failingFlusher struct{ header http.Header }

func (f *failingFlusher) Header() http.Header         { return f.header }
func (f *failingFlusher) Write(p []byte) (int, error) { return len(p), nil }
func (f *failingFlusher) WriteHeader(int)             {}

func TestRecorder(t *testing.T) {
	t.Run("the first status wins", func(t *testing.T) {
		rec := &recorder{ResponseWriter: httptest.NewRecorder()}
		rec.WriteHeader(http.StatusCreated)
		rec.WriteHeader(http.StatusInternalServerError)
		if rec.status != http.StatusCreated {
			t.Fatalf("status = %d, want 201", rec.status)
		}
	})
	t.Run("a write starts an implicit 200 and counts bytes", func(t *testing.T) {
		inner := httptest.NewRecorder()
		rec := &recorder{ResponseWriter: inner}
		if rec.started() {
			t.Fatal("started before anything was written")
		}
		rec.Write([]byte("hello"))
		rec.Write([]byte("!!"))
		if rec.status != 200 || rec.bytes != 7 || !rec.started() || inner.Body.String() != "hello!!" {
			t.Fatalf("status %d, bytes %d, body %q", rec.status, rec.bytes, inner.Body.String())
		}
	})
	t.Run("a flush starts the response", func(t *testing.T) {
		inner := httptest.NewRecorder()
		rec := &recorder{ResponseWriter: inner}
		if err := rec.FlushError(); err != nil {
			t.Fatal(err)
		}
		if !rec.started() || rec.status != 200 || !inner.Flushed {
			t.Fatalf("started %v, status %d, flushed %v", rec.started(), rec.status, inner.Flushed)
		}
	})
	t.Run("a flush that cannot be done reports it and still starts the response", func(t *testing.T) {
		rec := &recorder{ResponseWriter: &failingFlusher{header: http.Header{}}}
		if err := rec.FlushError(); !errors.Is(err, http.ErrNotSupported) {
			t.Fatalf("err = %v, want ErrNotSupported", err)
		}
		if !rec.started() {
			t.Fatal("the attempt to flush must still count as starting the response")
		}
	})
	t.Run("Unwrap returns the real writer", func(t *testing.T) {
		inner := httptest.NewRecorder()
		if (&recorder{ResponseWriter: inner}).Unwrap() != http.ResponseWriter(inner) {
			t.Fatal("Unwrap must return the wrapped writer")
		}
	})
}

// Without Unwrap a write deadline cannot reach the connection and the stream fails to
// deliver; the full stream tests (newEdge) catch that, and this one names it.
func TestStreamDeliversThroughTheChain(t *testing.T) {
	hub := newHub(t)
	srv := newEdge(t, hub, &stubEngine{view: view(1)}, nil, 0)
	resp, sc := openStream(t, srv.URL)
	_ = resp
	if ev := nextView(t, sc); ev.Seq != 1 {
		t.Fatalf("opener seq = %d, want 1", ev.Seq)
	}
	hub.Publish(view(2))
	if ev := nextView(t, sc); ev.Seq != 2 {
		t.Fatalf("seq = %d, want 2", ev.Seq)
	}
}
