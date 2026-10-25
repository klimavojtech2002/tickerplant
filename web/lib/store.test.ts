import { describe, expect, it, vi } from "vitest";

import { initialConnection } from "./connection";
import { initialSnapshot, StreamStore } from "./store";
import type { Telemetry, TelemetryEvent, ViewEvent } from "./types";

function view(seq: number, bid = "100.1", ask = "100.2"): ViewEvent {
  return { venue: "v", symbol: "s", seq, bids: [[bid, "1"]], asks: [[ask, "1"]] };
}

/** A store with a clock the test moves and a log it can read. */
function setup() {
  let t = 10_000;
  const logs: string[] = [];
  const store = new StreamStore({ now: () => t, log: (m) => logs.push(m) });
  return { store, logs, tick: (ms: number) => (t += ms) };
}

describe("StreamStore", () => {
  it("starts as the shared initial snapshot", () => {
    expect(new StreamStore().getSnapshot()).toBe(initialSnapshot);
    expect(initialSnapshot).toEqual({
      conn: initialConnection,
      view: null,
      telemetry: null,
      history: { points: [] },
      viewsChecked: 0,
      viewsRefused: 0,
      events: [],
      missedEvents: 0,
      eventBaseline: null,
    });
  });

  it("keeps only the latest view (client-side latest-wins) and stamps its receipt time", () => {
    const { store, tick } = setup();
    store.pushView(view(1));
    tick(500);
    store.pushView(view(2));
    expect(store.getSnapshot().view?.seq).toBe(2);
    expect(store.getSnapshot().conn.viewAt).toBe(10_500);
  });

  it("an accepted view is one change carrying both the view and the connection state", () => {
    const { store } = setup();
    const seen: unknown[] = [];
    store.subscribe(() => seen.push(store.getSnapshot()));
    store.pushView(view(1));
    expect(seen).toHaveLength(1);
    expect(store.getSnapshot()).toMatchObject({ view: { seq: 1 }, conn: { viewAt: 10_000 } });
  });

  it("notifies subscribers with a fresh snapshot object each change", () => {
    const { store } = setup();
    const before = store.getSnapshot();
    let calls = 0;
    const unsub = store.subscribe(() => calls++);
    store.pushView(view(1));
    expect(calls).toBe(1);
    expect(store.getSnapshot()).not.toBe(before); // useSyncExternalStore needs a new identity
    unsub();
    store.pushView(view(2));
    expect(calls).toBe(1);
  });

  it("dispatch does not notify, and keeps the snapshot, when the reducer returns the same state", () => {
    const { store } = setup();
    let calls = 0;
    store.subscribe(() => calls++);
    const before = store.getSnapshot();
    store.dispatch({ type: "connecting" }); // already connecting
    expect(calls).toBe(0);
    expect(store.getSnapshot()).toBe(before);
    store.dispatch({ type: "transient" });
    store.dispatch({ type: "transient" });
    expect(calls).toBe(1);
  });

  it("keeps the same view object across a telemetry-only change, so a memoised panel can skip it", () => {
    const { store } = setup();
    store.pushView(view(1));
    const v = store.getSnapshot().view;
    store.pushTelemetry({ t: 1, seq: 1, consumers: 1, events: [] } as unknown as Telemetry);
    expect(store.getSnapshot().view).toBe(v);
  });

  it("stores telemetry and its receipt time", () => {
    const { store } = setup();
    const t = { t: 1, seq: 2, consumers: 3, events: [] } as unknown as Telemetry;
    store.pushTelemetry(t);
    expect(store.getSnapshot().telemetry).toBe(t);
    expect(store.getSnapshot().conn.beatAt).toBe(10_000);
  });

  it("drops a crossed view in every build: counted, logged, never thrown, last good view kept", () => {
    for (const env of ["test", "production"]) {
      vi.stubEnv("NODE_ENV", env);
      try {
        const { store, logs } = setup();
        store.pushView(view(1));
        expect(() => store.pushView(view(2, "100.2", "100.2"))).not.toThrow();
        expect(store.getSnapshot().view?.seq).toBe(1);
        expect(store.getSnapshot().conn.dropped).toBe(1);
        expect(logs).toEqual(["crossed view dropped: bid 100.2 >= ask 100.2 at seq 2"]);
      } finally {
        vi.unstubAllEnvs();
      }
    }
  });

  it("accepts one-sided books without a cross check", () => {
    const { store } = setup();
    store.pushView({ venue: "v", symbol: "s", seq: 3, bids: [], asks: [["1", "1"]] });
    expect(store.getSnapshot().view?.seq).toBe(3);
    expect(store.getSnapshot().conn.dropped).toBe(0);
  });

  it("logs at most once a second across every drop path, and again after a second", () => {
    const { store, logs, tick } = setup();
    store.reject("malformed view", "a");
    store.pushView(view(1, "5", "5")); // crossed, same instant
    store.reject("malformed telemetry", "b");
    expect(logs).toHaveLength(1);
    expect(store.getSnapshot().conn.dropped).toBe(3); // counted every time, logged once
    tick(999);
    store.reject("malformed view", "c");
    expect(logs).toHaveLength(1);
    tick(1);
    store.reject("malformed view", "d");
    expect(logs).toHaveLength(2);
    expect(logs[1]).toBe("malformed view dropped: d");
  });

  it("truncates a long payload to 200 characters in the log", () => {
    const { store, logs } = setup();
    store.reject("malformed view", "x".repeat(500));
    expect(logs[0]).toBe(`malformed view dropped: ${"x".repeat(200)}…`);
    const { store: s2, logs: l2 } = setup();
    s2.reject("malformed view", "y".repeat(200));
    expect(l2[0]).toBe(`malformed view dropped: ${"y".repeat(200)}`); // exactly 200 is not cut
  });

  describe("the mid history", () => {
    function withMono() {
      let m = 1000;
      const store = new StreamStore({ now: () => 10_000, mono: () => m, log: () => {} });
      return { store, advance: (ms: number) => (m += ms) };
    }

    it("adds a point per view, on the monotonic clock, and not closer than the minimum gap (a closer view replaces the last mid)", () => {
      const { store, advance } = withMono();
      store.pushView(view(1, "100.0", "100.2"));
      advance(100);
      store.pushView(view(2, "100.0", "100.4")); // too soon for a point: replaces the last mid
      advance(500);
      store.pushView(view(3, "100.0", "100.4"));
      expect(store.getSnapshot().history.points.map((p) => [p.t, p.mid])).toEqual([
        [1000, "100.20"],
        [1600, "100.20"],
      ]);
    });

    it("adds nothing for a book with a missing side", () => {
      const { store, advance } = withMono();
      store.pushView({ venue: "v", symbol: "s", seq: 1, bids: [], asks: [["100.2", "1"]] });
      advance(1000);
      store.pushView({ venue: "v", symbol: "s", seq: 2, bids: [["100.1", "1"]], asks: [] });
      expect(store.getSnapshot().history.points).toEqual([]);
    });

    it("starts over when the connection leaves the open state, and so never joins two connections", () => {
      const { store, advance } = withMono();
      store.dispatch({ type: "open", now: 10_000 });
      store.pushView(view(1));
      advance(1000);
      store.pushView(view(2));
      expect(store.getSnapshot().history.points).toHaveLength(2);
      store.dispatch({ type: "closed", attempt: 1, retryAt: 20_000 });
      expect(store.getSnapshot().history.points).toEqual([]);
      store.dispatch({ type: "open", now: 20_000 });
      advance(5000);
      store.pushView(view(3));
      expect(store.getSnapshot().history.points).toHaveLength(1);
    });

    it("keeps the history through a change that does not leave the open state", () => {
      const { store, advance } = withMono();
      store.dispatch({ type: "open", now: 10_000 });
      store.pushView(view(1));
      advance(1000);
      store.dispatch({ type: "telemetry", now: 11_000 });
      expect(store.getSnapshot().history.points).toHaveLength(1);
    });

    it("keeps the history when a crossed view is refused", () => {
      const { store, advance } = withMono();
      store.pushView(view(1));
      advance(1000);
      store.pushView(view(2, "100.5", "100.2")); // crossed
      expect(store.getSnapshot().history.points).toHaveLength(1);
    });
  });

  describe("the page's own check of views", () => {
    it("counts a view that has both sides as checked, and none as refused", () => {
      const { store } = setup();
      store.pushView(view(1));
      store.pushView(view(2));
      expect(store.getSnapshot()).toMatchObject({ viewsChecked: 2, viewsRefused: 0 });
    });

    it("counts a crossed view as checked and as refused, once, and shows the old book", () => {
      const { store } = setup();
      store.pushView(view(1));
      store.pushView(view(2, "100.5", "100.2"));
      expect(store.getSnapshot()).toMatchObject({ viewsChecked: 2, viewsRefused: 1, view: { seq: 1 } });
      expect(store.getSnapshot().conn.dropped).toBe(1);
    });

    it("counts a locked book as crossed", () => {
      const { store } = setup();
      store.pushView(view(1, "100.2", "100.2"));
      expect(store.getSnapshot()).toMatchObject({ viewsChecked: 1, viewsRefused: 1 });
    });

    it("does not count a book with an empty side, which has no crossing to test for", () => {
      const { store } = setup();
      store.pushView({ venue: "v", symbol: "s", seq: 1, bids: [], asks: [["100.2", "1"]] });
      store.pushView({ venue: "v", symbol: "s", seq: 2, bids: [["100.1", "1"]], asks: [] });
      expect(store.getSnapshot()).toMatchObject({ viewsChecked: 0, viewsRefused: 0 });
      expect(store.getSnapshot().view?.seq).toBe(2); // still accepted and shown
    });

    it("keeps the counts through a reconnect, since they describe this page", () => {
      const { store } = setup();
      store.dispatch({ type: "open", now: 10_000 });
      store.pushView(view(1));
      store.pushView(view(2, "100.5", "100.2"));
      store.dispatch({ type: "transient" });
      store.dispatch({ type: "open", now: 12_000 });
      expect(store.getSnapshot()).toMatchObject({ viewsChecked: 2, viewsRefused: 1 });
    });

    it("does not count a frame that failed validation before it reached the store", () => {
      const { store } = setup();
      store.reject("malformed view", "x");
      expect(store.getSnapshot()).toMatchObject({ viewsChecked: 0, viewsRefused: 0 });
      expect(store.getSnapshot().conn.dropped).toBe(1);
    });
  });

  describe("the fault events", () => {
    const ev = (id: number, over: Partial<TelemetryEvent> = {}): TelemetryEvent => ({
      id,
      kind: "gap",
      atUnixMs: 1_000 * id,
      expectedSeq: 1,
      gotSeq: 2,
      reason: "sequence gap",
      durationMs: 5,
      ...over,
    });
    const frame = (over: Partial<Telemetry> = {}): Telemetry => ({
      t: 100,
      seq: 1,
      gaps: 0,
      resyncs: 0,
      disconnects: 0,
      wouldCrosses: 0,
      checksumMismatches: 0,
      snapshotRejects: 0,
      snapshotErrors: 0,
      rejected: 0,
      consumers: 1,
      events: [],
      ...over,
    });
    const ids = (store: StreamStore) => store.getSnapshot().events.map((e) => e.id);

    it("merges events across frames, newest first, and sets the baseline once from the first frame", () => {
      const { store } = setup();
      store.pushTelemetry(frame({ events: [ev(1), ev(2)] }));
      expect(ids(store)).toEqual([2, 1]);
      expect(store.getSnapshot().eventBaseline).toBe(2);
      store.pushTelemetry(frame({ t: 101, resyncs: 1, events: [ev(1), ev(2), ev(3)] }));
      expect(ids(store)).toEqual([3, 2, 1]);
      expect(store.getSnapshot().eventBaseline).toBe(2); // not moved by a later frame
    });

    it("has a baseline of 0 when the first frame has no events, so every later event is new", () => {
      const { store } = setup();
      store.pushTelemetry(frame());
      expect(store.getSnapshot().eventBaseline).toBe(0);
    });

    it("keeps the same list object when a frame changes nothing, and a new one when a duration is filled in", () => {
      const { store } = setup();
      store.pushTelemetry(frame({ events: [ev(1, { durationMs: -1 })] }));
      const first = store.getSnapshot().events;
      store.pushTelemetry(frame({ t: 101, events: [ev(1, { durationMs: -1 })] }));
      expect(store.getSnapshot().events).toBe(first);
      store.pushTelemetry(frame({ t: 102, events: [ev(1, { durationMs: 12 })] }));
      expect(store.getSnapshot().events).not.toBe(first);
      expect(store.getSnapshot().events[0]!.durationMs).toBe(12);
    });

    it("counts events that fell between two frames as missed, and only those", () => {
      const { store } = setup();
      store.pushTelemetry(frame({ events: [ev(1), ev(2)] }));
      store.pushTelemetry(frame({ t: 101, resyncs: 2, events: [ev(2), ev(3)] }));
      expect(store.getSnapshot().missedEvents).toBe(0);
      store.pushTelemetry(frame({ t: 102, resyncs: 6, events: [ev(7), ev(8)] })); // 4, 5 and 6 were never seen
      expect(store.getSnapshot().missedEvents).toBe(3);
      store.pushTelemetry(frame({ t: 103, resyncs: 7, events: [ev(7), ev(8), ev(9)] })); // an ordinary overlapping frame
      expect(store.getSnapshot().missedEvents).toBe(3); // the count is kept, not reset
    });

    it("starts over when the server restarts: an empty ring after a full one", () => {
      const { store } = setup();
      store.pushTelemetry(frame({ resyncs: 2, events: [ev(1), ev(2)] }));
      store.pushView(view(1));
      store.pushTelemetry(frame({ t: 101, resyncs: 2, events: [ev(3)] }));
      store.pushTelemetry(frame({ t: 2, resyncs: 0, events: [] }));
      expect(ids(store)).toEqual([]);
      expect(store.getSnapshot().eventBaseline).toBe(0);
      expect(store.getSnapshot().missedEvents).toBe(0);
      expect(store.getSnapshot().viewsChecked).toBe(1); // the page's own count is not the server's
    });

    it("starts over when a counter falls although the newest id is the same", () => {
      const { store } = setup();
      store.pushTelemetry(frame({ gaps: 5, events: [ev(1)] }));
      store.pushTelemetry(frame({ t: 101, gaps: 0, events: [ev(1, { kind: "cross" })] }));
      expect(store.getSnapshot().events.map((e) => e.kind)).toEqual(["cross"]);
      expect(store.getSnapshot().eventBaseline).toBe(1);
    });

    it("does not let a restart mix the old events with the new", () => {
      const { store } = setup();
      store.pushTelemetry(frame({ resyncs: 3, events: [ev(1), ev(2), ev(3)] }));
      store.pushTelemetry(frame({ t: 5, resyncs: 1, events: [ev(1)] }));
      expect(ids(store)).toEqual([1]);
    });
  });
});
