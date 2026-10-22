package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// The stream handler subscribes to the hub first and reads the engine's current view
// second, so a view can already sit in the consumer's slot when the opener is written.
// These tests make that happen deterministically (the stubEngine hook publishes inside
// View()). The edge sends a view only if the engine published it after the last one sent,
// judged by its generation: the sequence number does not decide, because a view rebuilt
// after a resync can have a lower or equal one and must still be shown.

// sv is a view's sequence number and generation.
type sv struct {
	seq market.Sequence
	gen uint64
}

func (v sv) view() *book.View { return viewGen(v.seq, v.gen) }

// settle republishes v until the hub has counted the given number of deliveries. A Publish
// counts as delivered only when the consumer's slot is empty, which happens only after the
// handler took, and so judged, the view that was in it. No sleeps: Publish never blocks,
// Gosched yields to the handler.
func settle(t *testing.T, hub *delivery.Hub, v *book.View, delivered uint64) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for hub.Stats().Delivered < delivered {
		select {
		case <-deadline:
			t.Fatal("handler never took the view from its slot")
		default:
		}
		hub.Publish(v)
		runtime.Gosched()
	}
}

// waitForConsumer waits until the stream handler has subscribed to the hub.
func waitForConsumer(t *testing.T, hub *delivery.Hub) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for hub.Stats().Consumers < 1 {
		select {
		case <-deadline:
			t.Fatal("the handler never subscribed")
		default:
		}
		runtime.Gosched()
	}
}

func TestStreamDedupesByGen(t *testing.T) {
	cases := []struct {
		name     string
		opener   sv
		injected sv   // lands in the slot at the seam
		next     sv   // published after the handler judged the injected view
		sent     []sv // the views the client sees after the opener, in order, ending with next
	}{
		{"the same view at the seam is not sent twice",
			sv{8, 8}, sv{8, 8}, sv{9, 9}, []sv{{9, 9}}},
		{"an older view at the seam is dropped",
			sv{8, 8}, sv{7, 7}, sv{9, 9}, []sv{{9, 9}}},
		{"a view rebuilt with a lower sequence after a resync is sent",
			sv{5, 5}, sv{3, 6}, sv{6, 7}, []sv{{3, 6}, {6, 7}}},
		{"a view rebuilt with an equal sequence is sent",
			sv{8, 8}, sv{8, 9}, sv{9, 10}, []sv{{8, 9}, {9, 10}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := delivery.New(1 << 20) // never disconnects on lag: only the dedupe may drop
			defer hub.Close()
			eng := &stubEngine{view: tc.opener.view()}
			eng.onView = func() { hub.Publish(tc.injected.view()) }
			srv := newEdge(t, hub, eng, nil, 0)

			_, sc := openStream(t, srv.URL)
			if first := nextView(t, sc); first.Seq != uint64(tc.opener.seq) {
				t.Fatalf("opener seq = %d, want %d", first.Seq, tc.opener.seq)
			}
			// The hook already published the injected view (Delivered == 1: the slot was empty).
			settle(t, hub, tc.injected.view(), 2)

			hub.Publish(tc.next.view())
			for i, want := range tc.sent {
				if got := nextView(t, sc); got.Seq != uint64(want.seq) {
					t.Fatalf("event %d after the opener has seq %d, want %d", i, got.Seq, want.seq)
				}
			}

			// A view that is not newer than the last one sent never reaches the client, however
			// often it is published: the next event is the tail below.
			// Each is judged on its own: the hub keeps only the newest view in the slot, so
			// publishing both at once would hide the first from the handler.
			for _, old := range []*book.View{tc.next.view(), viewGen(1, 1)} {
				delivered := hub.Stats().Delivered
				hub.Publish(old)
				settle(t, hub, old, delivered+2)
			}
			tail := sv{seq: 99, gen: tc.next.gen + 1}
			hub.Publish(tail.view())
			if got := nextView(t, sc); got.Seq != 99 {
				t.Fatalf("after the last sent view the client saw seq %d, want the tail 99", got.Seq)
			}
		})
	}
}

