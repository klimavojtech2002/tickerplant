# Decisions

Architecture decision records. Each is a choice that shapes the system and is not obvious from the code
alone. Format: context, decision, consequences, with the trade-off stated, not hidden.

The core is built and tested (see the README status table). Some of these now record lived experience —
ADR-0013, for instance, was taken after building the engine showed the vendored-broker plan did not fit —
while those for pending slices remain design-time intent, revised against reality as each slice lands.

See [ARCHITECTURE.md](ARCHITECTURE.md) for how these fit together and [correctness.md](correctness.md)
for the invariants they protect.

## ADR-0001 — Ports and adapters around a canonical book/trade model

**Status:** Accepted

**Context.** The system consumes several venues, each with its own WebSocket protocol, message shape,
sequencing scheme, and recovery procedure, and must serve several outputs (a fan-out stream, a
dashboard, metrics). If venue specifics leaked into the reconstruction logic, every new venue would
mean touching the core, and the core could not be tested without a live socket.

**Decision.** Ingestion adapters translate each venue into one canonical model; the domain (normalizer
and order-book engine) depends only on that model and on an abstract transport port, never on sockets;
delivery adapters sit on the output side.

**Consequences.**
- The domain is pure logic over the model and the port, and is directly testable without any I/O.
- Adding a venue is one adapter; the synthetic generator (ADR-0005) and a live socket are
  interchangeable behind the same port.
- Trade-off: a translation layer per venue, and the discipline to keep the model free of any single
  venue's quirks.

## ADR-0002 — Vendor the fan-out broker, do not share `internal/` across modules

**Status:** Superseded by ADR-0013 (the in-process fan-out is purpose-built, not vendored)

**Context.** The fan-out layer is the standard-library SSE broker already built and tested in the
sibling `arbitrage-engine` (`internal/broker`). Reuse is desirable. But Go forbids importing another
module's `internal/` package, so a direct dependency is impossible without restructuring.

**Decision.** Vendor a copy of the broker into `tickerplant/internal/broker`, rather than extract it to
a shared public module.

**Consequences.**
- Each project keeps a single, self-contained module with no cross-repo build coupling; either can
  evolve its copy without coordinating a release of the other.
- The broker arrives already tested; it sets the quality bar for the rest of the code.
- Trade-off: two copies can drift. Accepted: the broker is small, stable, and domain-neutral; a
  shared module would add a release surface that neither project needs yet. Revisit if a third
  consumer appears.

## ADR-0003 — Integer ticks on the book path, never `float64`

**Status:** Accepted

**Context.** Prices and sizes are decimal quantities with venue-defined precision. IEEE-754 binary
floating point cannot represent most decimal fractions exactly, so float arithmetic on the book path
would let levels fail to compare equal, fail to net to zero on a delete, and accumulate rounding error
that drifts the book silently — the exact failure this project exists to prevent.

**Decision.** Represent price and size as integers in the venue's smallest increment (ticks and lot
units), parsed directly from the wire decimal string. No `float64` appears on the book path. Where a
computed quantity could exceed 64 bits, use `math/big`. Any rounding is a single, deliberate, documented
step at one place, never an accident of representation.

**Consequences.**
- Level identity, deletes, and the never-crosses comparison are exact integer operations.
- Checksum computation (ADR-0007) works on the exact wire representation, so a local checksum can match
  the venue's.
- The chosen storage width is `int64` in scaled units; a wire value that would overflow it is rejected
  at parse time (a loud error), not silently wrapped. `math/big` is reserved for later *derived*
  quantities that can exceed 64 bits (cumulative notional, price × size on the metrics path), not for
  the stored price/size on the book path.
- Trade-off: each adapter must carry the venue's tick and lot metadata and parse decimals itself,
  rather than calling `strconv.ParseFloat`. This is the point.

## ADR-0004 — A sequence gap triggers resync, never a guess

**Status:** Accepted

