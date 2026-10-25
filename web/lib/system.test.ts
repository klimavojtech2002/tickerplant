import { describe, expect, it } from "vitest";

import type { PollState } from "./status";
import { describeStart, latencyBars, slowest, systemState } from "./system";
import type { Status } from "./types";

const status = (over: Partial<Status> = {}): Status => ({
  schema: 1,
  mode: "synthetic",
  chaos: true,
  version: "v1",
  venue: "synthetic",
  symbol: "DEMO",
  startedAtUnixMs: 1,
  depth: 10,
  lastEventAgeMs: 5,
  delivery: { consumers: 1, delivered: 2, dropped: 3, published: 4 },
  engine: {
    gaps: 0,
    resyncs: 0,
    disconnects: 0,
    wouldCrosses: 0,
    checksumMismatches: 0,
    snapshotRejects: 0,
    snapshotErrors: 0,
    rejected: 0,
  },
  ...over,
});

describe("systemState", () => {
  const s = status();
  const cases: [string, PollState, string][] = [
    ["not answered yet", { kind: "loading", failures: 0 }, "loading"],
    ["failed twice without ever answering", { kind: "loading", failures: 2 }, "loading"],
    ["answering", { kind: "ok", status: s, lastOkAt: 1 }, "ok"],
    ["one or two misses behind, with numbers", { kind: "degraded", status: s, failures: 2, lastOkAt: 1 }, "ok"],
    ["unavailable after having answered", { kind: "unavailable", status: s, failures: 3, lastOkAt: 5 }, "stale"],
    ["unavailable and never answered", { kind: "unavailable", status: null, failures: 3, lastOkAt: null }, "gone"],
  ];
  it.each(cases)("%s", (_name, poll, want) => {
    expect(systemState(poll).kind).toBe(want);
  });

  it("keeps the numbers and the time they were received for a server that went quiet", () => {
    const r = systemState({ kind: "unavailable", status: s, failures: 3, lastOkAt: 5 });
    expect(r).toEqual({ kind: "stale", status: s, lastOkAt: 5 });
  });
});

describe("latencyBars", () => {
  const l = (p50Ns: number, p99Ns: number, p999Ns: number, maxNs: number) => ({
    count: 10,
    p50Ns,
    p99Ns,
    p999Ns,
    maxNs,
  });

  it("sizes each bar as a whole percent of the largest value, so the largest is 100", () => {
    expect(latencyBars(l(500, 5_000, 10_000, 20_000))).toEqual({ p50: 2, p99: 25, p999: 50, max: 100 });
  });

  it("does not let an estimate above the exact maximum push a bar past 100, and the largest is still 100", () => {
    // p99 and p99.9 are bucket midpoints and can read above the true max (the golden has p99 50118 and max 50000)
    const b = latencyBars(l(120, 50_118, 50_118, 50_000));
    expect(b.p99).toBe(100);
    expect(b.p999).toBe(100);
    expect(b.max).toBe(99);
    for (const v of Object.values(b)) {
      expect(v).toBeLessThanOrEqual(100);
    }
  });

  it("gives every bar 0 when nothing was measured, without dividing by zero", () => {
    expect(latencyBars(l(0, 0, 0, 0))).toEqual({ p50: 0, p99: 0, p999: 0, max: 0 });
  });

  it("gives equal values equal bars", () => {
    expect(latencyBars(l(7, 7, 7, 7))).toEqual({ p50: 100, p99: 100, p999: 100, max: 100 });
  });

  it("stays exact for values beyond what a float can tell apart", () => {
    const big = Number.MAX_SAFE_INTEGER;
    expect(latencyBars(l(big - 1, big, big, big))).toEqual({ p50: 99, p99: 100, p999: 100, max: 100 });
  });

  it("names the largest value as the scale", () => {
    expect(slowest(l(1, 9, 5, 7))).toBe(9);
    expect(slowest(l(0, 0, 0, 0))).toBe(0);
  });
});

describe("describeStart", () => {
  const now = new Date(2026, 9, 25, 15, 0, 0);

  it("gives the time of day for a start today", () => {
    expect(describeStart(new Date(2026, 9, 25, 9, 5, 7).getTime(), now)).toBe("09:05:07");
  });

  it("puts the date before the time for any other day", () => {
    expect(describeStart(new Date(2026, 9, 24, 23, 59, 1).getTime(), now)).toBe("24 Oct 23:59:01");
    expect(describeStart(new Date(2025, 9, 25, 9, 5, 7).getTime(), now)).toBe("25 Oct 09:05:07");
  });

  it.each([
    ["no start time", 0],
    ["a negative one", -5],
    ["one in the future", new Date(2026, 9, 25, 15, 0, 1).getTime()],
  ])("says nothing for %s", (_name, ms) => {
    expect(describeStart(ms, now)).toBeNull();
  });

  it("accepts the present moment", () => {
    expect(describeStart(now.getTime(), now)).toBe("15:00:00");
  });
});
