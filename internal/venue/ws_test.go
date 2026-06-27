package venue

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func fast() Backoff { return Backoff{Min: time.Millisecond, Max: 5 * time.Millisecond, Factor: 2} }

func recv(t *testing.T, frames <-chan []byte) string {
	t.Helper()
	select {
	case f, ok := <-frames:
		if !ok {
			t.Fatal("frame channel closed unexpectedly")
		}
		return string(f)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a frame")
		return ""
	}
}

func waitClosed(t *testing.T, frames <-chan []byte) {
	t.Helper()
	for {
		select {
		case _, ok := <-frames:
			if !ok {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatal("frame channel was not closed")
		}
	}
}

// --- a scripted fake connection (the conn seam), so the loop logic is deterministic ---

type readResult struct {
	data []byte
	err  error
}

type fakeConn struct {
	feed     chan readResult
	wrote    chan []byte
	writeErr error
	closed   atomic.Bool
}

func newFakeConn() *fakeConn {
	return &fakeConn{feed: make(chan readResult, 8), wrote: make(chan []byte, 8)}
}

func (f *fakeConn) read(ctx context.Context) ([]byte, error) {
	select {
	case r := <-f.feed:
		return r.data, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeConn) write(_ context.Context, data []byte) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.wrote <- data
	return nil
}

func (f *fakeConn) close() { f.closed.Store(true) }

// dialerOf returns conns/errors in sequence; the last entry repeats for further dials.
func dialerOf(steps ...func() (conn, error)) dialer {
	var i atomic.Int32
	return func(context.Context, string) (conn, error) {
		n := int(i.Add(1)) - 1
		if n >= len(steps) {
			n = len(steps) - 1
		}
		return steps[n]()
	}
}

func returns(c conn) func() (conn, error) { return func() (conn, error) { return c, nil } }

func TestStreamForwardsFrames(t *testing.T) {
	fc := newFakeConn()
	fc.feed <- readResult{data: []byte("a")}
	fc.feed <- readResult{data: []byte("b")}
	s := Dial(context.Background(), StreamConfig{Backoff: fast(), dial: dialerOf(returns(fc))})
	defer s.Close()
	if recv(t, s.Frames()) != "a" || recv(t, s.Frames()) != "b" {
		t.Fatal("frames not forwarded in order")
	}
}

func TestStreamResubscribesAndReconnectsAfterReadError(t *testing.T) {
	c1, c2 := newFakeConn(), newFakeConn()
	c1.feed <- readResult{data: []byte("x")}
	c1.feed <- readResult{err: errors.New("dropped")} // read error -> reconnect
	c2.feed <- readResult{data: []byte("y")}
	s := Dial(context.Background(), StreamConfig{
		Subscribe: [][]byte{[]byte("sub")}, Backoff: fast(),
		dial: dialerOf(returns(c1), returns(c2)),
	})
	defer s.Close()
	if recv(t, s.Frames()) != "x" || recv(t, s.Frames()) != "y" {
		t.Fatal("did not reconnect and deliver the second connection's frame")
	}
	if string(<-c1.wrote) != "sub" || string(<-c2.wrote) != "sub" {
		t.Fatal("the subscribe message must be re-sent on every connect")
	}
}

func TestStreamReconnectsAfterDialError(t *testing.T) {
	fc := newFakeConn()
	fc.feed <- readResult{data: []byte("ok")}
	var dials atomic.Int32
	d := func(context.Context, string) (conn, error) {
		if dials.Add(1) <= 2 {
			return nil, errors.New("dial refused")
		}
		return fc, nil
	}
	s := Dial(context.Background(), StreamConfig{Backoff: fast(), dial: d})
	defer s.Close()
	if recv(t, s.Frames()) != "ok" {
		t.Fatal("did not recover after dial errors")
	}
	if dials.Load() < 3 {
		t.Fatalf("expected retries, got %d dials", dials.Load())
	}
}

func TestStreamReconnectsAfterSubscribeWriteError(t *testing.T) {
	bad := newFakeConn()
	bad.writeErr = errors.New("write failed")
	good := newFakeConn()
	good.feed <- readResult{data: []byte("ok")}
	s := Dial(context.Background(), StreamConfig{
		Subscribe: [][]byte{[]byte("sub")}, Backoff: fast(),
		dial: dialerOf(returns(bad), returns(good)),
	})
	defer s.Close()
	if recv(t, s.Frames()) != "ok" {
		t.Fatal("a subscribe write error must trigger a reconnect")
	}
	if !bad.closed.Load() {
		t.Fatal("the failed connection must be closed")
	}
}

// A silent-but-open connection must be detected by the read deadline and reconnected,
// not hang forever (dead-connection detection).
func TestStreamReadTimeoutReconnects(t *testing.T) {
	var dials atomic.Int32
	d := func(context.Context, string) (conn, error) {
		dials.Add(1)
		return newFakeConn(), nil // never feeds: read blocks until the deadline
	}
	s := Dial(context.Background(), StreamConfig{Backoff: fast(), ReadTimeout: 5 * time.Millisecond, dial: d})
	defer s.Close()
	deadline := time.After(2 * time.Second)
	for dials.Load() < 2 { // a second dial proves the silent connection was dropped and reconnected
		select {
		case <-deadline:
			t.Fatalf("read timeout did not reconnect (dials=%d)", dials.Load())
		default:
		}
	}
}

func TestStreamCloseClosesFramesIdempotent(t *testing.T) {
	fc := newFakeConn()
	fc.feed <- readResult{data: []byte("hi")}
	s := Dial(context.Background(), StreamConfig{Backoff: fast(), dial: dialerOf(returns(fc))})
	recv(t, s.Frames())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_ = s.Close() // idempotent
	waitClosed(t, s.Frames())
	if !fc.closed.Load() {
		t.Fatal("the connection must be closed when the stream stops")
	}
}

// signalHandler signals on each log record, so a test can act exactly when the loop
// reaches the "reconnecting" log line (immediately before the backoff wait).
type signalHandler struct{ ch chan struct{} }

func (h signalHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h signalHandler) Handle(context.Context, slog.Record) error {
	h.ch <- struct{}{}
	return nil
}
func (h signalHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h signalHandler) WithGroup(string) slog.Handler      { return h }

// Cancelling during the backoff wait must stop the loop promptly, not after the full
// delay (the wait's ctx.Done case).
func TestStreamCancelDuringBackoffWait(t *testing.T) {
	logged := make(chan struct{}, 1)
	d := func(context.Context, string) (conn, error) { return nil, errors.New("dial refused") }
	// a long Min: the loop parks in the wait, so only ctx.Done can end it quickly.
	s := Dial(context.Background(), StreamConfig{
		Backoff: Backoff{Min: 10 * time.Second, Max: 10 * time.Second, Factor: 2},
		Logger:  slog.New(signalHandler{ch: logged}),
		dial:    d,
	})
	<-logged // the loop has logged and is now in the 10s backoff wait
	start := time.Now()
	_ = s.Close()
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("Close waited %v — it did not interrupt the backoff wait", waited)
	}
}

