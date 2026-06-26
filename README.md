# tickerplant

A real-time service that reconstructs live order books from multiple crypto exchanges, normalizes
them into one canonical stream, and fans that stream out to consumers — with correctness guarantees
that hold under reconnects, sequence gaps, and slow consumers.

In plain terms: it connects to several exchanges at once, rebuilds each one's live book of buy and
sell orders from a snapshot plus a flood of incremental updates, and serves a single clean,
ordered, gap-free view to anyone downstream — and it can prove the book it serves stays correct: no
silent loss, duplication, or reordering across reconnects and gaps.

[![CI](https://github.com/klimavojtech2002/tickerplant/actions/workflows/ci.yml/badge.svg)](https://github.com/klimavojtech2002/tickerplant/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## The problem

Anyone building on top of market data — an exchange, a market maker, a data vendor, an analytics
product — needs one reliable, normalized view of the market. Getting there is harder than it looks.
Each venue speaks a different WebSocket protocol, sends a different message shape, sequences updates
differently, and disconnects without warning. The valuable, expensive part is not a trading
strategy; it is the **data layer underneath it**: ingesting many messy feeds and turning them into a
single stream you can trust.

The single hardest piece of that layer is the **order book**. An exchange does not send you the book;
it sends a snapshot and then a continuous stream of incremental deltas, and you reconstruct the book
yourself. Do it slightly wrong — apply a delta across a sequence gap, mishandle the snapshot-vs-stream
race — and the book silently goes wrong: it crosses (a bid priced at or above the ask), or it drifts
from reality with no error raised. This is the canonical place real systems have subtle, expensive
bugs, and it is the centerpiece of this project.

## What it does

- Ingests live order-book and trade feeds from several crypto exchanges over WebSocket.
- Reconstructs each venue's L2 order book from snapshot plus incremental deltas, detecting sequence
  gaps and resynchronizing from a fresh snapshot when one is found.
- Normalizes every venue's heterogeneous format into a single canonical book and trade model.
- Fans the normalized stream out to consumers and a live dashboard with backpressure handling.

## The order book is the thesis

The book is maintained behind two invariants that are checked continuously, not assumed:

- **The book never crosses.** The best bid is always strictly below the best ask. A violation is a
  bug, surfaced immediately, not served downstream.
- **The book stays in sync with the venue.** Where a venue numbers its updates, the book applies them
  strictly in order and a missing number is a gap; where a venue publishes a checksum instead, a
  mismatch is the equivalent signal. Either way the book does not guess — it discards the live state
  and resynchronizes from a new snapshot, following each exchange's documented recovery procedure
  (e.g. buffering the stream, then dropping events at or before the snapshot's last update id).

These invariants are enforced with property-based tests: generate arbitrary legal and illegal update
sequences and assert the book either stays correct or correctly resyncs. The correctness model, the
recovery procedure per venue, and the exact failure model are documented in
[docs/correctness.md](docs/correctness.md).

## Architecture — a sans-I/O core

The reconstruction and fan-out logic is written against an **abstract transport**, with no knowledge
of sockets or any specific exchange. Live exchange connections exist only at the edge, as adapters
implementing that transport. This is deliberate: it lets the entire correctness story be tested
**deterministically** — the core runs against recorded and synthetically generated feeds, where every
gap, reorder, and disconnect can be reproduced exactly — while live data is an integration concern,
never a dependency of the tests. (The same single-threaded, sans-I/O testing discipline is the
subject of my `seedloop` project.)

```
   exchanges (live)              tickerplant                          consumers
 ┌──────────────────┐      ┌────────────────────────┐
 │ venue A (WS)     │─────►│ ingestion adapters      │
 ├──────────────────┤      │   (transport port)      │
 │ venue B (WS)     │─────►│        │                │      ┌────────────────┐
 ├──────────────────┤      │   normalizer            │─────►│ stream consumers│
 │ recorded / synth │─────►│        │                │      ├────────────────┤
 │ (tests, same     │      │   order-book engine     │─────►│ live dashboard  │
 │  port)           │      │   (invariant-checked)   │      └────────────────┘
 └──────────────────┘      │        │                │
                           │   fan-out (broker)      │
                           └────────────────────────┘
```

The fan-out layer is the standard-library SSE/streaming broker first built for
`arbitrage-engine` — pooling, fan-out, reconnect, resumption. tickerplant will vendor its own copy of
that foundation (Go forbids importing another module's `internal/` package); the two lead with different
hard problems:
arbitrage-engine is about cross-venue detection and exact-money correctness on a synthetic feed;
tickerplant is about **order-book reconstruction correctness on real, live data**.

## On latency

Latency is reported honestly. The engine measures **internal processing latency** — the time from a
raw message arriving at an adapter to the normalized update leaving the fan-out — as a measured
distribution (p50/p99/p99.9/max), with the clock source and span stated, under a defined load harness on
documented hardware. The figure is reported once measured, not estimated in advance. End-to-end latency
from the exchange is dominated by network round-trip to the venue, which this system does not control and
does not claim to optimize. Closing real arbitrage
on that data requires colocated and MEV infrastructure that is explicitly out of scope (see below).

## Scope

Done deliberately narrow and deep: **two to three venues, reconstructed correctly**, with the
architecture and transport port making a fourth venue an adapter rather than a rewrite. The scope
favours depth over breadth on purpose.

**In scope:** live ingestion from 2–3 venues; L2 order-book reconstruction with gap detection and
snapshot resync; canonical book/trade model; low-latency fan-out with backpressure; a live dashboard;
metrics (per-venue lag, gap and reconnect counts, internal p99); deterministic and chaos tests
proving the failure model.

**Out of scope, and why:**

- **Trading and execution.** This is the data layer, not a bot. It places no orders.
- **Arbitrage capture, colocation, MEV.** Cross-venue opportunities may be shown to demonstrate the
  pipeline end to end; actually capturing them requires colocated and MEV infrastructure, which is a
  different and capital-intensive problem, deliberately not attempted here.
- **Crash persistence.** State lives in memory. The correctness guarantee covers reconnects, gaps,
  and slow consumers given a live process; it does not cover surviving a process crash.

## Failure model

The system guarantees no reordering or duplication of normalized updates, and no loss for consumers
that keep up, **for as long as the process stays live** — across reconnects and sequence gaps. A gap
triggers a documented resync rather than a guess. The slow consumer is the one exception: it is subject
to backpressure and, past a bound, disconnected, so it loses data rather than stalling the fan-out. The
system does not persist across crashes.

## Tech stack

Go, for its concurrency model and standard-library networking. The core ships as a **reusable
library** — the transport port, the normalizer, and the invariant-checked order-book engine — with a
thin service and a Next.js/TypeScript dashboard on top. Docker and docker-compose run the whole
system with one command; GitHub Actions runs CI. The streaming broker is written against the Go
standard library alone.

## Status

Documentation-first; build pending. The table below is the source of truth.

| Component | Status |
|-----------|--------|
| Canonical book/trade model (integer ticks, no float) | Done — tested |
| Per-venue normalization to the canonical model | Planned (with adapters) |
| Order-book engine (snapshot + delta, gap detect, resync, invariants) | Done — tested |
| Transport port + deterministic synthetic source (seeded, fault-injecting) | Done — tested |
| Recorded source (replay captured feeds) | Planned (with adapters) |
| Live exchange adapters (2–3 venues) | Planned |
| Fan-out broker (`internal/broker`, vendored from arbitrage-engine) | Planned — vendor a copy (proven in arbitrage-engine) |
| Dashboard (Next.js) | Planned |
| Metrics and benchmark harness | Planned |
| Docker, docker-compose, CI | Planned |

## Limitations

This is a market-data infrastructure project, not a trading system and not a profit claim. It runs
single-region against public exchange feeds. It does not place orders, model risk, or attempt to
capture the opportunities it can surface. Its value is one thing done rigorously: a correct,
observable, resilient normalized order-book stream.

## License

MIT — see [LICENSE](LICENSE).
