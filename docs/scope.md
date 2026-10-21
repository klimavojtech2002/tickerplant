# Scope

What this system does, what it deliberately does not, and where the hard boundaries are. The boundaries
below are drawn on purpose, with the reason for each, so the system does not claim more than it
checks.

## In scope

- Live ingestion from two venues over WebSocket (Binance and Kraken, ADR-0011).
- L2 order-book reconstruction from snapshot plus incremental deltas, with gap and checksum detection and
  snapshot resync ([correctness.md](correctness.md)).
- A single canonical book model with per-venue normalization at the adapter edge (a trade type is
  defined; no adapter emits trades).
- Non-blocking fan-out of the normalized stream to consumers, with bounded backpressure.
- A live dashboard fed by the HTTP edge (SSE), one fan-out consumer per connected client.
- Metrics: gap, resync and disconnect counts, delivery and drop counts, internal processing latency
  (p50/p99/p99.9/max).
- Deterministic fault-injection tests (a seeded simulation) that exercise the failure model instead of
  asserting it.

## Out of scope, and why

- **Trading and execution.** This is the data layer, not a bot. It places no orders, holds no position,
  and models no risk. Reconstruction correctness is the entire claim.
- **Arbitrage capture, colocation, MEV.** Capturing cross-venue opportunities requires colocated and
  MEV infrastructure — a different, capital-intensive problem — and is not attempted. The system
  neither detects nor shows such opportunities.
- **Crash persistence.** State lives in memory. The correctness checks cover reconnects, gaps, and
  slow consumers for a live process; it does not cover surviving a process crash. A write-ahead
  log or snapshotting is not built.
- **L3 / per-order books.** The model is L2, aggregated by price level. Reconstructing individual orders
  is a different data feed and a different problem.
- **Historical storage and replay as a product.** Tests run on a seeded synthetic source; the system is
  not a time-series database and does not serve historical queries.
- **A consumer that must not lose data.** Fan-out resolves backpressure by dropping and, past a bound,
  disconnecting a slow consumer (ADR-0006). A consumer requiring guaranteed delivery of every update
  needs a durable queue in front of it, which this system does not provide.

## The determinism boundary

The correctness story is checked deterministically; live data is an integration concern. The line is
explicit, because a test suite that quietly depends on a live exchange proves nothing repeatable.

| Concern | How it is handled |
|---------|-------------------|
| Order-book reconstruction, gap detection, resync | Tested deterministically against a seeded synthetic source behind the transport port. Same seed, same run. |
| Sequence gaps, reorders, duplicates, crossing snapshots, mid-stream disconnects | Generated on demand by the synthetic source; every failure mode is reproducible from a seed. |
| Live WebSocket connection, real venue quirks, network behaviour | Edge adapters, exercised by integration runs against the live venue — never a dependency of the engine's tests. |
| Internal processing latency | Measured under a defined load harness (ADR-0009). |
| Network round-trip to the exchange | Out of scope to optimise or claim; named, not measured as if it were ours. |

The reconstruction core is deterministic and is where the correctness claim lives. The live socket is the
one place nondeterminism is allowed, and it is quarantined behind the port so it cannot reach the tests
that check the thesis.

## Venue scope

The scope is deliberately two venues, reconstructed correctly, rather than five reconstructed
approximately. They are chosen for distinct recovery procedures — Binance's snapshot-vs-stream race and
Kraken's CRC32 checksum — so they exercise different procedures rather than the same one twice
(ADR-0011). Both are public, no-account feeds. OKX's in-band sequence is analysed in
[correctness.md](correctness.md) §7 but has no adapter. The transport port makes another venue an
adapter, not a rewrite.

## What "correct" means here

Two guards, checked continuously and exercised by simulation, not assumed: the book never crosses, and
the book stays in sync with the venue — by sequence where the venue numbers its updates, by checksum
where it publishes one — with a break forcing a documented resync. The full correctness model, the
per-venue procedures, and the simulation method are in [correctness.md](correctness.md).
