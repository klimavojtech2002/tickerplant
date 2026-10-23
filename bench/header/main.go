// Command header prints the facts a published benchmark run must state: what it ran on and how.
// Its output is the first part of a run log (bench/results); every line starts with "# " so the log
// parser can tell it from the benchmark output that follows.
//
//	go run ./bench/header
//
// The commit, whether the tree was clean, and a free-text note can be passed in the environment
// (BENCH_COMMIT, BENCH_DIRTY=true|false, BENCH_NOTE) for runs where there is no git checkout, such as
// inside a container build.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

func main() {
	fmt.Printf("# go-version: %s\n", runtime.Version())
	fmt.Printf("# goos-goarch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("# cpu: %s\n", cpuModel())
	fmt.Printf("# logical-cpus: %d\n", runtime.NumCPU())
	fmt.Printf("# gomaxprocs-default: %d\n", runtime.GOMAXPROCS(0))
	fmt.Printf("# gogc: %s\n", envOr("GOGC", "default"))
	fmt.Printf("# gomemlimit: %s\n", envOr("GOMEMLIMIT", "default"))
	fmt.Printf("# godebug: %s\n", envOr("GODEBUG", "none"))
	fmt.Printf("# clock-tick-ns: %d\n", clockTick().Nanoseconds())
	fmt.Printf("# date: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Printf("# commit: %s\n", commit())
	fmt.Printf("# tree: %s\n", tree())
	fmt.Printf("# note: %s\n", envOr("BENCH_NOTE", "none"))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// clockTick is the typical step of time.Now: the median of the non-zero steps seen over up to 100 ms of
// readings. It is the same function as in the benchmark's guard (bench/latency_test.go); keep them equal.
func clockTick() time.Duration {
	const maxSteps, span = 200_000, 100 * time.Millisecond
	steps := make([]time.Duration, 0, maxSteps)
	first := time.Now()
	prev := first
	for len(steps) < maxSteps {
		now := time.Now()
		if d := now.Sub(prev); d > 0 {
			steps = append(steps, d)
			prev = now
		}
		if now.Sub(first) >= span {
			break
		}
	}
	if len(steps) == 0 {
		return span // the clock did not move at all
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i] < steps[j] })
	return steps[len(steps)/2]
}

func cpuModel() string {
	switch runtime.GOOS {
	case "linux":
		f, err := os.Open("/proc/cpuinfo")
		if err != nil {
			return "unknown"
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if k, v, ok := strings.Cut(sc.Text(), ":"); ok && strings.TrimSpace(k) == "model name" {
				return strings.TrimSpace(v)
			}
		}
	case "windows":
		out, err := exec.Command("reg", "query", `HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "/v", "ProcessorNameString").Output()
		if err != nil {
			return "unknown"
		}
		for line := range strings.SplitSeq(string(out), "\n") {
			if _, v, ok := strings.Cut(line, "REG_SZ"); ok {
				return strings.TrimSpace(v)
			}
		}
	}
	return "unknown"
}

func commit() string {
	if v := os.Getenv("BENCH_COMMIT"); v != "" {
		return v
	}
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// tree reports whether tracked files differ from the commit. Untracked files are ignored on purpose:
// the log being written is one, and a log from a modified tree must not be published.
func tree() string {
	if v := os.Getenv("BENCH_DIRTY"); v != "" {
		if v == "true" {
			return "dirty"
		}
		return "clean"
	}
	out, err := exec.Command("git", "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil {
		return "unknown"
	}
	if strings.TrimSpace(string(out)) != "" {
		return "dirty"
	}
	return "clean"
}
