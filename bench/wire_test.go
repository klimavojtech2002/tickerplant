package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/metrics"
	"github.com/klimavojtech2002/tickerplant/internal/venue/binance"
)

// Wire frames for the wire benchmark. They are generated, not captured: synthetic Binance
// depthUpdate JSON in the real wire shape, pushed through the real binance.Source, so the decode and
// normalization cost is in the number. Prices live in a fixed band, bids below `mid - halfSpread` and asks
// above `mid + halfSpread`, so the book cannot cross or drift however many frames are drawn. The band holds
// at most wireBand levels a side, so the book the engine works on has about that many levels (not the
// thousands of a full Binance book); the README says so.
const (
	wireMid        = 6_000_000 // 60000.00 at price scale 2
	wireHalfSpread = 50
	wireBand       = 200
	wireFrameLevel = 8   // levels per side in one frame
	wireSnapLevels = 100 // levels per side in the snapshot
	wirePriceScale = 2
	wireSizeScale  = 8
	wireSizeUnit   = 100_000_000 // one whole unit at size scale 8
)

type wireFrame struct {
	raw        []byte
	bids, asks []market.Level // the levels the frame carries, as the adapter must decode them
}

type wireSet struct {
	snapshot     []byte
	lastUpdateID uint64
	frames       []wireFrame
	modelLevels  int // levels in the model's book after the last frame, both sides
}

func bidPrice(k int) market.Price { return market.Price(wireMid - wireHalfSpread - k) }
func askPrice(k int) market.Price { return market.Price(wireMid + wireHalfSpread + k) }

func fmtPrice(p market.Price) string { return fmt.Sprintf("%d.%02d", p/100, p%100) }
func fmtSize(s market.Size) string {
	return fmt.Sprintf("%d.%08d", s/wireSizeUnit, s%wireSizeUnit)
}

// buildWire builds a snapshot and n consecutive frames from a seed. The first frame follows the snapshot's
// lastUpdateId; each later one has U = u = the previous u + 1.
func buildWire(n int, seed int64) *wireSet {
	rng := rand.New(rand.NewSource(seed))
	bids, asks := map[int]market.Size{}, map[int]market.Size{}
	randSize := func() market.Size { return market.Size(1 + rng.Int63n(10*wireSizeUnit)) }
	for _, k := range rng.Perm(wireBand)[:wireSnapLevels] {
		bids[k] = randSize()
	}
	for _, k := range rng.Perm(wireBand)[:wireSnapLevels] {
		asks[k] = randSize()
	}
	const lastUpdateID = 1_000_000
	snapSide := func(m map[int]market.Size, price func(int) market.Price) [][]string {
		ks := make([]int, 0, len(m))
		for k := range m {
			ks = append(ks, k)
		}
		sort.Ints(ks)
		out := make([][]string, 0, len(ks))
		for _, k := range ks {
			out = append(out, []string{fmtPrice(price(k)), fmtSize(m[k])})
		}
		return out
	}
	// bids best first (smallest k is the highest bid), asks best first (smallest k is the lowest ask)
	snap, _ := json.Marshal(map[string]any{
		"lastUpdateId": lastUpdateID,
		"bids":         snapSide(bids, bidPrice),
		"asks":         snapSide(asks, askPrice),
	})
	ws := &wireSet{snapshot: snap, lastUpdateID: lastUpdateID, frames: make([]wireFrame, n)}
	side := func(m map[int]market.Size, price func(int) market.Price) ([][]string, []market.Level) {
		wire := make([][]string, 0, wireFrameLevel)
		lv := make([]market.Level, 0, wireFrameLevel)
		for _, k := range rng.Perm(wireBand)[:wireFrameLevel] {
			var size market.Size  // 0 deletes the level
			if rng.Intn(5) != 0 { // about 20 % deletes
				size = randSize()
				m[k] = size
			} else {
				delete(m, k)
			}
			wire = append(wire, []string{fmtPrice(price(k)), fmtSize(size)})
			lv = append(lv, market.Level{Price: price(k), Size: size})
		}
		return wire, lv
	}
	for i := range n {
		u := uint64(lastUpdateID + 1 + i)
		b, bl := side(bids, bidPrice)
		a, al := side(asks, askPrice)
		raw, _ := json.Marshal(map[string]any{"e": "depthUpdate", "E": 1700000000000 + i, "s": "BTCUSDT", "U": u, "u": u, "b": b, "a": a})
		ws.frames[i] = wireFrame{raw: raw, bids: bl, asks: al}
	}
	ws.modelLevels = len(bids) + len(asks)
	return ws
}

