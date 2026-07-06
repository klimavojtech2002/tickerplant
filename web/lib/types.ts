// Wire types for the HTTP edge (ADR-0017). Prices and sizes arrive as the venue's
// decimal strings and stay strings end to end: parsing them into a JS number (an IEEE
// float64) would corrupt int64-scaled ticks past 2^53 and silently round the rest.
// The sequence is a plain JSON number — venue sequence ids sit far below 2^53.

import { isPlainDecimal } from "./ticks";

/** One complete top-N book view; every SSE `view` event carries a whole one. */
export type ViewEvent = {
  venue: string;
  symbol: string;
  seq: number;
  /** [price, size] pairs as decimal strings, best first. */
  bids: [string, string][];
  asks: [string, string][];
};

/** The /metrics document. Latency quantiles are duration strings ("1.2ms"). */
export type Metrics = {
  venue: string;
  symbol: string;
  delivery: { consumers: number; delivered: number; dropped: number };
  engine: {
    gaps: number;
    resyncs: number;
    disconnects: number;
    wouldCrosses: number;
    checksumMismatches: number;
  };
  latency?: { count: number; p50: string; p99: string; p99_9: string; max: string };
};

/** parseView validates the boundary: TypeScript types do not exist at runtime, and
 * every price and size must be a plain decimal before BigInt math may see one — a
 * malformed frame is dropped by the caller, never thrown mid-render. */
export function parseView(data: string): ViewEvent | null {
  let raw: unknown;
  try {
    raw = JSON.parse(data);
  } catch {
    return null;
  }
  const v = raw as ViewEvent;
  if (
    typeof v !== "object" ||
    v === null ||
    typeof v.venue !== "string" ||
    typeof v.symbol !== "string" ||
    typeof v.seq !== "number" ||
    !isLevels(v.bids) ||
    !isLevels(v.asks)
  ) {
    return null;
  }
  return v;
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
