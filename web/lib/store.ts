// ViewStore is the client-side end of latest-wins conflation (ADR-0015): it keeps
// exactly one view — the freshest — and React reads it through useSyncExternalStore.
// Events arriving faster than React renders simply overwrite the slot; missed frames
// are by design not replayed, mirroring the delivery layer.

import { crossed } from "./ticks";
import type { ViewEvent } from "./types";

export type StreamStatus = "connecting" | "live" | "retrying";

export type Snapshot = {
  status: StreamStatus;
  view: ViewEvent | null;
  /** ms epoch of the last accepted view; 0 before the first. */
  updatedAt: number;
};

export class ViewStore {
  private snap: Snapshot = { status: "connecting", view: null, updatedAt: 0 };
  private listeners = new Set<() => void>();

  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  getSnapshot = (): Snapshot => this.snap;

  setStatus(status: StreamStatus): void {
    if (this.snap.status === status) {
      return;
    }
    this.snap = { ...this.snap, status };
    this.notify();
  }

  /**
   * push accepts the next view. A crossed book is never rendered: the server
   * guarantees the invariant, so a crossed frame here means a transport or client
   * bug — it is dropped loudly, and in development it throws (the render guard
   * mirroring the backend's never-crosses stance).
   */
  push(v: ViewEvent, now: number = Date.now()): void {
    if (v.bids.length > 0 && v.asks.length > 0 && crossed(v.bids[0][0], v.asks[0][0])) {
      const msg = `crossed view dropped: bid ${v.bids[0][0]} >= ask ${v.asks[0][0]} at seq ${v.seq}`;
      if (process.env.NODE_ENV !== "production") {
        throw new Error(msg);
      }
      console.error(msg);
      return;
    }
    this.snap = { status: this.snap.status, view: v, updatedAt: now };
    this.notify();
  }

  private notify(): void {
    for (const fn of this.listeners) {
      fn();
    }
  }
}
