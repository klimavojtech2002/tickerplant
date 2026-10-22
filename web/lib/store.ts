// StreamStore is the client-side end of latest-wins conflation (ADR-0015): it keeps
// exactly one view, the freshest, plus the connection state, and React reads it through
// useSyncExternalStore. Events arriving faster than React renders simply overwrite the
// slot; missed frames are by design not replayed, mirroring the delivery layer.
//
// A frame the page cannot use is dropped and counted, in every build: a crossed book is
// the one the server promises never to send, so seeing one means a transport or client
// bug, and the page shows the last good book and a counter instead of throwing.

import { connection, initialConnection, type ConnectionEvent, type ConnectionState } from "./connection";
import { crossed } from "./ticks";
import type { Telemetry, ViewEvent } from "./types";

export type Snapshot = {
  conn: ConnectionState;
  view: ViewEvent | null;
  telemetry: Telemetry | null;
};

/** The snapshot before anything arrives; also what the server renders, so it must be one stable object. */
export const initialSnapshot: Snapshot = { conn: initialConnection, view: null, telemetry: null };

/** One log line per second, at most, and never more than this many characters of a payload. */
const logEveryMs = 1000;
const maxLoggedChars = 200;

export type StoreDeps = {
  now?: () => number;
  log?: (message: string) => void;
};

export class StreamStore {
  private snap: Snapshot = initialSnapshot;
  private listeners = new Set<() => void>();
  private readonly now: () => number;
  private readonly log: (message: string) => void;
  private lastLogAt = Number.NEGATIVE_INFINITY;

  constructor(deps: StoreDeps = {}) {
    this.now = deps.now ?? (() => Date.now());
    this.log = deps.log ?? ((message) => console.error(message));
  }

  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  getSnapshot = (): Snapshot => this.snap;

  /** dispatch applies a connection event; subscribers hear about it only if the state changed. */
  dispatch(e: ConnectionEvent): void {
    const conn = connection(this.snap.conn, e);
    if (conn === this.snap.conn) {
      return;
    }
    this.set({ ...this.snap, conn });
  }

  /** pushView accepts the next view, or drops it (counted and logged) if it is crossed. */
  pushView(v: ViewEvent): void {
    if (v.bids.length > 0 && v.asks.length > 0 && crossed(v.bids[0][0], v.asks[0][0])) {
      this.reject("crossed view", `bid ${v.bids[0][0]} >= ask ${v.asks[0][0]} at seq ${v.seq}`);
      return;
    }
    this.set({ ...this.snap, conn: connection(this.snap.conn, { type: "view", now: this.now() }), view: v });
  }

  pushTelemetry(t: Telemetry): void {
    this.set({ ...this.snap, conn: connection(this.snap.conn, { type: "telemetry", now: this.now() }), telemetry: t });
  }

  /** reject counts a frame the page could not use and logs it, rate limited and truncated. */
  reject(what: string, raw: string): void {
    this.dispatch({ type: "invalid" });
    const t = this.now();
    if (t - this.lastLogAt < logEveryMs) {
      return;
    }
    this.lastLogAt = t;
    this.log(`${what} dropped: ${raw.length > maxLoggedChars ? `${raw.slice(0, maxLoggedChars)}…` : raw}`);
  }

  private set(next: Snapshot): void {
    this.snap = next;
    for (const fn of this.listeners) {
      fn();
    }
  }
}
