// Package httpapi is the HTTP edge of the pipeline: it bridges the in-process fan-out
// (ADR-0013/0015) to Server-Sent Events and exposes the counters and latency the
// system already tracks. It adds no second backpressure mechanism — the Hub owns
// conflation and the slow-consumer policy; this layer only bounds each network write
// so a dead client cannot wedge a handler goroutine.
package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/market"
	"github.com/klimavojtech2002/tickerplant/internal/metrics"
)

// Engine is what the edge needs from the book engine: the current view (served as a
// new subscriber's first event) and the counters for /metrics. *book.Engine satisfies
// it; tests inject fixed values.
type Engine interface {
	View() *book.View
	Gaps() int
	Resyncs() int
	Disconnects() int
	WouldCrosses() int
	ChecksumMismatches() int
}

// Config wires the edge. PriceScale/SizeScale render ticks back to the venue's
// decimal strings, so no consumer ever needs float arithmetic to display them.
type Config struct {
	Hub        *delivery.Hub
	Engine     Engine
	Venue      market.Venue
	Symbol     market.Symbol
	PriceScale int
	SizeScale  int
	Latency    *metrics.Histogram // nil when the caller records no latency
	Log        *slog.Logger       // nil discards
	// WriteTimeout bounds each SSE event write, so a client that stopped reading
	// unblocks the handler once the kernel buffers fill (default 10s).
	WriteTimeout time.Duration
}

// New returns the edge handler: GET /stream (SSE, one complete top-N view per event)
// and GET /metrics (JSON snapshot of delivery stats, engine counters, and latency).
func New(cfg Config) http.Handler {
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 10 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	s := &server{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream", s.stream)
	mux.HandleFunc("GET /metrics", s.metrics)
	return mux
}

type server struct{ cfg Config }

// viewJSON is one complete top-N view on the wire. Prices and sizes are the venue's
// decimal strings — a JS client must not parse them into float64, and this shape makes
// not doing so the path of least resistance.
type viewJSON struct {
	Venue  string      `json:"venue"`
	Symbol string      `json:"symbol"`
	Seq    uint64      `json:"seq"`
	Bids   [][2]string `json:"bids"`
	Asks   [][2]string `json:"asks"`
}

func (s *server) viewPayload(v *book.View) []byte {
	out := viewJSON{
		Venue:  string(s.cfg.Venue),
		Symbol: string(s.cfg.Symbol),
		Seq:    uint64(v.LastSeq),
		Bids:   s.levels(v.Bids),
		Asks:   s.levels(v.Asks),
	}
	payload, _ := json.Marshal(out) // strings, integers, and slices of them cannot fail to marshal
	return payload
}

func (s *server) levels(ls []market.Level) [][2]string {
	out := make([][2]string, len(ls))
	for i, l := range ls {
		out[i] = [2]string{
			market.FormatScaled(int64(l.Price), s.cfg.PriceScale),
			market.FormatScaled(int64(l.Size), s.cfg.SizeScale),
		}
	}
	return out
}

// stream serves one SSE subscriber. The first event is the engine's current view, so
// a (re)connecting client renders immediately without a join protocol — every event
// is a complete view, which is the whole reconnect story under latest-wins (ADR-0015).
// lastSeq dedupes the seam between that first read and the Hub subscription. The
// doorbell closing (hub closed, or this consumer disconnected for lagging) ends the
// response; the client's EventSource reconnects and starts fresh.
func (s *server) stream(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	// The server buffers headers until the first flush; without this a client on a
	// quiet feed would hang waiting for the response to open (EventSource onopen).
	if http.NewResponseController(w).Flush() != nil {
		return
	}

	c := s.cfg.Hub.Subscribe()
	defer s.cfg.Hub.Unsubscribe(c.ID())

	var lastSeq market.Sequence
	if v := s.cfg.Engine.View(); v != nil {
		if !s.writeView(w, v) {
			return
		}
		lastSeq = v.LastSeq
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case _, ok := <-c.Ready():
			if !ok {
				return
			}
			v := c.Take()
			if v == nil || v.LastSeq <= lastSeq {
				continue // coalesced wakeup, or the view already sent as the opener
			}
			if !s.writeView(w, v) {
				return
			}
			lastSeq = v.LastSeq
		}
	}
}

// writeView writes one SSE event under the write deadline and reports whether the
// connection is still usable. A client that stopped reading fills the kernel buffers,
// the deadline fires, and the handler exits instead of wedging (the same stance as the
// venue transport's Close-unblock).
func (s *server) writeView(w http.ResponseWriter, v *book.View) bool {
	payload := s.viewPayload(v)
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(s.cfg.WriteTimeout)); err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "event: view\ndata: %s\n\n", payload); err != nil {
		return false
	}
	return rc.Flush() == nil
}

// metricsJSON is the /metrics document. Latency quantiles are duration strings
// ("1.2ms") — display-ready and float-free for any consumer.
type metricsJSON struct {
	Venue    string         `json:"venue"`
	Symbol   string         `json:"symbol"`
	Delivery delivery.Stats `json:"delivery"`
	Engine   engineJSON     `json:"engine"`
	Latency  *latencyJSON   `json:"latency,omitempty"`
}

type engineJSON struct {
	Gaps               int `json:"gaps"`
	Resyncs            int `json:"resyncs"`
	Disconnects        int `json:"disconnects"`
	WouldCrosses       int `json:"wouldCrosses"`
	ChecksumMismatches int `json:"checksumMismatches"`
}

type latencyJSON struct {
	Count uint64 `json:"count"`
	P50   string `json:"p50"`
	P99   string `json:"p99"`
	P999  string `json:"p99_9"`
	Max   string `json:"max"`
}

func (s *server) metrics(w http.ResponseWriter, _ *http.Request) {
	doc := metricsJSON{
		Venue:    string(s.cfg.Venue),
		Symbol:   string(s.cfg.Symbol),
		Delivery: s.cfg.Hub.Stats(),
		Engine: engineJSON{
			Gaps:               s.cfg.Engine.Gaps(),
			Resyncs:            s.cfg.Engine.Resyncs(),
			Disconnects:        s.cfg.Engine.Disconnects(),
			WouldCrosses:       s.cfg.Engine.WouldCrosses(),
			ChecksumMismatches: s.cfg.Engine.ChecksumMismatches(),
		},
	}
	if s.cfg.Latency != nil {
		// Each read locks separately; the block is a near-instant of a live histogram,
		// not one frozen snapshot — fine for a health scrape, each value exact.
		doc.Latency = &latencyJSON{
			Count: s.cfg.Latency.Count(),
			P50:   s.cfg.Latency.Percentile(0.50).String(),
			P99:   s.cfg.Latency.Percentile(0.99).String(),
			P999:  s.cfg.Latency.Percentile(0.999).String(),
			Max:   s.cfg.Latency.Max().String(),
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		s.cfg.Log.Error("metrics encode failed", "err", err)
	}
}
