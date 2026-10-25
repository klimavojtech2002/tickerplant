import { describe, expect, it } from "vitest";

import { describeFault, faultSummary, isResyncKind, mergeEvents, newEntries, restarted, ringSize } from "./faults";
import type { Telemetry, TelemetryEvent } from "./types";

const ev = (id: number, over: Partial<TelemetryEvent> = {}): TelemetryEvent => ({
  id,
  kind: "gap",
  atUnixMs: 1_000 * id,
  expectedSeq: 10 * id,
  gotSeq: 10 * id + 3,
  reason: "sequence gap",
  durationMs: 5,
  ...over,
});

const ids = (events: readonly TelemetryEvent[]) => events.map((e) => e.id);

describe("isResyncKind", () => {
  it.each([
    ["gap", true],
    ["cross", true],
    ["checksum", true],
    ["disconnect", true],
    ["snapshot-reject", false],
    ["snapshot-error", false],
    ["something-new", false],
  ])("%s", (kind, want) => {
    expect(isResyncKind(kind)).toBe(want);
  });
});

describe("mergeEvents", () => {
  it("starts from nothing: the frame's events, newest first, and nothing lost", () => {
    const r = mergeEvents([], [ev(1), ev(2), ev(3)]);
    expect(ids(r.events)).toEqual([3, 2, 1]);
    expect(r.lost).toBe(0);
  });

  it("keeps what it knew when a frame has none", () => {
    const known = mergeEvents([], [ev(1), ev(2)]).events;
    const r = mergeEvents(known, []);
    expect(ids(r.events)).toEqual([2, 1]);
    expect(r.lost).toBe(0);
  });

  it("does not duplicate the events of an overlapping frame, and loses none", () => {
    const known = mergeEvents([], [ev(1), ev(2), ev(3)]).events;
    const r = mergeEvents(known, [ev(2), ev(3), ev(4)]);
    expect(ids(r.events)).toEqual([4, 3, 2, 1]);
    expect(r.lost).toBe(0);
  });

  it("lets a later copy replace an earlier one, which is how a duration gets filled in", () => {
    const known = mergeEvents([], [ev(1, { durationMs: -1 })]).events;
    const r = mergeEvents(known, [ev(1, { durationMs: 38 })]);
    expect(r.events).toHaveLength(1);
    expect(r.events[0]!.durationMs).toBe(38);
  });

  it("handles a frame whose events are out of order", () => {
    const r = mergeEvents([], [ev(3), ev(1), ev(2)]);
    expect(ids(r.events)).toEqual([3, 2, 1]);
  });

  it("keeps the newest 16 and drops the oldest when more arrive, without counting them as lost", () => {
    const first = Array.from({ length: ringSize }, (_, i) => ev(i + 1));
    const known = mergeEvents([], first).events;
    expect(known).toHaveLength(ringSize);
    const r = mergeEvents(
      known,
      Array.from({ length: ringSize }, (_, i) => ev(i + 6)),
    ); // ids 6..21
    expect(ids(r.events)).toEqual(Array.from({ length: ringSize }, (_, i) => 21 - i));
    expect(r.lost).toBe(0);
  });

  it("counts events that happened between two frames that did not overlap", () => {
    const known = mergeEvents([], [ev(1), ev(2)]).events;
    const r = mergeEvents(known, [ev(6), ev(7)]); // 3, 4 and 5 were never seen
    expect(r.lost).toBe(3);
    expect(ids(r.events)).toEqual([7, 6, 2, 1]);
  });

  it("counts nothing as lost for the first frame, however high its ids", () => {
    expect(mergeEvents([], [ev(500), ev(501)]).lost).toBe(0);
  });

  it("does not change its inputs", () => {
    const known = [ev(2), ev(1)];
    const incoming = [ev(3)];
    const k = JSON.stringify(known);
    const i = JSON.stringify(incoming);
    mergeEvents(known, incoming);
    expect(JSON.stringify(known)).toBe(k);
    expect(JSON.stringify(incoming)).toBe(i);
  });

  it("agrees with a naive model on random frame sequences", () => {
    let seed = 3;
    const next = () => (seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0);
    for (let run = 0; run < 200; run++) {
      let known: TelemetryEvent[] = [];
      let top = 0;
      const all = new Map<number, TelemetryEvent>();
      for (let frame = 0; frame < 12; frame++) {
        top += next() % 4; // some frames add nothing
        const size = Math.min(ringSize, top);
        const ring = Array.from({ length: size }, (_, i) => ev(top - size + 1 + i));
        for (const e of ring) all.set(e.id, e);
        known = mergeEvents(known, ring).events;
        const want = [...all.keys()].sort((a, b) => b - a).slice(0, ringSize);
        expect(ids(known)).toEqual(want);
      }
    }
  });
});

