package kraken

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/source"
)

// wireFrame renders levels back to the wire form (JSON numbers at scales 1/8) so test
// frames look exactly like the captured live feed.
func wireFrame(typ string, bids, asks []market.Level, checksum uint32) []byte {
	level := func(l market.Level) string {
		return fmt.Sprintf(`{"price":%s,"qty":%s}`,
			market.FormatScaled(int64(l.Price), 1), market.FormatScaled(int64(l.Size), 8))
	}
	var bs, as []string
	for _, l := range bids {
		bs = append(bs, level(l))
	}
	for _, l := range asks {
		as = append(as, level(l))
	}
	return fmt.Appendf(nil, `{"channel":"book","type":"%s","data":[{"symbol":"BTC/USD","bids":[%s],"asks":[%s],"checksum":%d}]}`,
		typ, strings.Join(bs, ","), strings.Join(as, ","), checksum)
}

func newTestSource(frames chan []byte, redial func() <-chan []byte) *Source {
	return New(Config{
		Venue: "kraken", Symbol: "BTC/USD",
		PriceScale: 1, QtyScale: 8,
		Frames: frames, Redial: redial,
	})
}

// The in-band snapshot and the updates that follow it must form one contiguous
// synthetic sequence, so the engine's gap logic never fires on a healthy feed.
func TestSnapshotThenDeltasContiguous(t *testing.T) {
	bids, asks := goldenBook()
	ch := make(chan []byte, 5)
	ch <- wireFrame("snapshot", bids, asks, goldenChecksum)
	ch <- wireFrame("update", []market.Level{{Price: 452836, Size: 5}}, nil, 1)
	// noise between updates must be skipped by Next without consuming a sequence id
	ch <- []byte(`{"channel":"heartbeat"}`)
	ch <- []byte(`{"channel":"book","type":"update","data":[{"bids":[{"price":4.5e4,"qty":1}],"asks":[]}]}`) // malformed decimal: skipped
	ch <- wireFrame("update", nil, []market.Level{{Price: 452851, Size: 7}}, 2)
	s := newTestSource(ch, nil)

	snap, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.LastUpdateID != 1 || snap.Checksum != goldenChecksum {
		t.Fatalf("snapshot = seq %d checksum %d, want seq 1 checksum %d", snap.LastUpdateID, snap.Checksum, uint32(goldenChecksum))
	}
	for i, wantSeq := range []market.Sequence{2, 3} {
		ev, ok := s.Next(context.Background())
		if !ok || ev.Kind != source.EventDelta {
			t.Fatalf("event %d: ok=%v kind=%v, want a delta", i, ok, ev.Kind)
		}
		if ev.Delta.FirstSeq != wantSeq || ev.Delta.LastSeq != wantSeq {
			t.Fatalf("event %d: seq [%d,%d], want [%d,%d]", i, ev.Delta.FirstSeq, ev.Delta.LastSeq, wantSeq, wantSeq)
		}
		if ev.Received.IsZero() {
			t.Fatalf("event %d: Received is zero; the latency span must start at frame dequeue", i)
		}
	}
}

// A snapshot arriving mid-stream means the transport reconnected and re-subscribed:
// Next must surface a disconnect so the engine rebinds, and the following Snapshot
// must return that cached snapshot without redialing.
func TestMidStreamSnapshotEmitsDisconnectThenRebinds(t *testing.T) {
	bids, asks := goldenBook()
	ch := make(chan []byte, 4)
	ch <- wireFrame("snapshot", bids, asks, goldenChecksum)
	ch <- wireFrame("update", []market.Level{{Price: 452836, Size: 5}}, nil, 1)
	ch <- wireFrame("snapshot", bids, asks, goldenChecksum) // reconnect's fresh snapshot
	ch <- wireFrame("update", nil, []market.Level{{Price: 452851, Size: 7}}, 2)
	redials := 0
	s := newTestSource(ch, func() <-chan []byte { redials++; return ch })

	if _, err := s.Snapshot(context.Background()); err != nil { // seq 1
		t.Fatal(err)
	}
	if ev, ok := s.Next(context.Background()); !ok || ev.Kind != source.EventDelta || ev.Delta.LastSeq != 2 {
		t.Fatalf("first update: %+v ok=%v, want delta seq 2", ev, ok)
	}
	ev, ok := s.Next(context.Background())
	if !ok || ev.Kind != source.EventDisconnected {
		t.Fatalf("mid-stream snapshot: %+v ok=%v, want a disconnect", ev, ok)
	}
	snap, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.LastUpdateID != 3 {
		t.Fatalf("rebind snapshot seq = %d, want 3 (snapshot took the next synthetic id)", snap.LastUpdateID)
	}
	if redials != 0 {
		t.Fatalf("redials = %d, want 0 (the cached in-band snapshot suffices)", redials)
	}
	if ev, ok := s.Next(context.Background()); !ok || ev.Delta.LastSeq != 4 {
		t.Fatalf("post-rebind update: %+v ok=%v, want delta seq 4 (contiguous)", ev, ok)
	}
}

