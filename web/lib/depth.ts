// The numbers behind the depth chart, all exact. A side's cumulative size at a level is the sum of the sizes from the best price
// out to that level, in whole size ticks (BigInt). The top of the size axis is the smallest 1, 2 or 5 times a power of ten that
// is at least the largest cumulative size, and its ticks are exact fractions of it. Turning any of this into a pixel is the job
// of geometry.ts alone.

import { fromScaled, isPlainDecimal, scaleOf, toScaled } from "./ticks";
import type { ViewEvent } from "./types";

export type Level = {
  price: string;
  /** The size at this level, as the venue wrote it. */
  size: string;
  /** The total size from the best price out to this level, in size ticks. */
  cum: bigint;
};

/** The scale shared by every size of the book, or null when the sizes are not all plain, positive and of one scale. */
export function sizeScale(view: ViewEvent): number | null {
  const sizes = [...view.bids, ...view.asks].map(([, size]) => size);
  const first = sizes[0];
  if (first === undefined) {
    return null;
  }
  const scale = scaleOf(first);
  for (const size of sizes) {
    if (!isPlainDecimal(size) || scaleOf(size) !== scale || toScaled(size) === 0n) {
      return null;
    }
  }
  return scale;
}

/** cumulate lists a side's levels, best first, with the running total of size. */
export function cumulate(levels: readonly [string, string][]): Level[] {
  let total = 0n;
  return levels.map(([price, size]) => {
    total += toScaled(size);
    return { price, size, cum: total };
  });
}

/** niceTop is the smallest 1, 2 or 5 times a power of ten that is at least `max` (a positive whole number of size ticks). */
export function niceTop(max: bigint): bigint {
  if (max < 1n) {
    throw new Error("the largest cumulative size must be positive");
  }
  for (let power = 1n; ; power *= 10n) {
    for (const step of [1n, 2n, 5n]) {
      if (step * power >= max) {
        return step * power;
      }
    }
  }
}

/**
 * tickLabels gives the labels of the axis at a quarter, a half, three quarters and all of `top`, as exact decimals at the size scale
 * (two digits finer where a quarter needs them) with the zeros at the end removed: top 5 at scale 0 gives 1.25, 2.5, 3.75, 5.
 */
export function tickLabels(top: bigint, scale: number): string[] {
  return [1n, 2n, 3n, 4n].map((quarter) => {
    const label = fromScaled(top * 25n * quarter, scale + 2);
    return label.includes(".") ? label.replace(/0+$/, "").replace(/\.$/, "") : label;
  });
}