const telemetry = (over: Partial<Telemetry> = {}): Telemetry => ({
  t: 100,
  seq: 1,
  gaps: 2,
  resyncs: 3,
  disconnects: 1,
  wouldCrosses: 0,
  checksumMismatches: 0,
  snapshotRejects: 0,
  snapshotErrors: 0,
  rejected: 0,
  consumers: 1,
  events: [ev(1), ev(2)],
  ...over,
});

describe("restarted", () => {
  it("is false for the first frame and for an ordinary next one", () => {
    expect(restarted(null, 0, telemetry())).toBe(false);
    expect(restarted(telemetry(), 2, telemetry({ t: 101, resyncs: 4, events: [ev(1), ev(2), ev(3)] }))).toBe(false);
  });
  it("is true when the newest event id goes back", () => {
    expect(restarted(telemetry(), 2, telemetry({ t: 101, events: [ev(1)] }))).toBe(true);
  });
  it("is true for an empty ring after events were known", () => {
    expect(restarted(telemetry(), 2, telemetry({ t: 101, events: [], resyncs: 0, gaps: 0, disconnects: 0 }))).toBe(
      true,
    );
  });
  it("is false when only the server's clock goes back, since the wall clock can step without a restart", () => {
    expect(restarted(telemetry(), 2, telemetry({ t: 99 }))).toBe(false);
  });
  it.each([
    "resyncs",
    "gaps",
    "disconnects",
    "wouldCrosses",
    "checksumMismatches",
    "snapshotRejects",
    "snapshotErrors",
    "rejected",
  ] as const)("is true when %s falls although the newest id is the same", (counter) => {
    const before = telemetry({ [counter]: 5 });
    expect(restarted(before, 2, telemetry({ t: 101, [counter]: 4 }))).toBe(true);
  });
  it("is false when a counter stays level", () => {
    expect(restarted(telemetry(), 2, telemetry({ t: 100 }))).toBe(false);
  });
});

describe("faultSummary", () => {
  it("has nothing to say before any event", () => {
    expect(faultSummary(0, [])).toEqual({ repaired: 0, inProgress: false, latest: null });
  });

  it("counts every rebuild as repaired when the newest one has finished", () => {
    const r = faultSummary(3, [ev(3, { durationMs: 38 }), ev(2), ev(1)]);
    expect(r.repaired).toBe(3);
    expect(r.inProgress).toBe(false);
    expect(r.latest!.id).toBe(3);
  });

  it("leaves the rebuild that is running out of the count", () => {
    const r = faultSummary(3, [ev(3, { durationMs: -1 }), ev(2), ev(1)]);
    expect(r.repaired).toBe(2);
    expect(r.inProgress).toBe(true);
  });

  it("finds the running rebuild even when snapshot problems were recorded after it", () => {
    const r = faultSummary(4, [
      ev(6, { kind: "snapshot-reject", durationMs: 0, reason: "snapshot crossed" }),
      ev(5, { kind: "snapshot-error", durationMs: 0, reason: "transient snapshot failure" }),
      ev(4, { durationMs: -1 }),
      ev(3),
    ]);
    expect(r.inProgress).toBe(true);
    expect(r.repaired).toBe(3);
    expect(r.latest!.id).toBe(4);
  });

  it("does not take a snapshot event for a rebuild, whatever its duration", () => {
    const r = faultSummary(0, [ev(1, { kind: "snapshot-error", durationMs: -1, reason: "snapshot failed" })]);
    expect(r).toEqual({ repaired: 0, inProgress: false, latest: null });
  });

  it("never goes below zero when the frame shows the running rebuild before the counter has counted it", () => {
    expect(faultSummary(0, [ev(1, { durationMs: -1 })]).repaired).toBe(0);
  });

  it("says nothing is running when the running event has left the ring", () => {
    const events = [ev(9, { kind: "snapshot-error", durationMs: 0, reason: "snapshot failed" })];
    expect(faultSummary(5, events).inProgress).toBe(false);
  });

  it("uses the newest rebuild event, not an older one without a duration", () => {
    const r = faultSummary(2, [ev(2, { durationMs: 7 }), ev(1, { durationMs: -1 })]);
    expect(r.inProgress).toBe(false);
    expect(r.repaired).toBe(2);
  });
});