// A drift resync (checksum mismatch, socket alive) has no snapshot to wait for on the
// current connection: Snapshot must redial and bind the new subscription's snapshot,
// discarding any pre-snapshot updates from it — they describe a book the snapshot
// supersedes.
func TestSnapshotRedialsWhenNoneExpected(t *testing.T) {
	bids, asks := goldenBook()
	ch1 := make(chan []byte, 1)
	ch1 <- wireFrame("snapshot", bids, asks, goldenChecksum)

	ch2 := make(chan []byte, 4)
	ch2 <- []byte(`{"channel":"heartbeat"}`)
	ch2 <- wireFrame("update", []market.Level{{Price: 111111, Size: 1}}, nil, 9) // pre-snapshot: superseded
	ch2 <- wireFrame("snapshot", bids, asks, goldenChecksum)
	ch2 <- wireFrame("update", []market.Level{{Price: 452836, Size: 5}}, nil, 1)

	redials := 0
	s := newTestSource(ch1, func() <-chan []byte { redials++; return ch2 })

	if _, err := s.Snapshot(context.Background()); err != nil { // consumes ch1's snapshot
		t.Fatal(err)
	}
	snap, err := s.Snapshot(context.Background()) // drift resync: nothing pending
	if err != nil {
		t.Fatal(err)
	}
	if redials != 1 {
		t.Fatalf("redials = %d, want 1", redials)
	}
	if snap.LastUpdateID != 2 || snap.Checksum != goldenChecksum {
		t.Fatalf("redial snapshot = seq %d checksum %d, want seq 2 golden", snap.LastUpdateID, snap.Checksum)
	}
	ev, ok := s.Next(context.Background())
	if !ok || ev.Delta.LastSeq != 3 {
		t.Fatalf("post-redial update: %+v ok=%v, want delta seq 3", ev, ok)
	}
	if ev.Delta.Bids[0].Price == 111111 {
		t.Fatal("the superseded pre-snapshot update leaked through Next")
	}
}

// A malformed in-band snapshot must fail the waiting Snapshot loudly: nothing else on
// that subscription can end the wait, so skipping it would hang bootstrap forever.
func TestSnapshotMalformedInBandSnapshotFailsLoud(t *testing.T) {
	ch := make(chan []byte, 1)
	ch <- []byte(`{"channel":"book","type":"snapshot","data":[{"symbol":"BTC/USD",` +
		`"bids":[{"price":45284.05,"qty":1}],"asks":[]}]}`) // over-precision price at scale 1
	s := newTestSource(ch, nil)
	if _, err := s.Snapshot(context.Background()); !errors.Is(err, market.ErrMalformed) {
		t.Fatalf("err = %v, want a loud ErrMalformed, not a hang", err)
	}
}

// Without a Redial there is no way to obtain a second snapshot: fail loudly rather
// than block forever on a connection that will never send one.
func TestSnapshotWithoutRedialFailsLoud(t *testing.T) {
	bids, asks := goldenBook()
	ch := make(chan []byte, 1)
	ch <- wireFrame("snapshot", bids, asks, goldenChecksum)
	s := newTestSource(ch, nil)
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(context.Background()); !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("err = %v, want ErrNoSnapshot", err)
	}
}

// Closing the source, cancelling the context, and a closed frame channel must all end
// Next/Snapshot instead of hanging.
func TestNextAndSnapshotStop(t *testing.T) {
	s := newTestSource(make(chan []byte), nil)
	_ = s.Close()
	if _, ok := s.Next(context.Background()); ok {
		t.Fatal("Next after Close must report done")
	}
	if _, err := s.Snapshot(context.Background()); !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("Snapshot after Close: %v, want ErrNoSnapshot", err)
	}

	closed := make(chan []byte)
	close(closed)
	s2 := newTestSource(closed, nil)
	if _, ok := s2.Next(context.Background()); ok {
		t.Fatal("Next on a closed frame channel must report done")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s3 := newTestSource(make(chan []byte), nil)
	if _, ok := s3.Next(ctx); ok {
		t.Fatal("Next on a cancelled context must report done")
	}
	if _, err := s3.Snapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Snapshot on a cancelled context: %v, want context.Canceled", err)
	}

	s4 := newTestSource(closed, nil)
	if _, err := s4.Snapshot(context.Background()); !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("Snapshot on a closed frame channel: %v, want ErrNoSnapshot", err)
	}
}

