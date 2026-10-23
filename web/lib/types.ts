// Wire types for the HTTP edge (ADR-0017). Prices and sizes arrive as the venue's
// decimal strings and stay strings end to end: parsing them into a JS number (an IEEE
// float64) would corrupt int64-scaled ticks past 2^53 and silently round the rest.
// Every parser below checks the boundary: TypeScript types do not exist at runtime, so
// a body of the wrong shape must come back as null, never reach a render.

import { isPlainDecimal, scaleOf, toScaled } from "./ticks";

/** One complete top-N book view; every SSE `view` event carries a whole one. */
export type ViewEvent = {
  venue: string;
  symbol: string;
  seq: number;
  /** [price, size] pairs as decimal strings, best first. */
  bids: [string, string][];
  asks: [string, string][];
};

export type Mode = "synthetic" | "live" | "unknown";

/** The /api/status document (schema 1). Latency is in whole nanoseconds. */
export type Status = {
  schema: 1;
  mode: Mode;
  version: string;
  venue: string;
  symbol: string;
  /** Unix ms of the pipeline start; 0 when the edge was built without one. */
  startedAtUnixMs: number;
  depth: number;
  /** Ms since the engine last processed an event; -1 before the first one. */
  lastEventAgeMs: number;
  delivery: { consumers: number; delivered: number; dropped: number; published: number };
  engine: {
    gaps: number;
    resyncs: number;
    disconnects: number;
    wouldCrosses: number;
    checksumMismatches: number;
    snapshotRejects: number;
    snapshotErrors: number;
    rejected: number;
  };
  latency?: { count: number; p50Ns: number; p99Ns: number; p999Ns: number; maxNs: number };
};

/** One entry of the engine's fault log, as carried by the telemetry event. */
export type TelemetryEvent = {
  id: number;
  kind: string;
  atUnixMs: number;
  expectedSeq: number;
  gotSeq: number;
  reason: string;
  /** Recovery time in ms; -1 when unknown. */
  durationMs: number;
};

/** The once-a-second `telemetry` event on the stream. */
export type Telemetry = {
  t: number;
  seq: number;
  gaps: number;
  resyncs: number;
  disconnects: number;
  wouldCrosses: number;
  checksumMismatches: number;
  snapshotRejects: number;
  snapshotErrors: number;
  rejected: number;
  consumers: number;
  events: TelemetryEvent[];
};

type Obj = Record<string, unknown>;

