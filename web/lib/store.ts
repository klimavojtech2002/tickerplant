// StreamStore is the client-side end of latest-wins conflation (ADR-0015): it keeps
// exactly one view, the freshest, plus the connection state, and React reads it through
// useSyncExternalStore. Events arriving faster than React renders simply overwrite the
// slot; missed frames are by design not replayed, mirroring the delivery layer.
//
// A frame the page cannot use is dropped and counted, in every build: a crossed book is
// the one the server promises never to send, so seeing one means a transport or client
// bug, and the page shows the last good book and a counter instead of throwing.

import { connection, initialConnection, type ConnectionEvent, type ConnectionState } from "./connection";
import { addPoint, emptyHistory, type History } from "./history";
import { mid } from "./mid";
import { crossed } from "./ticks";
import type { Telemetry, ViewEvent } from "./types";

export type Snapshot = {
  conn: ConnectionState;
  view: ViewEvent | null;
  telemetry: Telemetry | null;
  /** The recent mids, for the line in the hero; cleared whenever the connection leaves the open state. */
  history: History;
};

/** The snapshot before anything arrives; also what the server renders, so it must be one stable object. */
export const initialSnapshot: Snapshot = {
  conn: initialConnection,
  view: null,
  telemetry: null,
  history: emptyHistory,
};

/** One log line per second, at most, and never more than this many characters of a payload. */
const logEveryMs = 1000;
const maxLoggedChars = 200;

export type StoreDeps = {
  now?: () => number;
  /** A monotonic clock for the history (default performance.now); the wall clock may jump. */
  mono?: () => number;
  log?: (message: string) => void;
};

export class StreamStore {
  private snap: Snapshot = initialSnapshot;
  private listeners = new Set<() => void>();
  private readonly now: () => number;
  private readonly mono: () => number;
  private readonly log: (message: string) => void;
  private lastLogAt = Number.NEGATIVE_INFINITY;

  constructor(deps: StoreDeps = {}) {
    this.now = deps.now ?? (() => Date.now());
    this.mono = deps.mono ?? (() => performance.now());
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
    // A line must never join two connections: when the stream leaves the open state the history starts over.
    const history = this.snap.conn.link === "open" && conn.link !== "open" ? emptyHistory : this.snap.history;
    this.set({ ...this.snap, conn, history });
  }

  /** pushView accepts the next view, or drops it (counted and logged) if it is crossed. */
  pushView(v: ViewEvent): void {
    const bestBid = v.bids[0];
    const bestAsk = v.asks[0];
    if (bestBid !== undefined && bestAsk !== undefined && crossed(bestBid[0], bestAsk[0])) {
      this.reject("crossed view", `bid ${bestBid[0]} >= ask ${bestAsk[0]} at seq ${v.seq}`);
      return;
    }
    // A book with a missing side has no mid, so it adds no point.
    const m = mid(bestBid?.[0], bestAsk?.[0]);
    const history = m === null ? this.snap.history : addPoint(this.snap.history, this.mono(), m);
    this.set({ ...this.snap, conn: connection(this.snap.conn, { type: "view", now: this.now() }), view: v, history });
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
