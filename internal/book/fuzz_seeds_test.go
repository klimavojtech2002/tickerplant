package book

// Seeds for FuzzEngineSteps. A seed is an input written with named operations, never hand-written
// bytes, and each carries a check of what it was written to contain.

// seedBuilder writes an input with named operations instead of hand-written bytes.
type seedBuilder struct{ b []byte }

func newSeed(depth int) *seedBuilder {
	s := &seedBuilder{b: []byte{byte(depth - 1)}}
	for i := range 12 {
		s.b = append(s.b, byte(3+i%7)) // the twelve initial sizes differ: 4, 5, ..., 10, 4, ...
	}
	return s
}

func (s *seedBuilder) op(kind int, side, off, sz int) *seedBuilder {
	s.b = append(s.b, byte(kind)|byte(side)<<7, byte(off)<<3|byte(sz&7))
	return s
}

func (s *seedBuilder) legal(side, off, sz int) *seedBuilder { return s.op(0, side, off, sz) }
func (s *seedBuilder) gap(side, off, sz int) *seedBuilder   { return s.op(opGap, side, off, sz) }
func (s *seedBuilder) reorder(side, off, sz int) *seedBuilder {
	return s.op(opReorder, side, off, sz)
}
func (s *seedBuilder) duplicate(side, off, sz int) *seedBuilder {
	return s.op(opDuplicate, side, off, sz)
}
func (s *seedBuilder) coalesce(side, off, sz int) *seedBuilder {
	return s.op(opCoalesce, side, off, sz)
}
func (s *seedBuilder) disconnect() *seedBuilder { return s.op(opDisconnect, 0, 0, 0) }

// cross appends a crossing delta on the given side (0 bid, 1 ask) or, with locked, one that locks the book.
func (s *seedBuilder) cross(side int, locked bool, off, sz int) *seedBuilder {
	if locked {
		sz |= 1
	} else {
		sz &^= 1
	}
	return s.op(opCross, side, off, sz)
}

// marker arms n crossed snapshots (kind 0), n locked ones (1) or one old one (2); kind 3 is a stale delta.
func (s *seedBuilder) marker(kind, n, stale int) *seedBuilder {
	s.b = append(s.b, byte(opMarker), byte(kind&3)|byte(n-1)<<2|byte(stale)<<2)
	return s
}

func (s *seedBuilder) bytes() []byte { return s.b }

type fuzzSeed struct {
	name  string
	data  []byte
	check func(*fuzzScript) bool // what the seed was written to contain
}

