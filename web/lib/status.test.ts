import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { initialPollState, nextPollState, pollStatus, type PollState } from "./status";
import type { Status } from "./types";
import { at } from "./testing/at";

const status: Status = {
  schema: 1,
  mode: "synthetic",
  chaos: false,
  version: "dev",
  venue: "synthetic",
  symbol: "DEMO",
  startedAtUnixMs: 1,
  depth: 10,
  lastEventAgeMs: 5,
  delivery: { consumers: 1, delivered: 10, dropped: 2, published: 12 },
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
};
const body = JSON.stringify(status);

const good = { ok: true, status } as const;
const bad = { ok: false } as const;

describe("nextPollState", () => {
  it("counts failures before anything succeeded, then reads unavailable with nothing to show", () => {
    let s: PollState = initialPollState;
    s = nextPollState(s, bad, 100);
    expect(s).toEqual({ kind: "loading", failures: 1 });
    s = nextPollState(s, bad, 200);
    expect(s).toEqual({ kind: "loading", failures: 2 });
    s = nextPollState(s, bad, 300);
    expect(s).toEqual({ kind: "unavailable", status: null, failures: 3, lastOkAt: null });
  });

  it("keeps the last good numbers through one or two failures, then reads unavailable", () => {
    let s = nextPollState(initialPollState, good, 1000);
    expect(s).toEqual({ kind: "ok", status, lastOkAt: 1000 });
    s = nextPollState(s, bad, 2000);
    expect(s).toEqual({ kind: "degraded", status, failures: 1, lastOkAt: 1000 });
    s = nextPollState(s, bad, 3000);
    expect(s).toEqual({ kind: "degraded", status, failures: 2, lastOkAt: 1000 });
    s = nextPollState(s, bad, 4000);
    expect(s).toEqual({ kind: "unavailable", status, failures: 3, lastOkAt: 1000 });
    s = nextPollState(s, bad, 5000);
    expect(s).toEqual({ kind: "unavailable", status, failures: 4, lastOkAt: 1000 });
  });

  it("a success from any state resets to ok", () => {
    const states: PollState[] = [
      initialPollState,
      { kind: "loading", failures: 2 },
      { kind: "degraded", status, failures: 2, lastOkAt: 1 },
      { kind: "unavailable", status: null, failures: 9, lastOkAt: null },
    ];
    for (const s of states) {
      expect(nextPollState(s, good, 777)).toEqual({ kind: "ok", status, lastOkAt: 777 });
    }
  });

  it("a failure after a recovery starts counting again from one", () => {
    let s = nextPollState(initialPollState, good, 1);
    s = nextPollState(s, bad, 2);
    s = nextPollState(s, bad, 3);
    s = nextPollState(s, good, 4);
    s = nextPollState(s, bad, 5);
    expect(s).toEqual({ kind: "degraded", status, failures: 1, lastOkAt: 4 });
  });
});

