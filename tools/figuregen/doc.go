// Package figuregen holds the commands that draw the figures in docs/figures.
//
// fig1 and social run the seeded synthetic source through the real engine; they need no
// network and their output is checked byte for byte against the committed SVG.
// fig2 and fig3 connect to the live Kraken feed, so they need the network and their output
// is a capture: fig3 stores its samples in docs/figures/fig3-capture.json and can redraw
// from that file with -from. Regenerate any figure with `go run ./tools/figuregen/<name>`
// from the repository root.
//
// The commands live in the module because they import internal/ packages.
package figuregen
