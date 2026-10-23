package httpapi

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// metricsContentType is the Prometheus text exposition format, version 0.0.4.
const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// label is one name and value of a sample; a caller lists them in alphabetical order.
type label struct{ name, value string }

// exposition builds a Prometheus text body. Every family gets a HELP line and then a TYPE
// line before its first sample, and the body ends in one newline, so it is complete the moment
// it is written.
type exposition struct{ buf bytes.Buffer }

func (e *exposition) family(name, typ, help string) {
	e.buf.WriteString("# HELP " + name + " " + escapeHelp(help) + "\n")
	e.buf.WriteString("# TYPE " + name + " " + typ + "\n")
}

func (e *exposition) sample(name string, labels []label, value string) {
	e.buf.WriteString(name)
	if len(labels) > 0 {
		e.buf.WriteByte('{')
		for i, l := range labels {
			if i > 0 {
				e.buf.WriteByte(',')
			}
			e.buf.WriteString(l.name + `="` + escapeLabel(l.value) + `"`)
		}
		e.buf.WriteByte('}')
	}
	e.buf.WriteString(" " + value + "\n")
}

// help text escapes a backslash and a newline; label values also escape the double quote.
var (
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	labelEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
)

func escapeHelp(s string) string { return helpEscaper.Replace(s) }

// escapeLabel also repairs invalid UTF-8: a value that comes from a flag or a linker
// setting with a stray byte must not make the scraper reject the whole scrape.
func escapeLabel(s string) string {
	return labelEscaper.Replace(strings.ToValidUTF8(s, "�"))
}

// fixed formats n / scale as a decimal using integers only: the whole part, then the
// fraction padded to digits places with trailing zeros removed. A float would be exact here
// for small values only; this is exact for every uint64.
func fixed(n, scale uint64, digits int) string {
	whole := strconv.FormatUint(n/scale, 10)
	frac := n % scale
	if frac == 0 {
		return whole
	}
	f := strconv.FormatUint(frac, 10)
	f = strings.Repeat("0", digits-len(f)) + f
	return whole + "." + strings.TrimRight(f, "0")
}

// secondsNs formats nanoseconds as seconds; secondsMs formats milliseconds as seconds.
func secondsNs(ns uint64) string { return fixed(ns, 1_000_000_000, 9) }
func secondsMs(ms uint64) string { return fixed(ms, 1000, 3) }

// epochMs is a time as milliseconds since the Unix epoch, or false for the zero time and for
// anything at or before the epoch (a clock that was never set, not a real moment).
func epochMs(t time.Time) (uint64, bool) {
	if t.IsZero() || t.UnixMilli() <= 0 {
		return 0, false
	}
	return uint64(t.UnixMilli()), true
}

// count formats a counter that is never negative.
func count(n int) string { return strconv.FormatUint(uint64(max(n, 0)), 10) }

func u64(n uint64) string { return strconv.FormatUint(n, 10) }

// latencyQuantiles are the quantiles of the latency summary and their label values.
var latencyQuantiles = []struct {
	label string
	p     float64
}{{"0.5", 0.50}, {"0.99", 0.99}, {"0.999", 0.999}}

