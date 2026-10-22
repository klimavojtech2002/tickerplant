package httpapi

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strings"
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
	v := viewGen(5, 5)
	v.Bids = []market.Level{{Price: 627217, Size: 135823999}, {Price: 627216, Size: 5000000}, {Price: 627210, Size: 1}}
	v.Asks = []market.Level{{Price: 627218, Size: 114202}, {Price: 627219, Size: 250000000}, {Price: 627230, Size: 7}}
	srv := newEdge(t, hub, &stubEngine{view: v}, nil, 0)

	resp, err := http.Get(srv.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// The first frame of a stream is telemetry (its bytes belong to the telemetry contract
	// test); the second is the opener, whose payload is the committed view.json that the
	// dashboard's tests also read. The file ends with a newline; the payload does not.
	const path = "testdata/view.json"
	frames := make(chan string, 2)
	go func() {
		rd := bufio.NewReader(resp.Body)
		for range 2 {
			var frame strings.Builder
			for {
				line, err := rd.ReadString('\n')
				frame.WriteString(line)
				if err != nil || line == "\n" {
					break
				}
			}
			frames <- frame.String()
		}
	}()
	var got [2]string
	for i := range got {
		select {
		case got[i] = <-frames:
		case <-time.After(5 * time.Second):
			t.Fatalf("frame %d never completed (got %q)", i, got)
		}
	}
	if !strings.HasPrefix(got[0], "event: telemetry\ndata: {") {
		t.Fatalf("first frame = %q, want a telemetry event", got[0])
	}
	payload, ok := strings.CutPrefix(got[1], "event: view\ndata: ")
	payload, ok2 := strings.CutSuffix(payload, "\n\n")
	if !ok || !ok2 {
		t.Fatalf("second frame = %q, want event: view, one data line and a blank line", got[1])
	}
	if *update {
		if err := os.WriteFile(path, []byte(payload+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	if payload+"\n" != string(want) {
		t.Fatalf("view payload differs from %s (run with -update to rewrite it if the change is intended):\n got %s\nwant %s", path, payload, want)
	}

	// The dashboard rejects a side that is out of order or repeats a price, so the golden
	// must itself be strictly ordered; the engine's own guarantee is tested in internal/book.
	var doc struct {
		Bids [][2]string `json:"bids"`
		Asks [][2]string `json:"asks"`
	}
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Bids) != 3 || len(doc.Asks) != 3 {
		t.Fatalf("golden has %d bids and %d asks, want 3 of each", len(doc.Bids), len(doc.Asks))
	}
	for i := 1; i < 3; i++ {
		if doc.Bids[i-1][0] <= doc.Bids[i][0] || doc.Asks[i-1][0] >= doc.Asks[i][0] {
			t.Fatalf("golden is not strictly ordered: bids %v asks %v", doc.Bids, doc.Asks)
		}
	}
}

// testdata/legacy-metrics.json is a real body of the old JSON /metrics, kept as the negative
// fixture of the dashboard's status parser (its tests must refuse it). This test keeps it
// honest: it must stay a JSON object with the old top-level keys and no schema field.
func TestLegacyMetricsFixtureStaysALegacyBody(t *testing.T) {
	raw, err := os.ReadFile("testdata/legacy-metrics.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("fixture is not a JSON object: %v", err)
	}
	for _, key := range []string{"venue", "symbol", "delivery", "engine", "latency"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("fixture lost its %q key", key)
		}
	}
	if _, ok := doc["schema"]; ok {
		t.Error("fixture has a schema field, so it would no longer be refused as a legacy body")
	}
}
