// Command fig3 regenerates docs/figures/fig3-latency.svg from a real, timed capture of
// tickerplant's own per-update latency (adapter frame-dequeue to view published, per
// ARCHITECTURE §9) against the real Kraken exchange.
//
// A live run writes the capture (docs/figures/fig3-capture.json) and then draws it. With
// -from the drawing is made from a stored capture alone, with no network, so the figure
// can be checked against the data it claims to show.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"runtime/debug"
	"slices"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/venue/kraken"
	"github.com/klimavojtech2002/tickerplant/tools/figuregen/card"
)

// resolution is the sample floor below which a duration reads as "no measurable
// wire/queueing time" rather than a real measured latency (matches time.Since's
// practical resolution on this platform).
const resolution = time.Microsecond

// captureSchema is bumped when the stored fields change meaning.
const captureSchema = 1

// capture is one live run, stored so the figure can be drawn again and checked. Only the
// samples above the resolution floor are kept; the rest are counted in Total.
type capture struct {
	Schema       int       `json:"schema"`
	Venue        string    `json:"venue"`
	Symbol       string    `json:"symbol"`
	Start        time.Time `json:"start"`
	WindowNS     int64     `json:"window_ns"`
	ResolutionNS int64     `json:"resolution_ns"`
	GoVersion    string    `json:"go_version"`
	GOOS         string    `json:"goos"`
	GOARCH       string    `json:"goarch"`
	Commit       string    `json:"commit"`
	Total        int       `json:"total_samples"`
	TailNS       []int64   `json:"tail_ns"` // sorted, each above ResolutionNS
}

func main() {
	out := flag.String("out", "docs/figures/fig3-latency.svg", "output SVG path")
	dataOut := flag.String("capture-out", "docs/figures/fig3-capture.json", "where a live run stores its capture")
	from := flag.String("from", "", "draw from this stored capture instead of capturing (no network)")
	symbol := flag.String("symbol", "BTC/USD", "Kraken v2 symbol")
	window := flag.Duration("capture", 5*time.Minute, "how long to capture live samples")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	fail := func(msg string, err error) {
		log.Error(msg, "err", err)
		os.Exit(1)
	}

	var c capture
	if *from != "" {
		raw, err := os.ReadFile(*from)
		if err != nil {
			fail("read capture", err)
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			fail("parse capture", err)
		}
	} else {
		var err error
		if c, err = liveCapture(*symbol, *window, log); err != nil {
			fail("capture", err)
		}
		raw, err := json.MarshalIndent(c, "", " ")
		if err != nil {
			fail("encode capture", err)
		}
		if err := os.WriteFile(*dataOut, append(raw, '\n'), 0o644); err != nil {
			fail("write capture", err)
		}
		fmt.Printf("wrote %s\n", *dataOut)
	}

	svg, err := render(c)
	if err != nil {
		fail("render", err)
	}
	if err := os.WriteFile(*out, []byte(svg), 0o644); err != nil {
		fail("write", err)
	}
	fmt.Printf("wrote %s: %d raw samples over %s\n", *out, c.Total, time.Duration(c.WindowNS))
}

// liveCapture runs the engine against Kraken for the window and records every update's latency.
func liveCapture(symbol string, window time.Duration, log *slog.Logger) (capture, error) {
	ctx, cancel := context.WithTimeout(context.Background(), window+30*time.Second)
	defer cancel()

	src, err := kraken.Live(ctx, kraken.LiveConfig{Symbol: symbol, Logger: log})
	if err != nil {
		return capture{}, fmt.Errorf("kraken live connect: %w", err)
	}
	defer src.Close()

	var samples []time.Duration
	eng, err := book.New(src, kraken.BookDepth,
		book.WithChecksum(kraken.Checksum), book.WithMaxDepth(kraken.BookDepth),
		book.WithLatencyObserver(func(d time.Duration) { samples = append(samples, d) }))
	if err != nil {
		return capture{}, fmt.Errorf("engine setup: %w", err)
	}
	if err := eng.Bootstrap(ctx); err != nil {
		return capture{}, fmt.Errorf("bootstrap: %w", err)
	}

	start := time.Now()
	for time.Since(start) < window {
		ok, err := eng.Step(ctx)
		if err != nil {
			return capture{}, fmt.Errorf("step: %w", err)
		}
		if !ok {
			break
		}
	}
	if len(samples) == 0 {
		return capture{}, errors.New("no latency samples captured")
	}
	c := capture{
		Schema: captureSchema, Venue: "kraken", Symbol: symbol, Start: start.UTC(),
		WindowNS: int64(window), ResolutionNS: int64(resolution),
		GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Commit: buildCommit(),
		Total: len(samples),
	}
	for _, d := range tailAboveResolution(samples) {
		c.TailNS = append(c.TailNS, int64(d))
	}
	return c, nil
}

// buildCommit is the revision the binary was built from, or "unknown" (for example under go run).
func buildCommit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return "unknown"
}

// tailAboveResolution returns the sorted samples above the measurement floor: the
// real, measured latency distribution the histogram plots.
func tailAboveResolution(samples []time.Duration) []time.Duration {
	var tail []time.Duration
	for _, d := range samples {
		if d > resolution {
			tail = append(tail, d)
		}
	}
	slices.Sort(tail)
	return tail
}

