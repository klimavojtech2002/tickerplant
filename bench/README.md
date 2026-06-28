# Latency benchmark

Measures the pipeline's **internal** processing latency: from an event being *due* at
the source to the updated top-of-book view leaving the fan-out. Network round-trip is
excluded and not claimed (ADR-0009).

## Method

- **Open-loop.** Events are due on a fixed schedule (`start + i*interval`), not when the
  previous one finishes. Service that falls behind does not slow the arrival rate, so
  backpressure shows up instead of being hidden.
- **Coordinated-omission-aware.** Each event's latency is measured from its *due* time,
  not from when processing started. A stall therefore inflates the tail across every
  event queued behind it, rather than appearing as one slow sample. The included
  `TestCoordinatedOmissionCorrection` proves it: under a 40ms stall the corrected p99
  rises sharply while the naive (service-time) p99 stays low.
- **Span.** `done - due`, where `done` is read right after `hub.Publish`, using the
  `time.Now` monotonic clock.

## Run

    go test -bench . -benchmem ./bench

It reports `p50-ns / p99-ns / p999-ns` (coordinated-omission-corrected) and `ns/op`
(the arrival interval, since the harness is rate-paced, not throughput-bound).

## Reading the numbers

They are **machine-dependent and are not a published claim.** On a host with coarse
timer resolution the tail reflects OS scheduling jitter at the target rate, not the
engine's service time, which is far smaller. Any quoted figure must state the hardware,
Go version, and the `interval` used — otherwise the number is meaningless.

Quoted figure (fill in when publishing): _Go x.y, <CPU>, <OS>, interval <d>_.
