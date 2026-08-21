// Command social regenerates docs/figures/social-preview.svg: a 1280x640 dark-mode
// card for GitHub's repo social preview / LinkedIn link thumbnails. Same real
// simulation run as fig1, rendered as a visual-first card with no headline copy —
// LinkedIn's own title/description fields carry the words.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

const (
	seed     = 42
	steps    = 250
	gapStep  = 101
	crossStp = 150
)

type point struct {
	idx      int
	bid, ask int64
}

func main() {
	out := flag.String("out", "docs/figures/social-preview.svg", "output SVG path")
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
		record(idx)
	}
	if len(points) == 0 {
		log.Fatalf("no published view captured (empty book)")
	}

	crossed := 0
	for _, p := range points {
		if p.bid >= p.ask {
			crossed++
		}
	}

	if err := os.WriteFile(*out, []byte(render(points, len(points), crossed)), 0o644); err != nil {
		log.Fatalf("write: %v", err)
	}
	fmt.Printf("wrote %s: %d published states, %d crossed\n", *out, len(points), crossed)
}

const (
	width, height = 1280, 640
	bg            = "#ffffff"
	line          = "#161616"
	accent        = "#d2222d"
	dim           = "#6b6b6b"
)

func render(points []point, total, crossed int) string {
	minP, maxP := points[0].bid, points[0].ask
	for _, p := range points {
		if p.bid < minP {
			minP = p.bid
		}
		if p.ask > maxP {
			maxP = p.ask
		}
	}
	pad := (maxP - minP) / 8
	if pad == 0 {
		pad = 1
	}
	minP -= pad
	maxP += pad
	maxIdx := points[len(points)-1].idx

	const (
		plotX, plotY = 90, 90
		plotW, plotH = 1100, 460
	)
	xOf := func(idx int) float64 { return plotX + float64(idx)/float64(maxIdx)*float64(plotW) }
	yOf := func(p int64) float64 {
		return plotY + float64(plotH) - float64(p-minP)/float64(maxP-minP)*float64(plotH)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" font-family="-apple-system,Segoe UI,Helvetica,Arial,sans-serif">`, width, height)
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%d" height="%d" fill="%s"/>`, width, height, bg)

	// Bid (steady line/no marker color), ask (accent-highlighted where it steps
	// through the two recovery points), both as clean step paths — thicker strokes
	// than the docs figures so the shape reads at thumbnail size.
	bidPath := stepPath(points, xOf, yOf, func(p point) int64 { return p.bid })
	askPath := stepPath(points, xOf, yOf, func(p point) int64 { return p.ask })
	fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="3" stroke-linejoin="round"/>`, bidPath, line)
	fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="3" stroke-linejoin="round" stroke-dasharray="10,7"/>`, askPath, accent)

	// Legend, so the two lines read as bid/ask without needing surrounding context.
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s" stroke-width="3"/>`, plotX, plotY-34, plotX+34, plotY-34, line)
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="17" fill="%s">bid</text>`, plotX+44, plotY-28, dim)
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s" stroke-width="3" stroke-dasharray="8,6"/>`, plotX+110, plotY-34, plotX+144, plotY-34, accent)
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="17" fill="%s">ask</text>`, plotX+154, plotY-28, dim)

	// The two recovery moments, each marked and labeled: what broke, and that it
	// resynced immediately — this is the whole story the image has to tell alone.
	labels := map[int][2]string{102: {"gap", "resynced"}, 150: {"illegal quote", "resynced"}}
	for _, at := range []int{102, 150} {
		if at > maxIdx {
			continue
		}
		x := xOf(at)
		fmt.Fprintf(&b, `<line x1="%.2f" y1="%d" x2="%.2f" y2="%d" stroke="%s" stroke-width="1.5" stroke-dasharray="3,4" opacity="0.7"/>`,
			x, plotY, x, plotY+plotH, dim)
		lbl := labels[at]
		fmt.Fprintf(&b, `<text x="%.2f" y="%d" font-size="15" fill="%s">%s</text>`, x+10, plotY+22, line, lbl[0])
		fmt.Fprintf(&b, `<text x="%.2f" y="%d" font-size="15" fill="%s">%s</text>`, x+10, plotY+44, accent, lbl[1])
	}

	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="20" font-family="ui-monospace,Consolas,monospace" fill="%s">%d of %d published book states ever crossed</text>`,
		plotX, height-48, dim, crossed, total)
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="18" font-weight="600" fill="%s" text-anchor="end">tickerplant</text>`,
		width-90, height-48, line)

	b.WriteString(`</svg>`)
	return b.String()
}

func stepPath(points []point, xOf func(int) float64, yOf func(int64) float64, val func(point) int64) string {
	path := fmt.Sprintf("M %.2f %.2f", xOf(points[0].idx), yOf(val(points[0])))
	for i := 1; i < len(points); i++ {
		x := xOf(points[i].idx)
		yPrev := yOf(val(points[i-1]))
		y := yOf(val(points[i]))
		path += fmt.Sprintf(" L %.2f %.2f L %.2f %.2f", x, yPrev, x, y)
	}
	return path
}
