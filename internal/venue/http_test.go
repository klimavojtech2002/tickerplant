package venue

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// A server that accepts the request and never answers must end the call within the
// client timeout, as a transient failure. The handler is released before the server closes,
// or Close would wait for it.
func TestGetTimesOutOnHungServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	_, _, err := Get(context.Background(), NewHTTPClient(50*time.Millisecond), srv.URL, 1<<20)
	if err == nil || !source.IsTransient(err) {
		t.Fatalf("err = %v, want a transient timeout", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("took %v, want about the 50ms timeout", took)
	}
}

// The caller's own cancellation is not a transient failure: retrying it would ignore a
// shutdown.
func TestGetCallerCancellationIsNotTransient(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, _, err := Get(ctx, NewHTTPClient(10*time.Second), srv.URL, 1<<20)
	if err == nil || source.IsTransient(err) {
		t.Fatalf("err = %v, want a non-transient cancellation", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
}

func TestGetConnectionRefusedIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	_, _, err := Get(context.Background(), NewHTTPClient(time.Second), url, 1024)
	if !source.IsTransient(err) {
		t.Fatalf("err = %v, want transient", err)
	}
}

func TestGetReturnsBodyAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		io.WriteString(w, "hello")
	}))
	defer srv.Close()
	c := NewHTTPClient(time.Second)

	b, st, err := Get(context.Background(), c, srv.URL, 1024)
	if err != nil || st != 200 || string(b) != "hello" {
		t.Fatalf("Get = %q, %d, %v; want hello, 200, nil", b, st, err)
	}
	b, st, err = Get(context.Background(), c, srv.URL+"/bad", 1024)
	if err != nil || st != 400 || b != nil {
		t.Fatalf("Get(400) = %q, %d, %v; want nil body, 400, nil error", b, st, err)
	}
}

func TestGetBodyOverLimitIsTransientTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, strings.Repeat("x", 100))
	}))
	defer srv.Close()
	_, _, err := Get(context.Background(), NewHTTPClient(time.Second), srv.URL, 99)
	if !errors.Is(err, ErrBodyTooLarge) || !source.IsTransient(err) {
		t.Fatalf("err = %v, want a transient ErrBodyTooLarge", err)
	}
}

// Exactly limit bytes is allowed; one more is not. A plain LimitReader would pass the
// second case and fail later as a decode error.
func TestReadLimitedBoundary(t *testing.T) {
	if b, err := ReadLimited(strings.NewReader("12345"), 5); err != nil || string(b) != "12345" {
		t.Fatalf("exactly at the limit: %q, %v", b, err)
	}
	if _, err := ReadLimited(strings.NewReader("123456"), 5); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("one over the limit: err = %v, want ErrBodyTooLarge", err)
	}
	if b, err := ReadLimited(strings.NewReader(""), 5); err != nil || len(b) != 0 {
		t.Fatalf("empty body: %q, %v", b, err)
	}
}

type endless struct{ n atomic.Int64 }

func (e *endless) Read(p []byte) (int, error) {
	e.n.Add(int64(len(p)))
	return len(p), nil
}

// An endless body must stop near the limit, not be read to exhaustion.
func TestReadLimitedStopsReading(t *testing.T) {
	var e endless
	_, err := ReadLimited(&e, 1<<16)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
	if got := e.n.Load(); got > 1<<20 {
		t.Fatalf("read %d bytes of an endless body, want a small multiple of the 64 KiB limit", got)
	}
}

var errSentinel = errors.New("sentinel")

func TestStatusErrClassification(t *testing.T) {
	cases := []struct {
		status    int
		transient bool
	}{
		{429, true}, {500, true}, {502, true}, {503, true}, {599, true},
		{400, false}, {401, false}, {403, false}, {404, false}, {418, false}, {301, false},
	}
	for _, c := range cases {
		err := StatusErr(errSentinel, c.status)
		if !errors.Is(err, errSentinel) {
			t.Errorf("%d: the sentinel must stay reachable: %v", c.status, err)
		}
		if got := source.IsTransient(err); got != c.transient {
			t.Errorf("%d: IsTransient = %v, want %v", c.status, got, c.transient)
		}
	}
}
