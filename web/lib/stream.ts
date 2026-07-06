// connectStream subscribes an EventSource to the edge's /stream and feeds a
// ViewStore. Reconnection is EventSource's own: on error it retries with backoff and
// the server's opener (the engine's current view) rebuilds the picture — no join
// protocol, no Last-Event-ID (ADR-0017). The constructor is injectable so tests can
// drive the whole lifecycle with a scripted fake.

import type { ViewStore } from "./store";
import { parseView } from "./types";

type EventSourceLike = {
  addEventListener(type: string, fn: (ev: MessageEvent) => void): void;
  close(): void;
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
  es.onerror = () => store.setStatus("retrying"); // EventSource keeps retrying on its own
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