**Context.** Where a venue numbers its updates (Binance, OKX), a missing number means one or more
updates were lost; where a venue publishes a checksum instead (Kraken, ADR-0007), a mismatch is the
equivalent signal. The book cannot be correctly advanced across the loss: the missing deltas are unknown,
and applying later deltas on a stale book produces a state that may not cross (so the cheap invariant
misses it) yet no longer matches the venue.

**Decision.** On a detected gap or checksum mismatch, discard the live book state and re-bootstrap from a
fresh snapshot, following the venue's documented recovery procedure (for a separate-snapshot venue like
Binance, buffer the stream then drop events at or before the snapshot id; single-socket feeds resubscribe
and take a fresh in-band snapshot — correctness.md §7). The book does not advance across the loss; it
discards and rebuilds rather than interpolating.

**Consequences.**
- A gap costs a brief, bounded resync rather than indefinite silent drift.
- Resync is a normal, exercised path, not an error branch; it is covered by simulation tests (ADR-0005),
  not just asserted.
- Trade-off: a venue that gaps frequently spends time resyncing. Acceptable; correctness is the
  product, and the resync rate is a measured metric, not a hidden cost.

## ADR-0005 — A deterministic synthetic source is the primary test driver

**Status:** Accepted

**Context.** The correctness story (gap detection, the snapshot-vs-stream race, resync, reconnect) is
exactly the behaviour that is hardest to trigger and reproduce against a live socket. A test that depends
on a real exchange is slow, flaky, unrepeatable, and silent about the cases that matter.

**Decision.** The order-book engine is built and tested against a deterministic, seeded synthetic
source behind the transport port (ADR-0001). The generator emits arbitrary legal and illegal update
sequences (gaps, reorders, duplicates, crossing snapshots, mid-stream disconnects), and the same seed
reproduces the same run bit-for-bit. Live sockets are edge adapters and an integration concern, never a
dependency of the engine's tests.

**Consequences.**
- Every failure mode is reproducible on demand from a seed; a rare bug becomes a fixed test case.
- The engine's correctness is proven by simulation over generated sequences, not asserted in prose.
- This is deterministic simulation testing applied to one component; it is the same discipline the
  sibling `seedloop` project generalises.
- Trade-off: the generator must itself be correct and faithful to each venue's wire semantics. It is
  small, pure, and reviewed as carefully as the engine.

## ADR-0006 — Bounded, non-blocking fan-out; a slow consumer is dropped, not allowed to stall

**Status:** Accepted

**Context.** Many consumers read the normalized stream at different speeds. An unbounded per-consumer
buffer grows without limit under a spike; a blocking send lets the slowest consumer stall the entire
fan-out and, behind it, the engine.

**Decision.** Each consumer has a bounded channel. The fan-out send is non-blocking: if a consumer's
buffer is full, its event is dropped and the loss is counted; past a sustained bound the consumer is
disconnected. Backpressure is resolved in favour of the healthy majority and the live process.

**Consequences.**
- One slow consumer cannot block the engine or the other consumers.
- Drops are observable (a counter), not silent.
- Trade-off: a slow consumer loses data rather than receiving it late. For live market data a consumer
  needs the current state, not a replay of stale updates, so this is the correct product choice; a
  consumer that must not lose data is out of scope (see [scope.md](scope.md)).

## ADR-0007 — Validate venue checksums where provided, to catch drift the invariant cannot

**Status:** Accepted

**Context.** The never-crosses invariant ([correctness.md](correctness.md) §5) catches a book that
crosses, but a book can be wrong without crossing: a level off by one lot deep in the book leaves best
bid below best ask yet does not match the venue. Some venues publish a periodic checksum over the top
levels precisely to catch this.

