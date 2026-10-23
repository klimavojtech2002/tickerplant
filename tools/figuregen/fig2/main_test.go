package main

import (
	"strings"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/market"
)

// A small book at price scale 2: bids 99.00 and 98.00, asks 100.00 and 101.00.
func testView() *book.View {
	return &book.View{
		Bids: []market.Level{{Price: 9900, Size: 3}, {Price: 9800, Size: 5}},
		Asks: []market.Level{{Price: 10000, Size: 2}, {Price: 10100, Size: 4}},
	}
}

// The capture time is an argument: the same input gives the same bytes, and the date in
// the caption is the one passed in, not the clock.
func TestRenderUsesTheGivenTimeAndIsDeterministic(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	st := runStats{checksumMismatches: 0, gaps: 1, resyncs: 2}
	a := render(testView(), 2, "BTC/USD", st, at)
	b := render(testView(), 2, "BTC/USD", st, at)
	if a != b {
		t.Fatal("two renders of the same input differ")
	}
	for _, want := range []string{
		"Wed, 04 Mar 2026 05:06:07 GMT", "bid 99.00 ask 100.00", "spread 1.00",
		"0 mismatches, 1 gaps, 2 resyncs", "BTC/USD",
	} {
		if !strings.Contains(a, want) {
			t.Errorf("figure does not contain %q", want)
		}
	}
	other := render(testView(), 2, "BTC/USD", st, at.Add(time.Hour))
	if other == a {
		t.Fatal("changing the time did not change the figure")
	}
}

func TestCumulative(t *testing.T) {
	cases := []struct {
		in   []market.Level
		want []int64
	}{
		{nil, []int64{}},
		{[]market.Level{{Price: 1, Size: 7}}, []int64{7}},
		{[]market.Level{{Price: 3, Size: 1}, {Price: 2, Size: 2}, {Price: 1, Size: 4}}, []int64{1, 3, 7}},
	}
	for _, c := range cases {
		got := cumulative(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("cumulative(%v) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("cumulative(%v) = %v, want %v", c.in, got, c.want)
			}
		}
	}
}