// wireData is built once per process: about 50 MB of frames is generated one time, not once per run.
var wireData = sync.OnceValue(func() *wireSet { return buildWire(benchWarmup+benchSamples, 1) })

// ---- the run -------------------------------------------------------------------------------------

type wireOpts struct {
	driver driver
	set    *wireSet
	// saturation is the largest share of the recorded events allowed to find two or more frames
	// waiting in the channel; 0 turns the guard off (the unit tests do).
	saturation float64
}

type wireResult struct {
	due, engine, publish *metrics.Histogram
	senderLate           int
	overInterval         int // events whose due-to-done exceeded one interval (includes scheduler wake-up)
	queueMax, deepQueue  int
	gcCycles             uint32
	elapsed              time.Duration
	resyncs, gaps        int
	wouldCrosses         int
	rejected, published  uint64
}

// runWire pushes frames through binance.Source into the engine and the fan-out. A pacer goroutine sends
// frame k when it is due, never earlier; this goroutine runs the engine, publishes, and records
// done - due(k) for every event after the warmup. The engine's own span (adapter dequeue to stored view)
// comes from the engine's latency observer, the same wiring the application uses.
func runWire(o wireOpts) (wireResult, error) {
	d, set := o.driver, o.set
	total := d.warmup + d.samples
	if len(set.frames) < total {
		return wireResult{}, fmt.Errorf("%d frames for a run of %d events", len(set.frames), total)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(set.snapshot) }))
	defer srv.Close()
	ch := make(chan []byte, 1024)
	src := binance.New(binance.Config{
		Venue: "BINANCE", Symbol: "BTCUSDT", PriceScale: wirePriceScale, SizeScale: wireSizeScale,
		Frames: ch, SnapshotURL: srv.URL, Client: srv.Client(),
	})
	defer src.Close()
	res := wireResult{due: metrics.NewHistogram(), engine: metrics.NewHistogram(), publish: metrics.NewHistogram()}
	cur := 0 // the event being stepped; the observer runs inside Step on this goroutine
	eng, err := book.New(src, 10, book.WithLatencyObserver(func(l time.Duration) {
		if cur >= d.warmup {
			res.engine.Record(l)
		}
	}))
	if err != nil {
		return res, err
	}
	hub := delivery.New(1 << 30)
	defer hub.Close()
	c := hub.Subscribe()
	go func() {
		for range c.Ready() { //nolint:revive // intentional drain
			c.Take()
		}
	}()
	ctx := context.Background()
	if err := eng.Bootstrap(ctx); err != nil {
		return res, err
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	start := d.clock.now()
	var wg sync.WaitGroup
	stop := make(chan struct{}) // closed if the engine fails, so the pacer cannot block on a full channel
	pacerErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ch)
		pr, err := d.runAt(start, func(k int, _ time.Time) {
			select {
			case ch <- set.frames[k].raw:
			case <-stop:
			}
		})
		res.senderLate = pr.senderLate
		pacerErr <- err
	}()

	seen := 0
	for k := 0; ; k++ {
		cur = k
		ok, err := eng.Step(ctx)
		if err != nil {
			close(stop)
			wg.Wait()
			return res, err
		}
		if !ok {
			break
		}
		t0 := time.Now()
		hub.Publish(eng.View())
		done := time.Now()
		seen++
		if k < d.warmup {
			continue
		}
		due := d.due(start, k)
		res.due.Record(done.Sub(due))
		res.publish.Record(done.Sub(t0))
		if done.Sub(due) > d.interval {
			res.overInterval++
		}
		depth := len(ch)
		if depth >= 2 {
			res.deepQueue++
		}
		res.queueMax = max(res.queueMax, depth)
	}
	end := time.Now()
	wg.Wait()
	if err := <-pacerErr; err != nil {
		return res, err
	}
	runtime.ReadMemStats(&after)
	res.gcCycles = after.NumGC - before.NumGC
	res.elapsed = end.Sub(d.due(start, d.warmup))
	res.resyncs, res.gaps, res.wouldCrosses = eng.Resyncs(), eng.Gaps(), eng.WouldCrosses()
	res.rejected, res.published = src.Rejected(), hub.Stats().Published

	switch {
	case seen != total:
		return res, fmt.Errorf("the engine saw %d of %d frames", seen, total)
	case res.engine.Count() != uint64(d.samples):
		return res, fmt.Errorf("the engine span has %d samples, want %d", res.engine.Count(), d.samples)
	case res.resyncs != 0 || res.gaps != 0 || res.rejected != 0 || res.wouldCrosses != 0:
		return res, fmt.Errorf("the run is not clean: resyncs %d, gaps %d, rejected %d, would-crosses %d", res.resyncs, res.gaps, res.rejected, res.wouldCrosses)
	case res.published != uint64(total):
		return res, fmt.Errorf("the hub published %d of %d views", res.published, total)
	case o.saturation > 0 && float64(res.deepQueue) > o.saturation*float64(d.samples):
		return res, fmt.Errorf("overloaded: %d of %d events found two or more frames queued (limit %.0f%%); lower the rate", res.deepQueue, d.samples, o.saturation*100)
	}
	return res, nil
}

