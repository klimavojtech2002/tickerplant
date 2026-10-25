import { describe, expect, it } from "vitest";

import { depthBox, depthShape, nearestPoint, sparkPath } from "./geometry";
import { addPoint, emptyHistory, type History } from "./history";
import type { ViewEvent } from "./types";

const build = (series: [number, string][]): History => series.reduce((h, [t, m]) => addPoint(h, t, m), emptyHistory);

// the numbers of a path, in order
// a coordinate with one decimal, as whole tenths
const tenths = (s: string) => BigInt(s.replace(".", ""));
const nums = (path: string) => [...path.matchAll(/-?\d+(?:\.\d+)?/g)].map((m) => m[0]);

describe("sparkPath", () => {
  it("draws nothing for fewer than two points", () => {
    expect(sparkPath(emptyHistory, 100, 40)).toBeNull();
    expect(sparkPath(build([[0, "1.00"]]), 100, 40)).toBeNull();
  });

  it("puts the lowest mid on the floor of the box and the highest on the ceiling, within the margin", () => {
    const p = sparkPath(
      build([
        [0, "10.00"],
        [1000, "20.00"],
        [2000, "15.00"],
      ]),
      100,
      40,
      4,
    )!;
    const [x0, y0, x1, y1, x2, y2] = nums(p.line);
    expect([x0, x1, x2]).toEqual(["0.0", "50.0", "100.0"]); // by time, not by count
    expect(y0).toBe("36.0"); // lowest: height - margin
    expect(y1).toBe("4.0"); // highest: margin
    expect(y2).toBe("20.0"); // halfway
  });

  it("places points by their own times, so a gap in time is a gap on the line", () => {
    const p = sparkPath(
      build([
        [0, "1.0"],
        [1000, "2.0"],
        [9000, "1.5"],
      ]),
      90,
      20,
    )!;
    const xs = [nums(p.line)[0], nums(p.line)[2], nums(p.line)[4]];
    expect(xs).toEqual(["0.0", "10.0", "90.0"]);
  });

  it("draws a flat line through the middle when every mid is equal", () => {
    const p = sparkPath(
      build([
        [0, "5.00"],
        [1000, "5.00"],
      ]),
      100,
      40,
      4,
    )!;
    const ys = [nums(p.line)[1], nums(p.line)[3]];
    expect(ys).toEqual(["20.0", "20.0"]);
  });

  it("closes the area down to the baseline", () => {
    const p = sparkPath(
      build([
        [0, "1.0"],
        [1000, "2.0"],
      ]),
      100,
      40,
    )!;
    expect(p.area.startsWith(p.line)).toBe(true);
    expect(p.area.endsWith("L 100 40 L 0 40 Z")).toBe(true);
  });

  it("orders mids exactly, beyond what a float can tell apart", () => {
    const p = sparkPath(
      build([
        [0, "90071992547409.930"],
        [1000, "90071992547409.935"],
      ]),
      100,
      40,
      4,
    )!;
    const [, y0, , y1] = nums(p.line);
    expect(y0).toBe("36.0");
    expect(y1).toBe("4.0");
  });
});

const book = (bids: [string, string][], asks: [string, string][]): ViewEvent => ({
  venue: "v",
  symbol: "s",
  seq: 1,
  bids,
  asks,
});

