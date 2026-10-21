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
 │ Binance  (WS)      │──┐     │                          │
 │ Kraken   (WS)      │──┴────►│                          │      ┌─────────────────┐
 │                    │        │  order-book engine       │─────►│ in-process      │──► log consumer
 ├────────────────────┤        │   (invariant-checked,    │      │ fan-out         │──► HTTP edge (SSE) ──► dashboard
 │ synthetic source   │──────► │    single-writer)        │      │ (latest-wins)   │
 │ (tests, demo)      │        │                          │      └─────────────────┘
 └────────────────────┘        │  canonical model         │
        transport port ───────►└──────────────────────────┘
```

One process runs a single source (the diagram shows every source the port accepts, not what runs at
once): the synthetic one, or one live venue chosen with `-live -venue binance|kraken`. With `-http`, the
HTTP edge also serves `/metrics`, read directly from the engine counters and the fan-out's stats. The
synthetic source and a live socket implement the same transport port, so the engine cannot tell them
apart — the basis of deterministic testing (ADR-0005). Binance and Kraken are wired live; the README
status table lists what is built.

## 2. Canonical model

One vocabulary, defined in [correctness.md](correctness.md) §1: Venue, Book, Level, Side, Snapshot,
Delta, Trade, Sequence. Venue terms are mapped to it at the adapter edge and never leak inward. Trade is
defined, but no source emits trades and the engine ignores them. Price and size are integers scaled to
the symbol's decimal precision, with no `float64` on the book path (ADR-0003).

## 3. Data flow

A raw venue message enters an ingestion adapter, which normalizes it into a canonical snapshot or delta
and hands it to the engine through the port. The engine applies it to the venue's book, checking the
invariants on every update (§5). The engine stores the resulting view atomically, and the run loop in
`internal/app` hands each new view to the in-process fan-out, which delivers the latest to every
consumer without blocking (a per-consumer conflating slot, §7). A sequence gap, a would-cross delta or a
checksum mismatch short-circuits this flow into a resync (§6) instead of producing a wrong book.

## 4. The sans-I/O core and the transport port

The transport port is the seam. It is a small interface the engine reads source events through and
issues snapshot requests across — no sockets, no HTTP, no venue knowledge. Two implementations satisfy
it: the deterministic synthetic source (a seeded generator that can be scheduled to emit gaps, reorders,
duplicates, disconnects, crossing deltas and coalesced deltas) and the live venue adapters. Because the
seam is small and pure, the correctness-critical code is the code under test, and live data is an
integration concern, never a test dependency (ADR-0001, ADR-0005).

## 5. The order-book engine

The thesis. It reconstructs each venue's L2 book from a snapshot plus deltas and maintains it under two
guards checked continuously: the book never crosses (universal), and the book stays in sync with the
venue — detected by sequence monotonicity where the venue numbers its updates and by checksum where it
publishes one instead ([correctness.md](correctness.md) §4–§6). A delta carries the absolute size at a
level; zero deletes it. The book is held as two price-sorted slices: a level is found by binary search
(O(log n)) and inserted or deleted by shifting (O(n) in the worst case; a full Binance book
can hold thousands of levels per side), and the best bid/ask is O(1) (index 0).

Each venue's book has exactly one writer goroutine; readers obtain a consistent view through an
atomically published immutable view, so reads take no lock on the writer's hot path (ADR-0008). The
published view is a bounded top-N snapshot, so a publish costs O(depth), not a full O(n) book copy.
One documented owner per book, verified under `-race`.

## 6. Resync and the failure model

A detected sequence gap, a delta that would cross the book, a checksum mismatch, or a disconnect event
(emitted by the Kraken adapter and the synthetic source; a Binance reconnect shows up through the
sequence check) all converge on one path: discard the live book and re-bootstrap from a fresh snapshot,
following the venue's documented procedure ([correctness.md](correctness.md) §3, §7, §8). The book never
interpolates across a gap. Resync is a normal, exercised path, counted as a metric, not an error branch
— a resync in place of silent drift (ADR-0004).

## 7. Fan-out and backpressure

The normalized stream is delivered through a small in-process fan-out (`internal/delivery`, ADR-0013):
the engine stores a complete top-N view, the run loop hands it to the hub, and each consumer holds a
size-1 latest slot. Delivery is non-blocking and latest-wins: if a consumer has not taken its previous
view, the newer one supersedes it, so a lagging consumer always jumps to the freshest book rather than
replaying stale ones (ADR-0015). A consumer that stays behind past a bound (1024 consecutive supersedes
in `internal/app`) is disconnected, and an SSE client reconnects and starts from the current view, so
one slow consumer cannot stall the engine or the others. The reasoning for dropping over blocking is in
ADR-0006; for latest-wins conflation over a FIFO buffer, ADR-0015; for an in-process fan-out rather than
vendoring an SSE broker, ADR-0013.

## 8. Concurrency model

One documented owner per resource: one writer per venue book (§5), one connection manager per venue, the
fan-out owning its consumer set. Communication is over channels and per-consumer latest-value slots;
the fan-out's shared state is guarded by a mutex and the engine's counters are atomic. Shared state is
either atomic or mutex-guarded inside its owner, and there is no lock on the book's read path.

CI runs the default test suite under the race detector (the opt-in live-venue job does not), so a data
race fails the build rather than being an occasional check.

## 9. Observability and latency measurement

The engine exposes gap, resync, disconnect, would-cross, and checksum-mismatch counters, the fan-out's
delivered/dropped counts, and internal processing latency as a distribution (p50/p99/p99.9/max), all
served by the HTTP edge's `/metrics` endpoint (ADR-0017). "Internal" is exact: from the adapter taking
the raw frame off its frame channel to the applied view stored for readers, stamped on each event at
dequeue and observed by the engine for every applied delta (monotonic clock), so no wire-wait is ever
inside a sample. Queue-wait before the dequeue is excluded by construction — this is service time;
sojourn under load is the bench harness's job, measured open-loop and coordinated-omission-aware
(ADR-0009). Samples of 1ns or less land in the lowest bucket and read as about 1ns, which is the
histogram's lowest bucket, not a real duration. End-to-end latency from the exchange is dominated by
network round-trip, which this system does not control and does not claim to optimise (ADR-0009).

## 10. Testing strategy

- **Deterministic simulation** is primary: the engine runs against the seeded synthetic source, which
  generates legal and illegal sequences; the same seed reproduces the same run, so any failure is a
  replayable test case (ADR-0005).
- **Simulation tests** check after every step that no view crosses, and at the end that the book
  matches an independent truth book, rather than checking a happy path.
- **Race detection**: CI runs the default suite under `go test -race`; book ownership is verified, not
  assumed.
- **Integration tests** run the live Binance and Kraken adapters against the real venue, behind the
  port, separate from the deterministic suite.
- **Benchmarks** measure internal latency under the load harness (§9).

The full correctness model and simulation method are in [correctness.md](correctness.md) §10.

## 11. What is built

The README status table is the source of truth for what is implemented. The core is built and tested —
canonical model, transport port and synthetic source, order-book engine, the in-process fan-out with a
runnable demo (`internal/app` wires it, `cmd/tickerplant` is the command), the metrics and benchmark
harness, the Binance and Kraken live adapters, the HTTP edge (SSE stream + /metrics, ADR-0017), the
dashboard (ADR-0018), CI, and the Docker packaging (ADR-0019). There is no OKX adapter (not planned; see
[scope.md](scope.md)).