// BenchmarkWireLatency pushes synthetic Binance depthUpdate frames through the real adapter, the engine and
// the fan-out at a fixed arrival rate and reports the engine's own span (adapter dequeue to stored view),
// the corrected due-to-done latency, and the fan-out's Publish time. See README.md.
func BenchmarkWireLatency(b *testing.B) {
	requireCPUs(b)
	requireFineClock(b, wireInterval)
	set := wireData()
	d := benchDriver(wireInterval)
	b.ResetTimer()
	res, err := runWire(wireOpts{driver: d, set: set, saturation: 0.05})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(0, "ns/op")
	reportHist(b, "due", res.due, true)
	reportHist(b, "engine", res.engine, true)
	b.ReportMetric(float64(res.engine.Sum().Nanoseconds())/float64(res.engine.Count()), "engine-mean-ns")
	reportHist(b, "publish", res.publish, false)
	b.ReportMetric(float64(res.senderLate), "sender-late")
	b.ReportMetric(float64(res.overInterval), "over-interval")
	b.ReportMetric(float64(res.queueMax), "queue-max")
	busy := res.engine.Sum() + res.publish.Sum()
	b.ReportMetric(100*float64(busy)/float64(res.elapsed), "util-pct")
	b.ReportMetric(float64(res.gcCycles), "gc-cycles")
	b.ReportMetric(float64(set.modelLevels), "model-levels")
	reportRun(b, d)
}

// ---- tests -----------------------------------------------------------------------------------------