describe("depthShape", () => {
  const two = book(
    [
      ["100.0", "1.0"],
      ["99.0", "1.0"],
    ],
    [
      ["101.0", "2.0"],
      ["103.0", "1.0"],
    ],
  );

  it("steps each side outward from its best price, with price along x and cumulative size up y", () => {
    const d = depthShape(two)!;
    // x: prices 99.0 .. 103.0 over 0..1000; y: sizes 10, 20, 30 ticks of an axis topped at 50
    expect(d.bids!.line).toBe("M 250.0 392.0 L 250.0 392.0 L 250.0 317.6 L 0.0 317.6 L 0.0 243.2");
    expect(d.asks!.line).toBe("M 500.0 392.0 L 500.0 392.0 L 500.0 243.2 L 1000.0 243.2 L 1000.0 168.8");
  });

  it("closes each area down to the baseline at the far end of its side", () => {
    const d = depthShape(two)!;
    expect(d.bids!.area).toBe(`${d.bids!.line} L 0.0 392.0 Z`);
    expect(d.asks!.area).toBe(`${d.asks!.line} L 1000.0 392.0 Z`);
  });

  it("tops the axis at a round number and places the four ticks evenly", () => {
    const d = depthShape(two)!;
    expect(d.top).toBe(50n);
    expect(d.scale).toBe(1);
    expect(d.tickTops).toEqual([74.75, 51.5, 28.25, 5]);
  });

  it("puts the mid exactly halfway between the best prices on the price axis", () => {
    expect(depthShape(two)!.midLeft).toBe(37.5); // 100.5 on 99.0 .. 103.0
  });

  it("draws only the side that is there, and has no mid", () => {
    const d = depthShape(
      book(
        [
          ["100.0", "1.0"],
          ["99.0", "3.0"],
        ],
        [],
      ),
    )!;
    expect(d.asks).toBeNull();
    expect(d.midLeft).toBeNull();
    expect(d.bids!.line).toBe("M 1000.0 392.0 L 1000.0 392.0 L 1000.0 317.6 L 0.0 317.6 L 0.0 94.4"); // sizes 10 and 40 of a 50 axis
    const asksOnly = depthShape(
      book(
        [],
        [
          ["101.0", "1.0"],
          ["102.0", "1.0"],
        ],
      ),
    )!;
    expect(asksOnly.bids).toBeNull();
    expect(asksOnly.asks!.line.startsWith("M 0.0 392.0")).toBe(true);
  });

  it("puts a lone level in the middle of the axis instead of dividing by zero", () => {
    const d = depthShape(book([["100.0", "1.0"]], []))!;
    expect(d.bids!.line).toBe("M 500.0 392.0 L 500.0 392.0 L 500.0 20.0"); // the whole axis, 10 of 10
  });

  it("never draws above the top of the box or below the baseline", () => {
    const d = depthShape(two)!;
    const ys = [...(d.bids!.line + d.asks!.line).matchAll(/ (\d+\.\d) (?=L|$)/g)].map((m) => tenths(m[1]!));
    for (const y of ys) {
      expect(y).toBeGreaterThanOrEqual(BigInt(depthBox.top) * 10n);
      expect(y).toBeLessThanOrEqual(BigInt(depthBox.base) * 10n);
    }
  });

  it("keeps the steps ordered for cumulative sizes beyond 2^53", () => {
    const d = depthShape(
      book(
        [
          ["10", "90071992547409.93"],
          ["9", "40000000000000.00"],
        ],
        [["11", "50000000000000.01"]],
      ),
    )!;
    const ys = [...d.bids!.line.matchAll(/ (\d+\.\d) (?=L|$)/g)].map((m) => tenths(m[1]!));
    for (let i = 1; i < ys.length; i++) {
      expect(ys[i]! <= ys[i - 1]!).toBe(true);
    }
    expect(d.top).toBe(20000000000000000n);
  });

  it.each([
    ["an empty book", book([], [])],
    ["a zero size", book([["100.0", "0.0"]], [["101.0", "1.0"]])],
    ["sizes of two scales", book([["100.0", "1.0"]], [["101.0", "1.00"]])],
    ["prices of two scales", book([["100.0", "1.0"]], [["101.00", "1.0"]])],
    ["a price that is not a plain decimal", book([["1e2", "1.0"]], [["101.0", "1.0"]])],
    ["a crossed book", book([["102.0", "1.0"]], [["101.0", "1.0"]])],
    ["a locked book", book([["101.0", "1.0"]], [["101.0", "1.0"]])],
  ])("cannot draw %s", (_name, v) => {
    expect(depthShape(v)).toBeNull();
  });
});

describe("depthShape points and nearestPoint", () => {
  const two = book(
    [
      ["100.0", "1.0"],
      ["99.0", "1.0"],
    ],
    [
      ["101.0", "2.0"],
      ["103.0", "1.0"],
    ],
  );

  it("lists every level with its exact running size and its place on the chart", () => {
    const { points } = depthShape(two)!;
    expect(points.map((p) => [p.side, p.index, p.price, p.cum, p.left])).toEqual([
      ["bid", 0, "100.0", 10n, 25],
      ["bid", 1, "99.0", 20n, 0],
      ["ask", 0, "101.0", 20n, 50],
      ["ask", 1, "103.0", 30n, 100],
    ]);
    expect(points[0]!.top).toBe(79.4); // 317.6 of 400
  });

  it("finds the level nearest across the plot", () => {
    const { points } = depthShape(two)!;
    expect(nearestPoint(points, 0.01)!.price).toBe("99.0");
    expect(nearestPoint(points, 0.3)!.price).toBe("100.0");
    expect(nearestPoint(points, 0.45)!.price).toBe("101.0");
    expect(nearestPoint(points, 0.99)!.price).toBe("103.0");
  });

  it("clamps a position outside the plot to the nearest edge level", () => {
    const { points } = depthShape(two)!;
    expect(nearestPoint(points, -3)!.price).toBe("99.0");
    expect(nearestPoint(points, 7)!.price).toBe("103.0");
  });

  it("breaks a tie toward the level nearer the best price", () => {
    const { points } = depthShape(
      book(
        [
          ["100.0", "1.0"],
          ["98.0", "1.0"],
        ],
        [
          ["104.0", "1.0"],
          ["106.0", "1.0"],
        ],
      ),
    )!;
    // x shares: 98 -> 0, 100 -> .25, 104 -> .75, 106 -> 1; 0.5 is equally far from 100 and 104, both index 0, so the bid wins
    expect(nearestPoint(points, 0.5)!.price).toBe("100.0");
  });

  it("breaks a tie between levels of different rank toward the one nearer the best price", () => {
    const { points } = depthShape(
      book(
        [
          ["100.0", "1.0"],
          ["98.0", "1.0"],
        ],
        [
          ["102.0", "1.0"],
          ["106.0", "1.0"],
        ],
      ),
    )!;
    // 98 is at 0, 100 at .25: .125 is equally far from the second bid and the best bid
    expect(nearestPoint(points, 0.125)!.price).toBe("100.0");
  });

  it("is null for no levels", () => {
    expect(nearestPoint([], 0.5)).toBeNull();
  });
});