// percentile is the sample at index floor(p*(n-1)) of the sorted tail. That is a floor, not
// the nearest-rank rule; with tens of thousands of samples the two differ by one sample.
func percentile(tail []time.Duration, p float64) time.Duration {
	return tail[int(p*float64(len(tail)-1))]
}

const plotW, plotH = card.ContentWidth, 420

// render draws a stored capture. It fails on data that cannot be drawn rather than drawing nonsense.
func render(c capture) (string, error) {
	if c.Schema != captureSchema {
		return "", fmt.Errorf("capture schema %d, want %d", c.Schema, captureSchema)
	}
	if len(c.TailNS) == 0 {
		return "", errors.New("every sample was within measurement resolution; nothing to plot")
	}
	if c.Total < len(c.TailNS) {
		return "", fmt.Errorf("capture has %d samples in total but %d above the floor", c.Total, len(c.TailNS))
	}
	tail := make([]time.Duration, len(c.TailNS))
	for i, ns := range c.TailNS {
		tail[i] = time.Duration(ns)
		if ns <= c.ResolutionNS || (i > 0 && ns < c.TailNS[i-1]) {
			return "", fmt.Errorf("tail sample %d (%d ns) is at or below the floor or out of order", i, ns)
		}
	}
	window, symbol := time.Duration(c.WindowNS), c.Symbol
	p50, p90, p99 := percentile(tail, 0.50), percentile(tail, 0.90), percentile(tail, 0.99)

	const bins = 32
	maxTail := tail[len(tail)-1]
	binWidth := maxTail / bins
	if binWidth <= 0 {
		binWidth = time.Nanosecond
	}
	counts := make([]int, bins+1)
	maxCount := 0
	for _, d := range tail {
		i := min(int(d/binWidth), bins)
		counts[i]++
		if counts[i] > maxCount {
			maxCount = counts[i]
		}
	}
	axisMax := maxTail + maxTail/10

	xOf := func(d time.Duration) float64 { return float64(d) / float64(axisMax) * float64(plotW) }
	yOf := func(c int) float64 { return float64(plotH) - float64(c)/float64(maxCount)*float64(plotH) }

	var plot string
	barW := float64(plotW) / float64(bins+1)
	for i, c := range counts {
		if c == 0 {
			continue
		}
		x := float64(i) * barW
		y := yOf(c)
		plot += fmt.Sprintf(`<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" fill="%s"/>`,
			x+1, y, barW-2, float64(plotH)-y, card.Ink())
	}
	plot += fmt.Sprintf(`<text x="4" y="14" font-size="11" fill="%s">%d</text>`, card.Gray(), maxCount)

	for _, ms := range []int{0, 5, 10, 15, 20} {
		d := time.Duration(ms) * time.Millisecond
		if d > axisMax {
			continue
		}
		x := xOf(d)
		plot += fmt.Sprintf(`<text x="%.2f" y="%d" font-size="12" fill="%s">%dms</text>`, x, plotH+18, card.Gray(), ms)
	}

	plot += percentileLine(xOf(p50), plotH, fmt.Sprintf("p50 %s", fmtMs(p50)))
	plot += percentileLine(xOf(p90), plotH, fmt.Sprintf("p90 %s", fmtMs(p90)))
	plot += percentileLine(xOf(p99), plotH, fmt.Sprintf("p99 %s", fmtMs(p99)))

	total := c.Total
	sub := total - len(tail)
	pct := float64(sub) / float64(total) * 100

	d := card.Data{
		Kicker: "tickerplant — order-book correctness",
		Title:  []string{"Figure 3 — Per-update", "processing latency, real samples"},
		Description: []string{
			fmt.Sprintf("Live kraken %s connection, %s capture, span from a raw", symbol, window.Round(time.Second)),
			"WebSocket frame leaving the transport to the reconstructed view",
			"being published — no wire wait, no synthetic delay.",
		},
		PlotWidth:  plotW,
		PlotHeight: plotH,
		Plot:       plot,
		Caption: []card.CaptionLine{
			{Text: fmt.Sprintf("%d of %d updates (%.1f%%) were processed within measurement resolution (≤ 1µs)", sub, total, pct), Bold: true},
			{Text: fmt.Sprintf("— no wire wait, no queueing. The other %d are the real, measured distribution below.", len(tail))},
			{Text: fmt.Sprintf("N=%d raw samples, live kraken %s, %s capture window.", total, symbol, window.Round(time.Second)), Italic: true},
			{Text: fmt.Sprintf("Histogram of the %d samples above measurement resolution, %d bins.", len(tail), bins), Italic: true},
		},
	}
	return card.Render(d), nil
}

func percentileLine(x float64, plotH int, label string) string {
	return fmt.Sprintf(`<line x1="%.2f" y1="0" x2="%.2f" y2="%d" stroke="%s" stroke-width="1.5"/>`+
		`<text x="%.2f" y="16" font-size="12" font-family="ui-monospace,Consolas,monospace" fill="%s">%s</text>`,
		x, x, plotH, card.Ink(), x+6, card.Gray(), label)
}

func fmtMs(d time.Duration) string {
	return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
}
