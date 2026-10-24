import { describe, expect, it } from "vitest";

import {
  addPoint,
  change,
  collecting,
  emptyHistory,
  enoughPoints,
  formatSpan,
  maxPoints,
  minGapMs,
  span,
  windowMs,
  type History,
} from "./history";

const build = (times: number[], mid = (i: number) => `100.${String(i % 90).padStart(2, "0")}`): History =>
  times.reduce((h, t, i) => addPoint(h, t, mid(i)), emptyHistory);

describe("addPoint", () => {
  it("starts with the first point", () => {
    const h = addPoint(emptyHistory, 1000, "1.00");
    expect(h.points).toEqual([{ t: 1000, mid: "1.00" }]);
  });

  it("lets a point closer than the minimum gap replace the last mid but keep its time, and adds one at the gap", () => {
    const h = build([1000]);
    expect(addPoint(h, 1000 + minGapMs - 1, "9.99").points).toEqual([{ t: 1000, mid: "9.99" }]);
    expect(addPoint(h, 1000 + minGapMs - 1, h.points[0]!.mid)).toBe(h); // nothing changed
    expect(addPoint(h, 1000 + minGapMs, "9.99").points).toHaveLength(2);
  });

  it("drops points older than the window and keeps one exactly at its edge", () => {
    const h = build([0, 500, 1000]);
    const later = addPoint(h, windowMs + 500, "5.55"); // oldest kept: 500
    expect(later.points.map((p) => p.t)).toEqual([500, 1000, windowMs + 500]);
    const edge = addPoint(h, windowMs + 1000, "5.55"); // oldest kept: 1000
    expect(edge.points.map((p) => p.t)).toEqual([1000, windowMs + 1000]);
  });

  it("keeps at most the cap, dropping the oldest first", () => {
    const times = Array.from({ length: maxPoints + 20 }, (_, i) => i * minGapMs);
    const h = build(times);
    expect(h.points.length).toBeLessThanOrEqual(maxPoints);
    expect(h.points[h.points.length - 1]!.t).toBe(times[times.length - 1]);
  });

  it("starts over when time goes backwards, so a line never runs in reverse", () => {
    const h = build([5000, 6000, 7000]);
    expect(addPoint(h, 100, "2.00").points).toEqual([{ t: 100, mid: "2.00" }]);
  });

  it("starts over when the scale of the mids changes", () => {
    const h = build([1000, 2000]);
    expect(addPoint(h, 3000, "100.123").points).toEqual([{ t: 3000, mid: "100.123" }]);
  });

  it("does not change the history it was given", () => {
    const h = build([1000, 2000]);
    const before = JSON.stringify(h);
    addPoint(h, 3000, "3.00");
    expect(JSON.stringify(h)).toBe(before);
  });
});

describe("collecting, change and span", () => {
  it("is collecting under the enough-points count", () => {
    expect(collecting(emptyHistory)).toBe(true);
    expect(collecting(build(Array.from({ length: enoughPoints - 1 }, (_, i) => i * 1000)))).toBe(true);
    expect(collecting(build(Array.from({ length: enoughPoints }, (_, i) => i * 1000)))).toBe(false);
  });

  it("has no change with fewer than two points, and the exact change after that", () => {
    expect(change(emptyHistory)).toBeNull();
    expect(change(build([0]))).toBeNull();
    const h = build([0, 1000, 2000], (i) => ["10.00", "10.40", "9.75"][i]!);
    expect(change(h)).toBe("−0.25");
  });

  it("reports the span of the points, from their own times", () => {
    expect(span(emptyHistory)).toBe(0);
    expect(span(build([1000]))).toBe(0);
    expect(span(build([1000, 4000, 61_000]))).toBe(60_000);
  });
});

describe("formatSpan", () => {
  it.each([
    [0, "0 s"],
    [999, "0 s"],
    [45_000, "45 s"],
    [59_999, "59 s"],
    [60_000, "1 min"],
    [100_000, "1 min 40 s"],
    [120_000, "2 min"],
    [-5, "0 s"],
  ])("%d ms -> %s", (ms, want) => {
    expect(formatSpan(ms)).toBe(want);
  });
});
