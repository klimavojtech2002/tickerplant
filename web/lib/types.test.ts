import { describe, expect, it } from "vitest";

import { parseStatus, parseTelemetry, parseView } from "./types";
import { at } from "./testing/at";

type Doc = Record<string, unknown>;

const status = (): Doc => ({
  schema: 1,
  mode: "live",
  chaos: false,
  version: "v1.2.3",
  venue: "kraken",
  symbol: "BTC/USD",
  startedAtUnixMs: 1699999000000,
  depth: 10,
  lastEventAgeMs: 1500,
  delivery: { consumers: 1, delivered: 2, dropped: 3, published: 5 },
  engine: {
    gaps: 1,
    resyncs: 2,
    disconnects: 3,
    wouldCrosses: 4,
    checksumMismatches: 5,
    snapshotRejects: 6,
    snapshotErrors: 7,
    rejected: 8,
  },
  latency: { count: 3, p50Ns: 1986, p99Ns: 50118, p999Ns: 50118, maxNs: 50000 },
});

const okS = (o: unknown) => parseStatus(JSON.stringify(o));

describe("parseStatus", () => {
  it("accepts the full document and ignores extra fields", () => {
    expect(okS(status())).not.toBeNull();
    expect(okS({ ...status(), addedLater: 1 })).not.toBeNull();
  });

  it("accepts the legal edges", () => {
    expect(okS({ ...status(), lastEventAgeMs: -1 })).not.toBeNull(); // no event yet
    expect(okS({ ...status(), startedAtUnixMs: 0, version: "", depth: 0 })).not.toBeNull();
    for (const mode of ["synthetic", "live", "unknown"]) {
      expect(okS({ ...status(), mode })).not.toBeNull();
    }
    expect(okS({ ...status(), mode: "synthetic", chaos: true })?.chaos).toBe(true);
    expect(okS(status())?.chaos).toBe(false);
    const noLatency = status();
    delete noLatency.latency;
    expect(okS(noLatency)).not.toBeNull();
    // p99.9 above max is real: the quantiles are bucket bounds, so they are not ordered.
    const odd = { count: 1, p50Ns: 1, p99Ns: 2, p999Ns: 99999, maxNs: 5 };
    expect(okS({ ...status(), latency: odd })).not.toBeNull();
  });

  const refused: [string, (d: Doc) => void][] = [
    ["schema 2", (d) => (d.schema = 2)],
    ["schema missing", (d) => delete d.schema],
    ["schema as a string", (d) => (d.schema = "1")],
    ["a mode the client does not know", (d) => (d.mode = "batch")],
    ["chaos missing", (d) => delete d.chaos],
    ["chaos as a string", (d) => (d.chaos = "false")],
    ["chaos as a number", (d) => (d.chaos = 0)],
    ["version a number", (d) => (d.version = 1)],
    ["negative start", (d) => (d.startedAtUnixMs = -1)],
    ["fractional depth", (d) => (d.depth = 1.5)],
    ["age below the sentinel", (d) => (d.lastEventAgeMs = -2)],
    ["age beyond 2^53", (d) => (d.lastEventAgeMs = 2 ** 53)],
    ["delivery null", (d) => (d.delivery = null)],
    ["delivery missing a counter", (d) => (d.delivery = { consumers: 1, delivered: 2, dropped: 3 })],
    ["negative counter", (d) => (d.engine = { ...(d.engine as Doc), gaps: -1 })],
    ["counter beyond 2^53", (d) => (d.engine = { ...(d.engine as Doc), gaps: 2 ** 53 })],
    ["engine missing a counter", (d) => (d.engine = { gaps: 0 })],
    ["latency null", (d) => (d.latency = null)],
    ["latency partial", (d) => (d.latency = { count: 1 })],
    [
      "latency as duration strings",
      (d) => (d.latency = { count: 1, p50Ns: "1ns", p99Ns: "1", p999Ns: "1", maxNs: "1" }),
    ],
  ];
  it.each(refused)("refuses %s", (_name, mutate) => {
    const d = status();
    mutate(d);
    expect(okS(d)).toBeNull();
  });

  it.each(["null", "[]", "1", '"x"', "{", ""])("refuses a body that is %j", (body) => {
    expect(parseStatus(body)).toBeNull();
  });

  it("refuses each required field when it is removed", () => {
    for (const key of Object.keys(status())) {
      if (key === "latency") continue; // optional; covered above
      const d = status();
      delete d[key];
      expect(okS(d), `without ${key}`).toBeNull();
    }
  });
});

type Tel = Doc & { events: Doc[] };

const telemetry = (): Tel => ({
  t: 1700000000000,
  seq: 4182,
  gaps: 1,
  resyncs: 2,
  disconnects: 3,
  wouldCrosses: 4,
  checksumMismatches: 5,
  snapshotRejects: 6,
  snapshotErrors: 7,
  rejected: 9,
  consumers: 1,
  events: [
    {
      id: 1,
      kind: "gap",
      atUnixMs: 1699999990000,
      expectedSeq: 4182,
      gotSeq: 4185,
      reason: "sequence gap",
      durationMs: 38,
    },
    {
      id: 2,
      kind: "disconnect",
      atUnixMs: 1699999995000,
      expectedSeq: 0,
      gotSeq: 0,
      reason: "source disconnected",
      durationMs: -1,
    },
  ],
});

const okT = (o: unknown) => parseTelemetry(JSON.stringify(o));

