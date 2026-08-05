// Command fig1 regenerates docs/figures/fig1-recovery.svg from a real run of the
// deterministic synthetic source through the real engine — the same seed, step count,
// and injected faults the figure describes, not numbers copied from a prior run.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/source"
	"github.com/klimavojtech2002/tickerplant/tools/figuregen/card"
)

const (
	seed     = 42
	steps    = 250
	gapStep  = 101
	crossStp = 150
)

// point is one published view's top of book, indexed by how many Step calls had
// completed when it was published (0 = the bootstrap view).
type point struct {
	idx      int
	bid, ask int64
}

func main() {
	out := flag.String("out", "docs/figures/fig1-recovery.svg", "output SVG path")
	flag.Parse()

	cfg := source.Config{
		Venue: "synthetic", Symbol: "DEMO", Seed: seed, Steps: steps,
		Faults: map[int]source.Fault{gapStep: source.FaultGap, crossStp: source.FaultCross},
	}
	src := source.New(cfg)
	defer src.Close()

	eng := book.New(src, 10)
	ctx := context.Background()
	if err := eng.Bootstrap(ctx); err != nil {
		log.Fatalf("bootstrap: %v", err)
	}

	var points []point
	record := func(idx int) {
		v := eng.View()
		if v == nil || len(v.Bids) == 0 || len(v.Asks) == 0 {
			return
		}
		if v.Crosses() {
			log.Fatalf("invariant violated: published view at update %d crosses", idx)
		}
		points = append(points, point{idx, int64(v.Bids[0].Price), int64(v.Asks[0].Price)})
	}
	record(0)

	prevGaps, prevCross := eng.Gaps(), eng.WouldCrosses()
	gapAt, crossAt := -1, -1
	idx := 0
	for {
		ok, err := eng.Step(ctx)
		if err != nil {
			log.Fatalf("step: %v", err)
		}
		if !ok {
			break
		}
		idx++
		if eng.Gaps() > prevGaps {
			gapAt, prevGaps = idx, eng.Gaps()
		}
		if eng.WouldCrosses() > prevCross {
			crossAt, prevCross = idx, eng.WouldCrosses()
		}
		record(idx)
	}

	if len(points) == 0 {
		log.Fatalf("no published view captured (empty book)")
	}
	if err := os.WriteFile(*out, []byte(render(points, gapAt, crossAt, eng)), 0o644); err != nil {
		log.Fatalf("write: %v", err)
	}
	fmt.Printf("wrote %s: %d published states, gap detected at update %d, cross rejected at update %d\n",
		*out, len(points), gapAt, crossAt)
	fmt.Printf("gaps=%d resyncs=%d would-cross=%d\n", eng.Gaps(), eng.Resyncs(), eng.WouldCrosses())
}

const (
	plotW, plotH = card.ContentWidth, 420
)

