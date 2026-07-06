// pollMetrics fetches /metrics on an interval. Health is a scrape, not a stream:
// the counters move slowly and a 2s cadence keeps the edge's cost negligible.

import type { Metrics } from "./types";

export function pollMetrics(
  baseURL: string,
  onUpdate: (m: Metrics | null) => void,
  intervalMs = 2000,
  fetchFn: typeof fetch = fetch,
): () => void {
  let stopped = false;
  let inFlight = false;
  const tick = async () => {
    if (inFlight) {
      return; // a hung request must not pile ticks or let an old reply overtake a new one
    }
    inFlight = true;
    try {
      const resp = await fetchFn(`${baseURL}/metrics`);
      if (!resp.ok) {
        throw new Error(`status ${resp.status}`);
      }
      const m = (await resp.json()) as Metrics;
      if (!stopped) {
        onUpdate(m);
      }
    } catch {
      if (!stopped) {
        onUpdate(null); // the panel shows "unreachable" rather than stale numbers
      }
    } finally {
      inFlight = false;
    }
  };
  void tick();
  const id = setInterval(tick, intervalMs);
  return () => {
    stopped = true;
    clearInterval(id);
  };
}
