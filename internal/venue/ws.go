package venue

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/coder/websocket"
)

// maxFrameBytes bounds a single WebSocket message. Diff frames are small; an in-band
// snapshot (OKX/Kraken) is larger, so the limit is generous but not unbounded — it
// caps the allocation a hostile or buggy server can force.
const maxFrameBytes = 1 << 20

// conn is the minimal connection the stream needs. The real one wraps the WebSocket
// library; tests inject a fake, so the reconnect/backoff/liveness logic is exercised
// deterministically without a socket — the same sans-I/O split the engine uses.
type conn interface {
	read(ctx context.Context) ([]byte, error)
	write(ctx context.Context, data []byte) error
	close()
}

// dialer opens a conn to url. The default dials a real WebSocket; tests inject a fake.
type dialer func(ctx context.Context, url string) (conn, error)

// StreamConfig configures a reconnecting WebSocket stream.
type StreamConfig struct {
	URL         string        // ws:// or wss:// endpoint
	Subscribe   [][]byte      // text messages sent on every (re)connect, e.g. subscribe requests
	Backoff     Backoff       // reconnect delay policy
	BufferSize  int           // frame channel capacity (default 256)
	DialTimeout time.Duration // bound the handshake (0 = none)
	ReadTimeout time.Duration // reconnect if no frame arrives within this, to catch a dead-but-open
	// connection (0 = none). Must exceed the venue's heartbeat interval.
	Logger *slog.Logger

	dial dialer // unexported test seam; nil = the real WebSocket dialer
}

// Stream is a reconnecting source of raw WebSocket frames. It dials the URL, sends the
// subscribe messages, and forwards every received message to Frames(); on any
// connection error — including a read-deadline timeout on a silent connection — it
// reconnects with backoff and re-subscribes. Reconnection is transparent to the
// adapter: the sequence discontinuity after a reconnect drives the engine's resync
// (correctness.md §8), so this layer carries no book logic.
type Stream struct {
	cfg    StreamConfig
	frames chan []byte
	cancel context.CancelFunc
	done   chan struct{}
}

// Dial starts a Stream and its background connection loop. The loop runs until ctx is
// cancelled or Close is called; either way Frames() is then closed.
func Dial(ctx context.Context, cfg StreamConfig) *Stream {
	if cfg.BufferSize < 1 {
		cfg.BufferSize = 256
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.dial == nil {
		cfg.dial = realDial
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Stream{
		cfg:    cfg,
		frames: make(chan []byte, cfg.BufferSize),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go s.run(ctx)
	return s
}

// Frames returns the channel of raw frames. It is closed when the stream stops.
func (s *Stream) Frames() <-chan []byte { return s.frames }

// Close stops the stream and waits for its loop to finish; Frames() is closed by the
// time Close returns. Idempotent and safe under concurrent calls.
func (s *Stream) Close() error {
	s.cancel()
	<-s.done
	return nil
}

func (s *Stream) run(ctx context.Context) {
	defer close(s.done)
	defer close(s.frames) // runs first (LIFO): Frames is closed before Close's wait returns
	for attempt := 0; ; attempt++ {
		delivered, err := s.session(ctx)
		if ctx.Err() != nil {
			return // cancelled or closed
		}
		if delivered {
			attempt = 0 // a productive session resets the backoff
		}
		wait := s.cfg.Backoff.Next(attempt)
		s.cfg.Logger.Warn("websocket reconnecting", "url", s.cfg.URL, "in", wait, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// session holds one connection: dial, subscribe, then forward frames until an error,
// a silent-connection read timeout, or cancellation. delivered reports whether any
// frame was forwarded (to reset backoff). It returns a nil error only on cancellation.
func (s *Stream) session(ctx context.Context) (delivered bool, err error) {
	dialCtx := ctx
	if s.cfg.DialTimeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, s.cfg.DialTimeout)
		defer cancel()
	}
	c, err := s.cfg.dial(dialCtx, s.cfg.URL)
	if err != nil {
		return false, err
	}
	defer c.close()

	for _, msg := range s.cfg.Subscribe {
		if err := c.write(ctx, msg); err != nil {
			return delivered, err
		}
	}
	for {
		data, err := s.readFrame(ctx, c)
		if err != nil {
			return delivered, err
		}
		select {
		case s.frames <- data:
			delivered = true
		case <-ctx.Done():
			// Cancellation must win even when a stalled consumer has filled the buffer,
			// so Close never wedges behind a slow reader.
			return delivered, ctx.Err()
		}
	}
}

// readFrame reads one frame, applying the read deadline if configured. A timeout on a
// silent connection surfaces as an error, so the loop reconnects rather than hanging.
func (s *Stream) readFrame(ctx context.Context, c conn) ([]byte, error) {
	if s.cfg.ReadTimeout <= 0 {
		return c.read(ctx)
	}
	rctx, cancel := context.WithTimeout(ctx, s.cfg.ReadTimeout)
	defer cancel()
	return c.read(rctx)
}

// realDial opens a real WebSocket connection.
func realDial(ctx context.Context, url string) (conn, error) {
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(maxFrameBytes)
	return &wsConn{c: c}, nil
}

// wsConn adapts a *websocket.Conn to the conn seam.
type wsConn struct{ c *websocket.Conn }

func (w *wsConn) read(ctx context.Context) ([]byte, error) {
	_, data, err := w.c.Read(ctx)
	return data, err
}

func (w *wsConn) write(ctx context.Context, data []byte) error {
	return w.c.Write(ctx, websocket.MessageText, data)
}

func (w *wsConn) close() { _ = w.c.CloseNow() }
