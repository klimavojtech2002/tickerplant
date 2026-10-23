import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { StreamStore } from "./store";
import { connectStream, retryDelayMs } from "./stream";
import { FakeEventSource } from "./testing/fakeEventSource";
import { at } from "./testing/at";

const goodView = JSON.stringify({
  venue: "kraken",
  symbol: "BTC/USD",
  seq: 7,
  bids: [["62721.7", "1.35823999"]],
  asks: [["62721.8", "0.00114202"]],
});

const goodTelemetry = JSON.stringify({
  t: 1700000000000,
  seq: 7,
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
});

/** A store with a silent logger and a clock the test moves by hand. */
function setup(random = 0.5) {
  let t = 1_000_000;
  const logs: string[] = [];
  const store = new StreamStore({ now: () => t, log: (m) => logs.push(m) });
  const stop = connectStream("http://edge", store, {
    ES: (url) => new FakeEventSource(url),
    now: () => t,
    random: () => random,
  });
  return { store, stop, logs, advanceClock: (ms: number) => (t += ms) };
}

describe("connectStream", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeEventSource.reset();
  });
  afterEach(() => vi.useRealTimers());

  it("opens /stream and walks connecting, live, transient, live", () => {
    const { store } = setup();
    expect(FakeEventSource.last().url).toBe("http://edge/stream");
    expect(store.getSnapshot().conn.link).toBe("connecting");
    FakeEventSource.last().open();
    expect(store.getSnapshot().conn.link).toBe("open");
    expect(store.getSnapshot().conn).toMatchObject({ openAt: 1_000_000, beatAt: 1_000_000 }); // the injected clock
    FakeEventSource.last().dropTransient();
    expect(store.getSnapshot().conn.link).toBe("transient");
    FakeEventSource.last().open(); // the browser's own reconnect succeeded
    expect(store.getSnapshot().conn.link).toBe("open");
    expect(FakeEventSource.instances).toHaveLength(1); // it never built a second source
  });

  it("never rebuilds while the browser is reconnecting by itself", () => {
    setup();
    for (let i = 0; i < 5; i++) FakeEventSource.last().dropTransient();
    vi.advanceTimersByTime(120_000);
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("rebuilds after a refusal, with the backoff delay, and shows the countdown numbers", () => {
    const { store } = setup(0.5);
    FakeEventSource.last().fail();
    // attempt 1: cap 1000 ms, random 0.5 -> 500 ms
    expect(store.getSnapshot().conn).toMatchObject({ link: "closed", attempt: 1, retryAt: 1_000_500 });
    expect(at(FakeEventSource.instances, 0).close).toHaveBeenCalled();
    vi.advanceTimersByTime(499);
    expect(FakeEventSource.instances).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(store.getSnapshot().conn.link).toBe("connecting");
    expect(store.getSnapshot().conn.retryAt).toBeNull();
  });

  it("resets the attempt on open, not on the first message", () => {
    const { store } = setup(1 - Number.EPSILON);
    FakeEventSource.last().fail(); // attempt 1
    vi.advanceTimersByTime(1000);
    FakeEventSource.last().fail(); // attempt 2
    expect(store.getSnapshot().conn.attempt).toBe(2);
    vi.advanceTimersByTime(2000);
    FakeEventSource.last().emit("telemetry", goodTelemetry); // a message without an open
    FakeEventSource.last().fail();
    expect(store.getSnapshot().conn.attempt).toBe(3); // still counting
    vi.advanceTimersByTime(4000);
    FakeEventSource.last().open();
    FakeEventSource.last().fail();
    expect(store.getSnapshot().conn.attempt).toBe(1); // open reset it
  });

  it("ignores a second error while the rebuild is pending", () => {
    const { store } = setup();
    const es = FakeEventSource.last();
    es.fail();
    es.fail();
    es.fail();
    expect(vi.getTimerCount()).toBe(1);
    expect(store.getSnapshot().conn.attempt).toBe(1);
  });

  it("ignores events from a source that was closed and replaced", () => {
    const { store } = setup();
    const old = FakeEventSource.last();
    old.fail();
    vi.advanceTimersByTime(1000);
    const fresh = FakeEventSource.last();
    fresh.open();
    fresh.emit("view", goodView);
    old.emit("view", goodView.replace('"seq":7', '"seq":1'));
    old.emit("telemetry", goodTelemetry);
    old.open();
    old.dropTransient();
    expect(store.getSnapshot().view?.seq).toBe(7);
    expect(store.getSnapshot().conn).toMatchObject({ link: "open", dropped: 0 });
  });

  it("ignores events from a source that failed and is still referenced, before the rebuild", () => {
    const { store } = setup();
    const es = FakeEventSource.last();
    es.open();
    es.fail();
    es.emit("view", goodView);
    es.emit("telemetry", goodTelemetry);
    es.open();
    expect(store.getSnapshot().view).toBeNull();
    expect(store.getSnapshot().conn.link).toBe("closed");
  });

  it("parses views and telemetry into the store", () => {
    const { store } = setup();
    const es = FakeEventSource.last();
    es.open();
    es.emit("telemetry", goodTelemetry);
    es.emit("view", goodView);
    expect(store.getSnapshot().view?.seq).toBe(7);
    expect(store.getSnapshot().telemetry?.consumers).toBe(1);
    expect(store.getSnapshot().conn.viewAt).toBe(1_000_000);
  });

  it("drops malformed frames, counts them, and keeps the last good book", () => {
    const { store, logs } = setup();
    const es = FakeEventSource.last();
    es.emit("view", goodView);
    es.emit("view", `{"venue":1}`);
    es.emit("view", "not json");
    es.emit("view", goodView.replace("62721.7", "1e5"));
    es.emit("telemetry", `{"t":1}`);
    expect(store.getSnapshot().view?.seq).toBe(7);
    expect(store.getSnapshot().conn.dropped).toBe(4);
    expect(logs).toHaveLength(1); // one limiter for every drop path
    expect(logs[0]).toMatch(/^malformed view dropped/);
  });

  it("closes the source and clears the timer on cleanup, with a rebuild pending", () => {
    const { stop } = setup();
    FakeEventSource.last().fail();
    expect(vi.getTimerCount()).toBe(1);
    stop();
    expect(vi.getTimerCount()).toBe(0);
    vi.advanceTimersByTime(60_000);
    expect(FakeEventSource.instances).toHaveLength(1);
  });

  it("closes the live source on cleanup and ignores what it still sends", () => {
    const { store, stop } = setup();
    const es = FakeEventSource.last();
    es.open();
    stop();
    expect(es.close).toHaveBeenCalled();
    es.emit("view", goodView);
    es.dropTransient();
    expect(store.getSnapshot().view).toBeNull();
    expect(store.getSnapshot().conn.link).toBe("open");
  });

  // React Strict Mode mounts every effect twice in development: connect, clean up,
  // connect again. The discarded source's handlers stay wired to the same store.
  it("ignores late events from the source of a cleaned-up first mount", () => {
    const store = new StreamStore({ now: () => 1, log: () => {} });
    const deps = { ES: (url: string) => new FakeEventSource(url), now: () => 1, random: () => 0.5 };
    const stop1 = connectStream("http://edge", store, deps);
    const esA = FakeEventSource.last();
    esA.open();
    stop1();
    connectStream("http://edge", store, deps);
    const esB = FakeEventSource.last();
    esB.open();
    esB.emit("view", goodView);
    esA.dropTransient();
    esA.emit("view", goodView.replace('"seq":7', '"seq":1'));
    expect(store.getSnapshot().conn.link).toBe("open");
    expect(store.getSnapshot().view?.seq).toBe(7);
  });

  it("uses the global EventSource and timers, looked up when called", () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    try {
      const store = new StreamStore({ log: () => {} });
      const stop = connectStream("http://edge", store); // no deps at all
      FakeEventSource.last().fail();
      expect(vi.getTimerCount()).toBe(1); // the faked setTimeout was the one used
      vi.advanceTimersByTime(30_000);
      expect(FakeEventSource.instances).toHaveLength(2);
      stop();
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});

describe("retryDelayMs", () => {
  it.each([
    [1, 1, 1000],
    [2, 1, 2000],
    [3, 1, 4000],
    [4, 1, 8000],
    [5, 1, 16_000],
    [6, 1, 30_000], // 32 s capped
    [7, 1, 30_000],
    [20, 1, 30_000],
    [3, 0.5, 2000],
    [1, 0, 250], // the floor: jitter cannot make a refused client spin
    [6, 0.001, 250],
  ])("attempt %d with random %d waits %d ms", (attempt, random, want) => {
    expect(retryDelayMs(attempt, random)).toBe(want);
  });
});
