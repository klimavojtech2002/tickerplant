// StreamStore is the client-side end of latest-wins conflation (ADR-0015): it keeps
// exactly one view, the freshest, plus the connection state, and React reads it through
// useSyncExternalStore. Events arriving faster than React renders simply overwrite the
// slot; missed frames are by design not replayed, mirroring the delivery layer.
//
// A frame the page cannot use is dropped and counted, in every build: a crossed book is
// the one the server promises never to send, so seeing one means a transport or client
// bug, and the page shows the last good book and a counter instead of throwing.

import { connection, initialConnection, type ConnectionEvent, type ConnectionState } from "./connection";
import { mergeEvents, restarted } from "./faults";
import { addPoint, emptyHistory, type History } from "./history";
import { mid } from "./mid";
import { crossed } from "./ticks";
import type { Telemetry, TelemetryEvent, ViewEvent } from "./types";

export type Snapshot = {
  conn: ConnectionState;
  view: ViewEvent | null;
  telemetry: Telemetry | null;
  /** The recent mids, for the line in the hero; cleared whenever the connection leaves the open state. */
  history: History;
  /** Views that reached this page with both sides present, so a crossing could be tested for; counted since the page opened. */
  viewsChecked: number;
  /** Of those, the ones that were crossed and refused. */
  viewsRefused: number;
  /** The fault events seen so far, newest first, at most 16, merged across telemetry frames. */
  events: TelemetryEvent[];
  /** How many events happened between two frames and were never seen. */
  missedEvents: number;
  /** The newest event id in the first telemetry frame (0 if it had none), or null before any frame: where "new" begins. */
  eventBaseline: number | null;
};

/** The snapshot before anything arrives; also what the server renders, so it must be one stable object. */
export const initialSnapshot: Snapshot = {
  conn: initialConnection,
  view: null,
  telemetry: null,
  history: emptyHistory,
  viewsChecked: 0,
  viewsRefused: 0,
  events: [],
  missedEvents: 0,
  eventBaseline: null,
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
    // Only a book with both sides has a crossing to test for; the count says how many views the test was run on.
    const tested = bestBid !== undefined && bestAsk !== undefined;
    if (tested && crossed(bestBid[0], bestAsk[0])) {
      this.set({ ...this.snap, viewsChecked: this.snap.viewsChecked + 1, viewsRefused: this.snap.viewsRefused + 1 });
      this.reject("crossed view", `bid ${bestBid[0]} >= ask ${bestAsk[0]} at seq ${v.seq}`);
      return;
    }
    // A book with a missing side has no mid, so it adds no point.
    const m = mid(bestBid?.[0], bestAsk?.[0]);
    const history = m === null ? this.snap.history : addPoint(this.snap.history, this.mono(), m);
    this.set({
      ...this.snap,
      conn: connection(this.snap.conn, { type: "view", now: this.now() }),
      view: v,
      history,
      viewsChecked: this.snap.viewsChecked + (tested ? 1 : 0),
    });
  }

  pushTelemetry(t: Telemetry): void {
    const previous = this.snap.telemetry;
    // A server that started again numbers its events from 1 and counts from 0: what was kept of the old one is dropped.
    const fresh = previous === null || restarted(previous, this.snap.events[0]?.id ?? 0, t);
    const merged = mergeEvents(fresh ? [] : this.snap.events, t.events);
    const events = sameEvents(merged.events, this.snap.events) ? this.snap.events : merged.events;
    this.set({
      ...this.snap,
      conn: connection(this.snap.conn, { type: "telemetry", now: this.now() }),
      telemetry: t,
      events,
      missedEvents: fresh ? 0 : this.snap.missedEvents + merged.lost,
      eventBaseline: fresh ? (merged.events[0]?.id ?? 0) : this.snap.eventBaseline,
    });
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

/** sameEvents is true when two lists hold the same events, so a frame that changes nothing leaves the list as it was. */
function sameEvents(a: readonly TelemetryEvent[], b: readonly TelemetryEvent[]): boolean {
  return (
    a.length === b.length &&
    a.every((e, i) => {
      const o = b[i]!;
      return e.id === o.id && e.kind === o.kind && e.atUnixMs === o.atUnixMs && e.durationMs === o.durationMs;
    })
  );
}