describe("pollStatus", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  /** A fake fetch: the real one is never called under jsdom. It rejects when aborted. */
  function fakeFetch(handler: (n: number, init?: RequestInit) => Promise<Response> | "hang") {
    const calls: number[] = [];
    const f = vi.fn((_url: string, init?: RequestInit) => {
      calls.push(Date.now());
      const r = handler(calls.length, init);
      if (r !== "hang") return r;
      return new Promise<Response>((_res, rej) => {
        init?.signal?.addEventListener("abort", () => rej(new DOMException("aborted", "AbortError")));
      });
    });
    return { f: f as unknown as typeof fetch, calls, raw: f };
  }
  const okResp = () => Promise.resolve(new Response(body, { status: 200 }));

  it("polls /api/status now and every interval, reporting ok", async () => {
    const { f, calls, raw } = fakeFetch(okResp);
    const got: PollState[] = [];
    const stop = pollStatus("http://edge", (s) => got.push(s), { fetchFn: f });
    await vi.advanceTimersByTimeAsync(1);
    expect(at(raw.mock.calls, 0)[0]).toBe("http://edge/api/status");
    expect(got.map((s) => s.kind)).toEqual(["ok"]);
    await vi.advanceTimersByTimeAsync(2000);
    expect(calls).toHaveLength(2);
    expect(got).toHaveLength(2);
    stop();
  });

  it("starts attempts two seconds apart when they fail fast, and reads unavailable at the third", async () => {
    const { f, calls } = fakeFetch(() => Promise.resolve(new Response("down", { status: 503 })));
    const got: PollState[] = [];
    const t0 = Date.now();
    const stop = pollStatus("http://edge", (s) => got.push(s), { fetchFn: f });
    await vi.advanceTimersByTimeAsync(4001);
    expect(calls.map((c) => c - t0)).toEqual([0, 2000, 4000]);
    expect(got.map((s) => s.kind)).toEqual(["loading", "loading", "unavailable"]);
    stop();
  });

  it("abandons a hung request after the timeout and keeps the next start two seconds after the last", async () => {
    const { f, calls } = fakeFetch(() => "hang");
    const got: PollState[] = [];
    const t0 = Date.now();
    const stop = pollStatus("http://edge", (s) => got.push(s), { fetchFn: f });
    await vi.advanceTimersByTimeAsync(1499);
    expect(got).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(2); // 1500: aborted
    expect(got).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(2100); // third attempt starts at 4000, ends 5500
    expect(calls.map((c) => c - t0)).toEqual([0, 2000]);
    await vi.advanceTimersByTimeAsync(2000);
    expect(calls.map((c) => c - t0)).toEqual([0, 2000, 4000]);
    await vi.advanceTimersByTimeAsync(1500);
    expect(got.map((s) => s.kind)).toEqual(["loading", "loading", "unavailable"]);
    stop();
  });

  it("bounds a body that stalls after the headers arrive", async () => {
    // A real fetch errors the body stream when its signal aborts; the fake does the same.
    const { f } = fakeFetch((_n, init) => {
      const stalled = new ReadableStream<Uint8Array>({
        start(c) {
          init?.signal?.addEventListener("abort", () => c.error(new DOMException("aborted", "AbortError")));
        },
      }); // never yields or closes
      return Promise.resolve(new Response(stalled, { status: 200 }));
    });
    const got: PollState[] = [];
    const stop = pollStatus("http://edge", (s) => got.push(s), { fetchFn: f });
    await vi.advanceTimersByTimeAsync(1600);
    expect(got.map((s) => s.kind)).toEqual(["loading"]); // failed, not still waiting
    stop();
  });

  it("counts a non-2xx reply as a failed poll even when its body is a valid status", async () => {
    const { f } = fakeFetch(() => Promise.resolve(new Response(body, { status: 503 })));
    const got: PollState[] = [];
    const stop = pollStatus("http://edge", (s) => got.push(s), { fetchFn: f });
    await vi.advanceTimersByTimeAsync(1);
    expect(got).toEqual([{ kind: "loading", failures: 1 }]);
    stop();
  });

  it("counts a body that does not parse as a failed poll", async () => {
    const { f } = fakeFetch(() => Promise.resolve(new Response('{"venue":"x"}', { status: 200 })));
    const got: PollState[] = [];
    const stop = pollStatus("http://edge", (s) => got.push(s), { fetchFn: f });
    await vi.advanceTimersByTimeAsync(1);
    expect(got).toEqual([{ kind: "loading", failures: 1 }]);
    stop();
  });

  it("ignores a reply that arrives after stop", async () => {
    let release: (r: Response) => void = () => {};
    let signal: AbortSignal | null | undefined;
    const { f } = fakeFetch((_n, init) => {
      signal = init?.signal;
      return new Promise<Response>((res) => (release = res));
    });
    const got: PollState[] = [];
    const stop = pollStatus("http://edge", (s) => got.push(s), { fetchFn: f });
    await vi.advanceTimersByTimeAsync(10);
    stop();
    expect(signal?.aborted).toBe(true); // stop() also cancels the request in flight
    release(new Response(body, { status: 200 }));
    await vi.advanceTimersByTimeAsync(5000);
    expect(got).toHaveLength(0);
  });

  it("leaves no timer behind after a success or after stop", async () => {
    const { f } = fakeFetch(okResp);
    const stop = pollStatus("http://edge", () => {}, { fetchFn: f });
    await vi.advanceTimersByTimeAsync(1);
    expect(vi.getTimerCount()).toBe(1); // only the next poll; the timeout timer was cleared
    stop();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("does not stretch the interval when the clock steps back during an attempt", async () => {
    let clock = 10_000;
    const { f, calls } = fakeFetch(() => {
      clock -= 700; // elapsed would read -700
      return okResp();
    });
    const t0 = Date.now();
    const stop = pollStatus("http://edge", () => {}, { fetchFn: f, now: () => clock });
    await vi.advanceTimersByTimeAsync(2001);
    expect(calls.map((c) => c - t0)).toEqual([0, 2000]);
    stop();
  });

  it("never has two requests in flight", async () => {
    let inFlight = 0;
    let peak = 0;
    const { f } = fakeFetch(() => {
      inFlight++;
      peak = Math.max(peak, inFlight);
      return new Promise<Response>((res) =>
        setTimeout(() => {
          inFlight--;
          res(new Response(body, { status: 200 }));
        }, 1900),
      );
    });
    const stop = pollStatus("http://edge", () => {}, { fetchFn: f });
    await vi.advanceTimersByTimeAsync(10_000);
    expect(peak).toBe(1);
    stop();
  });
});
