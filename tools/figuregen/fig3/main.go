// Command fig3 regenerates docs/figures/fig3-latency.svg from a real, timed capture of
// tickerplant's own per-update latency (adapter frame-dequeue to view published, per
// ARCHITECTURE §9) against the real Kraken exchange.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
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

func main() {
	out := flag.String("out", "docs/figures/fig3-latency.svg", "output SVG path")
	symbol := flag.String("symbol", "BTC/USD", "Kraken v2 symbol")
	capture := flag.Duration("capture", 5*time.Minute, "how long to capture live samples")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *capture+30*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	src, err := kraken.Live(ctx, kraken.LiveConfig{Symbol: *symbol, Logger: log})
	if err != nil {
		log.Error("kraken live connect failed", "err", err)
		os.Exit(1)
	}
	defer src.Close()

	var samples []time.Duration
	eng := book.New(src, kraken.BookDepth).
		WithChecksum(kraken.Checksum).
		WithMaxDepth(kraken.BookDepth).
		WithLatencyObserver(func(d time.Duration) { samples = append(samples, d) })
	if err := eng.Bootstrap(ctx); err != nil {
		log.Error("bootstrap failed", "err", err)
		os.Exit(1)
	}

	deadline := time.Now().Add(*capture)
	for time.Now().Before(deadline) {
		ok, err := eng.Step(ctx)
		if err != nil {
			log.Error("step failed", "err", err)
			os.Exit(1)
		}
		if !ok {
			break
		}
	}
	if len(samples) == 0 {
		log.Error("no latency samples captured")
		os.Exit(1)
	}
	tail := tailAboveResolution(samples)
	if len(tail) == 0 {
		log.Error("every sample was within measurement resolution; nothing to plot", "totalSamples", len(samples))
		os.Exit(1)
	}

	if err := os.WriteFile(*out, []byte(render(samples, tail, *capture, *symbol)), 0o644); err != nil {
		log.Error("write failed", "err", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s: %d raw samples over %s\n", *out, len(samples), *capture)
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

const plotW, plotH = card.ContentWidth, 420

func render(samples, tail []time.Duration, window time.Duration, symbol string) string {
	percentile := func(p float64) time.Duration {
		idx := int(p * float64(len(tail)-1))
		return tail[idx]
	}
	p50, p90, p99 := percentile(0.50), percentile(0.90), percentile(0.99)

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

	total := len(samples)
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
	return card.Render(d)
}

func percentileLine(x float64, plotH int, label string) string {
	return fmt.Sprintf(`<line x1="%.2f" y1="0" x2="%.2f" y2="%d" stroke="%s" stroke-width="1.5"/>`+
		`<text x="%.2f" y="16" font-size="12" font-family="ui-monospace,Consolas,monospace" fill="%s">%s</text>`,
		x, x, plotH, card.Ink(), x+6, card.Gray(), label)
}

func fmtMs(d time.Duration) string {
	return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
}
