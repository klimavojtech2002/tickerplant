# Correctness

The order book is the thesis. The venue publishes a snapshot and a running stream of deltas; the client
holds the live book and applies each delta in turn. A single mistake — a delta applied across a sequence
gap, a mishandled snapshot-vs-stream race — corrupts the book with no error raised: it crosses, or it
drifts from reality. This document states what "correct" means here, how it is enforced, and how it is
proven rather than asserted.

The decisions behind these rules are in [decisions.md](decisions.md); how the pieces fit is in
[ARCHITECTURE.md](ARCHITECTURE.md); the boundaries are in [scope.md](scope.md).

> **Status: the order-book core is built and proven** (model, engine, synthetic source, fan-out — see
> the README status table). The invariants in §4–§6 are enforced and proven by the tests (§10); the
> venue-specific procedures (§7–§8) remain the spec the pending live adapters are held to.

## 1. The canonical model

One vocabulary, used everywhere. Venue terms are translated into it at the adapter edge and never leak
inward.

| Term | Meaning |
|------|---------|
| **Venue** | One exchange feed. |
| **Book** | A venue's L2 order book: two sides, bids and asks, keyed by price. |
| **Level** | One (price, size) pair on a side. L2 aggregates all orders at a price into one size. |
| **Side** | Bids (buy), sorted high to low — best bid is the highest. Asks (sell), sorted low to high — best ask is the lowest. |
| **Snapshot** | A full book at a point in time, carrying the update id it is current as of. |
| **Delta** | An incremental update: the new absolute size at a price level. Size zero deletes the level. |
| **Trade** | An executed fill: price, size, side, time, venue id. Carried alongside the book, not part of it. |
| **Sequence** | The update id(s) that order deltas and let a gap be detected — present where the venue numbers its updates, absent on checksum-only venues like Kraken (§4). |

A delta carries the **absolute** size at a level, not a change to add. A size of zero means remove the
level. Treating a delta as an increment is the most common reconstruction bug, so the model makes the
semantics explicit.

## 2. Price and size are integer-exact, never floating point

Prices and sizes are decimals with venue-defined precision. Binary floating point cannot represent most
decimals exactly, so on a float book two equal prices may not compare equal, a delete may not net to
zero, and rounding error accumulates into silent drift — the failure this project exists to prevent.

The book path uses integers in the venue's smallest increment (ticks for price, lot units for size),
parsed directly from the wire decimal string. Venues format decimals to a fixed width (Binance sends
`60220.02000000` for a two-decimal-tick price), so the parser accepts trailing-zero padding as lossless
but rejects any *significant* digit finer than the scale — over-precision is a loud error, padding is
not. No `float64` touches the book path. Level identity, the zero-size delete, the never-crosses
comparison, and the checksum are all exact integer operations.

The book path stores price and size separately and never multiplies them, so int64 holds the values for
the chosen venues (the per-venue maximum price-in-ticks and size-in-lots are stated in each adapter and
fit in 64 bits). `math/big` is reserved for a derived quantity that genuinely needs it — cumulative
notional (price × size) on the metrics path, not the book. Any rounding is one deliberate, documented
step, never an artifact of representation. (ADR-0003.)

## 3. Reconstruction, and the snapshot-vs-stream race

This race exists for venues that serve the snapshot from a separate source (Binance: REST snapshot + a
WebSocket diff stream). The snapshot is from the past; the delta stream runs continuously; between
requesting the snapshot and binding to the stream, deltas arrive, and binding them without double-applying
or dropping is the race. Single-socket feeds (OKX, Kraken) deliver the snapshot in-band as the first
message, so there is no race — see §7. The separate-source procedure, in the order the steps must happen
(`S` is the snapshot's last update id, `firstID` and `lastID` are a delta's sequence bounds):

1. Open the stream and **buffer** deltas immediately. Do not apply them yet.
2. Fetch the snapshot; note its last update id, `S`.
3. Discard every buffered delta whose `lastID` is at or before `S` — it is already in the snapshot.
4. Apply the remaining buffered deltas, then live ones. The first applied delta must cover `S+1` (its
   range spans `S+1`, i.e. `firstID <= S+1 <= lastID`); each subsequent delta must continue exactly from
   the previous one.
5. If no buffered or live delta covers `S+1` — the earliest available delta starts after `S+1` — the
   snapshot is older than the stream and there is a hole between them. Discard it and refetch a newer
   snapshot, repeating until one binds.

