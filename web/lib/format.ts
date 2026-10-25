// Display formatting for integer durations. Both functions cut digits off instead of
// rounding, so a figure never reads better than it is (1999 ns is "1.9 µs", not
// "2.0 µs"), and both use integer arithmetic only: the whole part is (n - n % d) / d and
// the digit comes from an integer remainder, so no result depends on float rounding.

const units: readonly { limit: number; div: number; name: string }[] = [
  { limit: 1_000_000, div: 1_000, name: "µs" },
  { limit: 1_000_000_000, div: 1_000_000, name: "ms" },
];

/**
 * formatLatency shows whole nanoseconds as µs, ms or s with one truncated decimal. Anything under a microsecond reads "<1 µs": the
 * clock and the histogram behind these numbers cannot resolve it reliably, so a figure such as "1 ns" would claim more than was measured.
 */
export function formatLatency(n: number): string {
  if (!Number.isSafeInteger(n) || n < 0) {
    return "n/a";
  }
  if (n < 1_000) {
    return "<1 µs";
  }
  for (const u of units) {
    if (n < u.limit) {
      return `${whole(n, u.div)}.${tenth(n, u.div)} ${u.name}`;
    }
  }
  return `${whole(n, 1_000_000_000)}.${tenth(n, 1_000_000_000)} s`;
}

/** formatAgeMs shows whole milliseconds as ms below a second, else seconds with a tenth. */
export function formatAgeMs(ms: number): string {
  if (!Number.isSafeInteger(ms) || ms < 0) {
    return "n/a";
  }
  if (ms < 1000) {
    return `${ms} ms`;
  }
  return `${whole(ms, 1000)}.${tenth(ms, 1000)} s`;
}

function whole(n: number, div: number): number {
  return (n - (n % div)) / div;
}

function tenth(n: number, div: number): number {
  return Math.floor((n % div) / (div / 10));
}
