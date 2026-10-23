package bench

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The published numbers are computed from the committed run log, not typed into a README. This file
// parses a log (the header lines the header program prints, then the raw `go test -bench` output),
// computes the median, minimum and maximum of every reported metric across the runs, and renders the
// tables. A test requires the READMEs to contain the rendered text verbatim, so a number in the README
// that is not in the log fails the build. The code is test code and uses floats; nothing here touches
// the price path.

const requiredRuns = 10

// benchLog is a parsed run log.
type benchLog struct {
	header  [][2]string                     // key, value, in the order printed
	runs    map[string][]map[string]float64 // benchmark name -> one metric map per run
	metrics map[string][]string             // benchmark name -> metric units in the order of the first run
	names   []string                        // benchmark names in order of appearance
	cpus    map[string]int                  // benchmark name -> the -cpu suffix
}

var benchLine = regexp.MustCompile(`^(Benchmark\w+)-(\d+)\s+(\d+)\s+(.*)$`)

// parseLog reads a log. It requires `runs` runs of every benchmark, refuses a log written from a modified
// tree, and ignores any line that is neither a header line nor a benchmark result. A byte-order mark and
// carriage returns are tolerated.
func parseLog(text string, runs int) (*benchLog, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	l := &benchLog{runs: map[string][]map[string]float64{}, metrics: map[string][]string{}, cpus: map[string]int{}}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		if rest, ok := strings.CutPrefix(line, "# "); ok {
			if k, v, ok := strings.Cut(rest, ": "); ok {
				l.header = append(l.header, [2]string{k, v})
			}
			continue
		}
		m := benchLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		cpus, _ := strconv.Atoi(m[2])
		fields := strings.Fields(m[4])
		if len(fields)%2 != 0 {
			return nil, fmt.Errorf("benchmark line has an odd number of value/unit fields: %q", line)
		}
		run := map[string]float64{}
		var order []string
		for i := 0; i < len(fields); i += 2 {
			v, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				return nil, fmt.Errorf("bad value %q in %q: %w", fields[i], line, err)
			}
			unit := fields[i+1]
			if unit == "ns/op" {
				continue // not a figure of this benchmark: the sample count does not follow b.N
			}
			run[unit] = v
			order = append(order, unit)
		}
		if _, seen := l.runs[name]; !seen {
			l.names = append(l.names, name)
			l.metrics[name] = order
			l.cpus[name] = cpus
		}
		l.runs[name] = append(l.runs[name], run)
	}
	if len(l.names) == 0 {
		return nil, fmt.Errorf("no benchmark lines in the log")
	}
	for _, n := range l.names {
		if got := len(l.runs[n]); got != runs {
			return nil, fmt.Errorf("%s has %d runs in the log, want exactly %d", n, got, runs)
		}
		for i, r := range l.runs[n] {
			for _, u := range l.metrics[n] {
				if _, ok := r[u]; !ok {
					return nil, fmt.Errorf("%s run %d lacks the metric %s", n, i+1, u)
				}
			}
		}
	}
	if v := l.fact("tree"); v != "clean" {
		return nil, fmt.Errorf("the log was produced from a tree that is %q, not clean; do not publish it", v)
	}
	return l, nil
}

func (l *benchLog) fact(key string) string {
	for _, kv := range l.header {
		if kv[0] == key {
			return kv[1]
		}
	}
	return ""
}

// stats gives the median (mean of the middle two for an even count), minimum and maximum of a metric.
func (l *benchLog) stats(name, unit string) (median, lo, hi float64) {
	vals := make([]float64, 0, len(l.runs[name]))
	for _, r := range l.runs[name] {
		vals = append(vals, r[unit])
	}
	sort.Float64s(vals)
	n := len(vals)
	if n%2 == 1 {
		median = vals[n/2]
	} else {
		median = (vals[n/2-1] + vals[n/2]) / 2
	}
	return median, vals[0], vals[n-1]
}

// sig3 rounds half away from zero to three significant figures.
func sig3(v float64) float64 {
	if v == 0 {
		return 0
	}
	mag := math.Floor(math.Log10(math.Abs(v)))
	scale := math.Pow(10, 2-mag)
	return math.Round(v*scale) / scale
}

// trimNum prints a number without trailing zeros.
func trimNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// formatMetric prints a value for its unit. A latency (unit ending in -ns) is written out in ns below
// 1000, in µs below a million ns and in ms above, to three significant figures from 1000 up; a count or
// a percentage below 1000 keeps its exact value (a half is possible in a median); anything larger is
// rounded to three significant figures.
func formatMetric(unit string, v float64) string {
	if strings.HasSuffix(unit, "-ns") {
		switch {
		case v < 1000:
			return trimNum(v) + " ns"
		case v < 1e6:
			return trimNum(sig3(v/1e3)) + " µs"
		default:
			return trimNum(sig3(v/1e6)) + " ms"
		}
	}
	if strings.HasSuffix(unit, "-pct") {
		return trimNum(sig3(v)) + " %"
	}
	if math.Abs(v) < 1000 {
		return trimNum(v)
	}
	return trimNum(sig3(v))
}