// The generated frames decode, through the real adapter, into exactly the levels the generator says they
// carry, in contiguous sequence after the snapshot, and the model's book never crosses.
func TestWireFramesRoundTrip(t *testing.T) {
	set := buildWire(200, 3)
	ch := make(chan []byte, len(set.frames))
	for _, f := range set.frames {
		ch <- f.raw
	}
	close(ch)
	src := binance.New(binance.Config{Venue: "BINANCE", Symbol: "BTCUSDT", PriceScale: wirePriceScale, SizeScale: wireSizeScale, Frames: ch})
	for i, f := range set.frames {
		ev, ok := src.Next(context.Background())
		if !ok {
			t.Fatalf("frame %d: the adapter returned no event", i)
		}
		want := market.Sequence(set.lastUpdateID + 1 + uint64(i))
		d := ev.Delta
		if d.FirstSeq != want || d.LastSeq != want {
			t.Fatalf("frame %d: sequence %d-%d, want %d", i, d.FirstSeq, d.LastSeq, want)
		}
		if !sameLevels(d.Bids, f.bids) || !sameLevels(d.Asks, f.asks) {
			t.Fatalf("frame %d decoded to %+v / %+v, the generator says %+v / %+v", i, d.Bids, d.Asks, f.bids, f.asks)
		}
		if len(f.bids) != wireFrameLevel || len(f.asks) != wireFrameLevel {
			t.Fatalf("frame %d has %d bids and %d asks, want %d each", i, len(f.bids), len(f.asks), wireFrameLevel)
		}
		for _, l := range f.bids {
			if l.Price >= market.Price(wireMid-wireHalfSpread+1) {
				t.Fatalf("frame %d: bid %d inside the spread", i, l.Price)
			}
		}
		for _, l := range f.asks {
			if l.Price <= market.Price(wireMid+wireHalfSpread-1) {
				t.Fatalf("frame %d: ask %d inside the spread", i, l.Price)
			}
		}
	}
	if src.Rejected() != 0 {
		t.Fatalf("the adapter rejected %d generated frames", src.Rejected())
	}
	if again := buildWire(200, 3); string(again.frames[199].raw) != string(set.frames[199].raw) || string(again.snapshot) != string(set.snapshot) {
		t.Fatal("the generator is not deterministic for a seed")
	}
}

func sameLevels(a, b []market.Level) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A short run through the whole wire path completes cleanly: the engine's span has one sample for every
// recorded event, nothing resyncs, nothing is rejected. No latency value is asserted.
func TestWireSmoke(t *testing.T) {
	set := buildWire(700, 5)
	d := driver{clock: realClock, interval: 300 * time.Microsecond, warmup: 100, samples: 500, minSamples: 1}
	res, err := runWire(wireOpts{driver: d, set: set})
	if err != nil {
		t.Fatal(err)
	}
	if res.due.Count() != 500 || res.engine.Count() != 500 || res.publish.Count() != 500 {
		t.Fatalf("samples: due %d engine %d publish %d, want 500 each", res.due.Count(), res.engine.Count(), res.publish.Count())
	}
}

// sameLevels must be able to say no.
func TestSameLevelsDistinguishes(t *testing.T) {
	a := []market.Level{{Price: 1, Size: 2}}
	if !sameLevels(a, []market.Level{{Price: 1, Size: 2}}) {
		t.Error("equal levels reported different")
	}
	for _, b := range [][]market.Level{{{Price: 1, Size: 3}}, {{Price: 2, Size: 2}}, nil, {{Price: 1, Size: 2}, {Price: 1, Size: 2}}} {
		if sameLevels(a, b) {
			t.Errorf("%v reported equal to %v", a, b)
		}
	}
}

// About a fifth of the generated level changes are deletes, as the README says.
func TestWireFramesHaveAboutAFifthDeletes(t *testing.T) {
	set := buildWire(2000, 11)
	deletes, total := 0, 0
	for _, f := range set.frames {
		for _, l := range append(append([]market.Level{}, f.bids...), f.asks...) {
			total++
			if l.Size == 0 {
				deletes++
			}
		}
	}
	if share := float64(deletes) / float64(total); share < 0.15 || share > 0.25 {
		t.Fatalf("%.1f%% of %d level changes are deletes, want about 20%%", 100*share, total)
	}
}

// A run that sends frames far faster than the engine can take them is refused, not published.
func TestWireRefusesAnOverloadedRun(t *testing.T) {
	set := buildWire(3000, 2)
	d := driver{clock: realClock, interval: 2 * time.Microsecond, warmup: 50, samples: 2000, minSamples: 1}
	_, err := runWire(wireOpts{driver: d, set: set, saturation: 0.05})
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("an overloaded run was accepted: %v", err)
	}
}

func TestWireRefusesTooFewFrames(t *testing.T) {
	d := driver{clock: realClock, interval: time.Millisecond, warmup: 10, samples: 50, minSamples: 1}
	if _, err := runWire(wireOpts{driver: d, set: buildWire(20, 1)}); err == nil {
		t.Fatal("a run with fewer frames than events was accepted")
	}
}
