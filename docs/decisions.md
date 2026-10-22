# Decisions

Architecture decision records. Each is a choice that shapes the system and is not obvious from the code
alone. Format: context, decision, consequences, with the trade-off stated, not hidden.

The core is built and tested (see the README status table). Several of these are refined by later ones;
the Status lines say which.

See [ARCHITECTURE.md](ARCHITECTURE.md) for how these fit together and [correctness.md](correctness.md)
for the invariants they protect.

## ADR-0001 — Ports and adapters around a canonical book/trade model

**Status:** Accepted

**Context.** The system consumes several venues, each with its own WebSocket protocol, message shape,
sequencing scheme, and recovery procedure, and must serve several outputs (a fan-out stream, a
dashboard, metrics). If venue specifics leaked into the reconstruction logic, every new venue would
mean touching the core, and the core could not be tested without a live socket.

**Decision.** Ingestion adapters translate each venue into one canonical model; the domain (the
order-book engine) depends only on that model and on an abstract transport port, never on sockets;
delivery adapters sit on the output side.

**Consequences.**
- The domain is pure logic over the model and the port, and is directly testable without any I/O.
- Adding a venue is one adapter; the synthetic generator (ADR-0005) and a live socket are
  interchangeable behind the same port.
- Trade-off: a translation layer per venue, and the discipline to keep the model free of any single
  venue's quirks.

## ADR-0002 — Vendor the fan-out broker, do not share `internal/` across modules

**Status:** Superseded by ADR-0013 (the in-process fan-out is purpose-built, not vendored)

**Context.** The fan-out layer is the standard-library SSE broker that was built and tested in a
separate project, `arbitrage-engine` (`internal/broker`). Reuse is desirable. But Go forbids importing
another module's `internal/` package, so a direct dependency is impossible without restructuring.

**Decision.** Vendor a copy of the broker into `tickerplant/internal/broker`, rather than extract it to
a shared public module.

**Consequences.**
- Each project keeps a single, self-contained module with no cross-repo build coupling; either can
  evolve its copy without coordinating a release of the other.
- The broker was already tested; it set the quality bar for the rest of the code.
- Trade-off: two copies can drift. Accepted: the broker is small, stable, and domain-neutral; a
  shared module would add a release surface that neither project needs yet. A third
  consumer would reopen the question.

## ADR-0003 — Scaled integers on the book path, never `float64`

**Status:** Accepted

**Context.** Prices and sizes are decimal quantities with venue-defined precision. IEEE-754 binary
floating point cannot represent most decimal fractions exactly, so float arithmetic on the book path
would let levels fail to compare equal, fail to net to zero on a delete, and accumulate rounding error
that drifts the book silently — the exact failure this project exists to prevent.

**Decision.** Represent price and size as integers scaled to the venue's decimal precision for the
symbol (a price scale and a size scale), parsed directly from the wire decimal string. No `float64`
appears on the book path. Nothing is rounded: a value with more significant fractional digits than the
scale is rejected, and so is a number with no digits before the point (".5"; "0.5" is accepted).

**Consequences.**
- Level identity, deletes, and the never-crosses comparison are exact integer operations.
- Checksum computation (ADR-0007) works on the scaled integers, whose digits equal the wire digits
  because nothing is rounded, so a local checksum can match the venue's.
- The chosen storage width is `int64` in scaled units; a wire value that would overflow it is rejected
  at parse time (a loud error), not silently wrapped. `math/big` is not used: no stored or
  computed quantity in the code can exceed 64 bits.
- Trade-off: each adapter must carry the symbol's price and size decimal scales and parse decimals
  itself, rather than calling `strconv.ParseFloat`.

## ADR-0004 — A sequence gap triggers resync, never a guess

**Status:** Accepted

**Context.** Where a venue numbers its updates (Binance), a missing number means one or more
updates were lost; where a venue publishes a checksum instead (Kraken, ADR-0007), a mismatch is the
equivalent signal. The book cannot be correctly advanced across the loss: the missing deltas are unknown,
and applying later deltas on a stale book produces a state that may not cross (so the cheap invariant
misses it) yet no longer matches the venue.

**Decision.** On a detected gap, would-cross delta, checksum mismatch or disconnect, discard the live
book state and re-bootstrap from a fresh snapshot, following the venue's documented recovery procedure
(for a separate-snapshot venue like Binance, buffer the stream then drop events at or before the
snapshot id; single-socket feeds reconnect and take a fresh in-band snapshot — correctness.md §7). The
book does not advance across the loss; it discards and rebuilds rather than interpolating.

**Consequences.**
- A gap costs one resync (a fresh snapshot) rather than indefinite silent drift; repeated failure ends
  in a loud error after a fixed number of attempts.
- Resync is a normal, exercised path, not an error branch; it is covered by simulation tests (ADR-0005),
  not just asserted.
- Trade-off: a venue that gaps frequently spends time resyncing. Acceptable; correctness is the
  product, and the resync rate is a measured metric, not a hidden cost.

