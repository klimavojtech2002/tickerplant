import { describe, expect, it } from "vitest";

import { mid, midChange } from "./mid";
import { fromScaled, scaleOf, toScaled } from "./ticks";

// a small seeded generator, so a failure repeats
function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
    return s;
  };
}

describe("mid", () => {
  const cases: [string, string | undefined, string | undefined, string | null][] = [
    ["one tick apart", "62721.7", "62721.8", "62721.75"],
    ["the same price", "100.00", "100.00", "100.000"],
    ["an even sum has a trailing zero", "10.0", "10.2", "10.10"],
    ["scale 0 gains one digit", "100", "103", "101.5"],
    ["scale 0 and a whole mid", "100", "102", "101.0"],
    ["zero prices", "0", "0", "0.0"],
    ["a high scale", "0.00000001", "0.00000002", "0.000000015"],
    ["more than 2^53 ticks", "90071992547409.93", "90071992547409.95", "90071992547409.940"],
    ["no bid", undefined, "1.0", null],
    ["no ask", "1.0", undefined, null],
    ["different scales", "1.0", "1.00", null],
    ["a price that is not a plain decimal", "1,0", "2.0", null],
    ["a negative price", "-1.0", "2.0", null],
    ["an empty price", "", "2.0", null],
  ];
  it.each(cases)("%s", (_name, bid, ask, want) => {
    expect(mid(bid, ask)).toBe(want);
  });

  it("is exactly (bid + ask) / 2 for random prices at several scales, against an independent oracle", () => {
    const next = rng(7);
    for (const scale of [0, 1, 2, 5, 8, 12]) {
      for (let i = 0; i < 200; i++) {
        const b = BigInt(next()) * BigInt(next() % 1000);
        const a = b + BigInt(next() % 100_000);
        const bid = fromScaled(b, scale);
        const ask = fromScaled(a, scale);
        const got = mid(bid, ask)!;
        expect(scaleOf(got)).toBe(scale + 1);
        // the oracle: 2 * mid must equal bid + ask, with no rounding anywhere
        expect(toScaled(got) * 2n).toBe((b + a) * 10n);
      }
    }
  });
});

describe("midChange", () => {
  const cases: [string, string, string, string | null][] = [
    ["a rise", "62721.40", "62724.75", "+3.35"],
    ["a fall", "62724.75", "62721.40", "−3.35"],
    ["no change keeps the scale and has no sign", "10.000", "10.000", "0.000"],
    ["a sub-unit fall at scale 3", "0.010", "0.005", "−0.005"],
    ["a large fall", "100000.00", "1.00", "−99999.00"],
    ["scale 0", "5", "9", "+4"],
    ["different scales", "1.0", "1.00", null],
    ["not a decimal", "x", "1.0", null],
  ];
  it.each(cases)("%s", (_name, oldest, newest, want) => {
    expect(midChange(oldest, newest)).toBe(want);
  });

  it("never prints a negative zero or a plus on zero", () => {
    expect(midChange("1.00", "1.00")).toBe("0.00");
    expect(midChange("1.00", "1.00")).not.toMatch(/[+−-]/);
  });
});
