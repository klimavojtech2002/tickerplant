# Latency benchmark

Measures the pipeline's **internal** processing latency. Network round-trip is excluded and not claimed
(ADR-0009). Two benchmarks, three spans, kept apart:

- **Engine span** (`engine-*`): the frame taken off the adapter's channel to the view stored for readers. This
  is the span the running system reports (the latency histogram behind `/metrics`); the benchmark measures it
  with the same wiring. Wire benchmark only.
- **Due to done** (`due-*`, and `svc-*` for the synthetic pipeline): from the moment an event was due to the
  moment `hub.Publish` returned. It contains queueing behind earlier events, the scheduler's wake-up of a
  parked goroutine, the channel hand-off and the fan-out. In the synthetic pipeline there is no channel: the
  driver calls `Step` itself, so "due" is the moment of that call and `svc` is service time plus the pacer's overshoot.
- **Publish** (`publish-*`): the time around `hub.Publish`. It includes signalling the consumer: sub-microsecond
  in the synthetic run and about 18 µs on the wire run. The cause of the difference was not measured; a consumer
  that has to be woken is a guess.

## Method

- **Open-loop.** Event `k` is due at `start + k*interval`, whatever happened to the events before it. Service that
  falls behind does not slow the arrival rate, so backpressure shows up instead of being hidden.
- **Coordinated-omission-aware.** Latency is measured from when an event was due, not from when it began, so a stall
  inflates the tail of every event queued behind it. `TestCoordinatedOmissionCorrection` shows it.
- **Pacing.** A gap to the next due time over 2 ms is slept through up to 1.5 ms before it; the rest is a spin on the
  clock. At the benchmark intervals it is a pure spin: `time.Sleep` below its own tick oversleeps and releases a burst
  of catch-up events, which would set the schedule instead of the system. The pacer sends a frame as soon as it is
  due, never earlier.
- **Fixed sample count.** Each run is 2,000 warmup events and 100,000 recorded events, independent of `b.N` (Go calls a
  benchmark again with a growing `b.N`, which would change the number of events and the tail). Run it with
  `-benchtime=1x -count=10 -cpu 4`; a driver refuses fewer than 10,000 recorded events.
- **Guards.** Both benchmarks fail rather than publish when the host has fewer than four CPUs or `GOMAXPROCS` is below
  four (the pacer spins on one core), when the clock advances in steps coarser than a fifth of the interval, or when
  the engine resyncs, sees a gap, or the hub did not publish every event. The wire benchmark also fails on a rejected
  frame or a refused crossing, when the engine span does not hold exactly one sample for every recorded event, and when
  it is overloaded (two or more frames waiting in the channel for more than 5% of the events).
- **Windows.** The clock there advances in steps of about half a millisecond or more, which cannot hold or measure a 50 µs or a
  200 µs schedule, so both benchmarks refuse to run and print the step they measured. The engine's own latency
  histogram reads the same clock: on Windows small spans are quantised to zero or one step.
- **Frames.** `BenchmarkWireLatency` sends generated Binance `depthUpdate` frames, labelled synthetic: 8 bid and 8 ask
  levels each (about 20% deletes), prices in a fixed band around 60000.00 so the book never crosses, price scale 2 and
  size scale 8, a snapshot of 100 levels per side served by a local HTTP server. The book settles near 300 levels, not
  the thousands of a full Binance book. They exercise the same decode and normalization code as live frames; Figure 3
  in the README is the live-traffic figure.
- **Other columns.** `sender-late` is the number of events, warmup included, that began more than one interval after
  they were due (the pacer was late, not the system). `over-interval` is the number of events whose due-to-done time exceeded
  one interval. `queue-max` is the most frames found waiting in the adapter's channel after an event was handled
  (recorded events only). `model-levels` is the size of the generator's model book (levels) after the last frame.
- **Utilisation** (`util-pct`) is the busy share of the benchmark goroutine: engine span plus `Publish` over elapsed
  time. The wake-up and the hand-off are not in it.
- **Garbage collection.** `gc-cycles` is the number of collections during the run, warmup included. The tail figures (p99.9
  and max) include those pauses, and the spinning pacer is subject to ordinary scheduler preemption.

## Run

On Linux, directly:

    go run ./bench/header > bench/results/<date>-<cpu>.txt
    go test -run '^$' -bench . -benchtime=1x -count=10 -cpu 4 ./bench >> bench/results/<date>-<cpu>.txt

