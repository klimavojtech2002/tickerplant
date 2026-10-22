// pollStatus fetches /api/status on a schedule. Health is a scrape, not a stream: the
// counters move slowly. One slow or failed scrape must not flash "unavailable", so the
// state only turns unavailable after three failures in a row, and until then the last
// good numbers stay on screen.

import { parseStatus, type Status } from "./types";

export type PollState =
  | { kind: "loading"; failures: number }
  | { kind: "ok"; status: Status; lastOkAt: number }
  | { kind: "degraded"; status: Status; failures: number; lastOkAt: number }
  | { kind: "unavailable"; status: Status | null; failures: number; lastOkAt: number | null };

export type PollResult = { ok: true; status: Status } | { ok: false };

/** The state shown before the first poll finishes. */
export const initialPollState: PollState = { kind: "loading", failures: 0 };

/** Failures in a row before the state reads unavailable. */
export const failuresBeforeUnavailable = 3;

/** nextPollState folds one poll result into the state; it is pure so the rule is a table. */
export function nextPollState(prev: PollState, r: PollResult, now: number): PollState {
  if (r.ok) {
    return { kind: "ok", status: r.status, lastOkAt: now };
  }
  const failures = prev.kind === "ok" ? 1 : prev.failures + 1;
  const status = prev.kind === "loading" ? null : prev.status;
  const lastOkAt = prev.kind === "loading" ? null : prev.lastOkAt;
  if (failures >= failuresBeforeUnavailable) {
    return { kind: "unavailable", status, failures, lastOkAt };
  }
  if (status === null || lastOkAt === null) {
    return { kind: "loading", failures }; // never succeeded: nothing to keep showing
  }
  return { kind: "degraded", status, failures, lastOkAt };
}

export type PollOptions = {
  /** Time from the start of one attempt to the start of the next. */
  intervalMs?: number;
  /** An attempt, headers and body, is abandoned after this long; keep it below the interval. */
  timeoutMs?: number;
  fetchFn?: typeof fetch;
  now?: () => number;
};

/** pollStatus starts polling and returns the function that stops it. */
export function pollStatus(
  baseURL: string,
  onUpdate: (state: PollState) => void,
  opts: PollOptions = {},
): () => void {
  const intervalMs = opts.intervalMs ?? 2000;
  const timeoutMs = opts.timeoutMs ?? 1500;
  const fetchFn = opts.fetchFn ?? fetch;
  const now = opts.now ?? Date.now;

  let state: PollState = initialPollState;
  let stopped = false;
  let next: ReturnType<typeof setTimeout> | undefined;
  let controller: AbortController | undefined;

  const attempt = async () => {
    const startedAt = now();
    controller = new AbortController();
    const abort = controller;
    // AbortSignal.timeout is not driven by fake timers, so the timer is our own; it
    // covers the body read as well, so a server that stalls after its headers is bounded.
    const timer = setTimeout(() => abort.abort(), timeoutMs);
    let result: PollResult = { ok: false };
    try {
      const resp = await fetchFn(`${baseURL}/api/status`, { signal: abort.signal });
      if (resp.ok) {
        const status = parseStatus(await resp.text());
        if (status !== null) {
          result = { ok: true, status };
        }
      }
    } catch {
      // network error, abort or unreadable body: a failed poll
    } finally {
      clearTimeout(timer);
    }
    if (stopped) {
      return; // a reply that arrives after stop() is ignored
    }
    state = nextPollState(state, result, now());
    onUpdate(state);
    const elapsed = Math.max(0, now() - startedAt);
    next = setTimeout(attempt, Math.max(0, intervalMs - elapsed));
  };

  void attempt();
  return () => {
    stopped = true;
    clearTimeout(next);
    controller?.abort();
  };
}
