package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"strings"
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
				pace: 150 * time.Millisecond, live: false, venue: "binance", symbol: "BTCUSDT", logLevel: "info", logFormat: "text"},
			wantCadence: 500,
		},
		{
			name: "live with default cadence logs every update",
			args: []string{"-live"},
			want: options{seed: 1, steps: 5000, depth: 10, every: 500, everySet: false,
				pace: 150 * time.Millisecond, live: true, venue: "binance", symbol: "BTCUSDT", logLevel: "info", logFormat: "text"},
			wantCadence: 1,
		},
		{
			name: "explicit -every at the default value is honoured on a live run",
			args: []string{"-live", "-every", "500"},
			want: options{seed: 1, steps: 5000, depth: 10, every: 500, everySet: true,
				pace: 150 * time.Millisecond, live: true, venue: "binance", symbol: "BTCUSDT", logLevel: "info", logFormat: "text"},
			wantCadence: 500,
		},
		{
			name: "explicit -every",
			args: []string{"-every", "7"},
			want: options{seed: 1, steps: 5000, depth: 10, every: 7, everySet: true,
				pace: 150 * time.Millisecond, live: false, venue: "binance", symbol: "BTCUSDT", logLevel: "info", logFormat: "text"},
			wantCadence: 7,
		},
		{
			name: "typed values parse",
			args: []string{"-seed", "42", "-steps", "100", "-pace", "1ms", "-depth", "3", "-venue", "binance", "-symbol", "ETHUSDT"},
			want: options{seed: 42, steps: 100, depth: 3, every: 500, everySet: false,
				pace: time.Millisecond, live: false, venue: "binance", symbol: "ETHUSDT", logLevel: "info", logFormat: "text"},
			wantCadence: 500,
		},
		{
			name: "http edge address",
			args: []string{"-http", ":8080"},
			want: options{seed: 1, steps: 5000, depth: 10, every: 500, everySet: false,
				pace: 150 * time.Millisecond, live: false, venue: "binance", symbol: "BTCUSDT", logLevel: "info", logFormat: "text", httpAddr: ":8080"},
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

func TestValidate(t *testing.T) {
	ok := options{depth: 10, steps: 5000, every: 500, pace: 150 * time.Millisecond, logLevel: "info", logFormat: "text"}
	cases := []struct {
		name    string
		mut     func(*options)
		wantErr string // substring naming the flag; empty = valid
	}{
		{"defaults", func(*options) {}, ""},
		{"depth min", func(o *options) { o.depth = 1 }, ""},
		{"depth max", func(o *options) { o.depth = maxDepth }, ""},
		{"depth zero", func(o *options) { o.depth = 0 }, "-depth 0"},
		{"depth negative", func(o *options) { o.depth = -1 }, "-depth -1"},
		{"depth too big", func(o *options) { o.depth = maxDepth + 1 }, "-depth 1001"},
		{"steps zero", func(o *options) { o.steps = 0 }, ""},
		{"steps negative", func(o *options) { o.steps = -5 }, "-steps -5"},
		{"every zero", func(o *options) { o.every = 0 }, ""},
		{"every negative", func(o *options) { o.every = -1 }, "-every -1"},
		{"pace zero", func(o *options) { o.pace = 0 }, ""},
		{"pace negative", func(o *options) { o.pace = -time.Second }, "-pace -1s"},
		{"live venue unknown", func(o *options) { o.live = true; o.venue = "okx" }, "-venue \"okx\""},
		{"live venue kraken", func(o *options) { o.live = true; o.venue = "kraken" }, ""},
		{"venue ignored without -live", func(o *options) { o.venue = "okx" }, ""},
		{"log level debug", func(o *options) { o.logLevel = "debug" }, ""},
		{"log level warn", func(o *options) { o.logLevel = "warn" }, ""},
		{"log level error", func(o *options) { o.logLevel = "error" }, ""},
		{"log level unknown", func(o *options) { o.logLevel = "loud" }, "-log-level \"loud\""},
		{"log format json", func(o *options) { o.logFormat = "json" }, ""},
		{"log format unknown", func(o *options) { o.logFormat = "xml" }, "-log-format \"xml\""},
		{"pace just below zero", func(o *options) { o.pace = -1 }, "-pace -1ns"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := ok
			c.mut(&o)
			err := o.validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("validate() = %v, want an error containing %q", err, c.wantErr)
			}
		})
	}
}

// parseFlags prints the refusal on its output and returns the error, so main exits 2.
func TestParseFlagsRefusesOutOfRange(t *testing.T) {
	var out bytes.Buffer
	if _, err := parseFlags([]string{"-depth", "99999"}, &out); err == nil {
		t.Fatal("-depth 99999 must be refused")
	}
	if !strings.Contains(out.String(), "-depth 99999 is out of range") {
		t.Fatalf("message = %q, want it to name the flag and value", out.String())
	}
}

// The built command, not just parseFlags, must exit 2 with the message on stderr.
func TestMainRefusesBadFlags(t *testing.T) {
	cases := [][]string{
		{"-depth", "-1"}, {"-depth", "99999"}, {"-steps", "-5"}, {"-every", "-1"}, {"-pace", "-1s"},
		{"-log-level", "loud"}, {"-log-format", "xml"},
	}
	for _, args := range cases {
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = append(os.Environ(), "TICKERPLANT_AS_MAIN=1")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 {
			t.Errorf("%v: err = %v, want exit status 2", args, err)
		}
		if !strings.Contains(stderr.String(), args[0]) {
			t.Errorf("%v: stderr = %q, want it to name the flag", args, stderr.String())
		}
	}
}

func TestParseFlagsVersionSkipsValidation(t *testing.T) {
	o, err := parseFlags([]string{"-version", "-depth", "-1"}, io.Discard)
	if err != nil || !o.version {
		t.Fatalf("parseFlags(-version) = %+v, %v; want version set and no error", o, err)
	}
}

func TestNewLogger(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger("warn", "json", &buf)
	log.Info("quiet")
	log.Warn("loud", "k", 1)
	out := buf.String()
	if strings.Contains(out, "quiet") || !strings.Contains(out, `"msg":"loud"`) || !strings.Contains(out, `"k":1`) {
		t.Fatalf("warn+json logger wrote %q", out)
	}
	buf.Reset()
	newLogger("info", "text", &buf).Info("hello", "k", 1)
	if !strings.Contains(buf.String(), "msg=hello") || !strings.Contains(buf.String(), "k=1") {
		t.Fatalf("default text logger wrote %q", buf.String())
	}
	buf.Reset()
	newLogger("debug", "text", &buf).Debug("deep")
	if !strings.Contains(buf.String(), "msg=deep") {
		t.Fatalf("debug level did not pass a debug line: %q", buf.String())
	}
}

// The built command prints the version and exits 0 before touching any other flag.
func TestMainPrintsVersion(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-version")
	cmd.Env = append(os.Environ(), "TICKERPLANT_AS_MAIN=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "tickerplant ") {
		t.Fatalf("output = %q, want \"tickerplant <version>\"", out)
	}
}
