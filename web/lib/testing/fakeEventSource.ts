// A scripted EventSource for tests: the whole lifecycle is driven by hand, so reconnect
// behaviour is tested without a socket (the same seam-injection idea as the Go suite).
// It enforces nothing the real one does not promise: close() sets readyState to CLOSED,
// but emit() still delivers to a closed source, so a test of "ignores events from a
// discarded source" proves the owner's guard and not the double.

import { vi } from "vitest";

export class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  static instances: FakeEventSource[] = [];

  /** The newest source created; throws if there is none, so a test never reads null. */
  static last(): FakeEventSource {
    const es = FakeEventSource.instances.at(-1);
    if (es === undefined) {
      throw new Error("no EventSource has been created");
    }
    return es;
  }

  static reset(): void {
    FakeEventSource.instances = [];
  }

  readyState = FakeEventSource.CONNECTING;
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  readonly close = vi.fn(() => {
    this.readyState = FakeEventSource.CLOSED;
  });
  private handlers = new Map<string, (ev: MessageEvent) => void>();

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, fn: (ev: MessageEvent) => void): void {
    this.handlers.set(type, fn);
  }

  /** The browser opened the connection. */
  open(): void {
    this.readyState = FakeEventSource.OPEN;
    this.onopen?.(new Event("open"));
  }

  /** The connection dropped and the browser is reconnecting by itself. */
  dropTransient(): void {
    this.readyState = FakeEventSource.CONNECTING;
    this.onerror?.(new Event("error"));
  }

  /** The browser gave up (a refusal): readyState CLOSED. */
  fail(): void {
    this.readyState = FakeEventSource.CLOSED;
    this.onerror?.(new Event("error"));
  }

  emit(type: string, data: string): void {
    this.handlers.get(type)?.({ data } as MessageEvent);
  }
}