func fuzzSeedList() []fuzzSeed {
	legals := func(s *seedBuilder, n int) *seedBuilder {
		for i := range n {
			s.legal(i%2, i%32, 1+i%7)
		}
		return s
	}
	clean := legals(newSeed(8), 40)
	// A change that only a gap hides, in the top two levels of a depth-2 book and never touched by the tail.
	gapHidden := newSeed(2).legal(0, 4, 3).gap(0, 1, 5).legal(1, 3, 2)
	oneEach := newSeed(5).legal(0, 3, 2).gap(0, 2, 4).legal(1, 3, 2).reorder(1, 4, 1).legal(0, 6, 3).
		duplicate(0, 5, 2).coalesce(1, 6, 3).cross(0, false, 3, 2).legal(0, 7, 1).cross(0, true, 0, 3).legal(1, 8, 2).cross(1, false, 4, 2).legal(0, 8, 2).cross(1, true, 0, 3).legal(1, 9, 2).
		disconnect().legal(0, 9, 3).marker(3, 1, 20).legal(1, 9, 2)
	lockedSnap := legals(newSeed(4), 4).marker(1, 1, 0).disconnect()
	crossedSnap := legals(newSeed(4), 4).marker(0, 3, 0).disconnect()
	oldSnap := legals(newSeed(4), 6).marker(2, 2, 0).disconnect()
	mix := legals(newSeed(6), 5).gap(1, 2, 3).marker(2, 1, 0).reorder(0, 5, 2).marker(0, 2, 0).disconnect().
		cross(0, false, 5, 1).coalesce(0, 4, 4).marker(1, 2, 0).gap(1, 6, 2).disconnect()
	// Deletes: a present level, an absent one, and every bid level of the book (the last is kept).
	deletes := newSeed(3).legal(0, 2, 0).legal(0, 3, 0).legal(1, 4, 0).legal(1, 3, 0)
	emptySide := newSeed(3)
	for _, off := range []int{2, 4, 6, 8, 10, 12} { // the six initial bids: 900, 890, ..., 850
		emptySide.legal(0, off, 0)
	}
	// An old snapshot that loses a change the tail never touches (the tail's bid offsets are 0, 10, 20,
	// 30, 8, 18, 28, 6, 16, 26), so a failure to notice the old snapshot stays visible.
	oldLoses := newSeed(2).legal(1, 2, 3).legal(0, 1, 5).marker(2, 2, 0).disconnect()
	livelock := legals(newSeed(4), 3)
	for range 18 {
		livelock.disconnect()
	}
	spaced := newSeed(4)
	for i := range 18 {
		spaced.disconnect()
		legals(spaced, 3+i%2)
	}
	eightBad := legals(newSeed(4), 3).marker(0, 4, 0).marker(1, 4, 0).disconnect()
	sevenBad := legals(newSeed(4), 3).marker(0, 4, 0).marker(1, 3, 0).disconnect()
	return []fuzzSeed{
		{"empty", nil, func(sc *fuzzScript) bool { return sc.faults == 0 && sc.legalOps == 0 }},
		{"clean", clean.bytes(), func(sc *fuzzScript) bool { return sc.faults == 0 && sc.legalOps == 40 }},
		{"gap-hides-a-top-change", gapHidden.bytes(), func(sc *fuzzScript) bool { return sc.gapOps == 1 && sc.depth == 2 }},
		{"one-of-each", oneEach.bytes(), func(sc *fuzzScript) bool {
			return sc.gapOps == 1 && sc.reorderOps == 1 && sc.disconnectOps == 1 && sc.crossOps == 4 &&
				sc.dupOps == 1 && sc.coalesceOps == 1 && sc.staleOps == 1 && sc.lockedCrosses == 2 && sc.askCrosses == 2
		}},
		{"deletes", deletes.bytes(), func(sc *fuzzScript) bool { return sc.deletes == 4 && sc.faults == 0 }},
		{"empty-a-side", emptySide.bytes(), func(sc *fuzzScript) bool { return sc.deletes == 6 && sc.faults == 0 }},
		{"old-snapshot-loses-a-level", oldLoses.bytes(), func(sc *fuzzScript) bool {
			return sc.oldSnapshots == 1 && sc.disconnectOps == 1 && sc.depth == 2
		}},
		{"locked-snapshot", lockedSnap.bytes(), func(sc *fuzzScript) bool { return sc.armedBad == 1 && sc.disconnectOps == 1 }},
		{"crossed-snapshots", crossedSnap.bytes(), func(sc *fuzzScript) bool { return sc.armedBad == 3 && sc.disconnectOps == 1 }},
		{"old-snapshot", oldSnap.bytes(), func(sc *fuzzScript) bool { return sc.oldSnapshots == 1 && sc.disconnectOps == 1 }},
		{"mix", mix.bytes(), func(sc *fuzzScript) bool { return sc.faults >= 6 && sc.oldSnapshots == 1 && sc.armedBad == 4 }},
		{"livelock", livelock.bytes(), func(sc *fuzzScript) bool { return sc.disconnectOps == 18 }},
		{"spaced-faults", spaced.bytes(), func(sc *fuzzScript) bool { return sc.disconnectOps == 18 && sc.legalOps >= 54 }},
		{"eight-bad-snapshots", eightBad.bytes(), func(sc *fuzzScript) bool { return sc.armedBad == 8 && sc.disconnectOps == 1 }},
		{"seven-bad-snapshots", sevenBad.bytes(), func(sc *fuzzScript) bool { return sc.armedBad == 7 && sc.disconnectOps == 1 }},
	}
}
