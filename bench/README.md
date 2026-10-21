# Latency benchmark

Measures the pipeline's **internal** processing latency: from an event being *due* at
the source to `hub.Publish` returning with the updated view. Network round-trip is
excluded and not claimed (ADR-0009).

## Method

- **Open-loop.** Events are due on a fixed schedule (`start + i*interval`), not when the
  previous one finishes. Service that falls behind does not slow the arrival rate, so
  backpressure shows up instead of being hidden.
- **Coordinated-omission-aware.** Each event's latency is measured from its *due* time,
  not from when processing started. A stall therefore inflates the tail across every
  event queued behind it, rather than appearing as one slow sample. The included
  `TestCoordinatedOmissionCorrection` shows it: under a 40ms stall the corrected p99
  rises sharply; the test asserts that the corrected p99 exceeds the
  naive p99 and is at least 10ms.
- **Span.** `done - due`, where `done` is read right after `hub.Publish`, using the
  `time.Now` monotonic clock.

## Run

    go test -bench . -benchmem ./bench

It reports `p50-ns / p99-ns / p999-ns / max-ns` (coordinated-omission-corrected) and `ns/op` (about the
arrival interval, since the harness is rate-paced, not throughput-bound). The interval is fixed at 50µs
(20,000 events/s).

## Reading the numbers

They are **machine-dependent and are not a published claim.** On a host with coarse timer resolution
the tail reflects OS scheduling jitter at the target rate, rather than the engine's service time. Any
quoted figure must state the hardware, Go version and arrival interval used — otherwise the number is
meaningless.
