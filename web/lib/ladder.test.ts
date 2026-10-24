import { describe, expect, it } from "vitest";

import { barShare, barShares, gap, trimZeros } from "./ladder";
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

describe("trimZeros", () => {
  it.each([
    ["0.50000000", ["0.5", "0000000"]],
    ["1.25000000", ["1.25", "000000"]],
    ["1.35823999", ["1.35823999", ""]],
    ["2.00000000", ["2", ".00000000"]],
    ["10.00", ["10", ".00"]],
    ["100", ["100", ""]],
    ["10", ["10", ""]],
    ["0.1", ["0.1", ""]],
    ["0.0", ["0", ".0"]],
    ["0", ["0", ""]],
  ])("%s", (size, want) => {
    expect(trimZeros(size)).toEqual(want);
  });

  it("never touches the whole part, and always joins back into the size, for random decimals", () => {
    const next = rng(5);
    for (let i = 0; i < 2000; i++) {
      const whole = String(next() % 1000) + "0".repeat(next() % 3);
      const fraction = next() % 4 === 0 ? "" : "." + String(next() % 100000).padStart(5, "0") + "0".repeat(next() % 4);
      const size = whole.replace(/^0+(?=\d)/, "") + fraction;
      const [kept, muted] = trimZeros(size);
      expect(kept + muted).toBe(size);
      expect(kept.split(".")[0]).toBe(size.split(".")[0]);
      expect(muted).toMatch(/^\.?0*$/);
      if (kept.includes(".")) {
        expect(kept.endsWith("0")).toBe(false);
      }
    }
  });
});

describe("barShare", () => {
  it.each([
    ["1.0", "1.0", 100],
    ["0.5", "1.0", 50],
    ["0.001", "1.000", 2], // a small size still shows
    ["0.0", "1.0", 0], // a zero size does not
    ["0.02", "1.00", 2],
    ["0.03", "1.00", 3],
  ])("%s of %s", (size, largest, want) => {
    expect(barShare(size, largest)).toBe(want);
  });
});

describe("barShares", () => {
  it("measures both sides against the largest size of the whole book", () => {
    const b = barShares(
      view(
        [
          ["10.0", "2.00"],
          ["9.9", "1.00"],
        ],
        [
          ["10.1", "4.00"],
          ["10.2", "0.04"],
        ],
      ),
    );
    expect(b.bids).toEqual([50, 25]);
    expect(b.asks).toEqual([100, 2]);
  });

  it("compares sizes of different scales by value, not by digits", () => {
    const b = barShares(view([["10.0", "5"]], [["10.1", "10.00"]]));
    expect(b.bids).toEqual([50]);
    expect(b.asks).toEqual([100]);
  });

  it("gives every bar 0 when every size is zero, and nothing for an empty side", () => {
    expect(barShares(view([["10.0", "0"]], [])).bids).toEqual([0]);
    expect(barShares(view([], [])).asks).toEqual([]);
  });
});

describe("gap", () => {
  it("gives the exact spread and the mid one digit finer", () => {
    expect(gap(view([["62721.7", "1"]], [["62721.8", "1"]]))).toEqual({ spread: "0.1", mid: "62721.75" });
  });
  it("works at scale 0", () => {
    expect(gap(view([["100", "1"]], [["103", "1"]]))).toEqual({ spread: "3", mid: "101.5" });
  });
  it.each([
    ["a bid side that is empty", view([], [["10.1", "1"]])],
    ["an ask side that is empty", view([["10.0", "1"]], [])],
    ["prices of different scales", view([["10.0", "1"]], [["10.15", "1"]])],
    ["a crossed book", view([["10.2", "1"]], [["10.1", "1"]])],
    ["a locked book", view([["10.1", "1"]], [["10.1", "1"]])],
  ])("is null for %s", (_name, v) => {
    expect(gap(v)).toBeNull();
  });
});
