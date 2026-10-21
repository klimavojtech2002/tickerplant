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

	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// The stream handler subscribes to the hub first and reads the engine's current view
// second, so a view can already sit in the consumer's slot when the opener is written.
// These tests make that happen deterministically (the stubEngine hook publishes inside
// View()) and pin what the edge does today: a view that is not newer than the opener is
// never sent.
//
// This is a characterization of the current rule, "send only if seq is greater than the
// last sent". It includes the lower-seq case, where a legitimate view after a resync is
// hidden: the engine can publish a snapshot with a lower sequence after a resync, and the
// edge withholds it until the sequence passes the last one sent. The row records that
// limitation; change it together with the rule.
func TestStreamNeverSendsViewNotNewerThanOpener(t *testing.T) {
	cases := []struct {
		name     string
		opener   market.Sequence
		injected market.Sequence // lands in the slot at the seam
		next     market.Sequence // the first view the client is allowed to see after the opener
	}{
		{"equal seq at the seam is not sent twice", 8, 8, 9},
		{"older view at the seam is dropped", 8, 7, 9},
		{"lower seq after a resync is hidden today", 5, 3, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := delivery.New(1 << 20) // never disconnects on lag: only the dedupe may drop
			defer hub.Close()
			eng := &stubEngine{view: view(tc.opener)}
			eng.onView = func() { hub.Publish(view(tc.injected)) }
			srv := newEdge(t, hub, eng, nil, 0)

			_, sc := openStream(t, srv.URL)
			if first := nextView(t, sc); first.Seq != uint64(tc.opener) {
				t.Fatalf("opener seq = %d, want %d", first.Seq, tc.opener)
			}

			// The hook already published the injected view (Delivered == 1: the slot was
			// empty). A later Publish counts as Delivered only when the slot is empty again,
			// which happens only after the handler has taken, and therefore judged, the
			// injected view. No sleeps: Publish never blocks, Gosched yields to the handler.
			deadline := time.After(5 * time.Second)
			for hub.Stats().Delivered < 2 {
				select {
				case <-deadline:
					t.Fatal("handler never took the injected view from its slot")
				default:
				}
				hub.Publish(view(tc.injected))
				runtime.Gosched()
			}

			hub.Publish(view(tc.next))
			got := nextView(t, sc)
			if got.Seq != uint64(tc.next) {
				t.Fatalf("client saw seq %d after the opener, want %d: the view at seq %d must be suppressed",
					got.Seq, tc.next, tc.injected)
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
