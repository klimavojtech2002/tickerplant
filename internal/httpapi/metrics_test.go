package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/metrics"
)

// ---- the format validator ------------------------------------------------------------

var (
	nameRe    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	valueRe   = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	quantileR = regexp.MustCompile(`^(0(\.[0-9]+)?|1(\.0+)?)$`)
)

// problems lists every way body departs from the Prometheus text format 0.0.4 as this project
// writes it. HELP before TYPE, one family at a time, and a sample for every family are house
// rules; the rest is the format. An empty result means the body is valid.
func problems(body string) []string {
	var out []string
	bad := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	if !strings.HasSuffix(body, "\n") {
		bad("no trailing newline")
	}
	type fam struct {
		typ     string
		samples int
	}
	families := map[string]*fam{}
	var order []string
	current, helped := "", ""
	series := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		switch {
		case line == "":
			bad("blank line")
		case strings.HasPrefix(line, "# HELP "):
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "# HELP "), " ")
			if !nameRe.MatchString(name) {
				bad("bad family name %q", name)
			}
			if families[name] != nil {
				bad("family %s declared twice", name)
			}
			helped = name
		case strings.HasPrefix(line, "# TYPE "):
			name, typ, _ := strings.Cut(strings.TrimPrefix(line, "# TYPE "), " ")
			if helped != name {
				bad("TYPE %s without HELP directly before it", name)
			}
			helped = ""
			switch typ {
			case "counter":
				if !strings.HasSuffix(name, "_total") {
					bad("counter %s does not end in _total", name)
				}
			case "gauge", "summary":
				if strings.HasSuffix(name, "_total") {
					bad("%s %s ends in _total", typ, name)
				}
			default:
				bad("unknown type %q", typ)
			}
			families[name] = &fam{typ: typ}
			order = append(order, name)
			current = name
		case strings.HasPrefix(line, "#"):
			bad("unknown comment %q", line)
		default:
			name, labels, value, ok := parseSample(line)
			if !ok {
				bad("unparseable sample %q", line)
				continue
			}
			f := name
			if families[f] == nil {
				for _, suffix := range []string{"_sum", "_count"} {
					base := strings.TrimSuffix(name, suffix)
					if base != name && families[base] != nil && families[base].typ == "summary" {
						f = base
					}
				}
			}
			if families[f] == nil {
				bad("sample %s has no TYPE", name)
				continue
			}
			if f != current {
				bad("family %s split: sample %s after %s", f, name, current)
			}
			families[f].samples++
			if !valueRe.MatchString(value) {
				bad("value %q of %s is not a plain decimal", value, name)
			}
			keys := make([]string, 0, len(labels))
			for _, l := range labels {
				if !nameRe.MatchString(l[0]) || strings.HasPrefix(l[0], "__") {
					bad("bad label name %q", l[0])
				}
				if !utf8.ValidString(l[1]) {
					bad("label value %q is not UTF-8", l[1])
				}
				if l[0] == "quantile" {
					if families[f].typ != "summary" || f != name {
						bad("quantile label on %s", name)
					} else if !quantileR.MatchString(l[1]) {
						bad("quantile %q outside [0,1]", l[1])
					}
				}
				keys = append(keys, l[0]+"="+l[1])
			}
			if families[f].typ == "summary" && f == name && !hasLabel(labels, "quantile") {
				bad("summary sample %s without a quantile", name)
			}
			if !sort.SliceIsSorted(labels, func(i, j int) bool { return labels[i][0] < labels[j][0] }) {
				bad("labels of %s are not in alphabetical order", name)
			}
			seen := map[string]bool{}
			for _, l := range labels {
				if seen[l[0]] {
					bad("label %s repeated in %s", l[0], name)
				}
				seen[l[0]] = true
			}
			sort.Strings(keys)
			id := name + "{" + strings.Join(keys, ",") + "}"
			if series[id] {
				bad("duplicate series %s", id)
			}
			series[id] = true
		}
	}
	for _, name := range order {
		if families[name].samples == 0 {
			bad("family %s has no samples", name)
		}
	}
	return out
}

func hasLabel(labels [][2]string, name string) bool {
	for _, l := range labels {
		if l[0] == name {
			return true
		}
	}
	return false
}

