package binance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// depthFrame builds a raw depthUpdate JSON frame, as the WebSocket would deliver it.
func depthFrame(first, final uint64, bids, asks [][]string) []byte {
	b, _ := json.Marshal(map[string]any{"e": "depthUpdate", "U": first, "u": final, "b": bids, "a": asks})
	return b
}

// snapshotServer serves a fixed REST depth snapshot and counts how many times it is
// fetched (bootstrap + each resync).
func snapshotServer(t *testing.T, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

const depthBody = `{"lastUpdateId":100,"bids":[["100.00","1.0"]],"asks":[["101.00","1.0"]]}`

func newSource(t *testing.T, frames <-chan []byte, url string, client *http.Client) *Source {
	t.Helper()
	return New(Config{
		Venue: "BINANCE", Symbol: "BTCUSDT", PriceScale: 2, SizeScale: 1,
		Frames: frames, SnapshotURL: url, Client: client,
	})
}

func TestSnapshotViaREST(t *testing.T) {
	srv, _ := snapshotServer(t, depthBody)
	snap, err := newSource(t, nil, srv.URL, srv.Client()).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.LastUpdateID != 100 || snap.Bids[0].Price != 10000 || snap.Asks[0].Price != 10100 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestSnapshotNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	_, err := newSource(t, nil, srv.URL, srv.Client()).Snapshot(context.Background())
	if !errors.Is(err, ErrSnapshotStatus) {
		t.Fatalf("err = %v, want ErrSnapshotStatus", err)
	}
}

func TestSnapshotRequestError(t *testing.T) {
	// a malformed URL fails at request construction, before any I/O
	_, err := newSource(t, nil, "://not-a-url", nil).Snapshot(context.Background())
	if err == nil {
		t.Fatal("a malformed URL must error")
	}
}

func TestSnapshotConnectionError(t *testing.T) {
	srv, _ := snapshotServer(t, depthBody)
	url := srv.URL
	srv.Close() // close before fetching: the dial fails
	_, err := newSource(t, nil, url, http.DefaultClient).Snapshot(context.Background())
	if err == nil {
		t.Fatal("a dead endpoint must error")
	}
}

func TestSnapshotBadJSON(t *testing.T) {
	srv, _ := snapshotServer(t, `{not json`)
	if _, err := newSource(t, nil, srv.URL, srv.Client()).Snapshot(context.Background()); err == nil {
		t.Fatal("invalid JSON must error")
	}
}

func TestSnapshotBadLevel(t *testing.T) {
	srv, _ := snapshotServer(t, `{"lastUpdateId":1,"bids":[["1.x","1.0"]],"asks":[]}`)
	if _, err := newSource(t, nil, srv.URL, srv.Client()).Snapshot(context.Background()); !errors.Is(err, market.ErrMalformed) {
		t.Fatalf("a bad decimal in the snapshot must be ErrMalformed, got %v", err)
	}
}

func TestNextSkipsNonDepthAndMalformedThenStopsOnClose(t *testing.T) {
	frames := make(chan []byte, 4)
	frames <- []byte(`{"result":null,"id":1}`)                         // subscription ack: skipped
	frames <- []byte(`not json`)                                       // junk: skipped
	frames <- depthFrame(105, 105, [][]string{{"1.x", "1.0"}}, nil)    // depthUpdate, bad decimal: skipped
	frames <- depthFrame(101, 101, [][]string{{"100.00", "2.0"}}, nil) // the first valid update
	close(frames)
	s := newSource(t, frames, "", nil)

	ev, ok := s.Next(context.Background())
	if ev.Received.IsZero() {
		t.Fatal("Received is zero; the latency span must start at frame dequeue")
	}
	if !ok || ev.Kind != source.EventDelta || ev.Delta.FirstSeq != 101 {
		t.Fatalf("first event = %+v, ok=%v; want a delta at seq 101", ev, ok)
	}
	if _, ok := s.Next(context.Background()); ok {
		t.Fatal("a closed frame channel must stop Next")
	}
}

func TestNextStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := newSource(t, make(chan []byte), "", nil).Next(ctx); ok {
		t.Fatal("a cancelled context must stop Next")
	}
}

