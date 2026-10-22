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
