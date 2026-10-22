package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/version"
)

func TestModeName(t *testing.T) {
	if got := modeName(true); got != "live" {
		t.Errorf("modeName(true) = %q, want live", got)
	}
	if got := modeName(false); got != "synthetic" {
		t.Errorf("modeName(false) = %q, want synthetic", got)
	}
}

// Prepare sets the mode from the options. Only the synthetic source can be prepared without a
// network; the live mapping is covered by TestModeName.
func TestPrepareSetsTheMode(t *testing.T) {
	src, cfg, err := Prepare(context.Background(), quietLog(), Options{Seed: 1, Steps: 10, Depth: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if cfg.mode != "synthetic" {
		t.Fatalf("mode = %q, want synthetic for the synthetic source", cfg.mode)
	}
}

// The status document of a running pipeline carries what the process knows about itself: its
// mode, version, venue and symbol, the pipeline depth, the moment it started and the source's
// count of unusable frames, and the age of the last event once there has been one.
func TestRunStatusDescribesTheProcess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := disconnectingSource()
	done := make(chan error, 1)
	before := time.Now().UnixMilli()
	go func() {
		_, err := Run(context.Background(), quietLog(), &rejectingSource{gatedSource: gs, rejected: 4}, Config{
			depth: 7, venue: "kraken", symbol: "BTC/USD", HTTPListener: ln, mode: "live",
		})
		done <- err
	}()
	t.Cleanup(func() {
		releaseOf(gs)()
		<-done
	})

	type statusDoc struct {
		Schema          int    `json:"schema"`
		Mode            string `json:"mode"`
		Version         string `json:"version"`
		Venue           string `json:"venue"`
		Symbol          string `json:"symbol"`
		StartedAtUnixMs int64  `json:"startedAtUnixMs"`
		Depth           int    `json:"depth"`
		LastEventAgeMs  int64  `json:"lastEventAgeMs"`
		Engine          struct {
			Disconnects int    `json:"disconnects"`
			Rejected    uint64 `json:"rejected"`
		} `json:"engine"`
	}
	var doc statusDoc
	deadline := time.Now().Add(5 * time.Second)
	for { // wait until the engine has recorded the disconnect the source serves
		resp, err := http.Get("http://" + ln.Addr().String() + "/api/status")
		if err == nil {
			decodeErr := json.NewDecoder(resp.Body).Decode(&doc)
			resp.Body.Close()
			if decodeErr == nil && doc.Engine.Disconnects == 1 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the status never showed the disconnect: %v %+v", err, doc)
		}
		time.Sleep(5 * time.Millisecond)
	}
	after := time.Now().UnixMilli()

	if doc.Schema != 1 || doc.Mode != "live" || doc.Venue != "kraken" || doc.Symbol != "BTC/USD" || doc.Depth != 7 {
		t.Fatalf("status = %+v, want schema 1, mode live, kraken BTC/USD, depth 7", doc)
	}
	if doc.Version == "" || doc.Version != version.String() {
		t.Fatalf("version = %q, want the build's version %q", doc.Version, version.String())
	}
	if doc.StartedAtUnixMs < before || doc.StartedAtUnixMs > after {
		t.Fatalf("startedAtUnixMs = %d, want it between %d and %d (when Run began)", doc.StartedAtUnixMs, before, after)
	}
	if doc.Engine.Rejected != 4 {
		t.Fatalf("rejected = %d, want the source's 4", doc.Engine.Rejected)
	}
	if doc.LastEventAgeMs < 0 || doc.LastEventAgeMs > 5000 {
		t.Fatalf("lastEventAgeMs = %d, want a small age: the engine has processed the disconnect", doc.LastEventAgeMs)
	}
}

// A start time set on the config is the one reported, not the time Run began.
func TestRunStatusHonoursAPresetStart(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := disconnectingSource()
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), quietLog(), gs, Config{
			depth: 7, venue: "kraken", symbol: "BTC/USD", HTTPListener: ln,
			startedAt: time.UnixMilli(1_600_000_000_123),
		})
		done <- err
	}()
	t.Cleanup(func() {
		releaseOf(gs)()
		<-done
	})

	var doc struct {
		StartedAtUnixMs int64 `json:"startedAtUnixMs"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get("http://" + ln.Addr().String() + "/api/status")
		if err == nil {
			decodeErr := json.NewDecoder(resp.Body).Decode(&doc)
			resp.Body.Close()
			if decodeErr == nil && doc.StartedAtUnixMs != 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the status never answered: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if doc.StartedAtUnixMs != 1_600_000_000_123 {
		t.Fatalf("startedAtUnixMs = %d, want the configured 1600000000123", doc.StartedAtUnixMs)
	}
}
