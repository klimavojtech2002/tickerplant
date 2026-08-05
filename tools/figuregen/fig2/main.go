// Command fig2 regenerates docs/figures/fig2-depth.svg from a real, live capture of
// tickerplant's own engine against the real Kraken exchange — not a mock book.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/venue/kraken"
	"github.com/klimavojtech2002/tickerplant/tools/figuregen/card"
)

func main() {
	out := flag.String("out", "docs/figures/fig2-depth.svg", "output SVG path")
	symbol := flag.String("symbol", "BTC/USD", "Kraken v2 symbol")
	settle := flag.Duration("settle", 5*time.Second, "how long to let the book receive live updates before capturing it")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	src, err := kraken.Live(ctx, kraken.LiveConfig{Symbol: *symbol, Logger: log})
	if err != nil {
		log.Error("kraken live connect failed", "err", err)
		os.Exit(1)
	}
	defer src.Close()
	priceScale, _ := src.Scales()

	eng := book.New(src, kraken.BookDepth).WithChecksum(kraken.Checksum).WithMaxDepth(kraken.BookDepth)
	if err := eng.Bootstrap(ctx); err != nil {
		log.Error("bootstrap failed", "err", err)
		os.Exit(1)
	}

	deadline := time.Now().Add(*settle)
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

	v := eng.View()
	if v == nil || len(v.Bids) == 0 || len(v.Asks) == 0 {
		log.Error("no view captured (empty book)")
		os.Exit(1)
	}
	if v.Crosses() {
		log.Error("invariant violated: captured view crosses")
		os.Exit(1)
	}
	if eng.ChecksumMismatches() > 0 || eng.Gaps() > 0 {
		log.Warn("capture saw drift or gaps", "checksumMismatches", eng.ChecksumMismatches(), "gaps", eng.Gaps())
	}

	if err := os.WriteFile(*out, []byte(render(v, priceScale, *symbol, eng)), 0o644); err != nil {
		log.Error("write failed", "err", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s: bid %s ask %s, checksumMismatches=%d gaps=%d resyncs=%d\n",
		*out, market.FormatScaled(int64(v.Bids[0].Price), priceScale), market.FormatScaled(int64(v.Asks[0].Price), priceScale),
		eng.ChecksumMismatches(), eng.Gaps(), eng.Resyncs())
}

const plotW, plotH = card.ContentWidth, 460

func render(v *book.View, priceScale int, symbol string, eng *book.Engine) string {
	bidCum := cumulative(v.Bids)
	askCum := cumulative(v.Asks)
	maxCum := max(bidCum[len(bidCum)-1], askCum[len(askCum)-1])

	minPrice := int64(v.Bids[len(v.Bids)-1].Price)
	maxPrice := int64(v.Asks[len(v.Asks)-1].Price)
	span := maxPrice - minPrice
	xOf := func(p int64) float64 { return float64(p-minPrice) / float64(span) * float64(plotW) }
	yOf := func(c int64) float64 { return float64(plotH) - float64(c)/float64(maxCum)*float64(plotH)*0.92 }

	baseline := plotH
	var plot string
	plot += card.Grid(plotW, plotH, 5)

	// Bid staircase, best (highest price, nearest spread) out to worst, filled to
	// the baseline. Ask staircase is the mirror, unfilled, dashed.
	bidPts := stairVertices(v.Bids, bidCum, xOf, yOf, baseline)
	fillPath := pathFrom(bidPts) + fmt.Sprintf(" L %.2f %d Z", bidPts[len(bidPts)-1].x, baseline)
	plot += fmt.Sprintf(`<path d="%s" fill="%s" fill-opacity="0.12" stroke="none"/>`, fillPath, card.Ink())
	plot += fmt.Sprintf(`<path d="%s" fill="none" stroke="%s" stroke-width="2"/>`, pathFrom(bidPts), card.Ink())

	askPts := stairVertices(v.Asks, askCum, xOf, yOf, baseline)
	plot += fmt.Sprintf(`<path d="%s" fill="none" stroke="%s" stroke-width="2" stroke-dasharray="6,4"/>`, pathFrom(askPts), card.Ink())

	for i, l := range v.Bids {
		plot += dot(xOf(int64(l.Price)), yOf(bidCum[i]), true)
	}
	for i, l := range v.Asks {
		plot += dot(xOf(int64(l.Price)), yOf(askCum[i]), false)
	}

	spread := int64(v.Asks[0].Price) - int64(v.Bids[0].Price)
	midX := (xOf(int64(v.Bids[0].Price)) + xOf(int64(v.Asks[0].Price))) / 2
	plot += fmt.Sprintf(`<line x1="%.2f" y1="0" x2="%.2f" y2="%d" stroke="%s" stroke-width="1" stroke-dasharray="2,3"/>`,
		midX, midX, plotH, card.Gray())
	plot += fmt.Sprintf(`<text x="%.2f" y="16" font-size="12" fill="%s" text-anchor="middle">spread %s</text>`,
		midX, card.Gray(), market.FormatScaled(spread, priceScale))

	plot += fmt.Sprintf(`<text x="%.2f" y="%d" font-size="13" font-family="ui-monospace,Consolas,monospace" fill="%s" text-anchor="middle">bid %s ask %s</text>`,
		midX, plotH+22, card.Ink(),
		market.FormatScaled(int64(v.Bids[0].Price), priceScale), market.FormatScaled(int64(v.Asks[0].Price), priceScale))

	d := card.Data{
		Kicker: "tickerplant — order-book correctness",
		Title:  []string{"Figure 2 — Live", "order-book depth"},
		Description: []string{
			fmt.Sprintf("Top %d price levels per side, captured from tickerplant's own", len(v.Bids)),
			"engine against the real Kraken exchange.",
		},
		Legend:     []card.Legend{{Label: "Bid"}, {Label: "Ask", Dash: "6,4"}},
		PlotWidth:  plotW,
		PlotHeight: plotH,
		Plot:       plot,
		Caption: []card.CaptionLine{
			{Text: fmt.Sprintf("Live kraken %s, captured %s via tickerplant's own engine.", symbol,
				time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")), Italic: true},
			{Text: fmt.Sprintf("CRC32 checksum-verified: %d mismatches, %d gaps, %d resyncs.",
				eng.ChecksumMismatches(), eng.Gaps(), eng.Resyncs()), Italic: true},
		},
	}
	return card.Render(d)
}

func cumulative(levels []market.Level) []int64 {
	out := make([]int64, len(levels))
	var sum int64
	for i, l := range levels {
		sum += int64(l.Size)
		out[i] = sum
	}
	return out
}

// point2 is one vertex of a staircase path, in plot-local SVG coordinates.
type point2 struct{ x, y float64 }

// stairVertices walks levels from best (nearest the spread) outward, returning the
// staircase outline: start at the baseline under the best level, up to its
// cumulative height, then a horizontal-then-vertical step to each next level.
func stairVertices(levels []market.Level, cum []int64, xOf func(int64) float64, yOf func(int64) float64, baseline int) []point2 {
	pts := []point2{{xOf(int64(levels[0].Price)), float64(baseline)}}
	prevY := float64(baseline)
	for i, l := range levels {
		x := xOf(int64(l.Price))
		y := yOf(cum[i])
		pts = append(pts, point2{x, prevY}, point2{x, y})
		prevY = y
	}
	return pts
}

func pathFrom(pts []point2) string {
	s := fmt.Sprintf("M %.2f %.2f", pts[0].x, pts[0].y)
	for _, p := range pts[1:] {
		s += fmt.Sprintf(" L %.2f %.2f", p.x, p.y)
	}
	return s
}

func dot(x, y float64, filled bool) string {
	fill := "none"
	if filled {
		fill = card.Ink()
	}
	return fmt.Sprintf(`<circle cx="%.2f" cy="%.2f" r="4" fill="%s" stroke="%s" stroke-width="1.5"/>`, x, y, fill, card.Ink())
}