func render(points []point, gapAt, crossAt int, eng *book.Engine) string {
	crossed := 0
	for _, p := range points {
		if p.bid >= p.ask {
			crossed++
		}
	}

	minP, maxP := points[0].bid, points[0].ask
	for _, p := range points {
		if p.bid < minP {
			minP = p.bid
		}
		if p.ask > maxP {
			maxP = p.ask
		}
	}
	pad := (maxP - minP) / 10
	if pad == 0 {
		pad = 1
	}
	minP -= pad
	maxP += pad
	maxIdx := points[len(points)-1].idx

	xOf := func(idx int) float64 { return float64(idx) / float64(maxIdx) * float64(plotW) }
	yOf := func(p int64) float64 {
		return float64(plotH) - float64(p-minP)/float64(maxP-minP)*float64(plotH)
	}

	var plot string
	plot += card.Grid(plotW, plotH, 5)
	plot += stepPath(points, xOf, yOf, func(p point) int64 { return p.bid }, "", card.Ink())
	plot += stepPath(points, xOf, yOf, func(p point) int64 { return p.ask }, "6,4", card.Ink())

	if gapAt >= 0 {
		plot += annotation(xOf(gapAt), plotH, "gap detected — update "+fmtInt(gapAt), "resynced — same update")
	}
	if crossAt >= 0 {
		plot += annotation(xOf(crossAt), plotH, "cross rejected — update "+fmtInt(crossAt), "resynced — same update")
	}

	plot += fmt.Sprintf(`<text x="4" y="14" font-size="11" fill="%s">%d</text>`, card.Gray(), maxP)
	plot += fmt.Sprintf(`<text x="4" y="%d" font-size="11" fill="%s">%d</text>`, plotH-8, card.Gray(), minP)
	plot += fmt.Sprintf(`<text x="0" y="%d" font-size="12" fill="%s">update 0</text>`, plotH+18, card.Gray())
	plot += fmt.Sprintf(`<text x="%d" y="%d" font-size="12" fill="%s" text-anchor="end">update %d</text>`,
		plotW, plotH+18, card.Gray(), maxIdx)

	d := card.Data{
		Kicker: "tickerplant — order-book correctness",
		Title:  []string{"Figure 1 — Best bid/ask", "through two injected failures"},
		Description: []string{
			"Deterministic synthetic source, seed " + fmtInt(seed) + ", " + fmtInt(steps) + " steps: one gap and",
			"one illegal (crossing) delta scheduled mid-run. The published top of book,",
			"bid and ask, over the whole run.",
		},
		Legend:     []card.Legend{{Label: "Bid"}, {Label: "Ask", Dash: "6,4"}},
		PlotWidth:  plotW,
		PlotHeight: plotH,
		Plot:       plot,
		Caption: []card.CaptionLine{
			{Text: fmt.Sprintf("%d of %d published book states ever crossed — not through the missing sequence", crossed, len(points)), Bold: true},
			{Text: "number, not through the rejected illegal quote."},
			{Text: fmt.Sprintf("Gaps: %d · Resyncs: %d · Rejected cross attempts: %d", eng.Gaps(), eng.Resyncs(), eng.WouldCrosses())},
			{Text: fmt.Sprintf("Synthetic source (internal/source/synthetic.go), seed %d, %d steps: a FaultGap", seed, steps), Italic: true},
			{Text: fmt.Sprintf("(update %d) and a FaultCross (update %d), both detected and resynced within the", gapAt, crossAt), Italic: true},
			{Text: "update they occurred.", Italic: true},
		},
	}
	return card.Render(d)
}

func stepPath(points []point, xOf func(int) float64, yOf func(int64) float64, val func(point) int64, dash, color string) string {
	if len(points) == 0 {
		return ""
	}
	path := fmt.Sprintf("M %.2f %.2f", xOf(points[0].idx), yOf(val(points[0])))
	for i := 1; i < len(points); i++ {
		x := xOf(points[i].idx)
		yPrev := yOf(val(points[i-1]))
		y := yOf(val(points[i]))
		path += fmt.Sprintf(" L %.2f %.2f L %.2f %.2f", x, yPrev, x, y)
	}
	dashAttr := ""
	if dash != "" {
		dashAttr = fmt.Sprintf(` stroke-dasharray="%s"`, dash)
	}
	return fmt.Sprintf(`<path d="%s" fill="none" stroke="%s" stroke-width="2"%s/>`, path, color, dashAttr)
}

func annotation(x float64, plotH int, line1, line2 string) string {
	var b string
	b += fmt.Sprintf(`<line x1="%.2f" y1="0" x2="%.2f" y2="%d" stroke="%s" stroke-width="1" stroke-dasharray="4,3"/>`,
		x, x, plotH, card.Ink())
	b += fmt.Sprintf(`<text x="%.2f" y="18" font-size="12" fill="%s">%s</text>`, x+8, card.Gray(), line1)
	b += fmt.Sprintf(`<text x="%.2f" y="34" font-size="12" fill="%s">%s</text>`, x+8, card.Gray(), line2)
	return b
}

func fmtInt(n int) string { return fmt.Sprintf("%d", n) }
