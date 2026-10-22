// The connection model: what the page knows about its link to the edge, as a pure
// reducer plus a pure function that turns the state into the words on the status pill.
// Nothing here has a timer or a clock of its own; every event that depends on time
// carries the client's receipt time, so each row of the state table is a plain table test.
//
// Two clocks matter and they are independent. The telemetry event arrives once a second
// whatever the market does, so its silence means the pipe is dead; views arrive only when
// the book changes, so their silence means a quiet market. Telling them apart is the
// whole point of this file.

import { formatAgeMs } from "./format";

export type Link = "connecting" | "open" | "transient" | "closed";

export type ConnectionState = {
  link: Link;
  /** True once any connection has opened, so "Connecting" becomes "Reconnecting". */
  everOpened: boolean;
  /** Rebuilds scheduled in a row; set only by `closed`, zeroed by `open`. */
  attempt: number;
  /** When the next rebuild starts (epoch ms); a number exactly while the link is closed. */
  retryAt: number | null;
  /** Receipt time of the last open; 0 = never. The age of a view that has not arrived yet. */
  openAt: number;
  /** Receipt time of the last open or telemetry event; 0 = none. */
  beatAt: number;
  /** Receipt time of the last accepted view; 0 = none since the last open. */
  viewAt: number;
  /** Frames refused: a view or telemetry body that failed validation, or a crossed view. */
  dropped: number;
};

export type ConnectionEvent =
  | { type: "connecting" } // a new EventSource was created
  | { type: "open"; now: number }
  | { type: "transient" } // error while the browser reconnects by itself
  | { type: "closed"; attempt: number; retryAt: number } // error with the source CLOSED; a rebuild is scheduled
  | { type: "telemetry"; now: number }
  | { type: "view"; now: number }
  | { type: "invalid" };

export const initialConnection: ConnectionState = {
  link: "connecting",
  everOpened: false,
  attempt: 0,
  retryAt: null,
  openAt: 0,
  beatAt: 0,
  viewAt: 0,
  dropped: 0,
};

/** connection folds one event into the state. It returns the same object when nothing changes. */
export function connection(s: ConnectionState, e: ConnectionEvent): ConnectionState {
  switch (e.type) {
    case "connecting":
      return s.link === "connecting" && s.retryAt === null ? s : { ...s, link: "connecting", retryAt: null };
    case "open":
      return {
        ...s,
        link: "open",
        everOpened: true,
        attempt: 0,
        retryAt: null,
        openAt: e.now,
        beatAt: e.now,
        viewAt: 0, // the opener view sets it again; an old view must not look fresh after an outage
      };
    case "transient":
      return s.link === "transient" && s.retryAt === null ? s : { ...s, link: "transient", retryAt: null };
    case "closed":
      return { ...s, link: "closed", attempt: e.attempt, retryAt: e.retryAt };
    case "telemetry":
      return { ...s, beatAt: e.now };
    case "view":
      return { ...s, viewAt: e.now };
    case "invalid":
      return { ...s, dropped: s.dropped + 1 };
  }
}

export type LabelKind = "connecting" | "live" | "quiet" | "no-data" | "reconnecting" | "transient";

/** After this long without a telemetry event the pipe is called dead. */
export const noDataAfterMs = 3000;
/** After this long without a view the market is called quiet. */
export const quietAfterMs = 10_000;
/** The longest wait the pill will ever count down from (the backoff cap, in seconds). */
const maxCountdownSecs = 30;

const age = (now: number, at: number) => Math.max(0, now - at);

/** connectionLabel is the one place the words on the pill come from. */
export function connectionLabel(s: ConnectionState, now: number): { kind: LabelKind; text: string } {
  switch (s.link) {
    case "connecting":
      return s.everOpened
        ? { kind: "reconnecting", text: "Reconnecting…" }
        : { kind: "connecting", text: "Connecting…" };
    case "transient":
      return { kind: "transient", text: "Reconnecting…" };
    case "closed": {
      const secs = Math.min(maxCountdownSecs, Math.max(0, Math.ceil(((s.retryAt ?? now) - now) / 1000)));
      return { kind: "reconnecting", text: `Reconnecting in ${secs} s (attempt ${s.attempt})` };
    }
    case "open": {
      const beat = age(now, s.beatAt);
      if (s.beatAt > 0 && beat > noDataAfterMs) {
        return { kind: "no-data", text: `No data from edge · ${Math.floor(beat / 1000)} s` };
      }
      // With no view yet the server sends telemetry only, so the wait is counted from the open.
      const sinceView = age(now, s.viewAt > 0 ? s.viewAt : s.openAt);
      if (sinceView >= quietAfterMs) {
        return { kind: "quiet", text: "Live · market quiet" };
      }
      return { kind: "live", text: s.viewAt > 0 ? `Live · ${formatAgeMs(sinceView)}` : "Live" };
    }
  }
}
