package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// statusSchema is the version of the /api/status document. It changes only when a field is
// removed or changes meaning; adding a field does not change it.
const statusSchema = 1

// statusDelivery is the fan-out's figures: the stream consumers the hub counts (the log reader
// is not one), the views delivered into and superseded in their slots, and every Publish call.
type statusDelivery struct {
	Consumers int    `json:"consumers"`
	Delivered uint64 `json:"delivered"`
	Dropped   uint64 `json:"dropped"`
	Published uint64 `json:"published"`
}

// statusEngine is the engine's counters and the source's count of frames it could not use.
type statusEngine struct {
	Gaps               int    `json:"gaps"`
	Resyncs            int    `json:"resyncs"`
	Disconnects        int    `json:"disconnects"`
	WouldCrosses       int    `json:"wouldCrosses"`
	ChecksumMismatches int    `json:"checksumMismatches"`
	SnapshotRejects    int    `json:"snapshotRejects"`
	SnapshotErrors     int    `json:"snapshotErrors"`
	Rejected           uint64 `json:"rejected"`
}

// statusLatency is the internal latency histogram in whole nanoseconds, so a client formats
// units itself and never parses a duration string. With Count 0 there are no samples and the
// quantiles are 0.
type statusLatency struct {
	Count uint64 `json:"count"`
	P50Ns int64  `json:"p50Ns"`
	P99Ns int64  `json:"p99Ns"`
	P999N int64  `json:"p999Ns"`
	MaxNs int64  `json:"maxNs"`
}

// statusJSON is the /api/status document. Each field is exact on its own, but the document is
// built from separate reads (the counters, the hub, the engine, the histogram), so it is not one
// atomic snapshot; a client compares polls over time.
//
// LastEventAgeMs is the edge clock's time minus the engine's last processed event, in whole
// milliseconds: -1 before the engine has seen any event, never negative otherwise. The engine
// stamps its events with its own clock, which the pipeline leaves at the wall clock, so
// injecting a different clock into one of them skews the age. Latency is the one field that can
// be absent: with no histogram attached it is left out rather than zeroed.
type statusJSON struct {
	Schema          int            `json:"schema"`
	Mode            string         `json:"mode"`
	Chaos           bool           `json:"chaos"`
	Version         string         `json:"version"`
	Venue           string         `json:"venue"`
	Symbol          string         `json:"symbol"`
	StartedAtUnixMs int64          `json:"startedAtUnixMs"`
	Depth           int            `json:"depth"`
	LastEventAgeMs  int64          `json:"lastEventAgeMs"`
	Delivery        statusDelivery `json:"delivery"`
	Engine          statusEngine   `json:"engine"`
	Latency         *statusLatency `json:"latency,omitempty"`
}

// lastEventAgeMs is how long ago the engine last processed an event, in whole milliseconds
// (rounded down): -1 when it has not yet, and 0 rather than a negative number if the engine's
// time is ahead of the edge clock.
func lastEventAgeMs(now, last time.Time) int64 {
	if last.IsZero() {
		return -1
	}
	d := now.Sub(last)
	if d < 0 {
		return 0
	}
	return d.Milliseconds()
}

// modeName is how a configured mode is reported: an edge built without one says so.
func modeName(mode string) string {
	if mode == "" {
		return "unknown"
	}
	return mode
}

// startedAtMs is the start time in Unix milliseconds, 0 when the caller did not set one (the
// zero time would otherwise report a large negative number).
func startedAtMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// status answers GET /api/status. The body is marshalled first and written with its length, so
// there is no half-written document to handle.
func (s *server) status(w http.ResponseWriter, _ *http.Request) {
	st := s.cfg.Hub.Stats()
	var rejected uint64
	if s.cfg.Rejected != nil {
		rejected = s.cfg.Rejected()
	}
	doc := statusJSON{
		Schema: statusSchema, Mode: modeName(s.cfg.Mode), Chaos: s.cfg.Chaos, Version: s.cfg.Version,
		Venue: string(s.cfg.Venue), Symbol: string(s.cfg.Symbol),
		StartedAtUnixMs: startedAtMs(s.cfg.StartedAt), Depth: s.cfg.Depth,
		LastEventAgeMs: lastEventAgeMs(s.now(), s.cfg.Engine.LastEventAt()),
		Delivery:       statusDelivery{Consumers: st.Consumers, Delivered: st.Delivered, Dropped: st.Dropped, Published: st.Published},
		Engine: statusEngine{
			Gaps: s.cfg.Engine.Gaps(), Resyncs: s.cfg.Engine.Resyncs(), Disconnects: s.cfg.Engine.Disconnects(),
			WouldCrosses: s.cfg.Engine.WouldCrosses(), ChecksumMismatches: s.cfg.Engine.ChecksumMismatches(),
			SnapshotRejects: s.cfg.Engine.SnapshotRejects(), SnapshotErrors: s.cfg.Engine.SnapshotErrors(),
			Rejected: rejected,
		},
	}
	if h := s.cfg.Latency; h != nil {
		doc.Latency = &statusLatency{
			Count: h.Count(), P50Ns: int64(h.Percentile(0.50)), P99Ns: int64(h.Percentile(0.99)),
			P999N: int64(h.Percentile(0.999)), MaxNs: int64(h.Max()),
		}
	}
	body, _ := json.Marshal(doc) // numbers and strings cannot fail to marshal
	body = append(body, '\n')
	hd := w.Header()
	hd.Set("Content-Type", "application/json")
	hd.Set("Cache-Control", "no-store")
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}
