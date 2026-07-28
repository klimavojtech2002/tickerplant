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

**Status:** Accepted (the drop-not-block policy stands; the specific channel-buffer mechanism below was
refined to a latest-value slot by ADR-0015)

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

**Decision.** Where a venue provides a checksum (Kraken's CRC32 over the top 10 levels), the venue
adapter supplies the checksum *function* (the venue's exact format) and the engine computes it over its
reconstructed top 10 levels after each applied update and on bootstrap, comparing against the venue's
published value. A mismatch is treated as drift and triggers resync (ADR-0004), the same as a sequence
gap. The split keeps the engine venue-neutral — it owns when and what to check, the adapter owns the
format. Where a venue provides no checksum (Binance, OKX), the sequence and never-crosses checks stand as
the available guards.

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

**Status:** Accepted (delivery mechanism refined by ADR-0015 — a per-consumer latest-value slot replaces
the bounded FIFO channel; the "not a vendored broker" decision stands)

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
- Refined by ADR-0015: the as-built delivery replaced the bounded FIFO per-consumer channel with a
  latest-value slot, which makes the "superseded by the next" property above the actual delivery
  semantic (the newest view wins, not the oldest-in-buffer). The bound and the disconnect-then-resync are
  unchanged.

## ADR-0014 — A vetted WebSocket client (`coder/websocket`), the one third-party dependency

**Status:** Accepted

**Context.** The live venue adapters (slice 0004) need a WebSocket client. The Go standard library has
none (`net/http` does HTTP and the server side of the upgrade, not a client), so the stdlib-first stance
(ADR-0010) needs a deliberate exception here. The options: a vetted third-party library, or hand-rolling
RFC 6455 (framing, masking, fragmentation, the close handshake, ping/pong). Hand-rolling is a few hundred
lines of fiddly protocol code to write, test, and defend — disproportionate to a project whose thesis is
order-book reconstruction, not transport framing, and riskier than a widely-used library.

**Decision.** Take a single dependency: `github.com/coder/websocket` (pinned). It was chosen over
`gorilla/websocket` because: it has **zero transitive dependencies** (the whole module graph stays
auditable, the cleanest possible deviation from stdlib-first); its API is `context.Context`-native, which
composes directly with the engine's already ctx-threaded `Next`/`Snapshot` instead of manual read
deadlines; concurrent writes are safe (gorilla panics on them, a known footgun); and it is actively
maintained (gorilla was archived, then revived but is openly seeking maintainers). The dependency is
isolated behind a thin `internal/venue.Stream`: the adapters consume a `<-chan []byte` of frames, so the
library is swappable without touching adapter or engine code.

**Consequences.**
- One module enters `go.mod`, with no transitive deps; everything else stays standard-library-only.
- The reconnect, backoff, and liveness policy is the project's (`internal/venue`), not the library's, so
  the recovery behaviour is ours to test and defend. A dead-but-open connection is caught by a read
  deadline (a silent socket past the deadline is dropped and reconnected), with a dial timeout bounding
  the handshake; an explicit ping/pong is a possible later refinement, not needed while venues send
  frequent updates or server heartbeats.
- The WebSocket path is integration-tested, never part of the deterministic suite (ADR-0005); its logic
  is unit-tested against a local in-process server.
- Trade-off: a dependency to track for security and version updates. Accepted: it is small, zero-dep, vetted, and
  isolated; the alternative (hand-rolled RFC 6455) is more code and more risk for no real gain.

## ADR-0015 — Latest-wins conflation in the fan-out (refines ADR-0006)

**Status:** Accepted

**Context.** ADR-0006 chose a bounded, non-blocking fan-out that drops under backpressure. As first built
(ADR-0013), each consumer had a bounded FIFO channel, so a full buffer dropped the *newest* view and the
consumer drained *older* ones. But every published view is a complete top-N book, so a lagging consumer
that receives stale views while the freshest is discarded is getting the opposite of what it needs — and
the code's own comment already claimed "latest-wins", which the FIFO buffer did not deliver.

**Decision.** Give each consumer a size-1 latest-value slot instead of a FIFO buffer: a mutex-guarded
`latest *View` plus a size-1 doorbell channel. `Publish` stores the newest view (so the freshest always
wins) and rings the doorbell without blocking; the consumer waits on the doorbell and `Take`s the latest.
A consumer that has not taken its previous view has it superseded; past `maxLag` consecutive supersedes it
is disconnected and must resync (ADR-0006, unchanged). The `buffer` size knob is removed — a conflating
slot is size-1 by definition.

**Consequences.**
- A lagging consumer always jumps to the freshest book; a stale view is never delivered after a newer one.
- `Publish` stays O(consumers) and non-blocking, so the engine is never stalled.
- The delivery pattern is the idiomatic Go "latest value": the payload lives in the slot, the doorbell is
  a pure wake-and-recheck, so coalesced wakeups are safe and no wakeup is lost. Every branch is
  deterministically reachable — no defensive dead arm, unlike a channel-of-views drain whose empty-slot
  path is reachable only under a race.
- `Delivered` counts views published into an empty slot and `Dropped` counts *superseded* views, so
  `Delivered + Dropped` is the number of publishes and `Dropped` the number of lag events; the consumer
  API is a handle (`Ready()` / `Take()`) rather than a bare receive channel.
- Trade-off: intermediate views are not retained. For a complete-snapshot feed that is the point; a
  consumer needing every delta would use a different, delta-level stream (out of scope).

## ADR-0016 — Adapting a windowed, sequence-less feed (Kraken)

**Status:** Accepted

**Context.** Kraken's v2 book channel differs from Binance on three axes at once: there is no
per-update sequence (the CRC32 checksum over the top 10 levels is the only integrity signal, ADR-0007);
the snapshot arrives in-band as the first message of a subscription, not from a REST endpoint; and the
feed is windowed — subscribed at depth 10, it never sends deletes for levels that fall outside the top
10. The last point is a documented client obligation ("truncate your book to the subscribed depth")
and it is load-bearing: in the first live run the engine kept levels beyond the window, one of them
died venue-side unseen, re-entered the local top-10 as a stale ghost, and the checksum drifted 4 times
in 200 updates. The engine's continuity check also needs some sequence, and bolting a "no sequence"
mode onto the thesis component would fork its logic for one venue.

**Decision.** Three small mechanisms, each at the layer that owns the concern:

- The adapter stamps a **synthetic monotonic sequence** (the snapshot and each emitted update take the
  next id), so the engine's gap logic stays inert and every real integrity decision flows through the
  checksum. Gaps can then only come from the adapter's own bookkeeping.
- The engine gains **`WithMaxDepth`**: after every bind and apply it truncates the book to the feed's
  subscribed window, so out-of-window ghosts cannot exist. Zero (the default) keeps every level —
  correct for full-book feeds (Binance, synthetic).
- A **drift resync redials**: a checksum mismatch needs a fresh in-band snapshot, which only a new
  subscription serves. An in-socket unsubscribe/resubscribe would also be a new subscription, but it
  adds a second recovery path with its own ack states and write plumbing for a rare event; tearing the
  transport down and re-dialing reuses the one path that already exists — reconnect = re-bootstrap
  (correctness.md §8) — for disconnect and drift alike. A snapshot arriving
  unrequested (the transport reconnected) is cached and surfaced as a disconnect so the engine rebinds.

**Consequences.**

- The engine stays one code path; the venue quirk lives in one option and one adapter.
- `kraken.Checksum` needs no per-symbol scales: stripping the decimal point and leading zeros reduces
  each field to the decimal digits of the scaled integer, so the function is scale-free and a
  swapped-scales mistake is unrepresentable. Scales still matter where they belong — parsing the wire
  at the pair's precision (AssetPairs `pair_decimals`/`lot_decimals`).
- Drift recovery costs a reconnect (TCP+TLS+WS). Checksum drift is rare (zero in the 200-update live
  verification once truncation landed), and a single recovery path wins on simplicity.
- The live integration test asserts zero mismatches over a live stretch, so a format or window
  regression fails against the real venue, not just fixtures.

## ADR-0017 — The HTTP edge: SSE streaming complete views

**Status:** Accepted

**Context.** The fan-out is in-process (ADR-0013) and conflates to the latest complete top-N view
(ADR-0015); nothing exposed the pipeline outside the process. The dashboard (and anything else)
needs a network stream and the system's health counters.

**Decision.** A thin `internal/httpapi` handler with two endpoints.
- `GET /stream` — Server-Sent Events, one complete view per event. SSE over WebSocket: the flow is
  strictly one-way, EventSource reconnects natively, and no client->server protocol exists to need
  a socket. Complete views over deltas: ADR-0015 already made every published view whole, so a
  client is a pure renderer — no reducer, no join protocol, and reconnect is just "the first event
  is the engine's current view" (with a sequence dedupe on the opener/subscription seam). No
  `Last-Event-ID`: replaying superseded views would undo the conflation the delivery layer exists
  to provide.
- `GET /metrics` — a JSON snapshot of the delivery stats, the engine counters, and the internal
  latency histogram (p50/p99/p99.9/max as duration strings): one sample per applied delta, spanning
  the adapter's raw-message dequeue to the view leaving for the fan-out (correctness of the span is
  the engine's job — WithLatencyObserver — so no wire-wait can enter a sample).

Prices and sizes cross the wire as the venue's decimal strings, rendered server-side from the
integer ticks at the venue scales. int64 ticks do not fit JS's float64 past 2^53, and a string is
un-mis-parseable by accident — the no-float discipline extends across the wire.

The layer adds no second backpressure mechanism. A slow SSE client lags its Hub consumer, the Hub
conflates and eventually disconnects it (ADR-0006/0015), and the handler's only own guard is a
per-event write deadline so a dead connection cannot wedge a goroutine. Shutdown is `Close`, not
`Shutdown`: open SSE streams never drain on their own, and the hub closing has already ended every
handler loop.

**Consequences.**
- The client stays honest and thin; the server stays the single source of truth.
- Every event re-sends the whole top-N (~1 KB at depth 10). Conflation bounds the rate to what
  each client sustains, so the bandwidth cost is the price of the no-reducer client; revisit only
  with a measured need.
- A quiet feed sends nothing after the opener; EventSource keeps the connection open. Keepalive
  comments are unnecessary for the demo topology (no proxies) and were left out deliberately.
- The counters travel as JSON numbers (small ints); only prices/sizes and durations need the
  string treatment.
- Plain JSON rather than the Prometheus exposition format: one process, one scraper (the
  dashboard), no aggregation layer — adopting the format without its ecosystem would be cargo
  cult. The quantiles are cumulative since process start, which fits a demo's "has it ever
  drifted" question; windowed quantiles are an aggregation concern and arrive with one, if ever.

## ADR-0018 — The dashboard is a renderer, and floats never touch the price path

**Status:** Accepted

**Context.** The HTTP edge (ADR-0017) streams complete top-N views with prices and sizes as the
venue's decimal strings. The dashboard has to display them, derive a spread and bar widths, and
survive disconnects — in a language whose only built-in number is a float64.

**Decision.**
- **Strings end to end, BigInt where math is needed.** Prices and sizes render verbatim. The two
  derived values — spread and bar share — strip the decimal point (all values in a feed share the
  venue's scale) and compute in BigInt; only a final 0–100 integer becomes a JS number, as a CSS
  width. `parseFloat`/`Number` never see a price. int64 ticks exceed 2^53, so this is correctness,
  not style.
- **A renderer, not a reconstruction.** Every event is a whole book, so the client keeps exactly
  one view — the freshest — in a small external store (the ADR-0015 shape again) read through
  `useSyncExternalStore`. There is no reducer to get wrong; missed frames are by design not
  replayed. A crossed frame is refused loudly (dev throws, production drops with an error log):
  the server owns the invariant, the client owns never rendering a violation of it.
- **Reconnect is EventSource's.** Native retry + the edge's current-view opener; the client adds
  only a status badge, which degrades "live" to "stalled" when views stop arriving, and separately
  reports "disconnected" (not "retrying") when the browser's own readyState shows it has given up
  for good, so a silently dead pipe cannot look healthy or look like it will recover on its own.
- **Boundary validation.** TypeScript types do not exist at runtime, so every event is shape-checked
  before it enters the store; malformed frames are dropped with an error log.
- **Minimal, hermetic toolchain.** Next.js + React (the portfolio's stack), vitest + Testing
  Library + jsdom for tests — the one dev-dependency cluster, justified as the standard minimal
  React test rig. System fonts only: a Google Fonts fetch would make the build depend on the
  network. Dark-only console aesthetic, deliberately: this is a market-data terminal, not a
  content site; side identity lives in the bid/ask bars — a green/red pair whose composited pixel
  (the color at the bar's own fill opacity over the surface, not the bare swatch) holds >=3:1
  contrast on the surface and stays separable under simulated color-vision deficiency — never
  in text, and the sides are named by their headers, so no information is color-alone.

**Consequences.**
- The client cannot disagree with the backend about numbers — it never re-derives them.
- Exactness is testable in plain functions: the suite pins digit-preservation past 2^53, the
  float-classic 0.4 − 0.1 = 0.3 spread, integer bar shares, and the crossed-frame refusal.
- No charting library: the book is two tables with proportional bars and the health panel is stat
  tiles, which HTML does natively.
- The page shows one instrument, because the process serves one. Multi-instrument is future work;
  the natural seam is keying the store by the event's venue/symbol identity, which every view
  already carries.

## Verified against

- Go language specification — `internal` package import rule (ADR-0002).
- IEEE-754 binary floating point and decimal representation (ADR-0003).
- Each venue's published order-book maintenance procedure, re-verified against the live documentation at
  the time each adapter is implemented (ADR-0011, slice 0004): Binance Spot WebSocket streams
  (<https://developers.binance.com/docs/binance-spot-api-docs/web-socket-streams>), Kraken WebSocket v2
  `book` (<https://docs.kraken.com/api/docs/websocket-v2/book/>), and OKX WebSocket `books`
  (<https://www.okx.com/docs-v5/en/>). See also [correctness.md](correctness.md) "Verified against".
