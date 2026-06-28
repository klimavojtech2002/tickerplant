package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

var _ source.Source = (*Source)(nil)

// ErrSnapshotStatus is returned when the REST depth endpoint replies with a non-200
// (e.g. a 429 rate limit); the caller decides whether to back off and retry.
var ErrSnapshotStatus = errors.New("binance: unexpected snapshot status")

// Config configures a Binance adapter. Frames is the stream of raw depthUpdate JSON
// frames — fed by the WebSocket layer in production, by a channel in tests — so the
// adapter's logic is testable without a socket. SnapshotURL is the REST /api/v3/depth
// endpoint for the symbol; Client defaults to http.DefaultClient.
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
	cfg    Config
	scales scales
	client *http.Client
	done   chan struct{}
	once   sync.Once
}

// New builds a Binance adapter from cfg.
func New(cfg Config) *Source {
	c := cfg.Client
	if c == nil {
		c = http.DefaultClient
	}
	return &Source{
		cfg:    cfg,
		scales: scales{price: cfg.PriceScale, size: cfg.SizeScale},
		client: c,
		done:   make(chan struct{}),
	}
}

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
			return source.Event{Kind: source.EventDelta, Delta: d}, true
		}
	}
}

// Snapshot fetches a fresh REST depth snapshot. Bootstrap and every resync call it, so
// it must return the current book; a non-200 is a loud error (ErrSnapshotStatus).
func (s *Source) Snapshot(ctx context.Context) (market.Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.SnapshotURL, nil)
	if err != nil {
		return market.Snapshot{}, fmt.Errorf("binance snapshot request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return market.Snapshot{}, fmt.Errorf("binance snapshot: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return market.Snapshot{}, fmt.Errorf("%w: %d", ErrSnapshotStatus, resp.StatusCode)
	}
	var d restDepth
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return market.Snapshot{}, fmt.Errorf("binance snapshot decode: %w", err)
	}
	return toSnapshot(s.cfg.Venue, s.cfg.Symbol, s.scales, d)
}

// Close releases the source; further Next calls return false. Safe to call once; the
// WebSocket layer (added with the live transport) owns the underlying connection.
func (s *Source) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
