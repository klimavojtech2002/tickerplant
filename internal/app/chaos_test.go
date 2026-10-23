package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// firstDisconnect reports whether src emits a disconnect within its stream.
func firstDisconnect(t *testing.T, src source.Source) bool {
	t.Helper()
	ctx := context.Background()
	for {
		e, ok := src.Next(ctx)
		if !ok {
			return false
		}
		if e.Kind == source.EventDisconnected {
			return true
		}
	}
}

// Chaos is for the synthetic source only. The library refuses the combination before it dials
// anything, so a live venue is never contacted.
func TestPrepareRefusesChaosWithLive(t *testing.T) {
	_, _, err := Prepare(context.Background(), quietLog(), Options{Chaos: true, Live: true, Venue: "binance", Symbol: "BTCUSDT", Depth: 5})
	if err == nil || !strings.Contains(err.Error(), "Chaos") || !strings.Contains(err.Error(), "Live") {
		t.Fatalf("err = %v, want one naming both Chaos and Live", err)
	}
}

// signature lists a source's whole stream as kind and sequence range, so two sources can be compared.
func signature(t *testing.T, src source.Source) []string {
	t.Helper()
	ctx := context.Background()
	var out []string
	for {
		e, ok := src.Next(ctx)
		if !ok {
			return out
		}
		out = append(out, fmt.Sprintf("%d:%d-%d", e.Kind, e.Delta.FirstSeq, e.Delta.LastSeq))
	}
}

// Prepare must give the synthetic source the schedule of the run's seed, not some other schedule.
func TestPrepareChaosUsesTheRunsSeed(t *testing.T) {
	const seed, steps = 4, 650
	src, _, err := Prepare(context.Background(), quietLog(), Options{Seed: seed, Steps: steps, Depth: 5, Every: 500, Chaos: true})
	if err != nil {
		t.Fatal(err)
	}
	want := source.New(source.Config{Venue: "synthetic", Symbol: "DEMO", Seed: seed, Steps: steps, FaultAt: source.ChaosSchedule(seed)})
	if got, w := signature(t, src), signature(t, want); !slices.Equal(got, w) {
		t.Fatalf("Prepare's chaos stream differs from the seed-%d schedule", seed)
	}
}

func TestPrepareChaosInjectsFaults(t *testing.T) {
	opts := Options{Seed: 4, Steps: 400, Depth: 5, Every: 500}
	src, _, err := Prepare(context.Background(), quietLog(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if firstDisconnect(t, src) {
		t.Fatal("a run without Chaos must stay fault-free")
	}
	opts.Chaos = true
	src, _, err = Prepare(context.Background(), quietLog(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !firstDisconnect(t, src) {
		t.Fatal("a run with Chaos should disconnect within one period")
	}
}