// --- one real-server sanity test exercises realDial/wsConn against the library ---

func TestRealDialReceivesFrames(t *testing.T) {
	gotSub := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		_, sub, _ := c.Read(context.Background()) // the subscribe (exercises wsConn.write)
		gotSub <- string(sub)
		_ = c.Write(context.Background(), websocket.MessageText, []byte("real-1"))
		_ = c.Write(context.Background(), websocket.MessageText, []byte("real-2"))
		_, _, _ = c.Read(context.Background()) // hold open until the client disconnects
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	s := Dial(context.Background(), StreamConfig{
		URL: url, Subscribe: [][]byte{[]byte("subscribe-me")}, Backoff: fast(), DialTimeout: 2 * time.Second,
	})
	defer s.Close()
	if recv(t, s.Frames()) != "real-1" || recv(t, s.Frames()) != "real-2" {
		t.Fatal("real WebSocket transport did not deliver frames")
	}
	if got := <-gotSub; got != "subscribe-me" {
		t.Fatalf("server received subscribe %q, want %q", got, "subscribe-me")
	}
}

func TestRealDialError(t *testing.T) {
	// port 0 is not connectable, so the real dial fails fast.
	if _, err := realDial(context.Background(), "ws://127.0.0.1:0"); err == nil {
		t.Fatal("dial to an invalid address must error")
	}
}
