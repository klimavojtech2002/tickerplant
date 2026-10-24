// The mid price and its change, exactly. A mid is halfway between the best bid and the best ask, so it needs one more decimal
// than the prices: (bid + ask) / 2 at scale s is (bid + ask) * 5 at scale s + 1. Everything here is BigInt arithmetic on the
// venue's decimal strings; no number is ever a float.

import { fromScaled, isPlainDecimal, scaleOf, toScaled } from "./ticks";

/**
 * mid returns the mid price of the best bid and best ask as a decimal string one digit finer than the prices, or null when it is
 * not defined: a side is missing, a price is not a plain decimal, or the two prices have different scales.
 */
export function mid(bid: string | undefined, ask: string | undefined): string | null {
  if (bid === undefined || ask === undefined || !isPlainDecimal(bid) || !isPlainDecimal(ask)) {
    return null;
  }
  const scale = scaleOf(bid);
  if (scaleOf(ask) !== scale) {
    return null;
  }
  return fromScaled((toScaled(bid) + toScaled(ask)) * 5n, scale + 1);
}

/**
 * midChange is newest minus oldest as a signed decimal at the mids' own scale: "+3.35", "−0.5" (a real minus sign), or "0.00"
 * for no change. It is null when either mid is missing or the scales differ.
 */
export function midChange(oldest: string, newest: string): string | null {
  if (!isPlainDecimal(oldest) || !isPlainDecimal(newest)) {
    return null;
  }
  const scale = scaleOf(oldest);
  if (scaleOf(newest) !== scale) {
    return null;
  }
  const diff = toScaled(newest) - toScaled(oldest);
  if (diff === 0n) {
    return fromScaled(0n, scale);
  }
  const magnitude = fromScaled(diff < 0n ? -diff : diff, scale);
  return diff < 0n ? `\u2212${magnitude}` : `+${magnitude}`;
}
