// Package card renders the docs/figures cards: a shared SVG frame (kicker, title,
// legend, bordered plot area, caption lines, footer link) so each figure's generator
// only builds its own plot markup, not the chrome around it.
package card

import (
	"fmt"
	"strings"
)

// ContentWidth is the plot area's width in SVG user units; a generator sizes its
// plot markup to this so it lines up with the card's margins.
const ContentWidth = width - 2*margin

const (
	width  = 1000
	margin = 40

	bg   = "#f7f6f2"
	ink  = "#1a1a1a"
	gray = "#767672"
	grid = "#dcdad4"

	// footerURL is the one place this repo's URL is written for a figure footer, so a
	// figure can never carry the broken concatenation the first hand-made batch did.
	footerURL = "github.com/klimavojtech2002/tickerplant"
)

// Legend is one entry in the plot's line legend (e.g. "Bid" solid, "Ask" dashed).
type Legend struct {
	Label string
	Dash  string // "" = solid; e.g. "6,4" = dashed
}

// CaptionLine is one line of text under the plot. Bold reads as a callout stat,
// Italic as a methodology/provenance note; plain is a secondary detail line.
type CaptionLine struct {
	Text   string
	Bold   bool
	Italic bool
}

// Data is everything a figure needs to render: text content plus one pre-built plot
// fragment (SVG markup with its own origin at the plot area's top-left corner).
type Data struct {
	Kicker      string
	Title       []string
	Description []string
	Legend      []Legend
	PlotWidth   int
	PlotHeight  int
	Plot        string
	Caption     []CaptionLine
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func esc(s string) string { return xmlEscaper.Replace(s) }

// Render lays out Data into a complete standalone SVG document.
func Render(d Data) string {
	y := margin

	y += 14
	kickerY := y
	y += 26

	titleStartY := y
	y += 34 * len(d.Title)

	y += 8
	descStartY := y
	y += 20 * len(d.Description)

	y += 12
	legendY := y
	y += 26

	plotY := y
	y += d.PlotHeight
	y += 50

	captionStartY := y
	y += 20 * len(d.Caption)

	y += 16
	dividerY := y
	y += 26
	totalHeight := y + margin

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" font-family="-apple-system,Segoe UI,Helvetica,Arial,sans-serif">`, width, totalHeight)
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%d" height="%d" fill="%s"/>`, width, totalHeight, bg)

	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="11" letter-spacing="1.5" fill="%s">%s</text>`,
		margin, kickerY, gray, esc(strings.ToUpper(d.Kicker)))

	ty := titleStartY
	for _, line := range d.Title {
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="Georgia,'Times New Roman',serif" font-size="26" font-weight="700" fill="%s">%s</text>`,
			margin, ty, ink, esc(line))
		ty += 34
	}

	dy := descStartY
	for _, line := range d.Description {
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="14" fill="#3a3a38">%s</text>`, margin, dy, esc(line))
		dy += 20
	}

	lx := margin
	for _, lg := range d.Legend {
		dash := ""
		if lg.Dash != "" {
			dash = fmt.Sprintf(` stroke-dasharray="%s"`, lg.Dash)
		}
		fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s" stroke-width="2"%s/>`,
			lx, legendY-5, lx+24, legendY-5, ink, dash)
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="13" fill="#3a3a38">%s</text>`, lx+32, legendY, esc(lg.Label))
		lx += 32 + len(lg.Label)*7 + 28
	}

	fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" fill="none" stroke="%s" stroke-width="1.5"/>`,
		margin, plotY, d.PlotWidth, d.PlotHeight, ink)
	fmt.Fprintf(&b, `<g transform="translate(%d,%d)">%s</g>`, margin, plotY, d.Plot)

	cy := captionStartY
	for _, c := range d.Caption {
		weight, style, fill := "400", "normal", "#3a3a38"
		if c.Bold {
			weight, fill = "700", ink
		}
		if c.Italic {
			style, fill = "italic", gray
		}
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="14" font-weight="%s" font-style="%s" fill="%s">%s</text>`,
			margin, cy, weight, style, fill, esc(c.Text))
		cy += 20
	}

	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="%s" stroke-width="1"/>`,
		margin, dividerY, width-margin, dividerY, grid)
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="12" fill="%s">%s</text>`, margin, dividerY+22, gray, footerURL)

	b.WriteString(`</svg>`)
	return b.String()
}

// Grid draws n evenly spaced horizontal gridlines across a w x h plot area (including
// the top and bottom edges), the light rules figures 1-3 use behind their data.
func Grid(w, h, n int) string {
	var b strings.Builder
	for i := range n {
		y := float64(i) / float64(n-1) * float64(h)
		fmt.Fprintf(&b, `<line x1="0" y1="%.1f" x2="%d" y2="%.1f" stroke="%s" stroke-width="1"/>`, y, w, y, grid)
	}
	return b.String()
}

// Ink and Gray expose the shared palette to figure-specific plot code (axis labels,
// annotation text) so every figure draws from the same two ink colors.
func Ink() string  { return ink }
func Gray() string { return gray }