// render writes the tables. "full" lists every metric of every benchmark; "summary" lists the headline
// latency figures only. Both begin with the facts of the run.
func (l *benchLog) render(mode string) string {
	var b strings.Builder
	b.WriteString("Run facts, from the header of the log:\n\n| | |\n|---|---|\n")
	for _, k := range []string{"go-version", "goos-goarch", "cpu", "logical-cpus", "clock-tick-ns", "date", "commit", "note"} {
		if v := l.fact(k); v != "" {
			fmt.Fprintf(&b, "| %s | %s |\n", k, v)
		}
	}
	for _, n := range l.names {
		var units []string
		for _, u := range l.metrics[n] {
			if mode == "summary" && !headline(u) {
				continue
			}
			units = append(units, u)
		}
		fmt.Fprintf(&b, "\n%s (-cpu %d, %d runs; median, then min and max across the runs)\n\n| metric | median | min | max |\n|---|---|---|---|\n", n, l.cpus[n], len(l.runs[n]))
		for _, u := range units {
			med, lo, hi := l.stats(n, u)
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", u, formatMetric(u, med), formatMetric(u, lo), formatMetric(u, hi))
		}
	}
	return b.String()
}

// headline reports whether the summary table shows a metric.
func headline(unit string) bool {
	for _, p := range []string{"svc-", "due-", "engine-p", "engine-m", "publish-p", "util-pct", "sender-late", "samples"} {
		if strings.HasPrefix(unit, p) {
			return true
		}
	}
	return false
}

// ---- tests ---------------------------------------------------------------------------------------

// sampleLog builds a log with `runs` runs of one benchmark whose p50 is 100*run ns and whose count is run.
func sampleLog(runs int, tree string) string {
	var b strings.Builder
	b.WriteString("# go-version: go1.0\n# cpu: Test CPU\n# tree: " + tree + "\n# commit: abc123\ngoos: linux\nBenchmarkWireLatency-4\t1\t")
	for i := 1; i <= runs; i++ {
		if i > 1 {
			b.WriteString("PASS\nBenchmarkWireLatency-4\t1\t")
		}
		fmt.Fprintf(&b, "0 ns/op\t%d.000 due-p50-ns\t%d.000 sender-late\t%d.500 util-pct\n", 100*i, i, i)
	}
	b.WriteString("PASS\nok  \tbench\t5s\n")
	return b.String()
}

func TestResultsMedianMinMaxAndRounding(t *testing.T) {
	for _, tc := range []struct {
		runs   int
		median string
	}{{3, "200 ns"}, {4, "250 ns"}} {
		l, err := parseLog(sampleLog(tc.runs, "clean"), tc.runs)
		if err != nil {
			t.Fatal(err)
		}
		med, lo, hi := l.stats("BenchmarkWireLatency", "due-p50-ns")
		if formatMetric("due-p50-ns", med) != tc.median || lo != 100 || hi != float64(100*tc.runs) {
			t.Errorf("%d runs: median %v (%s), min %v, max %v", tc.runs, med, formatMetric("due-p50-ns", med), lo, hi)
		}
	}
	for _, tc := range []struct {
		unit string
		v    float64
		want string
	}{
		{"due-p50-ns", 0, "0 ns"}, {"due-p50-ns", 999, "999 ns"}, {"due-p50-ns", 1000, "1 µs"},
		{"due-p50-ns", 1587.2, "1.59 µs"}, {"due-p50-ns", 999_999, "1000 µs"}, {"due-p50-ns", 1_234_567, "1.23 ms"},
		{"sender-late", 0, "0"}, {"sender-late", 2.5, "2.5"}, {"sender-late", 12345, "12300"},
		{"util-pct", 37.249, "37.2 %"}, {"util-pct", 0.5, "0.5 %"},
	} {
		if got := formatMetric(tc.unit, tc.v); got != tc.want {
			t.Errorf("formatMetric(%q, %v) = %q, want %q", tc.unit, tc.v, got, tc.want)
		}
	}
	if got := sig3(0.0012345); got != 0.00123 {
		t.Errorf("sig3(0.0012345) = %v", got)
	}
}

