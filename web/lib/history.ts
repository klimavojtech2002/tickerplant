// A short memory of the mid price, for the line in the hero. It is a convenience of this page, not data from the server: it
// starts empty when the page opens, forgets after two minutes and is cleared whenever the connection is rebuilt, so a line
// never joins two separate connections.
//
// A point is added when a new view arrives, never on a timer: in a quiet market there is nothing new to draw, and the line
// holds its last value. Times come from the caller's monotonic clock (performance.now), so a change of the system clock cannot
// freeze or reorder the line. Mids are decimal strings; this module never turns one into a number.

import { midChange } from "./mid";

export type Point = { readonly t: number; readonly mid: string };
export type History = { readonly points: readonly Point[] };

export const emptyHistory: History = { points: [] };

/** How long a point is kept, and how close together two points may be. */
export const windowMs = 120_000;
export const minGapMs = 500;
/** A window of 120 s with a point every 500 ms is exactly 240; the oldest point goes first. */
export const maxPoints = 240;
/** Under this many points the line is still "collecting". */
export const enoughPoints = 10;

/**
 * addPoint appends a mid taken at time t (ms, monotonic). It ignores a point closer than minGapMs to the last one, starts over
 * if the scale of the mids changes or time goes backwards, and drops points older than the window and beyond the cap.
 */
export function addPoint(h: History, t: number, mid: string): History {
  const last = h.points[h.points.length - 1];
  if (last !== undefined) {
    if (t < last.t || midScale(mid) !== midScale(last.mid)) {
      return { points: [{ t, mid }] };
    }
    if (t - last.t < minGapMs) {
      return h;
    }
  }
  let points = [...h.points, { t, mid }];
  const oldestKept = t - windowMs;
  const firstKept = points.findIndex((p) => p.t >= oldestKept);
  points = points.slice(firstKept === -1 ? points.length - 1 : firstKept);
  if (points.length > maxPoints) {
    points = points.slice(points.length - maxPoints);
  }
  return { points };
}

const midScale = (mid: string): number => {
  const dot = mid.indexOf(".");
  return dot === -1 ? 0 : mid.length - dot - 1;
};

/** collecting is true while there are too few points to draw a line worth reading. */
export function collecting(h: History): boolean {
  return h.points.length < enoughPoints;
}

/** change is the newest mid minus the oldest, as a signed decimal, or null with fewer than two points. */
export function change(h: History): string | null {
  const first = h.points[0];
  const last = h.points[h.points.length - 1];
  if (first === undefined || last === undefined || h.points.length < 2) {
    return null;
  }
  return midChange(first.mid, last.mid);
}

/** span is how many milliseconds the points cover. */
export function span(h: History): number {
  const first = h.points[0];
  const last = h.points[h.points.length - 1];
  return first === undefined || last === undefined ? 0 : last.t - first.t;
}

/** formatSpan words a duration for a caption: "45 s", "1 min", "1 min 40 s". Whole seconds, cut not rounded. */
export function formatSpan(ms: number): string {
  const secs = Math.max(0, Math.floor(ms / 1000));
  if (secs < 60) {
    return `${secs} s`;
  }
  const mins = Math.floor(secs / 60);
  const rest = secs % 60;
  return rest === 0 ? `${mins} min` : `${mins} min ${rest} s`;
}
