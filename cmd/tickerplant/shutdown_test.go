package main

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// When the pipeline ends while an SSE client is still connected, run must return and the
// client's stream must end with it. A stream handler only exits when the hub closes its
// consumer, so a run that forgot to close the hub would park here forever, with the client
// still attached. TestRunServesHTTPEdge closes its client before the run ends and so
// cannot see that; this test keeps the client open on purpose, and bounds every wait so a
// regression fails in seconds instead of hanging the package.
func TestRunShutdownWithLiveSSEClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := &gatedSource{
		snap:    market.Snapshot{LastUpdateID: 10, Bids: []market.Level{{Price: 100, Size: 1}}, Asks: []market.Level{{Price: 101, Size: 1}}},
		events:  []source.Event{},
		release: make(chan struct{}),
	}
	runDone := make(chan error, 1)
	go func() {
		_, err := run(context.Background(), quietLog(), gs, config{
			depth: 5, venue: "synthetic", symbol: "DEMO", httpLn: ln,
		})
		runDone <- err
	}()

	t.Cleanup(func() { // a failed assertion must not leave run parked on the gate
		select {
		case <-gs.release:
		default:
			close(gs.release)
		}
	})

	// Connect and read the opener, so the handler is subscribed and parked on the hub. The
	// listener is already open, so the connection is accepted even before Serve starts.
	base := "http://" + ln.Addr().String()
	resp, err := http.Get(base + "/stream")
	if err != nil {
		t.Fatalf("connecting to the edge: %v", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	opened := false
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") {
			opened = true
			break
		}
	}
	if !opened {
		t.Fatalf("stream closed before the opener: %v", sc.Err())
	}

	close(gs.release) // the source ends; the client is still attached

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run returned an error on a clean end: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return while an SSE client was attached: the hub was not closed before waiting on the consumer")
	}

	streamEnded := make(chan struct{})
	go func() {
		for sc.Scan() {
		}
		close(streamEnded)
	}()
	select {
	case <-streamEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("the client's stream is still open after run returned")
	}
}