## ADR-0005 — A deterministic synthetic source is the primary test driver

**Status:** Accepted

**Context.** The correctness story (gap detection, the snapshot-vs-stream race, resync, reconnect) is
exactly the behaviour that is hardest to trigger and reproduce against a live socket. A test that depends
on a real exchange is slow, flaky, unrepeatable, and silent about the cases that matter.

**Decision.** The order-book engine is built and tested against a deterministic, seeded synthetic source
behind the transport port (ADR-0001). The generator emits scheduled legal and illegal update sequences
(gaps, reorders, duplicates, crossing snapshots, mid-stream disconnects), and the same seed reproduces
the same event sequence (the `Received` timestamp aside). Live sockets are edge adapters and an
integration concern, never a dependency of the engine's tests.

**Consequences.**
- Every failure mode is reproducible on demand from a seed; a rare bug becomes a fixed test case.
- The engine's correctness is checked by simulation over generated sequences, not asserted in prose.
- This is deterministic simulation testing applied to one component.
- Trade-off: the generator must itself be correct and faithful to each venue's wire semantics. It is
  small and pure, cross-checked against an independent map-based truth book and pinned by golden-hash
  tests.

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
bid below best ask yet does not match the venue. Some venues publish a checksum over the top
levels with their updates precisely to catch this.

