import { describe, expect, it } from "vitest";

import { sparkPath } from "./geometry";
import { addPoint, emptyHistory, type History } from "./history";

const build = (series: [number, string][]): History => series.reduce((h, [t, m]) => addPoint(h, t, m), emptyHistory);

// the numbers of a path, in order
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
