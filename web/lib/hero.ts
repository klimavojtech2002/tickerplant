// Small pure helpers for the hero: which chip names the source, what the base currency of a symbol is, how many ticks the spread is,
// and the clock time a book was received.

import type { PollState } from "./status";
import { isPlainDecimal, scaleOf, toScaled } from "./ticks";

export type Chip = "none" | "demo" | "demo-chaos" | "unknown";

/**
 * modeChip says what the source of the numbers is, honestly: a synthetic feed is a demo (and says when faults are injected), a live
 * feed needs no chip, and until the status says which it is (not answered yet, or a mode the page does not know) the chip says so
 * instead of leaving the viewer to assume live data. The last good status is trusted while the status poll is failing.
 */
export function modeChip(poll: PollState): Chip {
  const status = poll.kind === "loading" ? null : poll.status;
  if (status === null) {
    return "unknown";
  }
  switch (status.mode) {
    case "synthetic":
      return status.chaos ? "demo-chaos" : "demo";
    case "live":
      return "none";
    default:
      return "unknown";
  }
}

/** baseCurrency is the traded asset of a "BASE/QUOTE" symbol, or null when the symbol has no such form. */
export function baseCurrency(symbol: string): string | null {
  const slash = symbol.indexOf("/");
  return slash > 0 ? symbol.slice(0, slash) : null;
}

/** spreadTicks is the spread in whole price ticks (the smallest step of the price), as a string; null if either price is unusable or the two have different scales. */
export function spreadTicks(bid: string | undefined, ask: string | undefined): string | null {
  if (
    bid === undefined ||
    ask === undefined ||
    !isPlainDecimal(bid) ||
    !isPlainDecimal(ask) ||
    scaleOf(bid) !== scaleOf(ask)
  ) {
    return null;
  }
  return (toScaled(ask) - toScaled(bid)).toString();
}

/** formatClock shows a time in the visitor's own time zone as HH:MM:SS. */
export function formatClock(
  ms: number,
  tz?: { getHours(): number; getMinutes(): number; getSeconds(): number },
): string {
  const d = tz ?? new Date(ms);
  const two = (n: number) => String(n).padStart(2, "0");
  return `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;
}
