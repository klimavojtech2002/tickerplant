//go:build integration

// Live integration test against the real Binance feed. Excluded from the default suite
// (the deterministic proof lives in the offline tests and the synthetic source); run it
// manually with: go test -tags integration ./internal/venue/binance
package binance

import (
	"context"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
)

func TestLiveBinanceReconstructs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	src, err := Live(ctx, LiveConfig{Symbol: "BTCUSDT"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer src.Close()

	eng := book.New(src, 10)
	if err := eng.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	steps := 0
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ok, err := eng.Step(ctx)
		if err != nil {
			t.Fatalf("step: %v", err)
		}
		if !ok {
			break
		}
		steps++
		if v := eng.View(); v != nil && v.Crosses() {
			t.Fatal("live Binance book crossed — reconstruction is wrong")
		}
	}
	if steps == 0 {
		t.Fatal("no live updates received")
	}
	t.Logf("live BTCUSDT: %d updates, resyncs=%d gaps=%d", steps, eng.Resyncs(), eng.Gaps())
}
