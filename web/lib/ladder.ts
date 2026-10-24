// What the order book table needs besides the venue's strings: the muted trailing zeros of a size, the share a bar fills, and the
// spread and mid for the row between the two sides. Prices and sizes stay the venue's text; every function here that returns a
// number returns a small integer (a percent), computed in BigInt.

import { mid } from "./mid";
import { barPct, crossed, isPlainDecimal, scaleOf, spread, toScaled } from "./ticks";
import type { ViewEvent } from "./types";

/**
 * trimZeros splits a size into the part to read and the trailing zeros of its fraction, which are drawn lighter:
 * Kept and muted always join back into the size, and only zeros after the point move: "0.50000000" is ["0.5", "0000000"],
 * "1.25000000" is ["1.25", "000000"], "2.00000000" is ["2", ".00000000"], "10" is ["10", ""].
 */
export function trimZeros(size: string): [kept: string, muted: string] {
  const dot = size.indexOf(".");
  if (dot === -1) {
    return [size, ""];
  }
  let end = size.length;
  while (end > dot + 1 && size[end - 1] === "0") {
    end--;
  }
  if (end === dot + 1) {
    return [size.slice(0, dot), size.slice(dot)]; // an all-zero fraction: the point goes with the zeros
  }
  return [size.slice(0, end), size.slice(end)];
}

/** padTo writes a size with at least `scale` fraction digits by adding zeros, so sizes of two scales can be compared. */
function padTo(size: string, scale: number): string {
  const have = scaleOf(size);
  if (have >= scale) {
    return size;
  }
  return (have === 0 ? `${size}.` : size) + "0".repeat(scale - have);
}

/**
 * barShare is the width of a size's bar as a whole percent of the largest size in the book (both written at one scale), at
 * least 2 so a small size still shows, and 0 for a size of zero.
 */
export function barShare(size: string, largest: string): number {
  if (toScaled(size) === 0n) {
    return 0;
  }
  return Math.max(2, barPct(size, largest));
}

/** The bars of both sides against the largest size of the whole book; sizes of different scales are compared fairly. */
export function barShares(view: ViewEvent): { bids: number[]; asks: number[] } {
  const all = [...view.bids, ...view.asks].map(([, size]) => size);
  const scale = all.reduce((m, s) => Math.max(m, scaleOf(s)), 0);
  let largest = "0";
  let largestValue = 0n;
  for (const size of all) {
    const padded = padTo(size, scale);
    const value = toScaled(padded);
    if (value > largestValue) {
      largestValue = value;
      largest = padded;
    }
  }
  const share = ([, size]: [string, string]) => (largestValue === 0n ? 0 : barShare(padTo(size, scale), largest));
  return { bids: view.bids.map(share), asks: view.asks.map(share) };
}

export type Gap = { spread: string; mid: string };

/**
 * gap is the spread and the mid for the row between the sides, or null when either is not defined: a side is empty, the two best
 * prices have different scales, or the book is crossed.
 */
export function gap(view: ViewEvent): Gap | null {
  const bid = view.bids[0]?.[0];
  const ask = view.asks[0]?.[0];
  if (bid === undefined || ask === undefined || !isPlainDecimal(bid) || !isPlainDecimal(ask)) {
    return null;
  }
  if (crossed(bid, ask)) {
    return null;
  }
  const m = mid(bid, ask); // null when the two prices have different scales
  return m === null ? null : { spread: spread(bid, ask), mid: m };
}