describe("newEntries", () => {
  it("is the distance of the newest id from where the page began", () => {
    expect(newEntries(2, [ev(5), ev(4)])).toBe(3);
  });
  it.each([
    ["before the first frame", null, [ev(5)]],
    ["with no events", 3, []],
    ["when nothing is new", 5, [ev(5)]],
    ["when the baseline is ahead", 9, [ev(5)]],
  ])("is 0 %s", (_name, baseline, events) => {
    expect(newEntries(baseline as number | null, events)).toBe(0);
  });
});

describe("describeFault", () => {
  it("words a gap with its sequence numbers and the time it took", () => {
    const t = describeFault(ev(1, { expectedSeq: 4182, gotSeq: 4185, durationMs: 38 }), 1);
    expect(t.title).toBe("Sequence gap");
    expect(t.detail).toBe("Expected 4182, got 4185. Rebuilt from a snapshot in 38 ms.");
    expect(t.rebuild).toBe("rebuilt from a snapshot in 38 ms");
  });

  it("never shows a sequence number of zero", () => {
    const t = describeFault(ev(1, { expectedSeq: 0, gotSeq: 0 }), 1);
    expect(t.detail).not.toMatch(/\b0\b/);
    expect(t.detail).toContain("An update was missed.");
    expect(describeFault(ev(1, { expectedSeq: 5, gotSeq: 0 }), 1).detail).not.toContain("Expected");
  });

  it("says under one millisecond for a duration of zero", () => {
    expect(describeFault(ev(1, { durationMs: 0 }), 1).detail).toContain("in under 1 ms.");
  });

  it("says rebuilding for the newest rebuild without a duration, and not recorded for an older one", () => {
    expect(describeFault(ev(2, { durationMs: -1 }), 2).detail.endsWith("Rebuilding…")).toBe(true);
    expect(describeFault(ev(1, { durationMs: -1 }), 2).detail).toContain("Rebuild time not recorded.");
  });

  it.each([
    ["cross", "Crossing update rejected", "It would have put a bid at or above the ask. Discarded."],
    ["checksum", "Checksum mismatch", "The book no longer matched the exchange's checksum after an update."],
    ["disconnect", "Feed disconnected", "The connection to the exchange dropped."],
  ])("words a %s", (kind, title, sentence) => {
    const t = describeFault(ev(1, { kind, expectedSeq: 0, gotSeq: 99, durationMs: 4 }), 1);
    expect(t.title).toBe(title);
    expect(t.detail).toBe(`${sentence} Rebuilt from a snapshot in 4 ms.`);
  });

  it("shows no price in a crossing entry", () => {
    expect(describeFault(ev(1, { kind: "cross", durationMs: 4 }), 1).detail).not.toMatch(/\d{3,}/);
  });

  it.each([
    ["snapshot-reject", "Snapshot refused", "snapshot crossed", "Snapshot crossed."],
    ["snapshot-error", "Snapshot failed", "transient snapshot failure", "Transient snapshot failure."],
  ])("words %s with the server's fixed reason and no rebuild", (kind, title, reason, detail) => {
    const t = describeFault(ev(1, { kind, reason, durationMs: 0, expectedSeq: 0 }), null);
    expect(t).toEqual({ title, detail, rebuild: null });
  });

  it("names a kind it does not know generically and keeps the server's reason", () => {
    const t = describeFault(ev(1, { kind: "something-new", reason: "a new reason", durationMs: 0 }), null);
    expect(t.title).toBe("Fault");
    expect(t.detail).toBe("A new reason.");
  });
});
