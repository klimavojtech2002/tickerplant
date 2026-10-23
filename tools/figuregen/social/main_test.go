package main

import (
	"os"
	"testing"
)

// The committed figure must be what the command produces. If this fails after a change
// to the engine, the source or the card layout, regenerate the file with
//
//	go run ./tools/figuregen/social
//
// and read the diff before committing it.
func TestCommittedFigureIsReproducible(t *testing.T) {
	svg, _, _, err := run()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../../docs/figures/social-preview.svg")
	if err != nil {
		t.Fatal(err)
	}
	got := []byte(svg)
	if string(got) == string(want) {
		return
	}
	at := 0
	for at < len(got) && at < len(want) && got[at] == want[at] {
		at++
	}
	t.Fatalf("docs/figures/social-preview.svg differs from the generated figure (%d bytes generated, %d committed, first difference at byte %d); regenerate with: go run ./tools/figuregen/social",
		len(got), len(want), at)
}

// Two runs give the same bytes: the figure has no clock or randomness outside the seed.
func TestRunIsDeterministic(t *testing.T) {
	a, _, _, err := run()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("two runs produced different figures")
	}
}
