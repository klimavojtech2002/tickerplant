package binance

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/source"
	"github.com/klimavojtech2002/tickerplant/internal/venue"
)

func TestNextCountsRejectedFramesAndSkipsThem(t *testing.T) {
	frames := make(chan []byte, 16)
	for _, raw := range [][]byte{
		[]byte(`{"result":null,"id":1}`),                                // subscription ack: not counted
		[]byte(`{"e":"trade","E":1,"s":"BTCUSDT"}`),                     // another event type: not counted
		[]byte(`not json`),                                              // counted
		[]byte(`{"e":"depthUpdate","U":"x","u":2}`),                     // wrong field type: counted
		depthFrame(105, 105, [][]string{{"1.x", "1.0"}}, nil),           // bad decimal: counted
		depthFrame(106, 106, nil, [][]string{{"101.00", "not-a-size"}}), // bad size: counted
	} {
		frames <- raw
	}
	frames <- depthFrame(101, 101, [][]string{{"100.00", "2.0"}}, nil)
	s := newSource(t, frames, "", nil)
	if got := s.Rejected(); got != 0 {
		t.Fatalf("Rejected = %d before any frame, want 0", got)
	}
	ev, ok := s.Next(context.Background())
	if !ok || ev.Kind != source.EventDelta || ev.Delta.FirstSeq != 101 {
		t.Fatalf("event = %+v, ok = %v; want the delta at 101 after the skipped frames", ev, ok)
	}
	if got := s.Rejected(); got != 4 {
		t.Fatalf("Rejected = %d, want 4 (the ack and the other event type are not counted)", got)
	}
}

// The log is a sample: a burst of bad frames writes one line a second, with the text cut
// short, while the counter keeps the full count.
func TestRejectLogIsRateLimitedAndBounded(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(1000, 0)
	frames := make(chan []byte, 512)
	s := New(Config{Venue: "BINANCE", Symbol: "BTCUSDT", PriceScale: 2, SizeScale: 1, Frames: frames,
		Logger: slog.New(slog.NewTextHandler(&buf, nil))})
	s.rejectLog = venue.NewLogEvery(time.Second, func() time.Time { return now })

	burst := func() {
		for range 100 {
			frames <- depthFrame(105, 105, [][]string{{strings.Repeat("x", 400), "1.0"}}, nil)
		}
		frames <- depthFrame(101, 101, [][]string{{"100.00", "2.0"}}, nil)
		if _, ok := s.Next(context.Background()); !ok {
			t.Fatal("Next stopped")
		}
	}
	burst()
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 {
		t.Fatalf("%d log lines for 100 rejects in one second, want 1:\n%s", n, buf.String())
	}
	now = now.Add(time.Second)
	burst()
	if n := strings.Count(buf.String(), "level=WARN"); n != 2 {
		t.Fatalf("%d log lines after the next second, want 2", n)
	}
	if strings.Contains(buf.String(), strings.Repeat("x", 250)) {
		t.Fatal("the logged reason was not cut")
	}
	if got := s.Rejected(); got != 200 {
		t.Fatalf("Rejected = %d, want 200: the counter counts every frame", got)
	}
}
