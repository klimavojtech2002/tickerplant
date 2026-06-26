# Scope

What this system does, what it deliberately does not, and where the hard boundaries are. The boundaries
below are drawn on purpose, with the reason for each, so the system does not claim more than it
guarantees.

## In scope

- Live ingestion from two to three venues over WebSocket (Binance, Kraken, OKX — ADR-0011).
- L2 order-book reconstruction from snapshot plus incremental deltas, with gap and checksum detection and
  snapshot resync ([correctness.md](correctness.md)).
- A single canonical book and trade model, with per-venue normalization at the adapter edge.
- Low-latency fan-out of the normalized stream to consumers, with bounded backpressure.
- A live dashboard as one fan-out consumer.
- Metrics: per-venue lag, gap and reconnect counts, internal p99 processing latency.
- Deterministic and chaos tests that prove the failure model, not assert it.

## Out of scope, and why

- **Trading and execution.** This is the data layer, not a bot. It places no orders, holds no position,
  and models no risk. Reconstruction correctness is the entire claim.
- **Arbitrage capture, colocation, MEV.** Cross-venue opportunities may be shown to demonstrate the
  pipeline end to end. Actually capturing them requires colocated and MEV infrastructure — a different,
  capital-intensive problem — and is deliberately not attempted. Detecting an opportunity is not the
  same as profiting from it.
- **Crash persistence.** State lives in memory. The correctness guarantee covers reconnects, gaps, and
  slow consumers for a live process; it does not cover surviving a process crash. Adding a write-ahead
  log or snapshotting is a coherent extension, not a current promise.
- **L3 / per-order books.** The model is L2, aggregated by price level. Reconstructing individual orders
  is a different data feed and a different problem.
- **Historical storage and replay as a product.** Recorded feeds drive tests; the system is not a
  time-series database and does not serve historical queries.
- **A consumer that must not lose data.** Fan-out resolves backpressure by dropping and, past a bound,
  disconnecting a slow consumer (ADR-0006). A consumer requiring guaranteed delivery of every update
  needs a durable queue in front of it, which this system does not provide.

## The determinism boundary

The correctness story is proven deterministically; live data is an integration concern. The line is
explicit, because a test suite that quietly depends on a live exchange proves nothing repeatable.

| Concern | How it is handled |
|---------|-------------------|
| Order-book reconstruction, gap detection, resync | Tested deterministically against a seeded synthetic source behind the transport port. Same seed, same run. |
| Sequence gaps, reorders, duplicates, crossing snapshots, mid-stream disconnects | Generated on demand by the synthetic source; every failure mode is reproducible from a seed. |
| Live WebSocket connection, real venue quirks, network behaviour | Edge adapters, exercised by integration runs against the live venue — never a dependency of the engine's tests. |
| Internal processing latency | Measured under a defined load harness on documented hardware (ADR-0009). |
| Network round-trip to the exchange | Out of scope to optimise or claim; named, not measured as if it were ours. |

The reconstruction core is deterministic and is where the correctness claim lives. The live socket is the
one place nondeterminism is allowed, and it is quarantined behind the port so it cannot reach the tests
that prove the thesis.

## Venue scope

The scope is deliberately two to three venues, reconstructed correctly, rather than five reconstructed
approximately. The three are chosen for distinct recovery procedures — Binance's snapshot-vs-stream race,
Kraken's CRC32 checksum, OKX's in-band sequence over a single connection — so they exercise three
different procedures rather than the same one three times (ADR-0011). All three are public, no-account
feeds. Binance and Kraken land first; OKX is an incremental adapter. The transport port makes a fourth
venue an adapter, not a rewrite.

## What "correct" means here

Two guards, checked continuously and proven by simulation, not assumed: the book never crosses, and the
book stays in sync with the venue — by sequence where the venue numbers its updates, by checksum where it
publishes one — with a break forcing a documented resync. The full correctness model, the per-venue
procedures, and the proof method are in [correctness.md](correctness.md).
