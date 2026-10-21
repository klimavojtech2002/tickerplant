package main

import (
	"errors"
	"flag"
	"io"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/app"
)

// parseFlags runs on a private FlagSet, so real argv slices drive it directly. The
// third case is the load-bearing one: an explicit -every at the default value must
// still set everySet, or a live run would wrongly override the requested cadence.
func TestParseFlags(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		want        options
		wantCadence int
		wantErr     bool
	}{
		{
			name: "defaults",
			args: nil,
			want: options{seed: 1, steps: 5000, depth: 10, every: 500, everySet: false,
				pace: 150 * time.Millisecond, live: false, venue: "binance", symbol: "BTCUSDT"},
			wantCadence: 500,
		},
		{
			name: "live with default cadence logs every update",
			args: []string{"-live"},
			want: options{seed: 1, steps: 5000, depth: 10, every: 500, everySet: false,
				pace: 150 * time.Millisecond, live: true, venue: "binance", symbol: "BTCUSDT"},
			wantCadence: 1,
		},
		{
			name: "explicit -every at the default value is honoured on a live run",
			args: []string{"-live", "-every", "500"},
			want: options{seed: 1, steps: 5000, depth: 10, every: 500, everySet: true,
				pace: 150 * time.Millisecond, live: true, venue: "binance", symbol: "BTCUSDT"},
			wantCadence: 500,
		},
		{
			name: "explicit -every",
			args: []string{"-every", "7"},
			want: options{seed: 1, steps: 5000, depth: 10, every: 7, everySet: true,
				pace: 150 * time.Millisecond, live: false, venue: "binance", symbol: "BTCUSDT"},
			wantCadence: 7,
		},
		{
			name: "typed values parse",
			args: []string{"-seed", "42", "-steps", "100", "-pace", "1ms", "-depth", "3", "-venue", "binance", "-symbol", "ETHUSDT"},
			want: options{seed: 42, steps: 100, depth: 3, every: 500, everySet: false,
				pace: time.Millisecond, live: false, venue: "binance", symbol: "ETHUSDT"},
			wantCadence: 500,
		},
		{
			name: "http edge address",
			args: []string{"-http", ":8080"},
			want: options{seed: 1, steps: 5000, depth: 10, every: 500, everySet: false,
				pace: 150 * time.Millisecond, live: false, venue: "binance", symbol: "BTCUSDT", httpAddr: ":8080"},
			wantCadence: 500,
		},
		{
			name:    "unknown flag errors",
			args:    []string{"-nope"},
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseFlags(c.args, io.Discard)
			if c.wantErr {
				if err == nil {
					t.Fatal("want a parse error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("parseFlags(%v) = %+v, want %+v", c.args, got, c.want)
			}
			if cad := app.LiveCadence(got.every, got.everySet, got.live); cad != c.wantCadence {
				t.Fatalf("composed cadence = %d, want %d", cad, c.wantCadence)
			}
		})
	}
}

// -h must surface flag.ErrHelp so main can exit 0, matching flag.ExitOnError's UX.
func TestParseFlagsHelpIsErrHelp(t *testing.T) {
	if _, err := parseFlags([]string{"-h"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseFlags(-h) = %v, want flag.ErrHelp", err)
	}
}
