package kraken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/venue"
)

// Default Kraken endpoints, verified against the live docs and API (2026-07-05): the
// v2 WebSocket at wss://ws.kraken.com/v2 (subscribe → in-band snapshot, then updates),
// pair precision from REST /0/public/AssetPairs, which accepts the v2 symbol form
// ("BTC/USD") and reports pair_decimals / lot_decimals matching the wire digits.
const (
	defaultRESTBase = "https://api.kraken.com"
	defaultWSBase   = "wss://ws.kraken.com"
)

// BookDepth is the subscribed book depth and therefore the engine's window
// (WithMaxDepth): Kraken never deletes levels that fall outside it, and its checksum
// covers exactly the top 10, so subscribing deeper would only add unmaintained levels
// the integrity guard cannot see.
const BookDepth = 10

// ErrPairsStatus is returned when the AssetPairs endpoint replies non-200.
var ErrPairsStatus = errors.New("kraken: unexpected AssetPairs status")

// LiveConfig configures a live Kraken source. Only Symbol is required, in the v2 form
// ("BTC/USD").
type LiveConfig struct {
	Symbol   string
	RESTBase string // default defaultRESTBase
	WSBase   string // default defaultWSBase
	Client   *http.Client
	Logger   *slog.Logger // transport logs (reconnects); nil discards
}

func (cfg LiveConfig) resolve() (restBase, wsBase string, client *http.Client) {
	restBase, wsBase, client = cfg.RESTBase, cfg.WSBase, cfg.Client
	if restBase == "" {
		restBase = defaultRESTBase
	}
	if wsBase == "" {
		wsBase = defaultWSBase
	}
	if client == nil {
		client = http.DefaultClient
	}
	return restBase, wsBase, client
}

// Live connects the Kraken adapter to the real venue: it reads the symbol's price and
// quantity scales from AssetPairs, dials the v2 book WebSocket, and returns a Source
// the engine consumes like any other. Wire the checksum with
// engine.WithChecksum(kraken.Checksum) — without it Kraken has no integrity guard at
// all, since the feed carries no sequence. The Source's Close tears down the socket.
func Live(ctx context.Context, cfg LiveConfig) (*Source, error) {
	if cfg.Symbol == "" {
		return nil, fmt.Errorf("kraken: empty symbol")
	}
	restBase, wsBase, client := cfg.resolve()

	priceScale, qtyScale, err := fetchScales(ctx, client, restBase, cfg.Symbol)
	if err != nil {
		return nil, err
	}

	// %q JSON-escapes the symbol, so the literal needs no marshal (and no dead error path).
	sub := fmt.Appendf(nil, `{"method":"subscribe","params":{"channel":"book","symbol":[%q],"depth":%d}}`, cfg.Symbol, BookDepth)
	dial := func() *venue.Stream {
		return venue.Dial(ctx, venue.StreamConfig{
			URL:         wsBase + "/v2",
			Subscribe:   [][]byte{sub},
			Backoff:     venue.Backoff{Min: time.Second, Max: 30 * time.Second, Factor: 2, Jitter: venue.FullJitter},
			DialTimeout: 10 * time.Second,
			ReadTimeout: 10 * time.Second, // Kraken heartbeats every second; a longer silence is a dead socket
			Logger:      cfg.Logger,
		})
	}

	// stream is reassigned by Redial (engine goroutine) and closed by onClose, which
	// Close makes safe to call from any goroutine — so the handoff is mutex-owned:
	// close and redial serialize, and a redial that loses the race to Close dials
	// nothing, so no stream can outlive the source.
	var mu sync.Mutex
	closed := false
	stream := dial()
	src := New(Config{
		Venue:      "kraken",
		Symbol:     market.Symbol(cfg.Symbol),
		PriceScale: priceScale,
		QtyScale:   qtyScale,
		Frames:     stream.Frames(),
		// A drift resync needs a fresh in-band snapshot, which only a new subscription
		// serves; reconnecting is the documented recovery shape (correctness.md §8) and
		// keeps one recovery path for both disconnect and drift.
		Redial: func() <-chan []byte {
			mu.Lock()
			defer mu.Unlock()
			if closed {
				ch := make(chan []byte)
				close(ch) // a closed source redials nothing; the caller sees a closed stream
				return ch
			}
			_ = stream.Close()
			stream = dial()
			return stream.Frames()
		},
	})
	src.onClose = func() {
		mu.Lock()
		defer mu.Unlock()
		closed = true
		_ = stream.Close()
	}
	return src, nil
}

// assetPairs is the AssetPairs response: result is keyed by pair name, and the two
// decimals fields are the wire precisions the book feed (and so the checksum) uses.
type assetPairs struct {
	Error  []string `json:"error"`
	Result map[string]struct {
		PairDecimals int `json:"pair_decimals"`
		LotDecimals  int `json:"lot_decimals"`
	} `json:"result"`
}

// fetchScales reads the symbol's pair_decimals (price) and lot_decimals (quantity)
// from AssetPairs, so normalization and the checksum need no hardcoded precision.
func fetchScales(ctx context.Context, client *http.Client, restBase, symbol string) (priceScale, qtyScale int, err error) {
	url := restBase + "/0/public/AssetPairs?pair=" + symbol
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("kraken AssetPairs request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("kraken AssetPairs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("%w: %d", ErrPairsStatus, resp.StatusCode)
	}
	var pairs assetPairs
	if err := json.NewDecoder(resp.Body).Decode(&pairs); err != nil {
		return 0, 0, fmt.Errorf("kraken AssetPairs decode: %w", err)
	}
	if len(pairs.Error) > 0 {
		return 0, 0, fmt.Errorf("kraken AssetPairs %q: %v: %w", symbol, pairs.Error, market.ErrMalformed)
	}
	// The result key is Kraken's internal pair name, not necessarily the query form
	// (BTC/USD ↔ XXBTZUSD), so take the single entry rather than looking the key up.
	if len(pairs.Result) != 1 {
		return 0, 0, fmt.Errorf("kraken AssetPairs %q: %d pairs in response, want 1: %w", symbol, len(pairs.Result), market.ErrMalformed)
	}
	for _, p := range pairs.Result {
		priceScale, qtyScale = p.PairDecimals, p.LotDecimals
	}
	return priceScale, qtyScale, nil
}
