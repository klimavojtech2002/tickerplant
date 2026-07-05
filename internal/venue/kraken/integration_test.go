//go:build integration

package kraken

import (
	"context"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
)

// TestLiveKrakenChecksumIntegrity runs the adapter against the real venue: the engine
// must bind the in-band snapshot and hold Kraken's own CRC32 across a stretch of live
// updates — the composed checksum seam confirmed against reality, not fixtures.
//
//	go test -tags integration -run TestLiveKrakenChecksumIntegrity ./internal/venue/kraken/
func TestLiveKrakenChecksumIntegrity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	src, err := Live(ctx, LiveConfig{Symbol: "BTC/USD"})
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	defer src.Close()

	eng := book.New(src, 10).WithChecksum(Checksum).WithMaxDepth(BookDepth)
	if err := eng.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	const steps = 200
	for i := 0; i < steps; i++ {
		ok, err := eng.Step(ctx)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("stream ended at step %d", i)
		}
		if v := eng.View(); v != nil && v.Crosses() {
			t.Fatalf("live book crossed at step %d", i)
		}
	}
	if eng.ChecksumMismatches() != 0 {
		t.Fatalf("ChecksumMismatches = %d over %d live updates, want 0 (CRC format drift?)", eng.ChecksumMismatches(), steps)
	}
	t.Logf("live: %d updates, mismatches=0 resyncs=%d gaps=%d disconnects=%d",
		steps, eng.Resyncs(), eng.Gaps(), eng.Disconnects())
}
