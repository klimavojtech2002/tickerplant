package app

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// rejectingSource is a gated source that also reports how many frames it could not use.
type rejectingSource struct {
	*gatedSource
	rejected uint64
}

func (r *rejectingSource) Rejected() uint64 { return r.rejected }

// telemetryOf runs the pipeline on src behind an HTTP edge, waits until the disconnect the
// source serves has been recorded, connects to the stream and returns the telemetry frame it
// receives on connecting.
func telemetryOf(t *testing.T, src source.Source, release func()) (rejected uint64, consumers int, kinds []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), quietLog(), src, Config{depth: 5, venue: "synthetic", symbol: "DEMO", HTTPListener: ln})
		done <- err
	}()
	t.Cleanup(func() {
		release()
		<-done
	})

	base := "http://" + ln.Addr().String()
	deadline := time.Now().Add(5 * time.Second)
	for { // wait until the engine has recorded the disconnect
		resp, err := http.Get(base + "/metrics")
		if err == nil {
			var doc struct {
				Engine struct {
					Disconnects int `json:"disconnects"`
				} `json:"engine"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&doc)
			resp.Body.Close()
			if decodeErr == nil && doc.Engine.Disconnects == 1 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine never recorded the disconnect: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	resp, err := http.Get(base + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	event, data := "", ""
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			event = name
		} else if d, ok := strings.CutPrefix(line, "data: "); ok {
			data = d
			break
		}
	}
	if event != "telemetry" {
		t.Fatalf("first event on the stream is %q, want telemetry (data %q)", event, data)
	}
	var tj struct {
		Rejected  uint64 `json:"rejected"`
		Consumers int    `json:"consumers"`
		Events    []struct {
			Kind string `json:"kind"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(data), &tj); err != nil {
		t.Fatalf("bad telemetry %q: %v", data, err)
	}
	for _, e := range tj.Events {
		kinds = append(kinds, e.Kind)
	}
	return tj.Rejected, tj.Consumers, kinds
}

func disconnectingSource() *gatedSource {
	return &gatedSource{
		snap:    market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		events:  []source.Event{{Kind: source.EventDisconnected}},
		release: make(chan struct{}),
	}
}

func releaseOf(gs *gatedSource) func() {
	return func() {
		select {
		case <-gs.release:
		default:
			close(gs.release)
		}
	}
}

// The edge's telemetry shows what the source reports about unusable frames, the stream
// consumers (the log reader is not one) and the engine's real fault log.
func TestTelemetryShowsRejectedFramesAndTheFaultLog(t *testing.T) {
	gs := disconnectingSource()
	rejected, consumers, kinds := telemetryOf(t, &rejectingSource{gatedSource: gs, rejected: 3}, releaseOf(gs))
	if rejected != 3 {
		t.Fatalf("rejected = %d, want the source's 3", rejected)
	}
	if consumers != 1 {
		t.Fatalf("consumers = %d, want 1: the one stream client, not the log reader", consumers)
	}
	if len(kinds) != 1 || kinds[0] != "disconnect" {
		t.Fatalf("events = %v, want the one disconnect the source served", kinds)
	}
}

// A source that cannot count unusable frames shows 0.
func TestTelemetryRejectedIsZeroWhenTheSourceCannotSay(t *testing.T) {
	gs := disconnectingSource()
	rejected, _, _ := telemetryOf(t, gs, releaseOf(gs))
	if rejected != 0 {
		t.Fatalf("rejected = %d, want 0 for a source without a Rejected method", rejected)
	}
}
