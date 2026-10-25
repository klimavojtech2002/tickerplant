import { describe, expect, it } from "vitest";

import { formatAgeMs, formatLatency } from "./format";

describe("formatLatency", () => {
  // Literal expectations: the rule is truncation, so 1050 is "1.0 µs", never "1.1 µs".
  const rows: [number, string][] = [
    [0, "<1 µs"],
    [1, "<1 µs"],
    [999, "<1 µs"],
    [1000, "1.0 µs"],
    [1050, "1.0 µs"],
    [1099, "1.0 µs"],
    [1100, "1.1 µs"],
    [1986, "1.9 µs"],
    [999_999, "999.9 µs"],
    [1_000_000, "1.0 ms"],
    [999_999_999, "999.9 ms"],
    [1_000_000_000, "1.0 s"],
    [2_500_000_000, "2.5 s"],
    [Number.MAX_SAFE_INTEGER, "9007199.2 s"],
    // The largest remainder just below a unit boundary must not carry into the next unit.
    [9_007_198_999_999_999, "9007198.9 s"],
  ];
  it.each(rows)("%d ns -> %s", (n, want) => {
    expect(formatLatency(n)).toBe(want);
  });

  it.each([-1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])("refuses %d", (n) => {
    expect(formatLatency(n)).toBe("n/a");
  });
});

describe("formatAgeMs", () => {
  const rows: [number, string][] = [
    [0, "0 ms"],
    [999, "999 ms"],
    [1000, "1.0 s"],
    [1500, "1.5 s"],
    [1999, "1.9 s"],
    [61_000, "61.0 s"],
  ];
  it.each(rows)("%d ms -> %s", (n, want) => {
    expect(formatAgeMs(n)).toBe(want);
  });
  it("refuses a negative or fractional age", () => {
    expect(formatAgeMs(-1)).toBe("n/a");
    expect(formatAgeMs(0.5)).toBe("n/a");
  });
});