function parseJSON(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

function isObj(x: unknown): x is Obj {
  return typeof x === "object" && x !== null && !Array.isArray(x);
}

/** A count: a safe integer, so it is exact, and not negative. */
function isCount(x: unknown): x is number {
  return typeof x === "number" && Number.isSafeInteger(x) && x >= 0;
}

/** Every named key of o is a count. */
function allCounts(o: unknown, keys: readonly string[]): o is Obj {
  return isObj(o) && keys.every((k) => isCount(o[k]));
}

const deliveryKeys = ["consumers", "delivered", "dropped", "published"] as const;
const engineKeys = [
  "gaps",
  "resyncs",
  "disconnects",
  "wouldCrosses",
  "checksumMismatches",
  "snapshotRejects",
  "snapshotErrors",
  "rejected",
] as const;
const latencyKeys = ["count", "p50Ns", "p99Ns", "p999Ns", "maxNs"] as const;

/**
 * parseStatus validates a /api/status body. Unknown extra fields are ignored so the
 * server can add fields without breaking the page; `schema` must be 1, so a change
 * that removes or redefines a field is refused instead of misread. The latency
 * quantiles are not checked against each other: they are histogram bucket bounds, so
 * p99.9 can sit above the exact max. A legacy /metrics body has no `schema` and fails.
 */
export function parseStatus(text: string): Status | null {
  const o = parseJSON(text);
  if (!isObj(o) || o.schema !== 1) {
    return null;
  }
  if (
    (o.mode !== "synthetic" && o.mode !== "live" && o.mode !== "unknown") ||
    typeof o.version !== "string" ||
    typeof o.venue !== "string" ||
    typeof o.symbol !== "string" ||
    !isCount(o.startedAtUnixMs) ||
    !isCount(o.depth) ||
    typeof o.lastEventAgeMs !== "number" ||
    !Number.isSafeInteger(o.lastEventAgeMs) ||
    o.lastEventAgeMs < -1 ||
    !allCounts(o.delivery, deliveryKeys) ||
    !allCounts(o.engine, engineKeys)
  ) {
    return null;
  }
  if ("latency" in o && !allCounts(o.latency, latencyKeys)) {
    return null; // absent is fine; null or a partial block is not
  }
  return o as unknown as Status;
}

/** parseTelemetry validates a `telemetry` event body; event ids must strictly increase. */
export function parseTelemetry(text: string): Telemetry | null {
  const o = parseJSON(text);
  if (
    !allCounts(o, [
      "t",
      "seq",
      "gaps",
      "resyncs",
      "disconnects",
      "wouldCrosses",
      "checksumMismatches",
      "snapshotRejects",
      "snapshotErrors",
      "rejected",
      "consumers",
    ]) ||
    !Array.isArray(o.events)
  ) {
    return null;
  }
  let prevID = -1;
  for (const e of o.events as unknown[]) {
    if (
      !allCounts(e, ["id", "atUnixMs", "expectedSeq", "gotSeq"]) ||
      typeof e.kind !== "string" ||
      typeof e.reason !== "string" ||
      typeof e.durationMs !== "number" ||
      !Number.isSafeInteger(e.durationMs) ||
      e.durationMs < -1 ||
      (e.id as number) <= prevID
    ) {
      return null;
    }
    prevID = e.id as number;
  }
  return o as unknown as Telemetry;
}

/**
 * parseView validates a `view` event body: every price and size must be a plain
 * decimal before BigInt math may see one, and a side must be what the server sends,
 * so a frame that breaks a rule is dropped by the caller, never thrown mid-render.
 * `seq` may go down (a resync), so it is not compared with earlier views.
 */
export function parseView(text: string): ViewEvent | null {
  const v = parseJSON(text);
  if (
    !isObj(v) ||
    typeof v.venue !== "string" ||
    typeof v.symbol !== "string" ||
    !isCount(v.seq) ||
    !isLevels(v.bids) ||
    !isLevels(v.asks)
  ) {
    return null;
  }
  const bids = v.bids;
  const asks = v.asks;
  // Scales first: ordering decimals of different widths is meaningless.
  if (!oneScale(bids, 0) || !oneScale(bids, 1) || !oneScale(asks, 0) || !oneScale(asks, 1)) {
    return null;
  }
  const bestBid = bids[0];
  const bestAsk = asks[0];
  if (bestBid !== undefined && bestAsk !== undefined && scaleOf(bestBid[0]) !== scaleOf(bestAsk[0])) {
    return null;
  }
  if (!strictly(bids, (a, b) => a > b) || !strictly(asks, (a, b) => a < b)) {
    return null;
  }
  return v as unknown as ViewEvent;
}

function isLevels(x: unknown): x is [string, string][] {
  return (
    Array.isArray(x) &&
    x.every(
      (l) =>
        Array.isArray(l) &&
        l.length === 2 &&
        typeof l[0] === "string" &&
        typeof l[1] === "string" &&
        isPlainDecimal(l[0]) &&
        isPlainDecimal(l[1]),
    )
  );
}

/** Every price (col 0) or size (col 1) of a side has the same fraction width. */
function oneScale(levels: [string, string][], col: 0 | 1): boolean {
  const first = levels[0];
  return first === undefined || levels.every((l) => scaleOf(l[col]) === scaleOf(first[col]));
}

/** Each price relates to the one before it by `before`: strict order, so no repeats. */
function strictly(levels: [string, string][], before: (a: bigint, b: bigint) => boolean): boolean {
  let prev: bigint | undefined;
  for (const l of levels) {
    const price = toScaled(l[0]);
    if (prev !== undefined && !before(prev, price)) {
      return false;
    }
    prev = price;
  }
  return true;
}