**Decision.** Where a venue provides a checksum (Kraken's CRC32 over the top 10 levels), the venue
adapter supplies the checksum *function* (the venue's exact format) and the engine computes it over its
reconstructed top 10 levels after each applied update and on bootstrap, comparing against the venue's
published value. A mismatch is treated as drift and triggers resync (ADR-0004), the same as a sequence
gap. The split keeps the engine venue-neutral — it owns when and what to check, the adapter owns the
format. Where a venue provides no checksum (Binance), the sequence and never-crosses checks stand as
the available guards.

**Consequences.**
- Silent drift is caught on venues that allow it to be caught, not assumed absent.
- Checksum validation depends on the exact integer representation (ADR-0003); on a float book the local
  checksum could not be made to match the venue's, since the exact decimal wire form is not recoverable.
- Trade-off: a per-update checksum computation over the top levels. Cheap relative to its value.

## ADR-0008 — Single-writer book with lock-free reads via atomic snapshot

**Status:** Accepted

**Context.** One goroutine applies updates to a venue's book; readers (the run loop that feeds the
fan-out, and the HTTP edge's opening event) need a consistent view. A read lock on the hot update path
would let a slow reader contend with the writer and add latency to every update.

**Decision.** Each venue's book has exactly one writer goroutine. Readers obtain a consistent view
through an atomically published immutable view, so reads take no lock on the writer's path. The published
view is a bounded top-N snapshot, so a publish is O(depth), not a full O(n) book copy.

**Consequences.**
- The hot path has one owner and no reader contention; ownership is documented and verified under
  `-race`.
- A reader always sees a coherent book, never a half-applied update.
- Trade-off: publishing a top-N view costs O(depth) per update, and a consumer needing the full deep
  book is not served by this path. Chosen against reader-writer lock contention on every update; the
  shipped design is the top-N view.

## ADR-0009 — Report internal processing latency only, measured, not end-to-end

**Status:** Accepted

**Context.** "Latency" is easy to inflate by quoting a number the system does not control. End-to-end
latency from an exchange is dominated by network round-trip to the venue, which this system neither
controls nor claims to optimise.

**Decision.** The system measures and reports only internal processing latency — from the adapter taking
a raw frame off its frame channel (decode included) to the view the engine has stored for readers — as a
distribution (p50/p99/p99.9/max), with the clock source and the measured span stated, under a defined
load harness. The benchmark harness additionally measures through the fan-out's `Publish`. The benchmark
is open-loop and coordinated-omission-aware.

**Consequences.**
- What is reported is defined by its span and clock; a figure is meaningful only with its hardware, Go
  version and interval.
- Network and exchange latency are named as out of scope, not buried.
- Trade-off: an internal figure is smaller than an end-to-end figure would be. An inflated figure
  would be a false claim, and the project's premise is that claims are verified.

## ADR-0010 — Standard library first; every dependency justified by an ADR

**Status:** Accepted

**Context.** Go's standard library covers networking, concurrency, JSON, and testing well enough for
this system. Each third-party dependency adds supply-chain surface, version drift, and something to
defend in review.

**Decision.** Build on the standard library. A third-party dependency is added only when it earns its
place, and only with an ADR stating what it gives that the standard library does not.

**Consequences.**
- The reconstruction core (market, book, source, delivery) and its tests import only the standard
  library.
- The Go module has one third-party dependency, explained in ADR-0014.
- Trade-off: more code is written by hand; the WebSocket client is the one thing taken as a dependency
  (ADR-0014).

## ADR-0011 — Three venues targeted, chosen for distinct recovery mechanisms

**Status:** Accepted; amended 2026-10-21 (two venues built, OKX not built)

**Context.** The thesis is reconstruction correctness, and correctness is venue-specific: each exchange
defines its own snapshot, sequencing, and recovery. Choosing venues by popularity would risk three
near-identical procedures and prove little. The scope is deliberately two to three venues (amended:
two), deep, not five shallow ([scope.md](scope.md)).

**Decision.** Target Binance, Kraken, and OKX — picked because each forces a different recovery
procedure: Binance the REST-snapshot-vs-WebSocket-diff race; Kraken a CRC32 checksum over the top 10
levels (no per-update sequence); OKX an in-band sequence over a single connection (`seqId`/`prevSeqId`).
All three are public, no-account feeds. Binance and Kraken land first; OKX would have been an
incremental adapter (amended below: not built). Coinbase was dropped: its only no-account feed carries
neither sequence nor checksum (strictly weaker), and its sequenced feed needs an account.

**Consequences.**
- The venues together exercise all three correctness checks (invariant, sequence, and checksum) rather
  than the same one three times; the two built venues already cover them (see the amendment).
- The transport port (ADR-0001) makes a fourth venue an adapter, not a rewrite.
- Trade-off: three documented procedures to track and re-verify against live venue docs at
  implementation time, since a venue's own recovery procedure can change: OKX deprecated its book
  checksum on 2026-06-23, leaving it sequence-only, caught by re-verification before an adapter
  was written.

**Amendment (2026-10-21).** Binance and Kraken are built and verified against the live venues. OKX was
not built and is out of scope: the two built venues already exercise the invariant, sequence and
checksum guards between them. OKX's in-band sequence is a different procedure
([correctness.md](correctness.md) §7) but was not built, to keep the scope at two venues. The OKX
procedure stays as analysis; the remark above about OKX deprecating its checksum records that analysis,
not a built adapter. The decision above is kept as written.

## ADR-0012 — A pull-based transport port, not a channel

**Status:** Accepted

**Context.** The engine reads source events and, on bootstrap or resync, requests a snapshot. The thesis
is deterministic simulation: a run must reproduce the same event sequence from a seed. A channel-based
port adds a producer goroutine whose interleaving with the consumer is scheduler-dependent —
nondeterminism exactly where the simulation needs none. Live venue feeds, by contrast, are push (a
socket).

**Decision.** The port is pull-based: `Next(ctx) (Event, bool)` plus `Snapshot(ctx)`. The synthetic
source steps a seeded generator single-threaded, so a run is reproducible. Live adapters
satisfy the same port by buffering their push socket behind `Next`.

**Consequences.**
- The engine's event loop is single-threaded and deterministic under test; no scheduler entropy can
  reach the correctness tests.
- The push→pull buffering lives in the live adapter, at the edge where nondeterminism belongs.
- Trade-off: a live adapter does a little more work (an internal buffer) than consuming a channel
  directly. Accepted; determinism of the core is worth more than a few lines at the edge.

## ADR-0013 — An in-process fan-out, not a vendored SSE broker (supersedes ADR-0002)

**Status:** Accepted (delivery mechanism refined by ADR-0015 — a per-consumer latest-value slot replaces
the bounded FIFO channel; the "not a vendored broker" decision stands)

**Context.** ADR-0002 proposed vendoring the standard-library SSE broker from arbitrage-engine as the
fan-out. Building the engine made the misfit clear: that broker pools *outbound* SSE-URL connections and
parses an SSE wire, but tickerplant's source of truth is the local engine, not a remote URL — the
pooling and HTTP plumbing have no in-process use. Adapting it would mean adding an upstream seam,
channels closed on Close, a safe subscribe/teardown and a slow-consumer disconnect: more code and risk
for no benefit. Meanwhile the engine already stores self-contained, immutable top-N views (ADR-0008), so
the consumer fan-out is a simple bounded, non-blocking broadcast.

**Decision.** Build a small purpose-fit in-process fan-out (`internal/delivery.Hub`): per-consumer
delivery (refined to a latest-value slot by ADR-0015), non-blocking broadcast, drops counted, a
persistently-slow consumer disconnected (ADR-0006). Do not vendor the SSE broker; arbitrage-engine
remains lineage (the same backpressure discipline), not code.

**Consequences.**
- The fan-out is small, single-purpose, and fully tested, with no SSE-URL machinery in-process delivery
  never uses.
- Drop-on-full is correct because each view is a complete snapshot, not a delta — a dropped view is
  superseded by the next.
- Trade-off: the "shared broker with arbitrage-engine" narrative is dropped. The two share a discipline,
  not a package. An HTTP/SSE delivery edge for the dashboard is a thin adapter over the Hub, added by
  ADR-0017.
- Refined by ADR-0015: the delivery uses a latest-value slot in place of the bounded FIFO per-consumer
  channel, which makes the "superseded by the next" property above the actual delivery
  semantic (the newest view wins, not the oldest-in-buffer). The bound and the disconnect-then-resync are
  unchanged.

## ADR-0014 — A vetted WebSocket client (`coder/websocket`), the one third-party dependency

**Status:** Accepted

**Context.** The live venue adapters need a WebSocket client. The Go standard library has none
(`net/http` does HTTP and the server side of the upgrade, not a client), so the stdlib-first stance
(ADR-0010) needs a deliberate exception here. The options: a vetted third-party library, or hand-rolling
RFC 6455 (framing, masking, fragmentation, the close handshake, ping/pong). Hand-rolling is a few
hundred lines of fiddly protocol code to write, test, and defend — disproportionate to a project whose
thesis is order-book reconstruction, not transport framing, and riskier than a widely-used library.

**Decision.** Take a single dependency: `github.com/coder/websocket` (pinned). It was chosen over
`gorilla/websocket` because: it has **zero transitive dependencies** (the whole module graph stays
auditable, the cleanest possible deviation from stdlib-first); its API is `context.Context`-native,
which composes directly with the engine's already ctx-threaded `Next`/`Snapshot` instead of manual read
deadlines; concurrent writes are safe (gorilla panics on them, a known footgun); and it is actively
maintained. The dependency is isolated behind a thin `internal/venue.Stream`: the adapters consume a
`<-chan []byte` of frames, so the library is swappable without touching adapter or engine code.

**Consequences.**
- One module enters `go.mod`, with no transitive deps; everything else stays standard-library-only.
- The reconnect, backoff, and liveness policy is the project's (`internal/venue`), not the library's, so
  the recovery behaviour is ours to test and defend. A dead-but-open connection is caught by a read
  deadline (a silent socket past the deadline is dropped and reconnected), with a dial timeout bounding
  the handshake; an explicit ping/pong is not used, because venues send frequent updates or server
  heartbeats.
- Connections to live venues are exercised only by the opt-in integration tests, never by the
  deterministic suite (ADR-0005). The reconnect, backoff and liveness logic is unit-tested against an
  injected fake connection, and the real dial against a local in-process server.
- Trade-off: a dependency to track for security and version updates. Accepted: it is small, zero-dep, vetted, and
  isolated; the alternative (hand-rolled RFC 6455) is more code and more risk for no real gain.
- Subscribe writes have a write timeout, and the REST calls the adapters make have a client timeout and
  a body bound (ADR-0021).

## ADR-0015 — Latest-wins conflation in the fan-out (refines ADR-0006)

**Status:** Accepted

**Context.** ADR-0006 chose a bounded, non-blocking fan-out that drops under backpressure. ADR-0013 gave
each consumer a bounded FIFO channel, so a full buffer dropped the *newest* view and the
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
- The payload lives in the slot and the doorbell is a pure wake-and-recheck, so coalesced wakeups are
  safe and no wakeup is lost. Every statement is covered by tests (100% statement coverage of
  `internal/delivery`).
- `Delivered` counts views published into an empty slot and `Dropped` counts *superseded* views, so
  `Delivered + Dropped` equals the publishes each counted consumer saw, summed over consumers.
  `Hub.TrySubscribe(limit)` refuses a consumer past a limit (`ErrFull`, or `ErrClosed` on a closed hub)
  under the same lock that adds it. `Hub.SubscribeInternal` gives the process its own reader (the log
  consumer): it receives views and can be disconnected for lagging, but it is in no count and uses up no
  slot. `Stats.Published` counts every `Publish` call.
- The consumer API is a handle (`Ready()` / `Take()`) rather than a bare receive channel.
- Trade-off: intermediate views are not retained. For a complete-snapshot feed that is the point; a
  consumer needing every delta would use a different, delta-level stream (out of scope).

## ADR-0016 — Adapting a windowed, sequence-less feed (Kraken)

**Status:** Accepted

**Context.** Kraken's v2 book channel differs from Binance on three axes at once: there is no
per-update sequence (the CRC32 checksum over the top 10 levels is the only integrity signal, ADR-0007);
the snapshot arrives in-band as the first message of a subscription, not from a REST endpoint; and the
feed is windowed — subscribed at depth 10, it never sends deletes for levels that fall outside the top
10. The last point is a documented client obligation ("truncate your book to the subscribed depth")
and it is load-bearing: a level kept beyond the window can die venue-side unseen, re-enter the local
top-10 as a stale ghost, and make the checksum drift.

The engine's continuity check also needs some sequence, and bolting a "no sequence" mode onto the thesis
component would fork its logic for one venue.

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
- Drift recovery costs a reconnect (TCP+TLS+WS). Checksum drift was not seen in the 200-update
  live test, and a single recovery path wins on simplicity.
- The live integration test asserts zero mismatches over a live stretch, so a format or window
  regression fails against the real venue, not just fixtures.

## ADR-0017 — The HTTP edge: SSE streaming complete views

**Status:** Accepted

**Context.** The fan-out is in-process (ADR-0013) and conflates to the latest complete top-N view
(ADR-0015); nothing exposed the pipeline outside the process. The dashboard (and anything else)
needs a network stream and the system's health counters.

**Decision.** A thin `internal/httpapi` handler with five endpoints and one middleware chain.
- `GET /stream` — Server-Sent Events: `view` events, each one complete view, and a `telemetry` event
  (below). SSE over WebSocket: the flow is strictly one-way, EventSource reconnects natively, and no
  client->server protocol exists to need a socket. Complete views over deltas: ADR-0015 already made
  every published view whole, so a client is a pure renderer — no reducer, no join protocol, and
  reconnect is just "the first event is the engine's current view" (with a dedupe on the
  opener/subscription seam, by the generation the engine raises on every publish, not by the venue's
  sequence number: a view rebuilt after a resync can carry a lower or equal sequence and must still be
  shown). No `Last-Event-ID`: replaying
  superseded views would undo the conflation the delivery layer exists to provide.
- `GET /metrics` — a JSON snapshot of the delivery stats (HTTP viewers only: the in-process log reader
  is not counted), the engine counters, and the internal
  latency histogram (p50/p99/p99.9/max as duration strings): one sample per applied delta, spanning
  the adapter's raw-message dequeue to the applied view being stored for readers (correctness of the
  span is the engine's job — WithLatencyObserver — so no wire-wait can enter a sample).
- `GET /api/status` — one JSON document for clients that should not parse strings, beside `/metrics`,
  which is unchanged. Fields, in order: `schema` (1; it changes only when a field is removed or changes
  meaning), `mode` (`synthetic`, `live`, or `unknown` for an edge built without one), `version`,
  `venue`, `symbol`, `startedAtUnixMs` (0 when unset), `depth`, `lastEventAgeMs`, `delivery` (consumers,
  delivered, dropped and published), `engine` (the counters and the source's count of frames it could
  not use) and `latency` (`count` and the quantiles as whole nanoseconds, left out when no histogram is
  attached; with a count of 0 the quantiles are 0). `lastEventAgeMs` is the time since the engine last
  processed an event, in whole milliseconds: -1 before the first one, never negative otherwise. A number
  with a documented marker is smaller for a client than a key that is sometimes missing, and the fault
  log uses the same convention for an unknown duration. The age is reported, not used to make `/readyz`
  false. The engine stamps events with its own clock and the edge reads its own, both the wall clock in
  the process, so injecting different clocks would skew the age. Uptime is the client's to derive: the
  telemetry timestamp minus `startedAtUnixMs`. The document is built from separate reads, so each field
  is exact but it is not one atomic snapshot. The exact bytes of one document are pinned by
  `internal/httpapi/testdata/status.json`, which the dashboard's tests are to read. The route is polled,
  so it is logged at debug level like the probes.
- `GET /healthz` — 200 `ok` whenever the process serves requests. `GET /readyz` — 200 `ok` when the
  book is usable and 503 `not ready` when it is not. Usable means the engine has a view and is not
  rebuilding, so `/readyz` is 503 from the start until the first view, and during a resync, snapshot
  retry delays included. A feed that has really failed ends the process (ADR-0021), so there is no
  failed-but-running state to report, and a quiet market does not make `/readyz` false: how old the last
  event is belongs on a status page, not in a probe that would restart a healthy service. Both answers
  are marked `no-store`. The listener opens after the source has connected, so `/healthz` refuses
  connections during a slow live start; a container check needs a start period for that.

Every response goes through one chain of three layers, outermost first. The access log writes one
line per response with the method, the matched route (`unmatched` for a 404, so a scanner cannot put
text of its choosing in the log), the status, the bytes and the duration; the health routes log at
debug level, and a stream logs once, when it ends. The recovery layer turns a handler panic into a
logged 500, or, if the response has already started and a 500 can no longer be sent, aborts the
connection so a cut-off body cannot look complete. The header layer sets `X-Content-Type-Options:
nosniff` and the cross-origin policy on every response, error responses included. The wrapper around
the response writer passes flushes and write deadlines through to the connection, which the stream
needs.

The edge serves at most `-max-streams` stream consumers at once (default 256, 1 to 10000). The handler
asks the hub for a slot before it writes the 200; when the hub is full the answer is `503` with
`Retry-After: 5`, and when the hub is closed (the process is stopping) it is `503` without the hint. Both
go through the chain, so they carry the cross-origin headers a browser needs to read them; a refused
request starts no ticker and sends no event. The in-process log reader is not counted. The flag requires
a value from 1 up, while the setting inside the edge treats 0 as "use the default"; the flag always
passes an explicit number.

The cross-origin policy is `-cors-origin`: `*` (the default, for the demo) lets any origin read the
edge; one origin such as `https://app.example` lets only that origin, with `Vary: Origin`. The origin
must be written the way a browser sends it (lowercase, no path, no default port), because the value is
compared byte for byte and a mismatch would otherwise fail silently; one validator checks it for the flag
and for the handler. The stream also sets `X-Accel-Buffering: no` so a buffering proxy does not hold
events back. The `-healthcheck` flag lets the container ask the edge itself: it sends `GET /healthz` to
127.0.0.1 at the `-http` port and exits 0 or 1, since the runtime image has no shell or curl. It takes
the same flags as the service it checks and validates them all, so a container passes it the command
line it runs with.

The stream also carries a `telemetry` event: one frame as soon as a client connects, before the opener
view and even when the engine has no view yet, then one every second. It holds the edge clock's time, the
current sequence, the engine counters, the number of frames the source could not use, the number of stream
consumers (the log reader is not one) and the engine's fault log, whose entries have an id, a kind, a
time, the sequence expected and the one received (0 when not applicable), a fixed reason and the
recovery time in milliseconds (-1 while unknown). `EventSource` does not deliver comment lines to a
page, so a named event is the only way for a client to tell a quiet market from a dead connection; it
is also the keepalive. The cadence is fixed, not a flag, because the dashboard is designed to treat
three missed beats as lost contact. A frame is built from separate reads, so each field is exact but
the frame is not one atomic snapshot. The exact bytes of a frame are pinned by
`internal/httpapi/testdata/telemetry.json`, which the dashboard's tests are to read.

Prices and sizes cross the wire as the venue's decimal strings, rendered server-side from the
scaled integers at the venue scales. int64 values do not fit JS's float64 past 2^53, and a string is
un-mis-parseable by accident — the no-float discipline extends across the wire.

The layer adds no second backpressure mechanism. A slow SSE client lags its Hub consumer, the Hub
conflates and eventually disconnects it (ADR-0006/0015), and the handler's only own guard is a
per-event write deadline so a dead connection cannot wedge a goroutine. On exit the hub is closed
first, which ends every stream handler, and then the edge is stopped with `Shutdown` under a 5 s bound;
if that expires, `Close` drops what is left.

**Consequences.**
- The client stays thin; the server stays the single source of truth.
- Every event re-sends the whole top-N (a few hundred bytes at depth 10). Conflation bounds the rate to
  what each client sustains, so the bandwidth cost is the price of the no-reducer client.
- A quiet feed sends only telemetry after the opener, once a second; EventSource keeps the connection
  open, and an idle proxy does not time the stream out. If a connection does drop, EventSource reconnects
  and starts from the current view.
- The cap counts stream consumers, not sockets. A peer that vanishes without closing keeps its slot until
  its request context ends or a write blocks (the write deadline fires only when the kernel buffer is
  full, which at one frame a second can take a long time), and a consumer the hub disconnects for lagging frees
  its slot at once while its handler may sit in a blocked write for up to the write timeout, so the open
  sockets can briefly exceed the cap. The cap protects the process; it does not stop one client from
  filling it, and limits per address belong to the proxy. A flood of refused requests costs one cheap
  response and one access-log line each.
- A browser's `EventSource` treats any answer other than a 200 event stream as a failed connection and does
  not retry by itself, and it cannot tell the two 503s apart. The dashboard has no backoff yet, so a
  refused user sees "disconnected" until the client side adds one; `Retry-After` is a hint for other
  clients and for people.
- `HEAD /stream` is routed like `GET /stream` (a `GET` pattern also matches `HEAD`) and stays open until
  the client leaves; nothing uses it.
- The counters travel as JSON numbers (small ints); only prices/sizes and durations need the
  string treatment.
- Plain JSON rather than the Prometheus exposition format: one process, one scraper (the
  dashboard), no aggregation layer — adopting the format without its ecosystem adds
  nothing. The quantiles are cumulative since process start, which fits a demo's "has it ever
  drifted" question; windowed quantiles belong to an aggregation layer, which this system does not have.

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
  Library + jsdom for tests — the test dev-dependencies, justified as the standard minimal
  React test rig. System fonts only: a Google Fonts fetch would make the build depend on the
  network. Dark-only console aesthetic, deliberately: this is a market-data terminal, not a
  content site; side identity lives in the bid/ask bars — a green/red pair whose composited pixel
  (the color at the bar's own fill opacity over the surface, not the bare swatch) is chosen to hold
  >=3:1 contrast on the surface and to stay separable under simulated color-vision deficiency — never
  in text, and the sides are named by their headers, so no information is color-alone.

**Consequences.**
- The client cannot disagree with the backend about numbers — it never re-derives them.
- Exactness is testable in plain functions: the suite pins digit-preservation past 2^53, the
  float-classic 0.4 − 0.1 = 0.3 spread, integer bar shares, and the crossed-frame refusal.
- No charting library: the book is two tables with proportional bars and the health panel is stat
  tiles, which HTML does natively.
- The page shows one instrument, because the process serves one. Multi-instrument is not built;
  the natural seam would be keying the store by the event's venue/symbol identity, which every view
  already carries.

## ADR-0019 — Distroless/nonroot for the service, a static export for the dashboard

**Status:** Accepted

**Context.** The system needs to run with one command on a reviewer's machine, with no account or
paid feed — `docker compose up`. Two images: the Go service (an HTTP edge plus the demo's log
lines on stderr) and the Next.js dashboard.

**Decision.**
- **Go service: `gcr.io/distroless/static-debian12:nonroot`.** The binary is `CGO_ENABLED=0`, so it
  needs nothing from the OS but CA certificates (for live `wss://`) — which the `nonroot` distroless
  variant already ships, along with a non-root user (uid 65532), with no shell and no package
  manager in the final image. A multi-stage build (a digest-pinned `golang` alpine builder → distroless final)
  keeps the toolchain out of what ships.
- **Dashboard: a static export, not a Next.js server.** The page is entirely client-side (one `"use
  client"` component, no API routes, no server actions, ADR-0018) — `next.config.ts`'s `output:
  "export"` produces plain HTML/CSS/JS, served by `nginx:1.27-alpine`. No Node runtime in the final
  image, and no server-side attack surface for something that does no server-side work.
  Unlike the Go service, this image's master process still starts as root (nginx's own standard
  pattern: bind port 80, then the worker processes — the ones that actually parse and serve every
  request — drop to the unprivileged `nginx` user). Left as upstream ships it rather than
  reconfiguring the listen port and writable paths to force full rootless mode: nginx's
  root-master/unprivileged-worker split is its standard pattern for exactly this exposure (a static file
  server, no exec, no user input reaching a shell), and hand-patching a stock config adds fragility for
  little extra hardening. Stated as a trade-off.
- **`NEXT_PUBLIC_EDGE_URL` stays at its code default for the compose demo.** A static export inlines
  environment variables at build time, not request time, so the edge URL cannot be picked up from
  the container's runtime environment the way a server-rendered app could. The dashboard's existing
  fallback (`app/page.tsx`) already points at `http://127.0.0.1:8080`, which is exactly the edge's
  host-mapped port in `docker-compose.yml` — the common case needs no override, because the
  EventSource connection runs in the reviewer's browser, not inside the container network.
- **Live mode is a command override, not a new flag.** `cmd/tickerplant` already reads `-live
  -venue -symbol` from argv; docker-compose.yml documents overriding the service's command
  (`docker compose run --rm --service-ports edge -live ...`) rather than inventing a
  compose-specific environment-variable indirection for something argv already does.

**Consequences.**
- Two build contexts (`.` for the service, `./web` for the dashboard), each with its own
  `.dockerignore` to keep the context small — the real boundary on what reaches the service image is
  its Dockerfile's explicit `COPY` list, not the ignore file; the dashboard's additionally
  excludes `node_modules`/`.next` so the build installs cleanly rather than copying a host's
  platform-specific artifacts.
- A multi-instrument or server-rendered dashboard would force revisiting the static-export choice;
  out of scope while the page serves one instrument (ADR-0018).
- Image sizes, measured: the Go service is **15.9 MB** (distroless static + a stripped, trimmed
  binary); the dashboard is **74.7 MB**, dominated by the `nginx:1.27-alpine` base rather than the
  handful of static files it serves.

## ADR-0020 — The process stops on SIGINT and SIGTERM and refuses bad flags

**Status:** Accepted

**Context.** In the container the binary runs as PID 1, and a PID 1 process with no handler ignores
SIGTERM, so `docker stop` waits for the kill timeout. Command-line values were unchecked: a negative
`-depth` would reach the book, and `-depth 99999` would build views far larger than any client renders.

**Decision.**
- `SIGINT` and `SIGTERM` cancel the run's context. After the first signal the handler is released, so a
  second one kills the process.
- Shutdown order is fixed: the source ends or the context is cancelled, the hub is closed (which ends every
  SSE handler), the edge is stopped with `Shutdown` under a 5 s bound and `Close` if that expires, then
  the source is closed. An interrupt during source setup is a clean exit, not a setup failure.
- The pace wait is a timer raced against the context, not `time.Sleep`.
- Flags are checked before anything starts: `-depth` 1 to 1000, `-steps`, `-every` and `-pace` not negative,
  `-venue` (with `-live`) one of binance, kraken, `-log-level` one of debug, info, warn, error, `-log-format` text or json. A bad value prints the flag, the
  value and the allowed range, and exits 2, like a parse error. Values are refused, never clamped.
  The 1000 ceiling is far above the 10 levels a dashboard shows and keeps one stream frame small.
- `-version` prints the version injected at link time (`ARG VERSION` in the Dockerfile), or what the Go
  toolchain recorded about the build, or `dev`.

**Consequences.**
- `docker stop` ends the run as soon as the pipeline unwinds instead of after the kill timeout.
- A signal test sends a real SIGTERM to a real process on Unix; on Windows, where a console signal cannot be
  sent to a child, only the signal list is checked.
- A slow client cannot hold shutdown past 5 s: `Close` drops it.

## ADR-0021 — Snapshot failures: bound the call, retry what is transient, then stop loudly

**Status:** Accepted

**Context.** A snapshot can fail without the book being wrong: a venue rate-limits (429), a connection
drops, a reply never comes. Before this decision the REST calls used `http.DefaultClient`, which never
times out, decoded replies without a size bound, and the first failed `Snapshot` ended the process.
Kraken's in-band snapshot wait had no deadline at all, so a subscription that never sent a book blocked
startup for ever.

**Decision.**
- Every REST call uses a client with a 15 s timeout (`venue.NewHTTPClient`) and reads at most 4 MiB (1
  MiB for the one-symbol info replies). A reply over the bound is refused, not truncated.
- A failure is marked transient (`source.Transient`) when it is a transport failure, a failure reading
  the body, a body over the bound, a 429 or a 5xx. Any other status (a 400 for a bad symbol) and
  anything the caller's own cancellation caused is permanent. Decode errors and malformed snapshots are
  permanent.
- `Bootstrap` retries a transient `Snapshot` failure up to 5 times, waiting 0.5 s doubling to 8 s with
  full jitter between tries, and then returns the last error. The wait ends when the context is
  cancelled. Retries do not use up the 8 bind attempts, which count snapshots that arrived but were
  rejected.
- Kraken's wait for its in-band snapshot has one 15 s timer (heartbeats do not restart it). On expiry
  the adapter marks the connection as owing no snapshot, so the retry redials. Subscribe writes on the
  WebSocket have a 10 s write timeout.
- The scale fetch at startup runs before the engine exists and is not retried in the process; a transient
  failure there ends the start and only the compose restart recovers it.
- `Retry-After` is not read: the capped, jittered delay already spaces the retries.
- After the retries are used up the process exits non-zero. Under compose `restart: on-failure:5` runs
  it a few more times; the cap also stops a bad-flag exit from looping.

**Consequences.**
- A stalled venue delays the engine by at most about 6 attempts of 15 s plus the delays (roughly 105 s),
  then the process stops. While it waits, the old view stays published and the frame channel is not
  drained, so the venue may drop the socket; the transport reconnects and the engine resyncs.
- Each failed `Snapshot` call is counted by the engine and shown as `snapshot-errors` in the final log line;
  shutdown is not counted.
- A wrong symbol fails on the first call instead of after the retries.

## ADR-0022 — A short log of faults in the engine, and a count of frames the adapters cannot use

**Status:** Accepted

**Context.** The engine counted gaps, resyncs and the rest, but two faults inside one second only moved
counters by two: nothing said what sequence it expected, what arrived, or how long the recovery took.
The adapters skipped a frame that failed to decode without a trace, so a changed venue format looked
like a quiet market.

**Decision.**
- The engine keeps the last 16 faults as events: gap, disconnect, would-cross delta, delta checksum
  drift, refused snapshot and failed `Snapshot` call. Each has a number that rises by one and is never
  reset, a time from the engine's clock, the sequence it expected and the one it got (0 when that does
  not apply), a fixed reason and a duration.
- The list is replaced as a whole and published through an atomic pointer, the same way as the book
  view, so readers need no lock and a list they hold never changes.
- A fault found in a delta or a disconnect is written when it is found, with the duration unknown (-1).
  When the resync that follows returns, success or not, the entry is replaced with the time the whole
  bootstrap took, looked up by number. One bootstrap can write more events than the list holds, so the
  fault may already be gone; then nothing is changed. A consumer keeps the newest copy of a number.
  Writing it only at the end would hide a fault whose recovery fails or hangs.
- Reasons are fixed phrases. Raw error text, which can hold addresses and venue text, is never put in
  an event, because the list is meant to be shown to anyone watching the feed.
- Each adapter counts the frames that were meant to be book data and could not be used (`Rejected`):
  frames that do not decode, a Kraken book frame of an unknown type or without data, and frames whose
  levels do not parse. Acks, heartbeats and status frames are not counted. Waiting for a Kraken
  snapshot still skips a malformed update without counting it, because the snapshot replaces it.
  A sample of the reason is logged at most once a second.
- The engine also reports the time of the last source event it processed. The clock is injectable.

**Consequences.**
- A faults list costs one small allocation per fault and nothing on the path of a normal delta; the
  extra work per event is one clock read and two atomic stores, about 18 ns.
- A feed that delivers only unusable frames moves the reject count but not the last-event time, so a
  status built from the time shows it as stale.
- On Kraken, which has no sequence numbers, the sequence in a checksum event is the adapter's own count.

## Verified against

- Go language specification — `internal` package import rule (ADR-0002).
- IEEE-754 binary floating point and decimal representation (ADR-0003).
- Each venue's published order-book maintenance procedure, re-verified against the live documentation at
  the time each adapter was written (ADR-0011): Binance Spot WebSocket streams
  (<https://developers.binance.com/docs/binance-spot-api-docs/web-socket-streams>), Kraken WebSocket v2
  `book` (<https://docs.kraken.com/api/docs/websocket-v2/book/>), and OKX WebSocket `books`
  (analysis only, <https://www.okx.com/docs-v5/en/>). See also [correctness.md](correctness.md)
  "Verified against".