// parseSample splits `name{a="b",c="d"} value` and unescapes the label values; ok is false for
// anything malformed, including an escape other than \\, \" and \n.
func parseSample(line string) (name string, labels [][2]string, value string, ok bool) {
	i := strings.IndexAny(line, "{ ")
	if i < 0 {
		return "", nil, "", false
	}
	name, rest := line[:i], line[i:]
	if rest[0] == '{' {
		rest = rest[1:]
		for !strings.HasPrefix(rest, "}") {
			eq := strings.Index(rest, `="`)
			if eq < 0 {
				return "", nil, "", false
			}
			lname := rest[:eq]
			rest = rest[eq+2:]
			var val strings.Builder
			closed := false
			for j := 0; j < len(rest); j++ {
				c := rest[j]
				if c == '\\' {
					if j+1 >= len(rest) {
						return "", nil, "", false
					}
					switch rest[j+1] {
					case '\\':
						val.WriteByte('\\')
					case '"':
						val.WriteByte('"')
					case 'n':
						val.WriteByte('\n')
					default:
						return "", nil, "", false
					}
					j++
					continue
				}
				if c == '"' {
					rest = rest[j+1:]
					closed = true
					break
				}
				val.WriteByte(c)
			}
			if !closed {
				return "", nil, "", false
			}
			labels = append(labels, [2]string{lname, val.String()})
			if !strings.HasPrefix(rest, ",") && !strings.HasPrefix(rest, "}") {
				return "", nil, "", false
			}
			rest = strings.TrimPrefix(rest, ",")
		}
		rest = rest[1:]
	}
	if !strings.HasPrefix(rest, " ") {
		return "", nil, "", false
	}
	return name, labels, rest[1:], true
}

