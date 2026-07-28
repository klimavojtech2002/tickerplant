// connectStream subscribes an EventSource to the edge's /stream and feeds a
// ViewStore. Reconnection is EventSource's own: on error it retries with backoff and
// the server's opener (the engine's current view) rebuilds the picture — no join
// protocol, no Last-Event-ID (ADR-0017). A fatal close (readyState CLOSED) is
// reported as "disconnected" rather than "retrying", since the browser has given up
// and only a page reload restarts it. The constructor is injectable so tests can
// drive the whole lifecycle with a scripted fake.

import type { ViewStore } from "./store";
import { parseView } from "./types";

// CLOSED is EventSource's readyState once the browser has given up for good (a
// non-2xx status, wrong Content-Type, or any response that never completes as an
// event stream) — distinct from a transient error it will retry on its own.
const CLOSED = 2;

type EventSourceLike = {
  addEventListener(type: string, fn: (ev: MessageEvent) => void): void;
  close(): void;
  readonly readyState: number;
  onopen: ((ev: Event) => void) | null;
  onerror: ((ev: Event) => void) | null;
};

type EventSourceCtor = new (url: string) => EventSourceLike;

export function connectStream(
  baseURL: string,
  store: ViewStore,
  ES: EventSourceCtor = EventSource as unknown as EventSourceCtor,
): () => void {
  const es = new ES(`${baseURL}/stream`);
  es.onopen = () => store.setStatus("live");
  es.onerror = () => {
    // A CLOSED readyState means the browser will never retry on its own — a
    // "retrying" badge would lie. Anything else is the ordinary transient error
    // EventSource is already recovering from.
    store.setStatus(es.readyState === CLOSED ? "disconnected" : "retrying");
  };
  es.addEventListener("view", (ev) => {
    const v = parseView(ev.data as string);
    if (v === null) {
      console.error("malformed view event dropped:", ev.data);
      return;
    }
    store.push(v);
  });
  return () => es.close();
}
