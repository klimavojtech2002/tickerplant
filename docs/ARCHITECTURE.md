# Architecture

A sans-I/O core that reconstructs L2 order books and fans a normalized stream out to consumers. The
reconstruction logic knows nothing about sockets or any specific exchange; live connections exist only
at the edge. This is what lets the entire correctness story
([correctness.md](correctness.md)) be tested deterministically. The decisions behind the structure are
in [decisions.md](decisions.md); the boundaries are in [scope.md](scope.md).

## 1. Layers

Ports and adapters around a pure domain. Venue specifics enter through ingestion adapters and are
translated into the canonical model at once; the domain depends only on the model and an abstract
transport port; delivery adapters sit on the output side.

```
   edge (adapters)                 core (pure domain)                    edge (adapters)
 ┌────────────────────┐        ┌──────────────────────────┐
 │ Binance  (WS)      │──┐     │  normalizer              │
 │ OKX      (WS)      │──┼────►│      │                   │      ┌─────────────────┐
 │ Kraken   (WS)      │──┘     │  order-book engine       │─────►│ in-process      │──► stream consumers
 ├────────────────────┤        │   (invariant-checked,    │      │ fan-out         │──► live dashboard
 │ synthetic source   │──────► │    single-writer)        │      │ (latest-wins)   │──► metrics
 │ (tests, same port) │        │      │                   │      └─────────────────┘
 └────────────────────┘        │  canonical model         │
        transport port ───────►└──────────────────────────┘
```

The synthetic source and a live socket implement the same transport port, so the engine cannot tell
them apart — the basis of deterministic testing (ADR-0005). The diagram is the target architecture:
Binance is wired live today; OKX, Kraken, and the dashboard are planned (see §11 and the README status
table).

## 2. Canonical model

One vocabulary, defined in [correctness.md](correctness.md) §1: Venue, Book, Level, Side, Snapshot,
Delta, Trade, Sequence. Venue terms are mapped to it at the adapter edge and never leak inward. Price and
size are integers in the venue's smallest increment, with no `float64` on the book path (ADR-0003).

## 3. Data flow

A raw venue message enters an ingestion adapter, which normalizes it into a canonical snapshot or delta
and hands it to the engine through the port. The engine applies it to the venue's book, checking the
invariants on every update (§5). The resulting normalized update is published to the in-process fan-out,
which delivers the latest to every consumer without blocking (a per-consumer conflating slot, §7). A
sequence gap or checksum mismatch short-circuits this flow into a resync (§6) instead of producing a
wrong book.

## 4. The sans-I/O core and the transport port

The transport port is the seam. It is a small interface the engine reads source events through and
issues snapshot requests across — no sockets, no HTTP, no venue knowledge. Two implementations satisfy
it: the deterministic synthetic source (a seeded generator that can emit any gap, reorder, duplicate, or
disconnect) and the live venue adapters. Because the seam is small and pure, the correctness-critical
code is the code under test, and live data is an integration concern, never a test dependency (ADR-0001,
ADR-0005).

## 5. The order-book engine

The thesis. It reconstructs each venue's L2 book from a snapshot plus deltas and maintains it under two
guards checked continuously: the book never crosses (universal), and the book stays in sync with the
venue — detected by sequence monotonicity where the venue numbers its updates and by checksum where it
publishes one instead ([correctness.md](correctness.md) §4–§6). A delta carries the absolute size at a
level; zero deletes it. The book is held in a price-sorted structure giving O(log n) level updates and
O(1) best-bid/ask reads.

Each venue's book has exactly one writer goroutine; readers obtain a consistent view through an
atomically published immutable view, so reads take no lock on the writer's hot path (ADR-0008). The
published view is a bounded top-N snapshot, so a publish costs O(depth), not a full O(n) book copy; a
persistent (structural-sharing) tree is the alternative if deeper views are needed (the top-N view ships
in slice 0003; the deeper alternative is measured and decided in slice 0006). One documented owner per
book, verified under `-race`.

## 6. Resync and the failure model

A detected sequence gap, a checksum mismatch, or a reconnect all converge on one path: discard the live
book and re-bootstrap from a fresh snapshot, following the venue's documented procedure
([correctness.md](correctness.md) §3, §7, §8). The book never interpolates across a gap. Resync is a
normal, exercised path, counted as a metric, not an error branch — a brief correct outage in place of
silent drift (ADR-0004).

## 7. Fan-out and backpressure

The normalized stream is delivered through a small in-process fan-out (`internal/delivery`, ADR-0013):
the engine publishes a complete top-N view, and each consumer holds a size-1 latest slot. Delivery is
non-blocking and latest-wins: if a consumer has not taken its previous view, the newer one supersedes it,
so a lagging consumer always jumps to the freshest book rather than replaying stale ones (ADR-0015). A
consumer that stays behind past a bound is disconnected and must resync, so one slow consumer cannot
stall the engine or the others. The reasoning for dropping over blocking is in ADR-0006; for latest-wins
conflation over a FIFO buffer, ADR-0015; for an in-process fan-out rather than vendoring an SSE broker,
ADR-0013.

## 8. Concurrency model

One documented owner per resource: one writer per venue book (§5), one connection manager per venue, the
fan-out owning its consumer set. Communication is over channels and per-consumer latest-value slots;
the fan-out's shared state is guarded by a mutex and the engine's counters are atomic. There is no shared
mutable state without a single owner, and no lock on the book's read path. The
race detector is on for every test run, and a clean `-race` is part of the definition of done, not an
occasional check.

## 9. Observability and latency measurement

The engine exposes per-venue lag, gap and reconnect counts, and dropped-event counts (read by the demo
today; a `/metrics` endpoint is planned), plus internal processing latency as a distribution
(p50/p99/p99.9/max). "Internal" is exact: from a raw message
arriving at an adapter to the normalized update leaving the fan-out, with the clock source and span
stated, under a defined open-loop, coordinated-omission-aware load harness on documented hardware.
End-to-end latency from the exchange is dominated by network round-trip, which this system does not
control and does not claim to optimise (ADR-0009).

## 10. Testing strategy

- **Deterministic simulation** is primary: the engine runs against the seeded synthetic source, which
  generates legal and illegal sequences; the same seed reproduces the same run, so any failure is a
  replayable test case (ADR-0005).
- **Property / simulation tests** assert the invariants continuously over generated runs — book stays
  correct or resyncs correctly — rather than checking a happy path.
- **Race detection** (`go test -race`) is always on; book ownership is verified, not assumed.
- **Integration tests** run the live Binance adapter against the real venue, behind the port, separate
  from the deterministic suite (Kraken/OKX adapters land later).
- **Benchmarks** measure internal latency under the load harness (§9).

The full correctness model and proof method are in [correctness.md](correctness.md) §10.

## 11. What is built

The build is sliced and audit-gated; the README status table is the source of truth for what is
implemented. The core is built and tested — canonical model, transport port and synthetic source,
order-book engine, the in-process fan-out with a runnable demo (`cmd/tickerplant`), the metrics and
benchmark harness, the Binance live adapter, and CI. Remaining: more live venues (Kraken, OKX) →
dashboard → docker packaging.
