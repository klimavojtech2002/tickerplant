package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/venue"
)

// Default Binance spot endpoints, verified against the live docs (2026-06-28):
// raw single-stream diff depth at wss://stream.binance.com:9443/ws/<lowersymbol>@depth
// (no subscribe message needed), REST snapshot and exchange info under api.binance.com.
const (
	defaultRESTBase = "https://api.binance.com"
	defaultWSBase   = "wss://stream.binance.com:9443"
)

// LiveConfig configures a live Binance source. Only Symbol is required (e.g. "BTCUSDT").
type LiveConfig struct {
	Symbol   string
	RESTBase string // default defaultRESTBase
	WSBase   string // default defaultWSBase
	Client   *http.Client
	Logger   *slog.Logger // transport logs (reconnects); nil discards
}

// resolve fills in the default endpoints and client.
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

// Live connects the Binance adapter to the real venue: it derives the symbol's price
// and size scales from exchange info, dials the diff-depth WebSocket, and returns a
// Source the engine consumes like any other. The Source's Close tears down the socket.
// The live path is integration-tested; the offline tests cover scale derivation.
func Live(ctx context.Context, cfg LiveConfig) (*Source, error) {
	if cfg.Symbol == "" {
		return nil, fmt.Errorf("binance: empty symbol")
	}
	restBase, wsBase, client := cfg.resolve()

	priceScale, sizeScale, err := fetchScales(ctx, client, restBase, cfg.Symbol)
	if err != nil {
		return nil, err
	}

	stream := venue.Dial(ctx, venue.StreamConfig{
		URL:         wsBase + "/ws/" + strings.ToLower(cfg.Symbol) + "@depth",
		Backoff:     venue.Backoff{Min: time.Second, Max: 30 * time.Second, Factor: 2, Jitter: venue.FullJitter},
		DialTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, // Binance depth ticks well under this; a longer silence is a dead socket
		Logger:      cfg.Logger,
	})

	src := New(Config{
		Venue:       "binance",
		Symbol:      market.Symbol(cfg.Symbol),
		PriceScale:  priceScale,
		SizeScale:   sizeScale,
		Frames:      stream.Frames(),
		SnapshotURL: restBase + "/api/v3/depth?symbol=" + cfg.Symbol + "&limit=5000",
		Client:      client,
	})
	src.onClose = func() { _ = stream.Close() }
	return src, nil
}

// scaleOf returns the number of significant fractional digits in a venue increment
// string ("0.01000000" -> 2, "0.00001000" -> 5, "1.00000000" -> 0), i.e. the decimal
// scale at which prices/sizes for that symbol are integer-exact.
func scaleOf(increment string) (int, error) {
	if increment == "" {
		return 0, fmt.Errorf("binance: empty increment: %w", market.ErrMalformed)
	}
	_, frac, hasDot := strings.Cut(strings.TrimRight(increment, "0"), ".")
	if !hasDot { // no fractional part -> scale 0 (a "1.0000" form trims to "1." and falls through with empty frac, also 0)
		return 0, nil
	}
	for i := 0; i < len(frac); i++ {
		if frac[i] < '0' || frac[i] > '9' {
			return 0, fmt.Errorf("binance: malformed increment %q: %w", increment, market.ErrMalformed)
		}
	}
	return len(frac), nil
}

type exchangeInfo struct {
	Symbols []struct {
		Symbol  string `json:"symbol"`
		Filters []struct {
			Type     string `json:"filterType"`
			TickSize string `json:"tickSize"`
			StepSize string `json:"stepSize"`
		} `json:"filters"`
	} `json:"symbols"`
}

// fetchScales reads the symbol's PRICE_FILTER.tickSize and LOT_SIZE.stepSize from
// exchange info and converts them to integer scales, so normalization needs no
// hardcoded per-symbol precision.
func fetchScales(ctx context.Context, client *http.Client, restBase, symbol string) (priceScale, sizeScale int, err error) {
	url := restBase + "/api/v3/exchangeInfo?symbol=" + symbol
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("binance exchangeInfo request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("binance exchangeInfo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("%w: %d", ErrSnapshotStatus, resp.StatusCode)
	}
	var info exchangeInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return 0, 0, fmt.Errorf("binance exchangeInfo decode: %w", err)
	}
	if len(info.Symbols) == 0 {
		return 0, 0, fmt.Errorf("binance exchangeInfo: symbol %q not found: %w", symbol, market.ErrMalformed)
	}
	gotPrice, gotSize := false, false
	for _, f := range info.Symbols[0].Filters {
		switch f.Type {
		case "PRICE_FILTER":
			if priceScale, err = scaleOf(f.TickSize); err != nil {
				return 0, 0, err
			}
			gotPrice = true
		case "LOT_SIZE":
			if sizeScale, err = scaleOf(f.StepSize); err != nil {
				return 0, 0, err
			}
			gotSize = true
		}
	}
	if !gotPrice || !gotSize {
		return 0, 0, fmt.Errorf("binance exchangeInfo: missing PRICE_FILTER/LOT_SIZE for %q: %w", symbol, market.ErrMalformed)
	}
	return priceScale, sizeScale, nil
}