Opening the stream before fetching the snapshot is what closes the race: anything that happened during
the fetch is in the buffer. Fetching first and then subscribing leaves a hole. Step 5 is the case most
implementations forget; without it a too-old snapshot binds onto a gap. (Per-venue specifics in §7.)

## 4. The book stays in sync, and a break triggers resync, never a guess

Two integrity guards detect that the local book has fallen out of sync with the venue. Both converge on
the same response: discard and rebuild.

- **Sequence**, where the venue numbers its updates (Binance, OKX). Each delta continues from the
  previous one. A break means at least one update was lost.
- **Checksum**, where the venue publishes one instead (Kraken). The local book is compared against the
  venue's periodic checksum (§6).

The missing or mismatched state cannot be reconstructed: the lost deltas are unknown, and applying later
deltas on a stale book yields a state that may not cross (so §5's check misses it) yet no longer matches
the venue. On a gap or a checksum mismatch, the book discards its live state and re-bootstraps from a
fresh snapshot (§3), following the venue's documented procedure rather than interpolating. Resync is a
normal, exercised path, not an error branch: a brief correct rebuild is preferred to a book that drifts
silently. (ADR-0004.)

## 5. The book never crosses

The strongest cheap check, and the one guard every venue shares. In any valid book the best bid is
strictly below the best ask: if a buyer bid at or above a seller's ask, the exchange would have matched
them, so the state cannot exist.

> **Safety — the book never crosses.** After every applied delta event, if both sides are non-empty,
> best bid < best ask. A violation is a reconstruction bug; it is surfaced immediately and never served
> downstream.

Two qualifications make this precise. The check is at the boundary of a complete delta *event*, not after
each individual level change: within one event a level can be deleted and a crossing level added in
either order, transiently crossing mid-event, which is not a bug. And when one side is empty (bootstrap,
a thin book) there is no best on that side and the comparison is vacuous. A cross almost always means a
delta was applied out of order or across a gap, so a cross is also a signal that §4's guarantee was
breached.

## 6. Checksums catch the drift the cross-check cannot

A book can be wrong without crossing: a level off by one lot deep in the book leaves best bid below best
ask yet no longer matches the venue. Some venues publish a periodic checksum over the top levels exactly
to catch this. Where one is provided, the book computes the same checksum over its local state after each
update and compares; a mismatch is treated as drift and triggers resync (§4), the same as a gap. The
checksum is computed on the exact integer representation (§2); a float book could not be made to match
the venue's value, because it could not reproduce the venue's exact decimal wire form. Where a venue
provides no checksum, the sequence and never-crosses checks are the available guards — strong, but not a
match for a positive integrity check. (ADR-0007.)

## 7. Per-venue recovery procedures

Correctness is venue-specific. The three venues are chosen because each forces a different mechanism
(ADR-0011). Exact field names, level counts, sequence semantics, and API versions are re-verified against
the live venue documentation when each adapter is implemented (slice 0004); the procedures below are the
model each adapter must satisfy.

**Binance — REST snapshot + WebSocket diff (the race).** Subscribe to the depth diff stream; each event
carries a first and final update id (`U` and `u`). Buffer events, fetch the REST depth snapshot
(`lastUpdateId`), drop buffered events with `u <= lastUpdateId`, and apply from the first event with
`U <= lastUpdateId+1 <= u`; thereafter each event's `U` must continue from the previous event's `u`, or
the procedure restarts from a fresh snapshot. If no buffered event covers `lastUpdateId+1`, refetch the
snapshot (§3 step 5). Sizes are absolute; zero removes a level; a delete for an absent level is normal
and ignored. One known bound: the REST snapshot has a maximum depth, so correctness is claimed to that
depth — Binance publishes no checksum, so a delta touching a level beyond the snapshot window has no
positive guard. This is the canonical snapshot-vs-stream race.

**OKX — in-band sequence over one connection.** The `books` channel delivers a snapshot and then updates
over a single WebSocket (public, no account), so there is no REST race. Each update carries `seqId` and
`prevSeqId`; an update is in order when its `prevSeqId` equals the last applied `seqId`, and a break
triggers resync. OKX also published a CRC32 checksum, but deprecated it on 2026-06-23 (the field is now
fixed at 0), so the sequence is the live integrity guard — a concrete reason the per-venue procedure is
re-verified against the docs at implementation time, not taken from memory. The mechanism the adapter
must get right is in-band sequence continuity, distinct from Binance's two-stream binding.

**Kraken — CRC32 checksum over the top levels.** The book feed sends an in-band snapshot and then
updates over one WebSocket; Kraken's integrity guard is a CRC32 checksum over the top 10 levels in its
documented string format, not a per-update sequence (the adapter stamps a synthetic monotonic sequence
so the engine's continuity check stays inert). After each applied update the engine recomputes the
checksum over its local top 10 — the adapter owns the format, the engine owns the comparison — and a
mismatch means drift and triggers resync. The feed is also windowed: subscribed at depth 10 it never
deletes levels that fall out of the top 10, so the book is truncated to the window at bind and after
every apply.
A level kept beyond the window goes stale out of sight and corrupts the top-10 when it re-enters —
observed on the live feed before truncation landed, caught by the checksum as repeated drift. The
checksum format is pinned against the guide's published golden vector and verified against the live
feed by the integration test (ADR-0016).

## 8. Reconnect is a full re-bootstrap, not a resume

After a WebSocket disconnect, updates were missed during the outage, so the old sequence position is
worthless. Reconnect runs the full bootstrap (§3): start buffering during the reconnect attempt, fetch a
fresh snapshot, rebind. Resuming from the pre-disconnect sequence is the same error as ignoring a gap.

## 9. Delivery for a live process

The reconstruction guarantee is paired with a delivery guarantee. For as long as the process stays live,
normalized updates are fanned out to consumers in order and without duplication, across reconnects and
sequence gaps. A gap is a documented resync, not a dropped update. Slow consumers are the one exception
to no-loss: each consumer holds a size-1 latest slot, so an unread view is superseded by the freshest and
the supersede is counted (latest-wins conflation, ADR-0015); past a sustained bound the consumer is
disconnected rather than allowed to stall the fan-out (ADR-0006). The newest state matters more than a
backlog of stale updates, so for live market data this is the correct trade-off; a consumer that must not
lose data needs a durable queue, which is out of scope ([scope.md](scope.md)).

> **Liveness — a correct update is delivered in bounded time.** A legal update sequence produces, in
> bounded time, a correct book and a delivered normalized update; an illegal sequence produces a resync,
> not a wrong book served as if right.

## 10. Proving it, not asserting it

None of the above is to be trusted because a comment claims it. It is established — the engine and its
harness are built (slices 0002–0003) — by deterministic simulation, by this method (ADR-0005):

- The engine runs against a seeded synthetic source behind the transport port, which emits arbitrary
  **legal and illegal** sequences: gaps, reorders, duplicates, crossing snapshots, an illegal crossing
  *delta* injected on the stream (the truth oracle stays legal), and mid-stream disconnects. The same
  seed reproduces the same run bit-for-bit, so any failure becomes a fixed, replayable test case.
- Tests are property/simulation tests, not happy-path checks: across generated sequences, the book either
  stays correct (§5, §6) or resyncs correctly (§4, §8), asserted continuously over the run against an
  independent truth book, not once at the end. The never-crosses guard (§5) is *driven*, not just
  watched: the simulation injects a crossing delta so the engine must apply, detect, reject, and resync
  it — counted exactly (`WouldCrosses`) so a regression that stopped rejecting crosses fails loudly.
- The race detector (`go test -race`) is always on; the book has one documented writer and lock-free
  reads (ADR-0008), and the absence of data races is verified, not assumed.
- Latency claims are measured with the clock source and span stated, under a defined load harness, and
  reported as a distribution (p50/p99/p99.9/max), not a single trimmed number (ADR-0009).

## What this does not cover

- **Crash persistence.** State lives in memory. The guarantee covers reconnects, gaps, and slow consumers
  given a live process; surviving a process crash is out of scope ([scope.md](scope.md)).
- **Execution.** This is the data layer. It places no orders and models no risk; reconstruction
  correctness is the whole claim.
- **L3 / per-order books.** The model is L2, aggregated by price level. Per-order reconstruction is a
  different feed and out of scope.

## Verified against

Primary sources, re-verified against the live documentation when each adapter is implemented (§7, slice
0004):
- Binance Spot — managing a local order book: <https://developers.binance.com/docs/binance-spot-api-docs/web-socket-streams>
- Kraken — WebSocket v2 `book` channel and CRC32 checksum: <https://docs.kraken.com/api/docs/websocket-v2/book/>
- OKX — WebSocket `books` channel (`seqId`/`prevSeqId`; CRC32 checksum deprecated 2026-06-23): <https://www.okx.com/docs-v5/en/>
- IEEE-754 binary floating point versus decimal representation (§2).
