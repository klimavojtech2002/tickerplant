package binance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
	"github.com/klimavojtech2002/tickerplant/internal/venue"
)

var _ source.Source = (*Source)(nil)

// ErrSnapshotStatus is returned when the REST depth endpoint replies with a non-200
// (e.g. a 429 rate limit); the caller decides whether to back off and retry.
var ErrSnapshotStatus = errors.New("binance: unexpected snapshot status")

// snapshotBodyLimit bounds a depth reply. A limit=5000 snapshot is about 0.3 MB, so 4 MiB is
// far above any real reply and still small enough to hold in memory.
const snapshotBodyLimit = 4 << 20

// Config configures a Binance adapter. Frames is the stream of raw depthUpdate JSON
// frames — fed by the WebSocket layer in production, by a channel in tests — so the
// adapter's logic is testable without a socket. SnapshotURL is the REST /api/v3/depth
// endpoint for the symbol; Client defaults to one with venue.DefaultHTTPTimeout.
type Config struct {
	Venue       market.Venue
	Symbol      market.Symbol
	PriceScale  int
	SizeScale   int
	Frames      <-chan []byte
	SnapshotURL string
	Client      *http.Client
}

// Source is the Binance transport-port adapter. It implements source.Source so the
// engine consumes it identically to the synthetic source.
type Source struct {
	cfg     Config
	scales  scales
	client  *http.Client
	done    chan struct{}
	once    sync.Once
	onClose func() // optional teardown (e.g. closing the live WebSocket), set by Live
}

// New builds a Binance adapter from cfg.
func New(cfg Config) *Source {
	c := cfg.Client
	if c == nil {
		c = venue.NewHTTPClient(venue.DefaultHTTPTimeout)
	}
	return &Source{
		cfg:    cfg,
		scales: scales{price: cfg.PriceScale, size: cfg.SizeScale},
		client: c,
		done:   make(chan struct{}),
	}
}

// Scales returns the symbol's price and size decimal scales, so an edge (the HTTP
// view stream) can render ticks back to the venue's exact decimal strings.
func (s *Source) Scales() (price, size int) { return s.cfg.PriceScale, s.cfg.SizeScale }

// Next returns the next book update. It skips frames that are not depthUpdates
// (subscription acks, pings) and frames that fail to normalize — a skipped frame
// leaves a sequence hole, which the engine's gap check turns into a resync, so a bad
// frame degrades to a correct rebuild rather than a wrong book. It returns false when
// the frame channel closes, the context is cancelled, or the source is closed.
func (s *Source) Next(ctx context.Context) (source.Event, bool) {
	for {
		select {
		case <-ctx.Done():
			return source.Event{}, false
		case <-s.done:
			return source.Event{}, false
		case raw, ok := <-s.cfg.Frames:
			received := time.Now() // the frame just left the transport: the latency span starts here
			if !ok {
				return source.Event{}, false
			}
			var u depthUpdate
			if err := json.Unmarshal(raw, &u); err != nil || u.Event != "depthUpdate" {
				continue
			}
			d, err := toDelta(s.cfg.Venue, s.cfg.Symbol, s.scales, u)
			if err != nil {
				continue
			}
			return source.Event{Kind: source.EventDelta, Received: received, Delta: d}, true
		}
	}
}

// Snapshot fetches a fresh REST depth snapshot. Bootstrap and every resync call it, so
// it must return the current book; a non-200 is a loud error (ErrSnapshotStatus).
func (s *Source) Snapshot(ctx context.Context) (market.Snapshot, error) {
	body, status, err := venue.Get(ctx, s.client, s.cfg.SnapshotURL, snapshotBodyLimit)
	if err != nil {
		return market.Snapshot{}, fmt.Errorf("binance snapshot: %w", err)
	}
	if status != http.StatusOK {
		return market.Snapshot{}, venue.StatusErr(ErrSnapshotStatus, status)
	}
	var d restDepth
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&d); err != nil {
		return market.Snapshot{}, fmt.Errorf("binance snapshot decode: %w", err)
	}
	return toSnapshot(s.cfg.Venue, s.cfg.Symbol, s.scales, d)
}

// Close releases the source; further Next calls return false. Safe to call once; the
// WebSocket layer owns the underlying connection.
func (s *Source) Close() error {
	s.once.Do(func() {
		close(s.done)
		if s.onClose != nil {
			s.onClose()
		}
	})
	return nil
}
