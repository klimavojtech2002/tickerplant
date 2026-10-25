// The one module where a price becomes a pixel. Drawing needs plain numbers (coordinates), so the lint rule that forbids Number()
// and toFixed() is switched off for this file alone, and a test checks that it is off nowhere else. Nothing returned from here is
// ever shown as a price: the values drawn are mids, kept as exact decimal strings everywhere else; here they are only compared
// (in BigInt) and scaled to a position.

import { chartScale, cumulate, niceTop, type Level } from "./depth";
import type { History } from "./history";
import { toScaled } from "./ticks";
import type { ViewEvent } from "./types";

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

// The depth chart is drawn in a 1000 by 400 box. Prices run left to right from the lowest bid to the highest ask, and size runs up
// from the baseline to the top of the axis. Positions are worked out as shares in ten-thousandths from BigInt values, so the only
// float is the final coordinate with one decimal.
export const depthBox = { width: 1000, height: 400, top: 20, base: 392 } as const;

export type Steps = {
  /** The path of the step line, from the best price outward. */
  line: string;
  /** The same path closed down to the baseline. */
  area: string;
};

/** One level of the book as a place on the chart, with the exact numbers a reader may ask about. */
export type DepthPoint = {
  side: "bid" | "ask";
  /** Position in its side, 0 for the best price. */
  index: number;
  price: string;
  /** The total size from the best price out to this level, in size ticks. */
  cum: bigint;
  /** Where the level's step sits, in percent of the plot width and height. */
  left: number;
  top: number;
};

export type DepthShape = {
  bids: Steps | null;
  asks: Steps | null;
  /** The size at the top of the axis, in size ticks. */
  top: bigint;
  /** The size scale of the book. */
  scale: number;
  /** Where the mid sits, in percent of the plot width, or null when a side is missing. */
  midLeft: number | null;
  /** Where the four ticks (a quarter, a half, three quarters, all) sit, in percent of the plot height. */
  tickTops: number[];
  /** Every level, bids then asks, for finding the one under the pointer. */
  points: DepthPoint[];
};

const percent = (v: number) => Math.round(v * 1000) / 1000;

/**
 * depthShape draws the cumulative size of both sides as steps. It returns null when the book cannot be drawn (see chartScale). A
 * side with no levels is null in the result; with one price on the whole axis the steps sit in the middle.
 */
export function depthShape(view: ViewEvent): DepthShape | null {
  const scale = chartScale(view);
  if (scale === null) {
    return null;
  }
  const bids = cumulate(view.bids);
  const asks = cumulate(view.asks);
  const biggest = (levels: Level[]) => levels[levels.length - 1]?.cum ?? 0n;
  const most = biggest(bids) > biggest(asks) ? biggest(bids) : biggest(asks);
  const top = niceTop(most);

  const lowest = toScaled((bids[bids.length - 1] ?? asks[0])!.price);
  const highest = toScaled((asks[asks.length - 1] ?? bids[0])!.price);
  const spanTicks = highest - lowest;
  const { width, height, top: ceiling, base } = depthBox;

  // a share in ten-thousandths of the price span, then a coordinate with one decimal
  const x = (price: bigint): number => {
    const share = spanTicks === 0n ? 5000 : Number(((price - lowest) * 10000n) / spanTicks);
    return (share / 10000) * width;
  };
  const y = (cum: bigint): number => base - (Number((cum * 10000n) / top) / 10000) * (base - ceiling);
  const pt = (px: number, py: number) => `${px.toFixed(1)} ${py.toFixed(1)}`;

  const steps = (levels: Level[]): Steps | null => {
    const first = levels[0];
    const last = levels[levels.length - 1];
    if (first === undefined || last === undefined) {
      return null;
    }
    let line = `M ${pt(x(toScaled(first.price)), base)}`;
    let previous: number = base;
    for (const level of levels) {
      const lx = x(toScaled(level.price));
      line += ` L ${pt(lx, previous)} L ${pt(lx, y(level.cum))}`;
      previous = y(level.cum);
    }
    return { line, area: `${line} L ${pt(x(toScaled(last.price)), base)} Z` };
  };

  let midLeft: number | null = null;
  const bestBid = bids[0];
  const bestAsk = asks[0];
  if (bestBid !== undefined && bestAsk !== undefined) {
    // the mid is (bid + ask) / 2; in the same ticks as the prices it is (bid + ask) * 5 over ten
    const midTen = (toScaled(bestBid.price) + toScaled(bestAsk.price)) * 5n;
    // with both sides present the best bid is below the best ask, so the span is not zero
    const share = Number(((midTen - lowest * 10n) * 10000n) / (spanTicks * 10n));
    midLeft = percent(share / 100);
  }
  const tickTops = [1, 2, 3, 4].map((q) => percent(((base - (q / 4) * (base - ceiling)) / height) * 100));

  const place =
    (side: "bid" | "ask") =>
    (level: Level, index: number): DepthPoint => ({
      side,
      index,
      price: level.price,
      cum: level.cum,
      left: percent((x(toScaled(level.price)) / width) * 100),
      top: percent((y(level.cum) / height) * 100),
    });
  const points = [...bids.map(place("bid")), ...asks.map(place("ask"))];

  return { bids: steps(bids), asks: steps(asks), top, scale, midLeft, tickTops, points };
}

/**
 * nearestPoint is the level whose step is closest across the plot to a position given as a fraction of its width (0 at the left
 * edge, 1 at the right). A tie goes to the level nearer the best price, then to the bid. It is null for no levels.
 */
export function nearestPoint(points: readonly DepthPoint[], fraction: number): DepthPoint | null {
  let best: DepthPoint | null = null;
  let bestDistance = Infinity;
  for (const p of points) {
    const distance = Math.abs(p.left / 100 - fraction);
    if (distance < bestDistance || (distance === bestDistance && best !== null && p.index < best.index)) {
      best = p;
      bestDistance = distance;
    }
  }
  return best;
}
