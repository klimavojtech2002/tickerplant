package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/klimavojtech2002/tickerplant/internal/book"
)

// telemetryEvery is how often a stream carries a telemetry event. It is not a setting: the
// dashboard is designed to treat three missed beats as "no data from the edge", so the cadence
// is part of what a client may rely on.
const telemetryEvery = time.Second

// eventJSON is one entry of the engine's fault log on the wire. Every field is always
// present, so a client can tell "no sequence" (0) and "duration unknown" (-1) from a missing
// key.
type eventJSON struct {
	ID          uint64 `json:"id"`
	Kind        string `json:"kind"`
	AtUnixMs    int64  `json:"atUnixMs"`
	ExpectedSeq uint64 `json:"expectedSeq"`
	GotSeq      uint64 `json:"gotSeq"`
	Reason      string `json:"reason"`
	DurationMs  int64  `json:"durationMs"`
}

// telemetryJSON is the payload of an `event: telemetry` frame. The counters, the hub figure,
// the view's sequence and the log are separate reads, so each field is exact but the frame is
// not one atomic snapshot; a client compares frames over time, not fields against each other.
type telemetryJSON struct {
	T                  int64       `json:"t"` // the edge clock, Unix milliseconds
	Seq                uint64      `json:"seq"`
	Gaps               int         `json:"gaps"`
	Resyncs            int         `json:"resyncs"`
	Disconnects        int         `json:"disconnects"`
	WouldCrosses       int         `json:"wouldCrosses"`
	ChecksumMismatches int         `json:"checksumMismatches"`
	SnapshotRejects    int         `json:"snapshotRejects"`
	SnapshotErrors     int         `json:"snapshotErrors"`
	Rejected           uint64      `json:"rejected"`
	Consumers          int         `json:"consumers"` // SSE consumers only: the log reader is not counted
	Events             []eventJSON `json:"events"`
}

// now returns the edge clock's time. A server built without New (some tests do) has no clock
// set and uses the wall clock.
func (s *server) now() time.Time {
	if s.cfg.Clock != nil {
		return s.cfg.Clock()
	}
	return time.Now()
}

// newTicker starts the per-connection tick source and returns it with its stop function.
func (s *server) newTicker() (<-chan time.Time, func()) {
	if s.cfg.ticker != nil {
		return s.cfg.ticker(telemetryEvery)
	}
	t := time.NewTicker(telemetryEvery)
	return t.C, t.Stop
}

// telemetryPayload builds the frame's JSON from the engine, the hub and the clock. v is the
// engine's current view, nil before the first one.
func (s *server) telemetryPayload(v *book.View) []byte {
	evs := s.cfg.Engine.Events()
	events := make([]eventJSON, len(evs)) // never nil, so an empty log is [] and not null
	for i, e := range evs {
		events[i] = eventJSON{
			ID: e.ID, Kind: string(e.Kind), AtUnixMs: e.AtUnixMs,
			ExpectedSeq: uint64(e.ExpectedSeq), GotSeq: uint64(e.GotSeq),
			Reason: e.Reason, DurationMs: e.DurationMs,
		}
	}
	var rejected uint64
	if s.cfg.Rejected != nil {
		rejected = s.cfg.Rejected()
	}
	var seq uint64
	if v != nil {
		seq = uint64(v.LastSeq)
	}
	doc := telemetryJSON{
		T: s.now().UnixMilli(), Seq: seq,
		Gaps: s.cfg.Engine.Gaps(), Resyncs: s.cfg.Engine.Resyncs(),
		Disconnects: s.cfg.Engine.Disconnects(), WouldCrosses: s.cfg.Engine.WouldCrosses(),
		ChecksumMismatches: s.cfg.Engine.ChecksumMismatches(),
		SnapshotRejects:    s.cfg.Engine.SnapshotRejects(), SnapshotErrors: s.cfg.Engine.SnapshotErrors(),
		Rejected: rejected, Consumers: s.cfg.Hub.Stats().Consumers, Events: events,
	}
	payload, _ := json.Marshal(doc) // numbers and strings cannot fail to marshal
	return payload
}

// writeTelemetry writes one telemetry frame; see writeEvent.
func (s *server) writeTelemetry(w http.ResponseWriter, v *book.View) bool {
	return s.writeEvent(w, "telemetry", s.telemetryPayload(v))
}

// writeEvent writes one named SSE event under a fresh write deadline and reports whether the
// connection is still usable. A client that stopped reading fills the kernel buffers, the
// deadline fires, and the handler exits instead of wedging (the same stance as the venue
// transport's Close-unblock). Views and telemetry share it, so both are covered.
func (s *server) writeEvent(w http.ResponseWriter, name string, payload []byte) bool {
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(s.cfg.WriteTimeout)); err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload); err != nil {
		return false
	}
	return rc.Flush() == nil
}