**Decision.** Where a venue provides a checksum (Kraken's CRC32 over the top levels), the adapter
computes the same checksum over its local book after applying each update and compares. A mismatch is
treated as drift and triggers resync (ADR-0004), the same as a sequence gap. Where a venue provides no
checksum (Binance, OKX), the sequence and never-crosses checks stand as the available guards.

**Consequences.**
- Silent drift is caught on venues that allow it to be caught, not assumed absent.
- Checksum validation depends on the exact integer representation (ADR-0003); on a float book the local
  checksum could not be made to match the venue's, since the exact decimal wire form is not recoverable.
- Trade-off: a per-update checksum computation over the top levels. Cheap relative to its value, and
  measured.

## ADR-0008 — Single-writer book with lock-free reads via atomic snapshot

**Status:** Accepted

**Context.** One goroutine applies updates to a venue's book; many readers (fan-out, dashboard, metrics)
need a consistent view. A read lock on the hot update path would let a slow reader contend with the
writer and add latency to every update.

**Decision.** Each venue's book has exactly one writer goroutine. Readers obtain a consistent view
through an atomically published immutable view, so reads take no lock on the writer's path. The published
view is a bounded top-N snapshot, so a publish is O(depth), not a full O(n) book copy.

**Consequences.**
- The hot path has one owner and no reader contention; ownership is documented and verified under
  `-race`.
- A reader always sees a coherent book, never a half-applied update.
- Trade-off: publishing a top-N view costs O(depth) per update, and a consumer needing the full deep book
  is not served by this path. Chosen against reader-writer lock contention on every update; if deeper
  views are required, a persistent (structural-sharing) tree is the alternative, measured and decided in
  slice 0006. The top-N view ships in slice 0003; the cost of either is benchmarked, not assumed.

## ADR-0009 — Report internal processing latency only, measured, not end-to-end

**Status:** Accepted

**Context.** "Latency" is easy to inflate by quoting a number the system does not control. End-to-end
latency from an exchange is dominated by network round-trip to the venue, which this system neither
controls nor claims to optimise.

**Decision.** The system measures and reports only internal processing latency — from a raw message
arriving at an adapter to the normalized update leaving the fan-out — as a distribution
(p50/p99/p99.9/max), with the clock source and the measured span stated, under a defined load harness on
documented hardware. The benchmark is open-loop and coordinated-omission-aware, so the tail is not hidden
by a single trimmed percentile.

**Consequences.**
- The reported number is honest, reproducible, and defensible — it measures what the code does.
- Network and exchange latency are named as out of scope, not buried.
- Trade-off: the headline number is smaller than an end-to-end figure would suggest. Correct; an
  inflated figure is a lie, and the project's entire premise is that claims are verified.

## ADR-0010 — Standard library first; every dependency justified by an ADR

**Status:** Accepted

**Context.** Go's standard library covers networking, concurrency, JSON, and testing well enough for
this system. Each third-party dependency adds supply-chain surface, version drift, and something to
defend in review.

**Decision.** Build on the standard library. A third-party dependency is added only when it earns its
place, and only with an ADR stating what it gives that the standard library does not.

**Consequences.**
- The reconstruction core, the broker, and the tests have no third-party runtime dependency.
- The dependency list is short and every entry is explained.
- Trade-off: more code written by hand (e.g. a WebSocket client, or a vetted minimal one justified
  here when slice 0004 lands). Acceptable, and the point of a portfolio that demonstrates the
  fundamentals rather than gluing libraries.

## ADR-0011 — Three venues, chosen for distinct recovery mechanisms

**Status:** Accepted

**Context.** The thesis is reconstruction correctness, and correctness is venue-specific: each exchange
defines its own snapshot, sequencing, and recovery. Choosing venues by popularity would risk three
near-identical procedures and prove little. The scope is deliberately two to three venues, deep, not
five shallow ([scope.md](scope.md)).

**Decision.** Target Binance, Kraken, and OKX — picked because each forces a different recovery
procedure: Binance the REST-snapshot-vs-WebSocket-diff race; Kraken a CRC32 checksum over the top 10
levels (no per-update sequence); OKX an in-band sequence over a single connection (`seqId`/`prevSeqId`).
All three are public, no-account feeds. Binance and Kraken land first; OKX is added as an incremental
adapter. Coinbase was dropped: its only no-account feed carries neither sequence nor checksum (strictly
weaker), and its sequenced feed needs an account.

**Consequences.**
- The three together exercise all three correctness checks (invariant, sequence, and checksum) rather
  than the same one three times.
- The transport port (ADR-0001) makes a fourth venue an adapter, not a rewrite.
- Trade-off: three documented procedures to track and re-verify against live venue docs at
  implementation time. Accepted; that tracking is the senior skill being demonstrated — borne out when
  OKX deprecated its book checksum on 2026-06-23, leaving it sequence-only, caught by re-verification.

## ADR-0012 — A pull-based transport port, not a channel

**Status:** Accepted

**Context.** The engine reads source events and, on bootstrap or resync, requests a snapshot. The thesis
is deterministic simulation: a run must reproduce bit-for-bit from a seed. A channel-based port adds a
producer goroutine whose interleaving with the consumer is scheduler-dependent — nondeterminism exactly
where the proof needs none. Live venue feeds, by contrast, are push (a socket).

**Decision.** The port is pull-based: `Next(ctx) (Event, bool)` plus `Snapshot(ctx)`. The synthetic
source steps a seeded generator single-threaded, so a run is reproducible. Live adapters (slice 0004)
satisfy the same port by buffering their push socket behind `Next`.

**Consequences.**
- The engine's event loop is single-threaded and deterministic under test; no scheduler entropy can
  reach the correctness proof.
- The push→pull buffering lives in the live adapter, at the edge where nondeterminism belongs.
- Trade-off: a live adapter does a little more work (an internal buffer) than consuming a channel
  directly. Accepted; determinism of the core is worth more than a few lines at the edge.

## ADR-0013 — An in-process fan-out, not a vendored SSE broker (supersedes ADR-0002)

**Status:** Accepted

**Context.** ADR-0002 planned to vendor the standard-library SSE broker from arbitrage-engine as the
fan-out. Building the engine made the misfit clear: that broker pools *outbound* SSE-URL connections and
parses an SSE wire, but tickerplant's source of truth is the local engine, not a remote URL — the pooling
and HTTP plumbing have no in-process use. Adapting it would mean adding an upstream seam it lacks and
fixing latent defects (channels not closed on Close, a subscribe/teardown race, no slow-consumer
disconnect): more code and risk for no benefit. Meanwhile the engine already publishes self-contained,
immutable top-N views (ADR-0008), so the consumer fan-out is a simple bounded, non-blocking broadcast.

**Decision.** Build a small purpose-fit in-process fan-out (`internal/delivery.Hub`): bounded per-consumer
channels, non-blocking broadcast, drops counted, a persistently-slow consumer disconnected (ADR-0006). Do
not vendor the SSE broker; arbitrage-engine remains lineage (the same backpressure discipline), not code.

**Consequences.**
- The fan-out is small, single-purpose, and fully tested, with no SSE-URL machinery in-process delivery
  never uses.
- Drop-on-full is correct because each view is a complete snapshot, not a delta — a dropped view is
  superseded by the next.
- Trade-off: the "shared broker with arbitrage-engine" narrative is dropped. The two share a discipline,
  not a package. An HTTP/SSE delivery edge for the dashboard is a thin adapter over the Hub, added later.

## Verified against

- Go language specification — `internal` package import rule (ADR-0002).
- IEEE-754 binary floating point and decimal representation (ADR-0003).
- Each venue's published order-book maintenance procedure, re-verified against the live documentation at
  the time each adapter is implemented (ADR-0011, slice 0004): Binance Spot WebSocket streams
  (<https://developers.binance.com/docs/binance-spot-api-docs/web-socket-streams>), Kraken WebSocket v2
  `book` (<https://docs.kraken.com/api/docs/websocket-v2/book/>), and OKX WebSocket `books`
  (<https://www.okx.com/docs-v5/en/>). See also [correctness.md](correctness.md) "Verified against".
