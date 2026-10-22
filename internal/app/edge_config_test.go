package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/httpapi"
	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// An edge configuration the handler refuses (here a scale out of range) ends Run before
// anything is served: the error says why, and the listener the caller supplied is never
// accepted on, so a client that connects is never answered.
func TestRunRefusesBadEdgeConfig(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	snap := market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}

	_, err = Run(context.Background(), quietLog(), &scriptSource{snap: snap},
		Config{depth: 5, HTTPListener: ln, scales: [2]int{19, 0}})
	if !errors.Is(err, httpapi.ErrConfig) {
		t.Fatalf("err = %v, want one wrapping httpapi.ErrConfig", err)
	}

	client := &http.Client{Timeout: 300 * time.Millisecond}
	_, err = client.Get("http://" + ln.Addr().String() + "/metrics")
	var ne net.Error
	if err == nil || !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("request = %v, want a timeout: nothing may serve the listener after the refusal", err)
	}
}

// The cors-origin setting reaches the edge: under a single origin only that origin is
// allowed, under the default any origin is.
func TestRunAppliesCORSPolicyToTheEdge(t *testing.T) {
	for _, tc := range []struct {
		name, policy, origin, want string
	}{
		{"default allows any origin", "", "https://other.example", "*"},
		{"star allows any origin", "*", "https://other.example", "*"},
		{"the configured origin is echoed", "https://app.example", "https://app.example", "https://app.example"},
		{"another origin gets no allow header", "https://app.example", "https://other.example", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			gs := &gatedSource{
				snap:    market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
				release: make(chan struct{}),
			}
			done := make(chan error, 1)
			go func() {
				_, err := Run(context.Background(), quietLog(), gs, Config{
					depth: 5, venue: "synthetic", symbol: "DEMO", HTTPListener: ln, corsOrigin: tc.policy,
				})
				done <- err
			}()
			t.Cleanup(func() {
				select {
				case <-gs.release:
				default:
					close(gs.release)
				}
				<-done
			})

			req, _ := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/metrics", nil)
			req.Header.Set("Origin", tc.origin)
			var resp *http.Response
			deadline := time.Now().Add(5 * time.Second)
			for {
				if resp, err = http.DefaultClient.Do(req); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the edge never answered: %v", err)
				}
				time.Sleep(5 * time.Millisecond)
			}
			resp.Body.Close()
			if got := resp.Header.Get("Access-Control-Allow-Origin"); got != tc.want {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreparePassesTheCORSOriginOn(t *testing.T) {
	src, cfg, err := Prepare(context.Background(), quietLog(), Options{Seed: 1, Steps: 10, Depth: 5, CORSOrigin: "https://app.example"})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if cfg.corsOrigin != "https://app.example" {
		t.Fatalf("corsOrigin = %q, want it carried from the options", cfg.corsOrigin)
	}
}

// blockingSnapshotSource holds the engine in its first bootstrap until snapshotGate is
// closed, then serves one snapshot, and then holds Next until endGate is closed.
type blockingSnapshotSource struct {
	snap         market.Snapshot
	snapshotGate chan struct{}
	endGate      chan struct{}
}

func (b *blockingSnapshotSource) Snapshot(ctx context.Context) (market.Snapshot, error) {
	select {
	case <-b.snapshotGate:
		return b.snap, nil
	case <-ctx.Done():
		return market.Snapshot{}, ctx.Err()
	}
}

func (b *blockingSnapshotSource) Next(ctx context.Context) (source.Event, bool) {
	select {
	case <-b.endGate:
	case <-ctx.Done():
	}
	return source.Event{}, false
}

func (b *blockingSnapshotSource) Close() error { return nil }

// The edge is served before the engine's first bootstrap, so the first answer from
// /readyz is 503 while the source holds the snapshot back, and it turns 200 once the book
// has a view. /healthz is up the whole time.
func TestReadyzIsUnavailableUntilTheFirstView(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	src := &blockingSnapshotSource{
		snap:         market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		snapshotGate: make(chan struct{}),
		endGate:      make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), quietLog(), src, Config{depth: 5, venue: "synthetic", symbol: "DEMO", HTTPListener: ln})
		done <- err
	}()
	t.Cleanup(func() {
		for _, g := range []chan struct{}{src.snapshotGate, src.endGate} {
			select {
			case <-g:
			default:
				close(g)
			}
		}
		<-done
	})

	status := func(path string) (int, error) {
		resp, err := http.Get("http://" + ln.Addr().String() + path)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	deadline := time.Now().Add(5 * time.Second)
	var first int
	for {
		if first, err = status("/readyz"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the edge never answered: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if first != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d while the first snapshot is held back, want 503", first)
	}
	if code, err := status("/healthz"); err != nil || code != http.StatusOK {
		t.Fatalf("/healthz = %d, %v while not ready; the process is up, want 200", code, err)
	}

	close(src.snapshotGate)
	for {
		if code, err := status("/readyz"); err == nil && code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("/readyz never turned 200 after the book bound")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A view the engine rebuilt after a resync reaches the hub even when its sequence number is
// the same as the one before it. The source disconnects straight after the first bind and
// serves the same snapshot again: two views, both with sequence 10.
func TestRunPublishesARebuiltViewWithTheSameSequence(t *testing.T) {
	snap := market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}
	src := &scriptSource{snap: snap, events: []source.Event{{Kind: source.EventDisconnected}}}
	res, err := Run(context.Background(), quietLog(), src, Config{depth: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.stats.Published != 2 {
		t.Fatalf("Published = %d, want 2: the rebuilt view has the same sequence and must still be published", res.stats.Published)
	}
	if res.view == nil || res.view.Gen != 2 || res.view.LastSeq != 10 {
		t.Fatalf("final view = %+v, want generation 2 at sequence 10", res.view)
	}
}

// A resync that rebinds to an older snapshot still publishes: bootstrap (10), a delta (11),
// then a disconnect served the snapshot at 10 again. The view-sequence gate already let this
// through; the test keeps it so under the generation gate.
func TestRunPublishesAResyncViewWithALowerSequence(t *testing.T) {
	snap := market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}}
	src := &scriptSource{snap: snap, events: []source.Event{
		{Kind: source.EventDelta, Delta: market.Delta{FirstSeq: 11, LastSeq: 11, Bids: []market.Level{{Price: 99, Size: 2}}}},
		{Kind: source.EventDisconnected},
	}}
	res, err := Run(context.Background(), quietLog(), src, Config{depth: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.stats.Published != 3 || res.view.LastSeq != 10 {
		t.Fatalf("Published = %d, final seq = %d; want 3 views and the last one at sequence 10", res.stats.Published, res.view.LastSeq)
	}
}

func TestPreparePassesTheMaxStreamsOn(t *testing.T) {
	src, cfg, err := Prepare(context.Background(), quietLog(), Options{Seed: 1, Steps: 10, Depth: 5, MaxStreams: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if cfg.maxStreams != 7 {
		t.Fatalf("maxStreams = %d, want 7 carried from the options", cfg.maxStreams)
	}
}

// The cap reaches the edge of a running pipeline: with one slot, the first stream is served and
// the second is refused with a 503.
func TestRunCapsStreams(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := disconnectingSource()
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), quietLog(), gs, Config{depth: 5, venue: "synthetic", symbol: "DEMO", HTTPListener: ln, maxStreams: 1})
		done <- err
	}()
	t.Cleanup(func() {
		releaseOf(gs)()
		<-done
	})

	url := "http://" + ln.Addr().String() + "/stream"
	var first *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for {
		if first, err = http.Get(url); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the edge never answered: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first stream got %d, want 200", first.StatusCode)
	}
	second, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second stream got %d, want 503 with a cap of 1", second.StatusCode)
	}
}