// The full engine run over a scripted healthy feed must see zero gaps: the synthetic
// sequence is what keeps the engine's continuity check inert for Kraken.
func TestEngineRunSyntheticSequenceNoGaps(t *testing.T) {
	bids, asks := goldenBook()
	ch := make(chan []byte, 7)
	ch <- wireFrame("snapshot", bids, asks, goldenChecksum)
	for i := range 5 {
		ch <- wireFrame("update", []market.Level{{Price: market.Price(452800 + i), Size: 5}}, nil, 0)
	}
	close(ch)
	s := newTestSource(ch, nil)
	eng := book.New(s, 10)
	if err := eng.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if eng.Gaps() != 0 || eng.Resyncs() != 0 || eng.Disconnects() != 0 {
		t.Fatalf("gaps=%d resyncs=%d disconnects=%d, want 0/0/0 on a healthy scripted feed",
			eng.Gaps(), eng.Resyncs(), eng.Disconnects())
	}
	if v := eng.View(); v == nil || v.Crosses() {
		t.Fatal("view must exist and not cross")
	}
}

// --- the composed seam (audit 0021 finding 4): engine + the real Kraken CRC ---

// The engine bootstrapping through WithChecksum(real Kraken CRC) against the guide's
// worked example must reproduce Kraken's published checksum. This anchors the whole
// composition — engine top-10 order, scales, field format — to an external value;
// swapped price/qty scales or a bid/ask order mistake at the call site cannot pass.
func TestSeamGoldenSnapshotBinds(t *testing.T) {
	bids, asks := goldenBook()
	ch := make(chan []byte, 1)
	ch <- wireFrame("snapshot", bids, asks, goldenChecksum)
	s := newTestSource(ch, nil)
	eng := book.New(s, 10).WithChecksum(Checksum).WithMaxDepth(BookDepth)
	if err := eng.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap against the golden vector: %v", err)
	}
	if eng.ChecksumMismatches() != 0 {
		t.Fatalf("ChecksumMismatches = %d, want 0", eng.ChecksumMismatches())
	}
	if v := eng.View(); v == nil || v.Crosses() || v.LastSeq != 1 {
		t.Fatalf("view = %+v, want uncrossed at synthetic seq 1", v)
	}
}

// A matching update must apply through the seam with no drift: the checksum recomputed
// by the engine over its own post-apply top-10 equals the one carried on the wire.
func TestSeamMatchingUpdateApplies(t *testing.T) {
	bids, asks := goldenBook()
	newBids := append([]market.Level{{Price: 452836, Size: 123456}}, bids...) // new best bid
	want := Checksum(newBids[:10], asks)

	ch := make(chan []byte, 2)
	ch <- wireFrame("snapshot", bids, asks, goldenChecksum)
	ch <- wireFrame("update", []market.Level{{Price: 452836, Size: 123456}}, nil, want)
	s := newTestSource(ch, nil)
	eng := book.New(s, 10).WithChecksum(Checksum).WithMaxDepth(BookDepth)
	if err := eng.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ok, err := eng.Step(context.Background()); !ok || err != nil {
		t.Fatalf("step: ok=%v err=%v", ok, err)
	}
	if eng.ChecksumMismatches() != 0 || eng.Resyncs() != 0 {
		t.Fatalf("mismatches=%d resyncs=%d, want 0/0 (the update's checksum matches)", eng.ChecksumMismatches(), eng.Resyncs())
	}
	if v := eng.View(); v.Bids[0].Price != 452836 || v.LastSeq != 2 {
		t.Fatalf("view best bid %+v seq %d, want the new bid at seq 2", v.Bids[0], v.LastSeq)
	}
}

// Drift end-to-end: an update whose checksum does not match the engine's rebuilt book
// must trigger a resync, which redials for a fresh in-band snapshot and rebinds to an
// uncrossed, checksum-verified book.
func TestSeamDriftResyncsViaRedial(t *testing.T) {
	bids, asks := goldenBook()
	ch1 := make(chan []byte, 2)
	ch1 <- wireFrame("snapshot", bids, asks, goldenChecksum)
	ch1 <- wireFrame("update", []market.Level{{Price: 452836, Size: 5}}, nil, 12345) // wrong checksum: drift
	ch2 := make(chan []byte, 1)
	ch2 <- wireFrame("snapshot", bids, asks, goldenChecksum)

	redials := 0
	s := newTestSource(ch1, func() <-chan []byte { redials++; return ch2 })
	eng := book.New(s, 10).WithChecksum(Checksum).WithMaxDepth(BookDepth)
	if err := eng.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ok, err := eng.Step(context.Background()); !ok || err != nil { // drift -> resync -> redial
		t.Fatalf("step: ok=%v err=%v", ok, err)
	}
	if eng.ChecksumMismatches() != 1 || eng.Resyncs() != 1 || redials != 1 {
		t.Fatalf("mismatches=%d resyncs=%d redials=%d, want 1/1/1", eng.ChecksumMismatches(), eng.Resyncs(), redials)
	}
	if v := eng.View(); v == nil || v.Crosses() || v.Bids[0] != bids[0] {
		t.Fatalf("post-resync view = %+v, want the golden book rebound", v)
	}
}
