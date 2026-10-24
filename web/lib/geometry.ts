// The one module where a price becomes a pixel. Drawing needs plain numbers (coordinates), so the lint rule that forbids Number()
// and toFixed() is switched off for this file alone, and a test checks that it is off nowhere else. Nothing returned from here is
// ever shown as a price: the values drawn are mids, kept as exact decimal strings everywhere else; here they are only compared
// (in BigInt) and scaled to a position.

import type { History } from "./history";
import { toScaled } from "./ticks";

export type Spark = {
  /** SVG path of the line. */
  line: string;
  /** The same path closed down to the baseline, for the soft fill under the line. */
  area: string;
};

/**
 * sparkPath draws the mids of a history into a width x height box with a margin. The x position comes from each point's own time,
 * the y position from the mid's place between the lowest and highest mid; with all mids equal the line is drawn in the middle.
 * It returns null with fewer than two points: there is no line to draw.
 */
export function sparkPath(h: History, width: number, height: number, margin = 4): Spark | null {
  const pts = h.points;
  const first = pts[0];
  const last = pts[pts.length - 1];
  if (first === undefined || last === undefined || pts.length < 2) {
    return null;
  }
  const values = pts.map((p) => toScaled(p.mid));
  let lo = values[0]!;
  let hi = values[0]!;
  for (const v of values) {
    if (v < lo) lo = v;
    if (v > hi) hi = v;
  }
  const range = hi - lo;
  const t0 = first.t;
  const dt = Math.max(1, last.t - t0);
  const innerH = height - 2 * margin;
  const coords = pts.map((p, i) => {
    const x = ((p.t - t0) / dt) * width;
    // a whole-thousandths share of the range, from BigInt, then to a position
    const share = range === 0n ? 0.5 : Number(((values[i]! - lo) * 1000n) / range) / 1000;
    const y = height - margin - share * innerH;
    return `${x.toFixed(1)} ${y.toFixed(1)}`;
  });
  const line = `M ${coords.join(" L ")}`;
  return { line, area: `${line} L ${width} ${height} L 0 ${height} Z` };
}