func TestResultsParserRules(t *testing.T) {
	good := sampleLog(10, "clean")
	if _, err := parseLog(good, 10); err != nil {
		t.Fatalf("a good log: %v", err)
	}
	// CRLF line endings and a byte-order mark, as a Windows shell redirect writes them.
	crlf := "\ufeff" + strings.ReplaceAll(good, "\n", "\r\n")
	a, err := parseLog(crlf, 10)
	if err != nil {
		t.Fatalf("CRLF with a BOM: %v", err)
	}
	b, _ := parseLog(good, 10)
	if a.render("full") != b.render("full") {
		t.Error("CRLF and a BOM changed the rendering")
	}
	for name, tc := range map[string]struct{ log, want string }{
		"nine runs":    {sampleLog(9, "clean"), "9 runs"},
		"dirty tree":   {sampleLog(10, "dirty"), "not clean"},
		"unknown tree": {sampleLog(10, "unknown"), "not clean"},
		"empty":        {"# tree: clean\n", "no benchmark lines"},
		"odd fields":   {"# tree: clean\nBenchmarkX-4\t1\t5 ns/op\t7\n", "odd number"},
		"bad value":    {"# tree: clean\nBenchmarkX-4\t1\tabc due-p50-ns\n", "bad value"},
	} {
		if _, err := parseLog(tc.log, 10); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one containing %q", name, err, tc.want)
		}
	}
}

func TestResultsRenderIsStable(t *testing.T) {
	l, err := parseLog(sampleLog(10, "clean"), 10)
	if err != nil {
		t.Fatal(err)
	}
	full := l.render("full")
	for _, want := range []string{
		"| go-version | go1.0 |", "| due-p50-ns | 550 ns | 100 ns | 1 µs |", "| sender-late | 5.5 | 1 | 10 |", "| util-pct | 6 % | 1.5 % | 10.5 % |",
		"BenchmarkWireLatency (-cpu 4, 10 runs; median, then min and max across the runs)",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("rendering lacks %q:\n%s", want, full)
		}
	}
	if strings.Contains(l.render("summary"), "util-pct") == false || strings.Contains(l.render("summary"), "gomaxprocs") {
		t.Error("the summary should keep the headline metrics and drop the constants")
	}
}

var markerRe = regexp.MustCompile(`(?s)<!-- bench:begin (full|summary) (\S+) -->\r?\n(.*?)<!-- bench:end -->`)

// publishedBlocks returns the marked blocks of a README: mode, log path and the text between the markers.
func publishedBlocks(readme string) [][3]string {
	var out [][3]string
	for _, m := range markerRe.FindAllStringSubmatch(readme, -1) {
		out = append(out, [3]string{m[1], m[2], strings.ReplaceAll(m[3], "\r", "")})
	}
	return out
}

// checkPublished compares every marked block in a README with the table rendered from the log it names.
func checkPublished(root, readme string) error {
	blocks := publishedBlocks(readme)
	for _, bl := range blocks {
		raw, err := os.ReadFile(filepath.Join(root, bl[1]))
		if err != nil {
			return fmt.Errorf("a marker names the log %s, which cannot be read: %w", bl[1], err)
		}
		l, err := parseLog(string(raw), requiredRuns)
		if err != nil {
			return fmt.Errorf("%s: %w", bl[1], err)
		}
		if want := l.render(bl[0]); strings.TrimSpace(bl[2]) != strings.TrimSpace(want) {
			return fmt.Errorf("the %s table between the markers differs from the one rendered from %s:\n--- README ---\n%s\n--- log ---\n%s", bl[0], bl[1], bl[2], want)
		}
	}
	return nil
}

func TestPublishedMarkersAreChecked(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bench", "results"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join("bench", "results", "x.txt")
	if err := os.WriteFile(filepath.Join(dir, logPath), []byte(sampleLog(10, "clean")), 0o644); err != nil {
		t.Fatal(err)
	}
	l, _ := parseLog(sampleLog(10, "clean"), 10)
	readme := "text\n<!-- bench:begin full " + filepath.ToSlash(logPath) + " -->\n" + l.render("full") + "<!-- bench:end -->\nmore\n"
	if err := checkPublished(dir, readme); err != nil {
		t.Fatalf("a README that matches its log: %v", err)
	}
	if err := checkPublished(dir, strings.Replace(readme, "550 ns", "551 ns", 1)); err == nil {
		t.Error("a README that differs from its log by one number was accepted")
	}
	if err := checkPublished(dir, strings.Replace(readme, "x.txt", "missing.txt", 1)); err == nil {
		t.Error("a marker naming a missing log was accepted")
	}
	if err := checkPublished(dir, strings.ReplaceAll(readme, "\n", "\r\n")); err != nil {
		t.Errorf("a README with CRLF line endings: %v", err)
	}
	if err := checkPublished(dir, "no markers here"); err != nil {
		t.Errorf("a README with no markers: %v", err)
	}
}

// The numbers in the two READMEs are the numbers in the committed log.
func TestPublishedNumbersMatchTheLog(t *testing.T) {
	root := ".."
	found := 0
	for _, f := range []string{"README.md", filepath.Join("bench", "README.md")} {
		raw, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		found += len(publishedBlocks(string(raw)))
		if err := checkPublished(root, string(raw)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if found == 0 {
		t.Skip("no benchmark log is published yet: no README carries a bench:begin marker")
	}
	if found < 2 {
		t.Errorf("only %d marked block found; both READMEs should carry one", found)
	}
}