// With no view yet at the time of the connection, the first view the engine publishes is
// the opener in effect: its generation starts at 1, above the unset zero, so it is sent.
func TestStreamSendsTheFirstViewWhenThereIsNoOpener(t *testing.T) {
	hub := delivery.New(1 << 20)
	defer hub.Close()
	srv := newEdge(t, hub, &stubEngine{}, nil, 0)
	_, sc := openStream(t, srv.URL)
	waitForConsumer(t, hub)
	hub.Publish(viewGen(4, 1))
	if got := nextView(t, sc); got.Seq != 4 {
		t.Fatalf("first view seq = %d, want 4", got.Seq)
	}
}

// A view that reaches the hub without a generation is unset, not new: the stream never sends
// it, so a hand-made view that forgets to set one fails a test instead of passing by luck.
func TestStreamSuppressesAViewWithoutAGeneration(t *testing.T) {
	for name, opener := range map[string]*book.View{
		"no opener":                nil,
		"after an opener of gen 5": viewGen(5, 5),
	} {
		t.Run(name, func(t *testing.T) {
			hub := delivery.New(1 << 20)
			defer hub.Close()
			srv := newEdge(t, hub, &stubEngine{view: opener}, nil, 0)
			_, sc := openStream(t, srv.URL)
			if opener != nil {
				if first := nextView(t, sc); first.Seq != 5 {
					t.Fatalf("opener seq = %d, want 5", first.Seq)
				}
			}
			waitForConsumer(t, hub)
			zero := viewGen(77, 0)
			hub.Publish(zero)
			settle(t, hub, zero, 2)
			hub.Publish(viewGen(8, 6))
			if got := nextView(t, sc); got.Seq != 8 {
				t.Fatalf("client saw seq %d, want 8: the view without a generation must be suppressed", got.Seq)
			}
		})
	}
}

// The exact bytes of one view event. A client (and the dashboard's parser) depends on
// the event name, the key order, string-typed decimals and the blank line that ends the
// frame; any drift here is a wire change and must be deliberate.
func TestViewEventBytes(t *testing.T) {
	hub := delivery.New(8)
	defer hub.Close()
	srv := newEdge(t, hub, &stubEngine{view: view(5)}, nil, 0)

	resp, err := http.Get(srv.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	want := "event: view\n" +
		`data: {"venue":"kraken","symbol":"BTC/USD","seq":5,"bids":[["62721.7","1.35823999"]],"asks":[["62721.8","0.00114202"]]}` + "\n\n"
	got := make([]byte, len(want))
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(resp.Body, got)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("reading the first frame: %v (got %q)", err, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("first frame never completed (got %q)", got)
	}
	if string(got) != want {
		t.Fatalf("first frame =\n%q\nwant\n%q", got, want)
	}
}

// The exact bytes of /metrics when no latency histogram is attached (the latency block
// is omitted, never zeroed). Key order and names are part of the contract the dashboard
// parses.
func TestMetricsBytes(t *testing.T) {
	hub := delivery.New(8)
	defer hub.Close()
	_ = hub.Subscribe()
	hub.Publish(view(1)) // delivered = 1
	hub.Publish(view(2)) // superseded: dropped = 1
	srv := newEdge(t, hub, &stubEngine{gaps: 1, resyncs: 2, disconnects: 3, crosses: 4, mismatches: 5, rejects: 6, snapErrors: 7}, nil, 0)

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"venue":"kraken","symbol":"BTC/USD",` +
		`"delivery":{"consumers":1,"delivered":1,"dropped":1},` +
		`"engine":{"gaps":1,"resyncs":2,"disconnects":3,"wouldCrosses":4,"checksumMismatches":5,` +
		`"snapshotRejects":6,"snapshotErrors":7}}` + "\n"
	if string(body) != want {
		t.Fatalf("/metrics body =\n%s\nwant\n%s", body, want)
	}
}

// testdata/legacy-metrics.json is a real /metrics body captured from the running edge. It
// is kept as a fixture of the document shape clients parse today. This test ties the file
// to the code: it must still decode into the current document type with no unknown
// fields, or the fixture has gone stale.
func TestLegacyMetricsFixtureMatchesCurrentShape(t *testing.T) {
	raw, err := os.ReadFile("testdata/legacy-metrics.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc metricsJSON
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("fixture no longer matches metricsJSON: %v", err)
	}
	if doc.Latency == nil || doc.Latency.Count == 0 || doc.Latency.P999 == "" {
		t.Fatalf("fixture lost its latency block: %+v", doc.Latency)
	}
}
