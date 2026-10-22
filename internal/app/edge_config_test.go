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
