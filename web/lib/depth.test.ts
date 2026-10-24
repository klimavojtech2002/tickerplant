import { describe, expect, it } from "vitest";

import { cumulate, niceTop, sizeScale, tickLabels } from "./depth";
import type { ViewEvent } from "./types";

const view = (bids: [string, string][], asks: [string, string][]): ViewEvent => ({
  venue: "v",
  symbol: "s",
  seq: 1,
  bids,
  asks,
});

function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
    return s;
  };
}

describe("sizeScale", () => {
  it("is the one scale of every size", () => {
    expect(sizeScale(view([["10", "1.50"]], [["11", "0.25"]]))).toBe(2);
    expect(sizeScale(view([["10", "3"]], []))).toBe(0);
  });
  it.each([
    ["sizes of two scales", view([["10", "1.5"]], [["11", "1.25"]])],
    ["a zero size", view([["10", "0.0"]], [["11", "1.0"]])],
    ["no levels", view([], [])],
    ["an exponent", view([["10", "1e3"]], [])],
  ])("is null for %s", (_name, v) => {
    expect(sizeScale(v)).toBeNull();
  });
});

describe("cumulate", () => {
  it("adds the sizes from the best price outward, exactly", () => {
    const levels = cumulate([
      ["100.0", "1.5"],
      ["99.9", "0.25"],
      ["99.8", "2.0"],
    ]);
    expect(levels.map((l) => l.cum)).toEqual([15n, 40n, 60n]);
    expect(levels.map((l) => l.price)).toEqual(["100.0", "99.9", "99.8"]);
    expect(levels.map((l) => l.size)).toEqual(["1.5", "0.25", "2.0"]);
  });

  it("is exact beyond what a float holds", () => {
    const levels = cumulate([
      ["1", "90071992547409.93"],
      ["2", "0.01"],
    ]);
    expect(levels[1]!.cum).toBe(9007199254740994n);
  });

  it("matches a plain running sum on random books", () => {
    const next = rng(9);
    for (let i = 0; i < 200; i++) {
      const sizes = Array.from({ length: 1 + (next() % 12) }, () => 1n + BigInt(next() % 100000));
      const levels = cumulate(
        sizes.map((s, k): [string, string] => [
          String(1000 - k),
          `${s / 100n}.${(s % 100n).toString().padStart(2, "0")}`,
        ]),
      );
      let total = 0n;
      sizes.forEach((s, k) => {
        total += s;
        expect(levels[k]!.cum).toBe(total);
      });
    }
  });

  it("is empty for a side with no levels", () => {
    expect(cumulate([])).toEqual([]);
  });
});

describe("niceTop", () => {
  it.each([
    [1n, 1n],
    [2n, 2n],
    [3n, 5n],
    [5n, 5n],
    [6n, 10n],
    [10n, 10n],
    [11n, 20n],
    [20n, 20n],
    [21n, 50n],
    [50n, 50n],
    [51n, 100n],
    [999n, 1000n],
    [1000n, 1000n],
    [1001n, 2000n],
    [90071992547409930n, 100000000000000000n],
  ])("%s -> %s", (max, want) => {
    expect(niceTop(max)).toBe(want);
  });

  it("is the smallest 1, 2 or 5 times a power of ten at least the maximum, checked against a brute force", () => {
    const candidates: bigint[] = [];
    for (let p = 1n; p <= 10_000_000n; p *= 10n) {
      candidates.push(p, 2n * p, 5n * p);
    }
    for (let max = 1n; max <= 12_000n; max++) {
      const want = candidates.find((c) => c >= max)!;
      expect(niceTop(max)).toBe(want);
    }
  });

  it("refuses zero, which a book of positive sizes cannot give", () => {
    expect(() => niceTop(0n)).toThrow();
  });
});

describe("tickLabels", () => {
  it.each([
    [1n, 0, ["0.25", "0.5", "0.75", "1"]],
    [2n, 0, ["0.5", "1", "1.5", "2"]],
    [5n, 0, ["1.25", "2.5", "3.75", "5"]],
    [10n, 0, ["2.5", "5", "7.5", "10"]],
    [100n, 0, ["25", "50", "75", "100"]],
    [1000n, 0, ["250", "500", "750", "1000"]],
    [50n, 2, ["0.125", "0.25", "0.375", "0.5"]],
    [1n, 8, ["0.0000000025", "0.000000005", "0.0000000075", "0.00000001"]],
    [100000000n, 8, ["0.25", "0.5", "0.75", "1"]],
  ])("top %s at scale %s", (top, scale, want) => {
    expect(tickLabels(top, scale)).toEqual(want);
  });

  it("is exact: four times the first label is the last, for every top and scale", () => {
    for (const scale of [0, 2, 8]) {
      for (const max of [1n, 7n, 123n, 4567n, 99999n]) {
        const top = niceTop(max);
        const labels = tickLabels(top, scale);
        const digits = (s: string) =>
          BigInt(s.replace(".", "")) * 10n ** BigInt(scale + 2 - (s.includes(".") ? s.length - s.indexOf(".") - 1 : 0));
        expect(digits(labels[0]!) * 4n).toBe(digits(labels[3]!));
        expect(digits(labels[1]!)).toBe(digits(labels[0]!) * 2n);
        expect(digits(labels[2]!)).toBe(digits(labels[0]!) * 3n);
      }
    }
  });
});
