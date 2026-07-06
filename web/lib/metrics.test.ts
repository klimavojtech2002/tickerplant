import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { pollMetrics } from "./metrics";
import type { Metrics } from "./types";

const doc: Metrics = {
  venue: "kraken",
  symbol: "BTC/USD",
  delivery: { consumers: 1, delivered: 10, dropped: 2 },
  engine: { gaps: 0, resyncs: 0, disconnects: 0, wouldCrosses: 0, checksumMismatches: 0 },
};

function okFetch(): typeof fetch {
  return vi.fn(async () => new Response(JSON.stringify(doc), { status: 200 })) as typeof fetch;
}

describe("pollMetrics", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("delivers the parsed document and repolls on the interval", async () => {
    const got: (Metrics | null)[] = [];
    const f = okFetch();
    pollMetrics("http://edge", (m) => got.push(m), 2000, f);
    await vi.advanceTimersByTimeAsync(1); // the immediate first tick
    expect(f).toHaveBeenCalledWith("http://edge/metrics");
    expect(got).toHaveLength(1);
    expect(got[0]?.delivery.delivered).toBe(10);
    await vi.advanceTimersByTimeAsync(2000);
    expect(got).toHaveLength(2);
  });

  it("reports null on an unreachable or failing endpoint — never stale numbers", async () => {
    const got: (Metrics | null)[] = [];
    const f = vi.fn(async () => new Response("down", { status: 503 })) as typeof fetch;
    pollMetrics("http://edge", (m) => got.push(m), 2000, f);
    await vi.advanceTimersByTimeAsync(1);
    expect(got).toEqual([null]);
  });

  it("a hung request does not pile ticks; polling resumes once it resolves", async () => {
    const got: (Metrics | null)[] = [];
    let release!: () => void;
    const hang = new Promise<Response>((res) => {
      release = () => res(new Response(JSON.stringify(doc), { status: 200 }));
    });
    let calls = 0;
    const f = vi.fn(async () => {
      calls++;
      return calls === 1 ? hang : new Response(JSON.stringify(doc), { status: 200 });
    }) as typeof fetch;
    pollMetrics("http://edge", (m) => got.push(m), 2000, f);
    await vi.advanceTimersByTimeAsync(9000); // four intervals pass while the first call hangs
    expect(calls).toBe(1); // no pile-up
    release();
    await vi.advanceTimersByTimeAsync(2000);
    expect(calls).toBe(2); // polling resumed
    expect(got.length).toBe(2);
  });

  it("stop() silences in-flight and future ticks", async () => {
    const got: (Metrics | null)[] = [];
    const stop = pollMetrics("http://edge", (m) => got.push(m), 2000, okFetch());
    stop(); // before the first tick resolves
    await vi.advanceTimersByTimeAsync(5000);
    expect(got).toHaveLength(0);
  });
});
