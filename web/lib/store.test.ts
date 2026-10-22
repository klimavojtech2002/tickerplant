import { describe, expect, it, vi } from "vitest";

import { initialConnection } from "./connection";
import { initialSnapshot, StreamStore } from "./store";
import type { Telemetry, ViewEvent } from "./types";

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
    expect(initialSnapshot).toEqual({ conn: initialConnection, view: null, telemetry: null });
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
});
