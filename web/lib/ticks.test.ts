import { describe, expect, it } from "vitest";

import { barPct, crossed, fromScaled, maxSize, scaleOf, spread, toScaled } from "./ticks";

describe("toScaled", () => {
  it("keeps the venue's digits exactly", () => {
    // 1.35823999 at scale 8 is 135823999 ticks — a float64 path would already be
    // suspect here, and hopeless for 19-digit sizes below.
    expect(toScaled("62721.7")).toBe(627217n);
    expect(toScaled("1.35823999")).toBe(135823999n);
    expect(toScaled("1000")).toBe(1000n);
    expect(toScaled("0.00000001")).toBe(1n);
    // beyond 2^53: the exact reason JS numbers are banned from this path
    expect(toScaled("92233720368.54775807")).toBe(9223372036854775807n);
  });
  it("rejects anything but a plain decimal", () => {
    for (const bad of ["", "1e5", "-1", "1.", ".5", "45284.x", "NaN", " 1"]) {
      expect(() => toScaled(bad), bad).toThrow();
    }
  });
});

describe("fromScaled / scaleOf", () => {
  it("round-trips the wire format", () => {
    for (const s of ["62721.7", "0.00114202", "1000", "0.5"]) {
      expect(fromScaled(toScaled(s), scaleOf(s))).toBe(s);
    }
  });
  it("pads small values at wide scales", () => {
    expect(fromScaled(1n, 8)).toBe("0.00000001");
    expect(fromScaled(0n, 2)).toBe("0.00");
    expect(fromScaled(7n, 0)).toBe("7");
  });
});

describe("crossed", () => {
  it("mirrors the backend boundary: equal prices are crossed", () => {
    expect(crossed("100.1", "100.2")).toBe(false);
    expect(crossed("100.2", "100.2")).toBe(true);
    expect(crossed("100.3", "100.2")).toBe(true);
  });
});

describe("spread", () => {
  it("is exact at the prices' own scale", () => {
    expect(spread("62721.7", "62721.8")).toBe("0.1");
    expect(spread("1000", "1002")).toBe("2");
    // 0.1 + 0.2 territory: floats would give 0.30000000000000004
    expect(spread("0.1", "0.4")).toBe("0.3");
  });
});

describe("barPct / maxSize", () => {
  it("shares are integer percents computed in BigInt", () => {
    expect(barPct("50", "100")).toBe(50);
    expect(barPct("100", "100")).toBe(100);
    expect(barPct("1", "3")).toBe(33);
    expect(barPct("0", "100")).toBe(0);
    expect(barPct("5", "0")).toBe(0); // an empty side cannot divide by zero
  });
  it("maxSize compares exactly, not lexically", () => {
    // lexical string compare would call "9" the largest
    expect(
      maxSize([
        ["1", "9"],
        ["2", "10"],
      ]),
    ).toBe("10");
    expect(maxSize([])).toBe("0");
  });
});