describe("parseTelemetry", () => {
  it("accepts the document, an empty log and a kind it does not know", () => {
    expect(okT(telemetry())).not.toBeNull();
    expect(okT({ ...telemetry(), events: [] })).not.toBeNull();
    const t = telemetry();
    at(t.events, 0).kind = "from-the-future";
    expect(okT(t)).not.toBeNull();
  });

  const refused: [string, (t: Tel) => void][] = [
    ["equal ids", (t) => (at(t.events, 1).id = 1)],
    ["decreasing ids", (t) => (at(t.events, 1).id = 0)],
    ["duration below the sentinel", (t) => (at(t.events, 0).durationMs = -2)],
    ["fractional duration", (t) => (at(t.events, 0).durationMs = 1.5)],
    ["negative event seq", (t) => (at(t.events, 0).gotSeq = -1)],
    ["events null", (t) => (t.events = null as unknown as Doc[])],
    ["events an object", (t) => (t.events = {} as unknown as Doc[])],
    ["t missing", (t) => delete t.t],
    ["seq negative", (t) => (t.seq = -1)],
    ["rejected missing", (t) => delete t.rejected],
    ["consumers missing", (t) => delete t.consumers],
    ["reason a number", (t) => (at(t.events, 0).reason = 1)],
    ["kind a number", (t) => (at(t.events, 0).kind = 1)],
  ];
  it.each(refused)("refuses %s", (_name, mutate) => {
    const t = telemetry();
    mutate(t);
    expect(okT(t)).toBeNull();
  });

  it("refuses a body that is not an object", () => {
    for (const body of ["null", "[]", "{", ""]) {
      expect(parseTelemetry(body)).toBeNull();
    }
  });
});

const view = (): Doc => ({
  venue: "kraken",
  symbol: "BTC/USD",
  seq: 7,
  bids: [
    ["62721.7", "1.35823999"],
    ["62721.6", "0.05000000"],
  ],
  asks: [
    ["62721.8", "0.00114202"],
    ["62721.9", "2.50000000"],
  ],
});

const okV = (o: unknown) => parseView(JSON.stringify(o));

describe("parseView", () => {
  it("accepts a well-formed view, empty sides, and a seq that went down", () => {
    expect(okV(view())).not.toBeNull();
    expect(okV({ ...view(), bids: [] })).not.toBeNull();
    expect(okV({ ...view(), bids: [], asks: [] })).not.toBeNull();
    expect(okV({ ...view(), seq: 0 })).not.toBeNull(); // a resync may restart low
  });

  const refused: [string, (v: Doc) => void][] = [
    [
      "bids ascending",
      (v) =>
        (v.bids = [
          ["62721.6", "1.00000000"],
          ["62721.7", "1.00000000"],
        ]),
    ],
    [
      "asks descending",
      (v) =>
        (v.asks = [
          ["62721.9", "1.00000000"],
          ["62721.8", "1.00000000"],
        ]),
    ],
    [
      "a duplicate bid price",
      (v) =>
        (v.bids = [
          ["62721.7", "1.00000000"],
          ["62721.7", "2.00000000"],
        ]),
    ],
    [
      "a duplicate ask price",
      (v) =>
        (v.asks = [
          ["62721.8", "1.00000000"],
          ["62721.8", "2.00000000"],
        ]),
    ],
    // Order alone would accept these two (62721.70 > 62721.6, 62721.8 < 62721.90), and the
    // other side matches the first price's width, so only the per-side scale check can refuse them.
    [
      "mixed price scale in bids that still sort",
      (v) => {
        v.bids = [
          ["62721.70", "1.00000000"],
          ["62721.6", "1.00000000"],
        ];
        v.asks = [
          ["62721.80", "1.00000000"],
          ["62721.90", "1.00000000"],
        ];
      },
    ],
    [
      "mixed price scale in asks that still sort",
      (v) =>
        (v.asks = [
          ["62721.8", "1.00000000"],
          ["62721.90", "1.00000000"],
        ]),
    ],
    [
      "mixed price scale in bids",
      (v) =>
        (v.bids = [
          ["62721.7", "1.00000000"],
          ["62721.60", "1.00000000"],
        ]),
    ],
    [
      "mixed size scale in bids",
      (v) =>
        (v.bids = [
          ["62721.7", "1.00000000"],
          ["62721.6", "1.0"],
        ]),
    ],
    [
      "mixed size scale in asks",
      (v) =>
        (v.asks = [
          ["62721.8", "1.00000000"],
          ["62721.9", "1.0"],
        ]),
    ],
    ["different price scales per side", (v) => (v.asks = [["62721.80", "1.00000000"]])],
    ["fractional seq", (v) => (v.seq = 1.5)],
    ["negative seq", (v) => (v.seq = -1)],
    ["seq beyond 2^53", (v) => (v.seq = 2 ** 53)],
    ["a non-decimal price", (v) => (v.bids = [["1e5", "1.00000000"]])],
    ["a non-decimal size", (v) => (v.bids = [["62721.7", "1e5"]])],
    ["a negative size", (v) => (v.asks = [["62721.8", "-1.0"]])],
    ["a level of three", (v) => (v.asks = [["62721.8", "1.0", "x"]])],
    ["venue missing", (v) => delete v.venue],
    ["symbol missing", (v) => delete v.symbol],
    ["symbol a number", (v) => (v.symbol = 5)],
  ];
  it.each(refused)("refuses %s", (_name, mutate) => {
    const v = view();
    mutate(v);
    expect(okV(v)).toBeNull();
  });

  it("refuses text that is not a view object", () => {
    for (const body of ["null", "[]", "{", ""]) {
      expect(parseView(body)).toBeNull();
    }
  });
});