func TestNextStopsAfterClose(t *testing.T) {
	s := newSource(t, make(chan []byte), "", nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.Close() // idempotent
	if _, ok := s.Next(context.Background()); ok {
		t.Fatal("a closed source must stop Next")
	}
}

// End to end: the engine consumes the Binance adapter exactly like the synthetic
// source and reconstructs an uncrossed book.
func TestEngineReconstructsFromAdapter(t *testing.T) {
	srv, _ := snapshotServer(t, depthBody)
	frames := make(chan []byte, 2)
	frames <- depthFrame(101, 101, [][]string{{"100.00", "2.0"}}, nil) // bid 100 -> 2
	frames <- depthFrame(102, 102, nil, [][]string{{"102.00", "3.0"}}) // add ask 102
	close(frames)

	eng := book.New(newSource(t, frames, srv.URL, srv.Client()), 10)
	if err := eng.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := eng.View()
	if v.Crosses() {
		t.Fatal("reconstructed book must not cross")
	}
	if v.Bids[0].Price != 10000 || v.Bids[0].Size != 20 {
		t.Fatalf("best bid = %+v, want {10000,20}", v.Bids[0])
	}
	if v.Asks[0].Price != 10100 || v.LastSeq != 102 {
		t.Fatalf("asks=%+v lastSeq=%d, want best ask 10100 at seq 102", v.Asks, v.LastSeq)
	}
}

// The bind boundary through the adapter: a stale first event (u <= snapshot id) is
// dropped, and a straddling event whose U is below the snapshot id (U <= L+1 <= u)
// binds without a resync — Binance's procedure (correctness.md §3), proven end to end.
func TestAdapterStraddleAndStaleDrop(t *testing.T) {
	srv, _ := snapshotServer(t, depthBody) // lastUpdateId = 100, bid 100@1, ask 101@1
	frames := make(chan []byte, 2)
	frames <- depthFrame(95, 100, [][]string{{"100.00", "9.0"}}, nil) // u=100 <= L: fully in snapshot, dropped
	frames <- depthFrame(98, 101, [][]string{{"100.00", "5.0"}}, nil) // U=98 < L+1=101 <= u=101: straddles, binds
	close(frames)

	eng := book.New(newSource(t, frames, srv.URL, srv.Client()), 10)
	if err := eng.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if eng.Resyncs() != 0 {
		t.Fatalf("straddle must bind without a resync: Resyncs=%d", eng.Resyncs())
	}
	v := eng.View()
	if v.LastSeq != 101 || v.Bids[0].Price != 10000 || v.Bids[0].Size != 50 {
		t.Fatalf("best bid = %+v at seq %d, want {10000,50} at 101 (stale dropped, straddle applied)", v.Bids[0], v.LastSeq)
	}
}

// A sequence gap in the stream makes the engine resync through the adapter's REST
// snapshot — the live counterpart of the synthetic gap test.
func TestGapTriggersResyncThroughAdapter(t *testing.T) {
	srv, hits := snapshotServer(t, depthBody)
	frames := make(chan []byte, 2)
	frames <- depthFrame(101, 101, [][]string{{"100.00", "2.0"}}, nil) // applies (101 = 100+1)
	frames <- depthFrame(200, 200, [][]string{{"100.00", "5.0"}}, nil) // gap: 200 > 102 -> resync
	close(frames)

	eng := book.New(newSource(t, frames, srv.URL, srv.Client()), 10)
	if err := eng.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if eng.Resyncs() != 1 {
		t.Fatalf("Resyncs=%d, want 1 (the gap triggers exactly one resync)", eng.Resyncs())
	}
	if hits.Load() != 2 { // bootstrap + one resync, both via the adapter's REST snapshot
		t.Fatalf("snapshot fetched %d times, want 2", hits.Load())
	}
	if eng.View().Crosses() {
		t.Fatal("book must not cross after resync")
	}
}
