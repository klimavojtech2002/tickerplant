package kraken

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
	"github.com/klimavojtech2002/tickerplant/internal/venue"
)

var goodUpdate = wireFrame("update", []market.Level{{Price: 450, Size: 100}}, nil, 1)

func TestNextCountsRejectedFramesAndSkipsThem(t *testing.T) {
	frames := make(chan []byte, 16)
	for _, raw := range []string{
		`{"method":"subscribe","success":true}`,                 // ack: not counted
		`{"channel":"heartbeat"}`,                               // heartbeat: not counted
		`{"channel":"status","data":[]}`,                        // status: not counted
		`{"error":"nope","success":false,"method":"subscribe"}`, // rejected subscribe: not counted
		`not json`, // counted
		`{"channel":"book","type":"update","data":[`,                                                            // truncated: counted
		`{"channel":"book","type":"rolled","data":[{"symbol":"X"}]}`,                                            // unknown type: counted
		`{"channel":"book","type":"update","data":[]}`,                                                          // no data: counted
		`{"channel":"book","type":"update","data":[{"symbol":"X","bids":[{"price":4.5e4,"qty":1}],"asks":[]}]}`, // bad level: counted
	} {
		frames <- []byte(raw)
	}
	frames <- goodUpdate
	s := newTestSource(frames, nil)
	if got := s.Rejected(); got != 0 {
		t.Fatalf("Rejected = %d before any frame, want 0", got)
	}
	ev, ok := s.Next(context.Background())
	if !ok || ev.Kind != source.EventDelta {
		t.Fatalf("event = %+v, ok = %v; want the good update after the skipped frames", ev, ok)
	}
	if got := s.Rejected(); got != 5 {
		t.Fatalf("Rejected = %d, want 5 (the four benign frames are not counted)", got)
	}
	if ev.Delta.FirstSeq != 1 {
		t.Fatalf("FirstSeq = %d, want 1: rejected frames must not use up a sequence number", ev.Delta.FirstSeq)
	}
}

// A malformed update read while Snapshot waits is superseded by the snapshot and is not
// counted; a malformed snapshot is a loud error. Both are as before.
func TestSnapshotDoesNotCountSkippedUpdates(t *testing.T) {
	frames := make(chan []byte, 4)
	frames <- []byte(`{"channel":"book","type":"update","data":[{"symbol":"X","bids":[{"price":4.5e4,"qty":1}],"asks":[]}]}`)
	frames <- wireFrame("snapshot", []market.Level{{Price: 450, Size: 100}}, []market.Level{{Price: 460, Size: 100}}, 1)
	s := newTestSource(frames, nil)
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.Rejected(); got != 0 {
		t.Fatalf("Rejected = %d, want 0: Snapshot skips superseded updates without counting", got)
	}
}

// The log is a sample: a burst of bad frames writes one line a second, with the venue
// text cut short, while the counter keeps the full count.
func TestRejectLogIsRateLimitedAndBounded(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(1000, 0)
	frames := make(chan []byte, 512)
	s := New(Config{Venue: "kraken", Symbol: "BTC/USD", PriceScale: 1, QtyScale: 8, Frames: frames,
		Logger: slog.New(slog.NewTextHandler(&buf, nil))})
	s.rejectLog = venue.NewLogEvery(time.Second, func() time.Time { return now })

	burst := func() {
		for range 100 {
			frames <- []byte(`{"channel":"book","type":"` + strings.Repeat("x", 400) + `","data":[{"symbol":"X"}]}`)
		}
		frames <- goodUpdate
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
