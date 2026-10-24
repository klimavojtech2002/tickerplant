import { describe, expect, it } from "vitest";

import { baseCurrency, formatClock, modeChip, spreadTicks } from "./hero";
import type { PollState } from "./status";
import type { Status } from "./types";

const status = (over: Partial<Status>): Status => ({
  schema: 1,
  mode: "live",
  chaos: false,
  version: "v",
  venue: "kraken",
  symbol: "BTC/USD",
  startedAtUnixMs: 1,
  depth: 10,
  lastEventAgeMs: 0,
  delivery: { consumers: 1, delivered: 1, dropped: 0, published: 1 },
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

describe("modeChip", () => {
  const cases: [string, PollState, string][] = [
    ["not answered yet", { kind: "loading", failures: 0 }, "unknown"],
    ["failed without ever answering", { kind: "loading", failures: 2 }, "unknown"],
    ["unavailable and never answered", { kind: "unavailable", status: null, failures: 3, lastOkAt: null }, "unknown"],
    ["live", { kind: "ok", status: status({ mode: "live" }), lastOkAt: 1 }, "none"],
    ["synthetic", { kind: "ok", status: status({ mode: "synthetic" }), lastOkAt: 1 }, "demo"],
    [
      "synthetic with faults injected",
      { kind: "ok", status: status({ mode: "synthetic", chaos: true }), lastOkAt: 1 },
      "demo-chaos",
    ],
    ["a mode the page does not know", { kind: "ok", status: status({ mode: "unknown" }), lastOkAt: 1 }, "unknown"],
    [
      "synthetic while a poll is failing keeps the last good answer",
      { kind: "degraded", status: status({ mode: "synthetic" }), failures: 1, lastOkAt: 1 },
      "demo",
    ],
    [
      "unavailable after a good answer keeps it",
      { kind: "unavailable", status: status({ mode: "synthetic", chaos: true }), failures: 3, lastOkAt: 1 },
      "demo-chaos",
    ],
  ];
  it.each(cases)("%s", (_name, poll, want) => {
    expect(modeChip(poll)).toBe(want);
  });
});

describe("baseCurrency", () => {
  it.each([
    ["BTC/USD", "BTC"],
    ["ETH/EUR", "ETH"],
    ["DEMO", null],
    ["/USD", null],
    ["", null],
  ])("%s", (symbol, want) => {
    expect(baseCurrency(symbol)).toBe(want);
  });
});

describe("spreadTicks", () => {
  it.each([
    ["62721.7", "62721.8", "1"],
    ["100.00", "100.25", "25"],
    ["5", "9", "4"],
    ["1.0", "1.0", "0"],
    [undefined, "1.0", null],
    ["1.0", undefined, null],
    ["x", "1.0", null],
    ["1.5", "20", null],
    ["100.5", "101", null],
  ])("%s %s -> %s", (bid, ask, want) => {
    expect(spreadTicks(bid, ask)).toBe(want);
  });
});

describe("formatClock", () => {
  it("pads hours, minutes and seconds", () => {
    expect(formatClock(0, { getHours: () => 7, getMinutes: () => 5, getSeconds: () => 9 })).toBe("07:05:09");
    expect(formatClock(0, { getHours: () => 23, getMinutes: () => 59, getSeconds: () => 59 })).toBe("23:59:59");
  });
  it("reads a real time in the local zone when none is given", () => {
    const d = new Date(2026, 9, 24, 12, 4, 11);
    expect(formatClock(d.getTime())).toBe("12:04:11");
  });
});