// The validator must not be vacuous: each of these bodies is wrong in the way its name says.
func TestValidatorRejectsBadBodies(t *testing.T) {
	good := "# HELP a_total Things.\n# TYPE a_total counter\na_total 1\n"
	if p := problems(good); len(p) != 0 {
		t.Fatalf("the baseline body has problems: %v", p)
	}
	cases := []struct{ name, body, want string }{
		{"no trailing newline", strings.TrimSuffix(good, "\n"), "no trailing newline"},
		{"blank line", good + "\n", "blank line"},
		{"sample before any TYPE", "a_total 1\n", "has no TYPE"},
		{"TYPE without HELP", "# TYPE a_total counter\na_total 1\n", "without HELP"},
		{"counter without _total", "# HELP a Things.\n# TYPE a counter\na 1\n", "does not end in _total"},
		{"gauge named _total", "# HELP a_total Things.\n# TYPE a_total gauge\na_total 1\n", "ends in _total"},
		{"unknown type", "# HELP a Things.\n# TYPE a histogram\na 1\n", "unknown type"},
		{"bad family name", "# HELP a:b Things.\n# TYPE a:b gauge\na:b 1\n", "bad family name"},
		{"family declared twice", good + "# HELP a_total Again.\n# TYPE a_total counter\na_total 2\n", "declared twice"},
		{"split family", good + "# HELP b Other.\n# TYPE b gauge\nb 1\na_total 2\n", "split"},
		{"duplicate series", good + "a_total 2\n", "duplicate series"},
		{"family without samples", good + "# HELP b Other.\n# TYPE b gauge\n", "no samples"},
		{"bad label name", "# HELP a Things.\n# TYPE a gauge\na{1x=\"y\"} 1\n", "bad label name"},
		{"reserved label name", "# HELP a Things.\n# TYPE a gauge\na{__x=\"y\"} 1\n", "bad label name"},
		{"bad escape", "# HELP a Things.\n# TYPE a gauge\na{x=\"\\t\"} 1\n", "unparseable"},
		{"unclosed label value", "# HELP a Things.\n# TYPE a gauge\na{x=\"y} 1\n", "unparseable"},
		{"invalid UTF-8 label value", "# HELP a Things.\n# TYPE a gauge\na{x=\"\xff\"} 1\n", "not UTF-8"},
		{"NaN value", "# HELP a Things.\n# TYPE a gauge\na NaN\n", "plain decimal"},
		{"exponent value", "# HELP a Things.\n# TYPE a gauge\na 1e3\n", "plain decimal"},
		{"quantile on a gauge", "# HELP a Things.\n# TYPE a gauge\na{quantile=\"0.5\"} 1\n", "quantile label"},
		{"quantile out of range", "# HELP s Latency.\n# TYPE s summary\ns{quantile=\"1.5\"} 1\ns_sum 1\ns_count 1\n", "outside [0,1]"},
		{"summary sample without a quantile", "# HELP s Latency.\n# TYPE s summary\ns 1\ns_sum 1\ns_count 1\n", "without a quantile"},
		{"gauge named like a summary's sum", "# HELP s Latency.\n# TYPE s summary\ns_sum 1\n# HELP s_sum Clash.\n# TYPE s_sum gauge\ns_sum 2\n", "duplicate series"},
		{"unknown comment", good + "# NOTE hi\n", "unknown comment"},
		{"negative value", "# HELP a Things.\n# TYPE a gauge\na -1\n", "plain decimal"},
		{"quantile on a summary's sum", "# HELP s Latency.\n# TYPE s summary\ns{quantile=\"0.5\"} 1\ns_sum{quantile=\"0.5\"} 1\ns_count 1\n", "quantile label"},
		{"labels out of alphabetical order", "# HELP a Things.\n# TYPE a gauge\na{y=\"1\",x=\"2\"} 1\n", "alphabetical"},
		{"a label name repeated", "# HELP a Things.\n# TYPE a gauge\na{x=\"1\",x=\"2\"} 1\n", "repeated"},
		{"no comma between labels", "# HELP a Things.\n# TYPE a gauge\na{x=\"1\"y=\"2\"} 1\n", "unparseable"},
	}
	for _, tc := range cases {
		p := problems(tc.body)
		found := false
		for _, line := range p {
			if strings.Contains(line, tc.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: problems = %v, want one containing %q", tc.name, p, tc.want)
		}
	}
}

// ---- the scrape ---------------------------------------------------------------------------

// fixedEdge is a server built directly, so the stream counters can be set to chosen values.
type fixedEdge struct {
	s   *server
	hub *delivery.Hub
}

func newFixedEdge(t *testing.T, mutate func(*Config)) fixedEdge {
	t.Helper()
	hub := delivery.New(1 << 20)
	t.Cleanup(hub.Close)
	c := hub.Subscribe() // one counted consumer
	hub.Publish(view(1))
	c.Take()
	hub.Publish(view(2)) // delivered 2
	hub.Publish(view(3))
	hub.Publish(view(4))
	hub.Publish(view(5)) // three superseded: dropped 3, published 5
	lat := metrics.NewHistogram()
	for _, ns := range []time.Duration{1000, 2000, 50_000} { // well inside their buckets
		lat.Record(ns)
	}
	cfg := Config{
		Hub: hub, Venue: "kraken", Symbol: "BTC/USD", Latency: lat, Mode: "live", Version: "v1.2.3",
		StartedAt: time.UnixMilli(1_699_999_000_123), Rejected: func() uint64 { return 8 },
		Engine: &stubEngine{
			gaps: 1, resyncs: 2, disconnects: 3, crosses: 4, mismatches: 5, rejects: 6, snapErrors: 7,
			lastEventAt: time.UnixMilli(1_699_999_998_500),
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	s := &server{cfg: cfg}
	s.streams.accepted.Store(3)
	s.streams.closed.Store(1)
	s.streams.full.Store(2)
	return fixedEdge{s: s, hub: hub}
}

func (f fixedEdge) scrape() (*httptest.ResponseRecorder, string) {
	rec := httptest.NewRecorder()
	f.s.metrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec, rec.Body.String()
}

// The contract file: the exact body of one deterministic scrape, every family present. The
// latency quantiles come from floating-point bucket arithmetic, so the file is guaranteed on
// the CI architecture, like the status golden; the summary test checks them against the exact
// samples as well.
func TestMetricsGolden(t *testing.T) {
	_, body := newFixedEdge(t, nil).scrape()
	const path = "testdata/metrics.prom"
	if *update {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	if body != string(want) {
		t.Fatalf("metrics body differs from %s (run with -update to rewrite it if the change is intended):\n got:\n%s\nwant:\n%s", path, body, want)
	}
	if p := problems(body); len(p) != 0 {
		t.Fatalf("the golden is not valid exposition text: %v", p)
	}
}

func TestMetricsVariantsAreValidAndOmitWhatIsUnknown(t *testing.T) {
	has := func(body, family string) bool { return strings.Contains(body, "# TYPE "+family+" ") }
	latencyFamilies := []string{
		"tickerplant_latency_seconds", "tickerplant_latency_max_seconds",
	}
	cases := []struct {
		name    string
		mutate  func(*Config)
		present []string
		absent  []string
	}{
		{"no histogram", func(c *Config) { c.Latency = nil }, nil, latencyFamilies},
		{"histogram without samples", func(c *Config) { c.Latency = metrics.NewHistogram() },
			[]string{"tickerplant_latency_seconds"}, []string{"tickerplant_latency_max_seconds"}},
		{"source cannot count rejected frames", func(c *Config) { c.Rejected = nil }, nil, []string{"tickerplant_source_rejected_frames_total"}},
		{"start time unset", func(c *Config) { c.StartedAt = time.Time{} }, nil, []string{"tickerplant_start_time_seconds"}},
		{"start time before the epoch", func(c *Config) { c.StartedAt = time.UnixMilli(-5) }, nil, []string{"tickerplant_start_time_seconds"}},
		{"no event yet", func(c *Config) { c.Engine = &stubEngine{} }, nil, []string{"tickerplant_engine_last_event_timestamp_seconds"}},
		{"last event before the epoch", func(c *Config) { c.Engine = &stubEngine{lastEventAt: time.UnixMilli(-1)} }, nil,
			[]string{"tickerplant_engine_last_event_timestamp_seconds"}},
	}
	for _, tc := range cases {
		_, body := newFixedEdge(t, tc.mutate).scrape()
		if p := problems(body); len(p) != 0 {
			t.Errorf("%s: invalid body: %v\n%s", tc.name, p, body)
		}
		for _, f := range tc.present {
			if !has(body, f) {
				t.Errorf("%s: family %s is missing", tc.name, f)
			}
		}
		for _, f := range tc.absent {
			if has(body, f) {
				t.Errorf("%s: family %s must be left out, not zeroed", tc.name, f)
			}
		}
	}
	// An empty histogram still reports its sum and count, which are true, and no quantiles.
	_, body := newFixedEdge(t, func(c *Config) { c.Latency = metrics.NewHistogram() }).scrape()
	for _, want := range []string{"tickerplant_latency_seconds_sum 0\n", "tickerplant_latency_seconds_count 0\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty histogram: missing %q", want)
		}
	}
	if strings.Contains(body, "quantile=") {
		t.Error("empty histogram: quantile samples must be left out")
	}
}

func TestMetricsFeedLabels(t *testing.T) {
	_, body := newFixedEdge(t, func(c *Config) { c.Mode = "" }).scrape()
	if !strings.Contains(body, `tickerplant_feed_info{mode="unknown",symbol="BTC/USD",venue="kraken"} 1`+"\n") {
		t.Fatalf("an empty mode must read unknown:\n%s", body)
	}
	_, body = newFixedEdge(t, func(c *Config) { c.Version = "" }).scrape()
	if !strings.Contains(body, `tickerplant_build_info{version=""} 1`+"\n") {
		t.Fatalf("an empty version is an empty label value:\n%s", body)
	}
}

func TestMetricsEscapesHostileLabelValues(t *testing.T) {
	_, body := newFixedEdge(t, func(c *Config) {
		c.Venue = "ve\"nue\\x\ny\xff"
		c.Symbol = "sym\nbol"
		c.Version = `v"1\`
	}).scrape()
	if p := problems(body); len(p) != 0 {
		t.Fatalf("hostile values broke the format: %v\n%s", p, body)
	}
	want := `tickerplant_feed_info{mode="live",symbol="sym\nbol",venue="ve\"nue\\x\ny` + "\uFFFD" + `"} 1`
	if !strings.Contains(body, want) {
		t.Fatalf("labels not escaped as expected, want %q in:\n%s", want, body)
	}
	if !strings.Contains(body, `tickerplant_build_info{version="v\"1\\"} 1`) {
		t.Fatalf("version not escaped:\n%s", body)
	}
}

func TestEscapers(t *testing.T) {
	help := map[string]string{"plain": "plain", `a\b`: `a\\b`, "a\nb": `a\nb`, `say "x"`: `say "x"`}
	for in, want := range help {
		if got := escapeHelp(in); got != want {
			t.Errorf("escapeHelp(%q) = %q, want %q", in, got, want)
		}
	}
	label := map[string]string{"plain": "plain", `a\b`: `a\\b`, "a\nb": `a\nb`, `say "x"`: `say \"x\"`, "": "", "a\xffb": "a\uFFFDb"}
	for in, want := range label {
		if got := escapeLabel(in); got != want {
			t.Errorf("escapeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFixed(t *testing.T) {
	ns := []struct {
		in   uint64
		want string
	}{
		{0, "0"}, {1, "0.000000001"}, {999, "0.000000999"}, {1986, "0.000001986"}, {1_000_000, "0.001"},
		{1_500_000_000, "1.5"}, {2_000_000_000, "2"}, {3_600_000_000_000, "3600"}, {53_000, "0.000053"},
		{18446744073709551615, "18446744073.709551615"},
	}
	for _, tc := range ns {
		if got := secondsNs(tc.in); got != tc.want {
			t.Errorf("secondsNs(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
	ms := []struct {
		in   uint64
		want string
	}{{0, "0"}, {1, "0.001"}, {1500, "1.5"}, {1000, "1"}, {1_700_000_000_123, "1700000000.123"}}
	for _, tc := range ms {
		if got := secondsMs(tc.in); got != tc.want {
			t.Errorf("secondsMs(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEpochMs(t *testing.T) {
	for _, tc := range []struct {
		t    time.Time
		ms   uint64
		want bool
	}{
		{time.Time{}, 0, false},
		{time.UnixMilli(0), 0, false},
		{time.UnixMilli(-1), 0, false},
		{time.UnixMilli(1), 1, true},
		{time.UnixMilli(1_700_000_000_123), 1_700_000_000_123, true},
	} {
		if ms, ok := epochMs(tc.t); ms != tc.ms || ok != tc.want {
			t.Errorf("epochMs(%v) = %d, %v; want %d, %v", tc.t, ms, ok, tc.ms, tc.want)
		}
	}
}

func TestCount(t *testing.T) {
	if got := count(7); got != "7" {
		t.Errorf("count(7) = %q", got)
	}
	if got := count(-3); got != "0" {
		t.Errorf("count(-3) = %q, want 0: a counter is never negative", got)
	}
}

// The summary's numbers against the exact samples: sum and count are exact, the quantiles are
// within the histogram's 2 percent band, and the max is exact. The estimated quantile may sit
// above the exact max (a bucket midpoint), which is why the max is its own family.
func TestMetricsSummarySemantics(t *testing.T) {
	_, body := newFixedEdge(t, func(c *Config) {
		h := metrics.NewHistogram()
		for range 100 {
			h.Record(100 * time.Microsecond)
		}
		c.Latency = h
	}).scrape()
	got := samples(body)
	if got["tickerplant_latency_seconds_count"] != "100" || got["tickerplant_latency_seconds_sum"] != "0.01" {
		t.Fatalf("count/sum = %s/%s, want 100/0.01", got["tickerplant_latency_seconds_count"], got["tickerplant_latency_seconds_sum"])
	}
	if got["tickerplant_latency_max_seconds"] != "0.0001" {
		t.Fatalf("max = %s, want the exact 0.0001", got["tickerplant_latency_max_seconds"])
	}
	for _, q := range []string{"0.5", "0.99", "0.999"} {
		v, err := strconv.ParseFloat(got[`tickerplant_latency_seconds{quantile="`+q+`"}`], 64)
		if err != nil {
			t.Fatalf("quantile %s: %v", q, err)
		}
		if v < 0.000098 || v > 0.000102 {
			t.Fatalf("quantile %s = %v, outside the 2%% band around 100µs", q, v)
		}
	}
}

// Each quantile label must be the fraction its value is computed from: a wrong fraction would put
// a mislabelled quantile on the wire. The spread below puts the three quantiles in three different
// buckets (100 µs, 1 ms, 10 ms), so a swapped or shifted fraction shows up in the numbers too.
func TestMetricsQuantilesUseTheirOwnFractions(t *testing.T) {
	for _, q := range latencyQuantiles {
		if got, err := strconv.ParseFloat(q.label, 64); err != nil || got != q.p {
			t.Errorf("quantile label %q is computed from %v", q.label, q.p)
		}
	}
	h := metrics.NewHistogram()
	for range 9000 {
		h.Record(100 * time.Microsecond)
	}
	for range 989 {
		h.Record(time.Millisecond)
	}
	for range 11 {
		h.Record(10 * time.Millisecond)
	}
	_, body := newFixedEdge(t, func(c *Config) { c.Latency = h }).scrape()
	got := samples(body)
	seen := map[string]bool{}
	for _, q := range []struct {
		label string
		p     float64
	}{{"0.5", 0.5}, {"0.99", 0.99}, {"0.999", 0.999}} {
		want := secondsNs(uint64(h.Percentile(q.p)))
		v := got[`tickerplant_latency_seconds{quantile="`+q.label+`"}`]
		if v != want {
			t.Errorf("quantile %s = %s, want %s", q.label, v, want)
		}
		seen[v] = true
	}
	if len(seen) != 3 {
		t.Errorf("the three quantiles should differ for this spread, got %v", got)
	}
}

// A HELP text with a backslash or a newline is escaped by family itself, not only by the helper.
func TestFamilyEscapesItsHelp(t *testing.T) {
	var e exposition
	e.family("x", "gauge", "a\\b\nc")
	e.sample("x", nil, "1")
	want := "# HELP x a\\\\b\\nc\n# TYPE x gauge\nx 1\n"
	if got := e.buf.String(); got != want {
		t.Fatalf("family wrote %q, want %q", got, want)
	}
}

// samples maps each sample line to its value, keyed by name and labels.
func samples(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndex(line, " ")
		out[line[:i]] = line[i+1:]
	}
	return out
}

// ---- the route -----------------------------------------------------------------------------

func TestMetricsRouteHeadersAndMethods(t *testing.T) {
	var sink logSink
	srv := statusEdge(t, Config{Log: sink.logger(-4), Latency: metrics.NewHistogram()})
	resp, body, err := get(t, http.MethodGet, srv.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("status/content type = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v", resp.Header)
	}
	if n, _ := strconv.Atoi(resp.Header.Get("Content-Length")); n != len(body) {
		t.Fatalf("Content-Length %q does not match the %d-byte body", resp.Header.Get("Content-Length"), len(body))
	}
	if p := problems(body); len(p) != 0 {
		t.Fatalf("served body is invalid: %v", p)
	}
	resp, _, err = get(t, http.MethodPost, srv.URL+"/metrics", nil)
	if err != nil || resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") == "" {
		t.Fatalf("POST = %v, %v; want 405 with Allow", resp, err)
	}
	resp, body, err = get(t, http.MethodHead, srv.URL+"/metrics", nil)
	if err != nil || resp.StatusCode != http.StatusOK || body != "" {
		t.Fatalf("HEAD = %v, %q, %v; want 200 with no body", resp, body, err)
	}
	sink.waitForLine(t, "route=\"GET /metrics\"", "level=DEBUG")
}

// ---- the stream outcome counters --------------------------------------------------------------

func TestStreamOutcomeCounters(t *testing.T) {
	hub := newHub(t)
	srv := capEdge(t, hub, 1, nil, nil)
	read := func() map[string]string {
		_, body, err := get(t, http.MethodGet, srv.URL+"/metrics", nil)
		if err != nil {
			t.Fatal(err)
		}
		return samples(body)
	}
	outcome := func(m map[string]string, o string) string {
		return m[`tickerplant_http_stream_requests_total{outcome="`+o+`"}`]
	}
	fresh := read()
	for _, o := range []string{"accepted", "closed", "full"} {
		if outcome(fresh, o) != "0" {
			t.Fatalf("a fresh server must report outcome %s as 0, got %q", o, outcome(fresh, o))
		}
	}

	first := open(t, srv.URL) // accepted
	t.Cleanup(func() { first.Body.Close() })
	waitConsumers(t, hub, 1)
	second := open(t, srv.URL) // over the cap: full
	second.Body.Close()
	hub.Close()
	third := open(t, srv.URL) // hub closing: closed
	third.Body.Close()

	m := read()
	if outcome(m, "accepted") != "1" || outcome(m, "full") != "1" || outcome(m, "closed") != "1" {
		t.Fatalf("outcomes accepted/full/closed = %s/%s/%s, want 1/1/1",
			outcome(m, "accepted"), outcome(m, "full"), outcome(m, "closed"))
	}
}

// A HEAD request to /stream runs the same handler and subscribes, so it counts as accepted.
func TestHeadStreamCountsAsAccepted(t *testing.T) {
	hub := newHub(t)
	srv := capEdge(t, hub, 0, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodHead, srv.URL+"/stream", nil)
	// The handler keeps running after the headers, so the connection must not go back to a pool
	// where the next request would wait behind it: a client without keep-alive closes it, which
	// also ends the handler.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	_, body, err := get(t, http.MethodGet, srv.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := samples(body)[`tickerplant_http_stream_requests_total{outcome="accepted"}`]; got != "1" {
		t.Fatalf("accepted = %q after a HEAD /stream, want 1", got)
	}
	cancel()
	hub.Close() // ends the handler; the leak gate then finds no goroutine or consumer left
	waitConsumers(t, hub, 0)
}

// ---- /metrics and /api/status read the same things ---------------------------------------------

func TestMetricsAgreeWithStatus(t *testing.T) {
	hub := newHub(t)
	c := hub.Subscribe()
	_ = c
	hub.Publish(view(1))
	hub.Publish(view(2))
	lat := metrics.NewHistogram()
	for _, ns := range []time.Duration{1000, 2000, 50_000} {
		lat.Record(ns)
	}
	srv := statusEdge(t, Config{
		Hub: hub, Latency: lat, Mode: "live", Version: "v9", Venue: "kraken", Symbol: "BTC/USD",
		Rejected: func() uint64 { return 8 },
		Engine:   &stubEngine{gaps: 1, resyncs: 2, disconnects: 3, crosses: 4, mismatches: 5, rejects: 6, snapErrors: 7},
	})
	_, mbody, err := get(t, http.MethodGet, srv.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, sbody := statusBody(t, srv)
	var st statusJSON
	if err := json.Unmarshal([]byte(sbody), &st); err != nil {
		t.Fatal(err)
	}
	m := samples(mbody)
	pairs := []struct {
		metric string
		status uint64
	}{
		{"tickerplant_engine_gaps_total", uint64(st.Engine.Gaps)},
		{"tickerplant_engine_resyncs_total", uint64(st.Engine.Resyncs)},
		{"tickerplant_engine_disconnects_total", uint64(st.Engine.Disconnects)},
		{"tickerplant_engine_would_crosses_total", uint64(st.Engine.WouldCrosses)},
		{"tickerplant_engine_checksum_mismatches_total", uint64(st.Engine.ChecksumMismatches)},
		{"tickerplant_engine_snapshot_rejects_total", uint64(st.Engine.SnapshotRejects)},
		{"tickerplant_engine_snapshot_errors_total", uint64(st.Engine.SnapshotErrors)},
		{"tickerplant_source_rejected_frames_total", st.Engine.Rejected},
		{"tickerplant_hub_delivered_total", st.Delivery.Delivered},
		{"tickerplant_hub_dropped_total", st.Delivery.Dropped},
		{"tickerplant_hub_published_total", st.Delivery.Published},
		{"tickerplant_hub_consumers", uint64(st.Delivery.Consumers)},
		{"tickerplant_latency_seconds_count", st.Latency.Count},
	}
	for _, p := range pairs {
		if m[p.metric] != strconv.FormatUint(p.status, 10) {
			t.Errorf("%s = %q on /metrics but %d on /api/status", p.metric, m[p.metric], p.status)
		}
	}
	if got, want := m["tickerplant_latency_max_seconds"], secondsNs(uint64(st.Latency.MaxNs)); got != want {
		t.Errorf("max = %q on /metrics but %q from /api/status", got, want)
	}
	if got, want := m[`tickerplant_latency_seconds{quantile="0.99"}`], secondsNs(uint64(st.Latency.P99Ns)); got != want {
		t.Errorf("p99 = %q on /metrics but %q from /api/status", got, want)
	}
}

// The two endpoints differ on purpose where the process has nothing to report: /api/status
// says 0 for a source that cannot count rejected frames and zero quantiles for an empty
// histogram, while /metrics leaves those series out.
func TestMetricsOmitsWhatStatusZeroes(t *testing.T) {
	srv := statusEdge(t, Config{Latency: metrics.NewHistogram()})
	_, mbody, _ := get(t, http.MethodGet, srv.URL+"/metrics", nil)
	_, sbody := statusBody(t, srv)
	if !strings.Contains(sbody, `"rejected":0`) || !strings.Contains(sbody, `"p50Ns":0`) {
		t.Fatalf("status no longer zeroes what it cannot know: %s", sbody)
	}
	m := samples(mbody)
	if _, ok := m["tickerplant_source_rejected_frames_total"]; ok {
		t.Error("/metrics must omit the rejected-frames counter for a source that cannot count")
	}
	if _, ok := m[`tickerplant_latency_seconds{quantile="0.5"}`]; ok {
		t.Error("/metrics must omit the quantiles of an empty histogram")
	}
}
