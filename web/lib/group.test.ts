import { describe, expect, it } from "vitest";

import { groupThousands, splitFraction, ungroup } from "./group";

function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
    return s;
  };
}

describe("groupThousands", () => {
  const cases: [string, string][] = [
    ["0", "0"],
    ["12", "12"],
    ["123", "123"],
    ["1234", "1,234"],
    ["12345", "12,345"],
    ["123456", "123,456"],
    ["1234567", "1,234,567"],
    ["62721.75", "62,721.75"],
    ["0.12345678", "0.12345678"],
    ["1234.5678901", "1,234.5678901"],
    ["-1234.5", "-1,234.5"],
    ["-12", "-12"],
    ["100000000", "100,000,000"],
  ];
  it.each(cases)("%s -> %s", (input, want) => {
    expect(groupThousands(input)).toBe(want);
  });

  it("returns anything that is not a plain decimal untouched", () => {
    for (const s of ["", "abc", "1,234", "1.", ".5", "1e5", "1 234", "+5", "--1"]) {
      expect(groupThousands(s)).toBe(s);
    }
  });

  it("is lossless: removing the separators gives back the exact string, for random digit strings", () => {
    const next = rng(11);
    for (let i = 0; i < 2000; i++) {
      const whole = String(next()) + String(next() % 1000).padStart(3, "0");
      const fraction = next() % 3 === 0 ? "" : "." + String(next()).padStart(4, "0");
      const sign = next() % 5 === 0 ? "-" : "";
      const s = `${sign}${whole.replace(/^0+(?=\d)/, "")}${fraction}`;
      expect(ungroup(groupThousands(s))).toBe(s);
    }
  });

  it("never touches the fraction and never puts a comma at either end", () => {
    const g = groupThousands("1234567.1234567");
    expect(g.split(".")[1]).toBe("1234567");
    expect(g).not.toMatch(/^,|,$|,\./);
  });
});

describe("splitFraction", () => {
  it.each([
    ["62,721.75", ["62,721", ".75"]],
    ["100", ["100", ""]],
    ["0.5", ["0", ".5"]],
    ["", ["", ""]],
  ])("%s", (input, want) => {
    expect(splitFraction(input)).toEqual(want);
  });
});
