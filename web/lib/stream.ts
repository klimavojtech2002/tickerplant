// connectStream owns the page's one subscription to the edge's /stream and feeds a
// StreamStore. It is the only place that creates an EventSource and the only place with
// a timer, so a connection that must be rebuilt is rebuilt by exactly one owner.
//
// EventSource reconnects by itself after a dropped connection: the error it reports then
// has readyState CONNECTING and the right move is to leave it alone (building another
// source would connect twice). After a refusal (a non-200 answer such as the server's
// stream cap, or a wrong content type) the browser gives up for good: readyState is
// CLOSED, and only a new EventSource recovers. That rebuild waits with a capped
// exponential backoff and jitter, so a refused page does not hammer the edge.
//
// Every handler checks that its source is still the current one, which makes a late
// event from a discarded source (React Strict Mode mounts every effect twice in
// development, and a rebuild leaves the old source behind) inert.

import type { ConnectionEvent } from "./connection";
import type { StreamStore } from "./store";
import { parseTelemetry, parseView } from "./types";

// EventSource readyState values.
const CONNECTING = 0;
const CLOSED = 2;

/** The backoff starts at one second, doubles, and stops growing at thirty. */
const baseDelayMs = 1000;
const maxDelayMs = 30_000;
/** A refusal must never make the client spin, so jitter cannot shorten a wait below this. */
const minDelayMs = 250;

export type EventSourceLike = {
  addEventListener(type: string, fn: (ev: MessageEvent) => void): void;
  close(): void;
  readonly readyState: number;
  onopen: ((ev: Event) => void) | null;
  onerror: ((ev: Event) => void) | null;
};

export type StreamDeps = {
  ES: (url: string) => EventSourceLike;
  setTimeout: (fn: () => void, ms: number) => unknown;
  clearTimeout: (id: unknown) => void;
  random: () => number;
  now: () => number;
};

// The defaults look the globals up when called, never when this module loads: a native
// timer called as a method of another object throws "Illegal invocation" in a browser,
// and a binding made at load would ignore timers a test installs later.
const defaultDeps: StreamDeps = {
  ES: (url) => new globalThis.EventSource(url) as unknown as EventSourceLike,
  setTimeout: (fn, ms) => globalThis.setTimeout(fn, ms),
  clearTimeout: (id) => globalThis.clearTimeout(id as ReturnType<typeof setTimeout>),
  random: () => Math.random(),
  now: () => Date.now(),
};

/** retryDelayMs is the wait before rebuild number `attempt` (1 for the first). */
export function retryDelayMs(attempt: number, random: number): number {
  const cap = Math.min(maxDelayMs, baseDelayMs * 2 ** (attempt - 1));
  return Math.max(minDelayMs, random * cap);
}

export function connectStream(baseURL: string, store: StreamStore, overrides: Partial<StreamDeps> = {}): () => void {
  const d: StreamDeps = { ...defaultDeps, ...overrides };
  let current: EventSourceLike | null = null;
  let attempt = 0;
  let timer: unknown;

  const start = () => {
    timer = undefined;
    const es = d.ES(`${baseURL}/stream`);
    current = es;
    store.dispatch({ type: "connecting" });
    const live = () => current === es; // cleanup and a rebuild both clear `current`
    const send = (e: ConnectionEvent) => store.dispatch(e);

    es.onopen = () => {
      if (!live()) return;
      attempt = 0; // reset on open, not on the first message
      send({ type: "open", now: d.now() });
    };
    es.onerror = () => {
      if (!live()) return;
      if (es.readyState === CONNECTING) {
        send({ type: "transient" }); // the browser is reconnecting by itself: leave it alone
        return;
      }
      if (es.readyState !== CLOSED) return;
      es.close();
      current = null; // later events from this source are stale
      attempt += 1;
      const delay = retryDelayMs(attempt, d.random());
      send({ type: "closed", attempt, retryAt: d.now() + delay });
      timer = d.setTimeout(start, delay);
    };
    es.addEventListener("view", (ev) => {
      if (!live()) return;
      const raw = ev.data as string;
      const v = parseView(raw);
      if (v === null) {
        store.reject("malformed view", raw);
        return;
      }
      store.pushView(v);
    });
    es.addEventListener("telemetry", (ev) => {
      if (!live()) return;
      const raw = ev.data as string;
      const t = parseTelemetry(raw);
      if (t === null) {
        store.reject("malformed telemetry", raw);
        return;
      }
      store.pushTelemetry(t);
    });
  };

  start();
  return () => {
    if (timer !== undefined) d.clearTimeout(timer);
    current?.close();
    current = null;
  };
}