Anywhere with Docker (this is how the published run was made; the ten benchmark runs take about 4 minutes, plus the build):

    docker build -f bench/Dockerfile --output type=local,dest=bench/results \
      --build-arg BENCH_COMMIT=$(git rev-parse HEAD) \
      --build-arg BENCH_DIRTY=$(test -z "$(git status --porcelain --untracked-files=no)" && echo false || echo true) \
      --build-arg BENCH_NOTE="where it ran" --build-arg RUN_ID=$(date +%s) .

The log must be written as ASCII with LF line endings (in PowerShell use `Out-File -Encoding ascii`). A log from a
modified tree is refused by the test. To compare two runs, `go install golang.org/x/perf/cmd/benchstat@latest` and
`benchstat old.txt new.txt`; that tool is not a dependency of this repository and is not exercised here.

## The published run

The tables below are rendered from the committed log by `go test ./bench`, which fails if they differ. The check
compares the displayed values (three significant figures), so an edit of the log below that precision would not be
noticed by the test; the log's history in git is the record. The values are the median across ten runs of each run's
figure, with the minimum and maximum across the runs; quantiles are histogram estimates with 2% bucket resolution. It
ran in a Linux container (a WSL2 virtual machine) on a Windows laptop in ordinary use. `engine-mean-ns` comes from the
histogram's exact sum and count. The synthetic and wire benchmarks use different intervals (50 µs and 200 µs): their
rows are not comparable.

<!-- bench:begin full bench/results/2026-10-23-i7-10870h-linux-vm.txt -->
Run facts, from the header of the log:

| | |
|---|---|
| go-version | go1.26.10 |
| goos-goarch | linux/amd64 |
| cpu | Intel(R) Core(TM) i7-10870H CPU @ 2.20GHz |
| logical-cpus | 16 |
| clock-tick-ns | 100 |
| date | 2026-10-23T04:38:23Z |
| commit | 4a5f01f8c4f3f21b04756e6d0b15cf6ecb9fe3e6 |
| note | Linux container (golang:1.26.10-alpine) under Docker Desktop, a WSL2 virtual machine, on a Windows 11 laptop in ordinary use; not bare metal; no attempt was made to quiet the machine |

BenchmarkPipelineLatency (-cpu 4, 10 runs; median, then min and max across the runs)

| metric | median | min | max |
|---|---|---|---|
| gomaxprocs | 4 | 4 | 4 |
| interval-ns | 50 µs | 50 µs | 50 µs |
| publish-p50-ns | 399 ns | 302 ns | 399 ns |
| publish-p99-ns | 26.4 µs | 19.4 µs | 35.1 µs |
| samples | 100000 | 100000 | 100000 |
| sender-late | 271.5 | 75 | 946 |
| svc-max-ns | 677 µs | 236 µs | 7.33 ms |
| svc-p50-ns | 2.17 µs | 1.91 µs | 2.73 µs |
| svc-p99-ns | 36.2 µs | 24.6 µs | 76 µs |
| svc-p999-ns | 157 µs | 63.6 µs | 2.53 ms |
| warmup | 2000 | 2000 | 2000 |

BenchmarkWireLatency (-cpu 4, 10 runs; median, then min and max across the runs)

| metric | median | min | max |
|---|---|---|---|
| due-max-ns | 9.96 ms | 4.69 ms | 14.8 ms |
| due-p50-ns | 155 µs | 152 µs | 164 µs |
| due-p99-ns | 295 µs | 249 µs | 349 µs |
| due-p999-ns | 2.34 ms | 573 µs | 8.8 ms |
| engine-max-ns | 1.17 ms | 283 µs | 10.2 ms |
| engine-mean-ns | 24.5 µs | 23.4 µs | 28.3 µs |
| engine-p50-ns | 22 µs | 20.6 µs | 25.1 µs |
| engine-p99-ns | 57.6 µs | 53.2 µs | 85.5 µs |
| engine-p999-ns | 103 µs | 90.8 µs | 171 µs |
| gc-cycles | 3 | 3 | 3 |
| gomaxprocs | 4 | 4 | 4 |
| interval-ns | 200 µs | 200 µs | 200 µs |
| model-levels | 313 | 313 | 313 |
| over-interval | 13300 | 11300 | 19300 |
| publish-p50-ns | 17.7 µs | 17.2 µs | 19 µs |
| publish-p99-ns | 30.2 µs | 26.6 µs | 39.5 µs |
| queue-max | 42.5 | 21 | 73 |
| samples | 100000 | 100000 | 100000 |
| sender-late | 159 | 29 | 623 |
| util-pct | 19.8 % | 18.9 % | 22.1 % |
| warmup | 2000 | 2000 | 2000 |
<!-- bench:end -->