// metrics answers GET /metrics in the Prometheus text format. A value the process does not
// have is left out, never reported as zero. Each value is read on its own, so each is exact but
// the scrape is not one atomic snapshot.
func (s *server) metrics(w http.ResponseWriter, _ *http.Request) {
	var e exposition
	mode := s.cfg.Mode
	if mode == "" {
		mode = "unknown"
	}
	e.family("tickerplant_build_info", "gauge", "Build information; the value is always 1.")
	e.sample("tickerplant_build_info", []label{{"version", s.cfg.Version}}, "1")
	e.family("tickerplant_feed_info", "gauge", "The feed this process serves; the value is always 1.")
	e.sample("tickerplant_feed_info", []label{{"mode", mode}, {"symbol", string(s.cfg.Symbol)}, {"venue", string(s.cfg.Venue)}}, "1")

	if ms, ok := epochMs(s.cfg.StartedAt); ok {
		e.family("tickerplant_start_time_seconds", "gauge", "Start time of the pipeline, in seconds since the Unix epoch.")
		e.sample("tickerplant_start_time_seconds", nil, secondsMs(ms))
	}
	if ms, ok := epochMs(s.cfg.Engine.LastEventAt()); ok {
		e.family("tickerplant_engine_last_event_timestamp_seconds", "gauge",
			"Wall-clock time of the last event the engine processed, in seconds since the Unix epoch.")
		e.sample("tickerplant_engine_last_event_timestamp_seconds", nil, secondsMs(ms))
	}

	st := s.cfg.Hub.Stats()
	e.family("tickerplant_hub_consumers", "gauge", "Stream consumers currently connected (the in-process log reader is not counted).")
	e.sample("tickerplant_hub_consumers", nil, count(st.Consumers))
	e.counter("tickerplant_hub_delivered_total", "Views placed into stream consumers' slots.", u64(st.Delivered))
	e.counter("tickerplant_hub_dropped_total", "Views replaced in a slot before the consumer took them.", u64(st.Dropped))
	e.counter("tickerplant_hub_published_total", "Views published to the hub, whether or not anyone was connected.", u64(st.Published))

	eng := s.cfg.Engine
	e.counter("tickerplant_engine_gaps_total", "Sequence gaps detected.", count(eng.Gaps()))
	e.counter("tickerplant_engine_resyncs_total", "Resyncs started after the book was bound.", count(eng.Resyncs()))
	e.counter("tickerplant_engine_disconnects_total", "Source disconnects handled.", count(eng.Disconnects()))
	e.counter("tickerplant_engine_would_crosses_total", "Deltas or snapshots that would have crossed the book and were refused.", count(eng.WouldCrosses()))
	e.counter("tickerplant_engine_checksum_mismatches_total", "Book checksums that did not match the venue's.", count(eng.ChecksumMismatches()))
	e.counter("tickerplant_engine_snapshot_rejects_total", "Snapshots refused while binding or resyncing.", count(eng.SnapshotRejects()))
	e.counter("tickerplant_engine_snapshot_errors_total", "Snapshot requests that failed.", count(eng.SnapshotErrors()))
	if s.cfg.Rejected != nil {
		e.counter("tickerplant_source_rejected_frames_total", "Frames the source could not use.", u64(s.cfg.Rejected()))
	}

	e.family("tickerplant_http_stream_requests_total", "counter",
		"Stream requests by outcome: accepted, refused because the hub is closing, refused because the stream cap is reached.")
	e.sample("tickerplant_http_stream_requests_total", []label{{"outcome", "accepted"}}, u64(s.streams.accepted.Load()))
	e.sample("tickerplant_http_stream_requests_total", []label{{"outcome", "closed"}}, u64(s.streams.closed.Load()))
	e.sample("tickerplant_http_stream_requests_total", []label{{"outcome", "full"}}, u64(s.streams.full.Load()))

	if h := s.cfg.Latency; h != nil {
		n := h.Count()
		const name = "tickerplant_latency_seconds"
		e.family(name, "summary", "Internal processing latency per applied delta, from the adapter taking the raw message off its channel "+
			"to the applied view being stored. Quantiles are estimates within 2 percent (bucket midpoints) over the whole run.")
		if n > 0 {
			for _, q := range latencyQuantiles {
				e.sample(name, []label{{"quantile", q.label}}, secondsNs(uint64(h.Percentile(q.p))))
			}
		}
		e.sample(name+"_sum", nil, secondsNs(uint64(h.Sum())))
		e.sample(name+"_count", nil, u64(n))
		if n > 0 {
			e.family("tickerplant_latency_max_seconds", "gauge", "Largest processing latency seen, exact.")
			e.sample("tickerplant_latency_max_seconds", nil, secondsNs(uint64(h.Max())))
		}
	}

	hd := w.Header()
	hd.Set("Content-Type", metricsContentType)
	hd.Set("Cache-Control", "no-store")
	hd.Set("Content-Length", strconv.Itoa(e.buf.Len()))
	_, _ = w.Write(e.buf.Bytes())
}

// counter writes a one-sample counter family.
func (e *exposition) counter(name, help, value string) {
	e.family(name, "counter", help)
	e.sample(name, nil, value)
}
