package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func loadFixture(t *testing.T) capture {
	t.Helper()
	raw, err := os.ReadFile("testdata/capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var c capture
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// The drawing of a stored capture is fixed. After a deliberate change to the layout,
// regenerate the golden with
//
//	go run ./tools/figuregen/fig3 -from tools/figuregen/fig3/testdata/capture.json -out tools/figuregen/fig3/testdata/golden.svg
func TestRenderMatchesGolden(t *testing.T) {
	got, err := render(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/golden.svg")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		at := 0
		for at < len(got) && at < len(want) && got[at] == want[at] {
			at++
		}
		t.Fatalf("render differs from testdata/golden.svg (%d bytes against %d, first difference at byte %d)", len(got), len(want), at)
	}
}

func TestRenderShowsTheCapturesFacts(t *testing.T) {
	got, err := render(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"800 of 1000 updates (80.0%)", "N=1000 raw samples", "TEST/USD", "1m0s capture"} {
		if !strings.Contains(got, want) {
			t.Errorf("figure does not contain %q", want)
		}
	}
}

func TestRenderRefusesDataItCannotDraw(t *testing.T) {
	good := loadFixture(t)
	mutate := func(f func(*capture)) capture {
		c := good
		c.TailNS = append([]int64(nil), good.TailNS...)
		f(&c)
		return c
	}
	cases := []struct {
		name string
		c    capture
		want string
	}{
		{"wrong schema", mutate(func(c *capture) { c.Schema = 9 }), "schema"},
		{"negative floor", mutate(func(c *capture) { c.ResolutionNS = -100; c.TailNS = []int64{-5} }), "resolution"},
		{"no window", mutate(func(c *capture) { c.WindowNS = 0 }), "window"},
		{"no symbol", mutate(func(c *capture) { c.Symbol = "" }), "symbol"},
		{"sample near the int64 limit", mutate(func(c *capture) { c.TailNS[len(c.TailNS)-1] = 1<<63 - 1 }), "refusing"},
		{"no tail", mutate(func(c *capture) { c.TailNS = nil }), "nothing to plot"},
		{"total below tail", mutate(func(c *capture) { c.Total = 5 }), "in total"},
		{"sample at the floor", mutate(func(c *capture) { c.TailNS[0] = c.ResolutionNS }), "floor"},
		{"unsorted", mutate(func(c *capture) { c.TailNS[3], c.TailNS[4] = c.TailNS[4], c.TailNS[3] }), "order"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := render(tc.c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestTailAboveResolution(t *testing.T) {
	us := time.Microsecond
	cases := []struct {
		name string
		in   []time.Duration
		want []time.Duration
	}{
		{"empty", nil, nil},
		{"all within the floor", []time.Duration{0, us, us}, nil},
		{"sorted and filtered", []time.Duration{5 * us, 0, 2 * us, us, 3 * us}, []time.Duration{2 * us, 3 * us, 5 * us}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tailAboveResolution(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestPercentile(t *testing.T) {
	ten := []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := []struct {
		name string
		tail []time.Duration
		p    float64
		want time.Duration
	}{
		{"one sample", []time.Duration{7}, 0.99, 7},
		{"all equal", []time.Duration{4, 4, 4}, 0.5, 4},
		{"p50 of ten is index 4", ten, 0.50, 5},
		{"p90 of ten is index 8", ten, 0.90, 9},
		{"p99 of ten floors to index 8", ten, 0.99, 9},
		{"p100 is the last", ten, 1, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := percentile(tc.tail, tc.p); got != tc.want {
				t.Fatalf("percentile(%v, %v) = %v, want %v", tc.tail, tc.p, got, tc.want)
			}
		})
	}
}

// A tail of one sample, or of equal samples, still draws (the top sample falls in the last bin).
func TestRenderWithAllEqualAndSingleSample(t *testing.T) {
	for _, tail := range [][]int64{{5000}, {5000, 5000, 5000}} {
		c := capture{Schema: captureSchema, Symbol: "X", WindowNS: int64(time.Minute), ResolutionNS: 1000, Total: 10, TailNS: tail}
		if _, err := render(c); err != nil {
			t.Fatalf("tail %v: %v", tail, err)
		}
	}
}

// negativeCoordinate matches an x position left of the plot, which an overflow would produce.
var negativeCoordinate = regexp.MustCompile(`\bx[12]?="-`)

// Whatever a stored file holds, render either draws it or returns an error; it never panics
// and never writes NaN, Inf or a negative x position into the SVG.
func FuzzRender(f *testing.F) {
	f.Add(int64(1000), int64(60e9), int64(5000), int64(9000), 10)
	f.Add(int64(-100), int64(1), int64(-5), int64(0), 1)
	f.Add(int64(1000), int64(1), int64(1<<62), int64(1<<63-1), 1<<30)
	f.Fuzz(func(t *testing.T, floor, window, a, b int64, total int) {
		tail := []int64{a, b}
		if a > b {
			tail = []int64{b, a}
		}
		svg, err := render(capture{Schema: captureSchema, Symbol: "X", WindowNS: window, ResolutionNS: floor, Total: total, TailNS: tail})
		if err != nil {
			return
		}
		if strings.Contains(svg, "NaN") || strings.Contains(svg, "Inf") || negativeCoordinate.MatchString(svg) {
			t.Fatalf("bad number in the figure for floor %d window %d tail %v total %d", floor, window, tail, total)
		}
	})
}
