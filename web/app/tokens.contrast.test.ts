// @vitest-environment node
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

// Reads tokens.css and holds every colour pair the interface uses to its contrast ratio, in both themes. jsdom does not apply this
// CSS, so this is the check on the token values themselves; the browser check measures the colours actually painted.

const css = readFileSync(join(__dirname, "tokens.css"), "utf8");

/** The text between the braces of the block that starts at `header` (one level of nesting is balanced). */
function block(header: string): string {
  const start = css.indexOf(header);
  if (start < 0) {
    throw new Error(`no block ${header}`);
  }
  let i = css.indexOf("{", start);
  const open = i;
  let depth = 0;
  for (; i < css.length; i++) {
    if (css[i] === "{") depth++;
    if (css[i] === "}" && --depth === 0) {
      return css.slice(open + 1, i);
    }
  }
  throw new Error(`unterminated block ${header}`);
}

function vars(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const m of text.matchAll(/(--[a-z0-9-]+):\s*([^;]+);/g)) {
    out[m[1]!] = m[2]!.trim();
  }
  return out;
}

type RGBA = [number, number, number, number];
function parse(value: string): RGBA {
  const hex = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(value);
  if (hex) {
    let h = hex[1]!;
    if (h.length === 3) h = [...h].map((c) => c + c).join("");
    return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16), 1];
  }
  const rgba = /^rgba?\(([^)]+)\)$/.exec(value);
  if (rgba) {
    const p = rgba[1]!.split(",").map((x) => parseFloat(x));
    return [p[0]!, p[1]!, p[2]!, p[3] ?? 1];
  }
  throw new Error(`cannot parse colour ${value}`);
}
const over = (fg: RGBA, bg: RGBA): RGBA => [
  fg[0] * fg[3] + bg[0] * (1 - fg[3]),
  fg[1] * fg[3] + bg[1] * (1 - fg[3]),
  fg[2] * fg[3] + bg[2] * (1 - fg[3]),
  1,
];
const lum = (c: RGBA) => {
  const f = (v: number) => {
    v /= 255;
    return v <= 0.04045 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * f(c[0]) + 0.7152 * f(c[1]) + 0.0722 * f(c[2]);
};
const ratio = (a: RGBA, b: RGBA) => {
  const x = lum(a);
  const y = lum(b);
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
};

const light = vars(block(":root {"));
const darkByAttribute = vars(block(':root[data-theme="dark"] {'));
const dark = { ...light, ...darkByAttribute };

// [foreground token, surface token(s), required ratio, what it is used for]
const pairs: [string, string[], number, string][] = [
  ["--label", ["--bg", "--card"], 4.5, "primary text"],
  ["--label-2", ["--bg", "--card"], 4.5, "secondary text"],
  ["--label-3", ["--bg", "--card"], 4.5, "tertiary text"],
  ["--green", ["--card", "--bg"], 4.5, "best bid price"],
  ["--red", ["--card", "--bg"], 4.5, "best ask price"],
  ["--orange", ["--card", "--bg"], 4.5, "recovered fault and refused-update text"],
  ["--blue-ink", ["--card", "--bg"], 4.5, "links"],
  ["--green-ink", ["--green-soft:--card", "--green-soft:--bg"], 4.5, "the consistent badge"],
  ["--blue-ink", ["--blue-soft:--card", "--blue-soft:--bg"], 4.5, "the demo chip"],
  ["--label", ["--orange-soft:--card", "--orange-soft:--bg"], 4.5, "the paused banner"],
  ["--label-2", ["--orange-soft:--card", "--orange-soft:--bg"], 4.5, "the paused banner detail"],
  ["--green", ["--card"], 3, "bid line and bar edge"],
  ["--red", ["--card"], 3, "ask line and bar edge"],
  ["--blue", ["--card"], 3, "processing-time bars"],
  ["--orange", ["--card"], 3, "fault icons"],
  ["--focus", ["--card", "--bg"], 3, "focus ring"],
];

function surface(spec: string, tokens: Record<string, string>): RGBA {
  // "--soft:--base" is a translucent tint painted over a base surface
  const [top, base] = spec.split(":");
  const tint = parse(tokens[top!]!);
  return base ? over(tint, parse(tokens[base]!)) : tint;
}

describe.each([
  ["light", light],
  ["dark", dark],
] as const)("%s theme", (_name, tokens) => {
  it.each(pairs)("%s on %j needs %s:1 (%s)", (fg, surfaces, need) => {
    for (const s of surfaces) {
      const bg = surface(s, tokens);
      const colour = over(parse(tokens[fg]!), bg);
      expect(ratio(colour, bg), `${fg} on ${s}`).toBeGreaterThanOrEqual(need);
    }
  });
});

// The paused and stale hero paints the mid price (large text, so 3:1) and the line (graphics, 3:1) at this opacity.
const dimmedOpacity = 0.85;
describe.each([
  ["light", light],
  ["dark", dark],
] as const)("%s theme, dimmed hero", (_name, tokens) => {
  it.each([
    ["--label", "the whole part of the mid price"],
    ["--label-3", "the fraction of the mid price"],
    ["--green", "the line"],
  ])("%s keeps 3:1 at reduced opacity (%s)", (fg) => {
    for (const s of ["--bg", "--card"]) {
      const bg = surface(s, tokens);
      const [r, g, b] = parse(tokens[fg]!);
      const colour = over([r, g, b, dimmedOpacity], bg);
      expect(ratio(colour, bg), `${fg} on ${s}`).toBeGreaterThanOrEqual(3);
    }
  });
});

describe("the dark theme", () => {
  it("is declared twice, for the system setting and for an explicit choice, and the two copies are identical", () => {
    const system = vars(block(':root:not([data-theme="light"]) {'));
    expect(Object.keys(system).length).toBeGreaterThan(10);
    expect(system).toEqual(darkByAttribute);
  });
  it("overrides only tokens that the light theme also defines", () => {
    for (const name of Object.keys(darkByAttribute)) {
      expect(light, name).toHaveProperty(name);
    }
  });
});

describe("the contrast and forced-colour variants", () => {
  it("exist", () => {
    expect(css).toContain("@media (prefers-contrast: more)");
    expect(css).toContain("@media (forced-colors: active)");
  });
  it("the forced-colour rule is strong enough to beat both dark blocks", () => {
    // :root:not([...]) and :root[data-theme] have specificity (0,2,0); a bare :root would lose to them
    expect(css).toMatch(/@media \(forced-colors: active\) \{\s*:root:root \{/);
  });
  it("lift secondary text to 7:1 in each theme when more contrast is asked for", () => {
    const lightByAttribute = vars(block('@media (prefers-contrast: more) {\n  :root[data-theme="light"] {'));
    const darkByAttribute = vars(block('@media (prefers-contrast: more) {\n  :root[data-theme="dark"] {'));
    // the blocks that apply to a visitor who follows the system setting must say exactly what the explicit ones say
    const lightBySystem = vars(
      block('@media (prefers-contrast: more) and (prefers-color-scheme: light) {\n  :root:not([data-theme="dark"]) {'),
    );
    const darkBySystem = vars(
      block('@media (prefers-contrast: more) and (prefers-color-scheme: dark) {\n  :root:not([data-theme="light"]) {'),
    );
    expect(lightBySystem).toEqual(lightByAttribute);
    expect(darkBySystem).toEqual(darkByAttribute);
    expect(Object.keys(lightByAttribute)).toEqual(expect.arrayContaining(["--label-2", "--label-3", "--hairline"]));
    expect(Object.keys(darkByAttribute)).toEqual(expect.arrayContaining(["--label-2", "--label-3", "--hairline"]));
    const lightMore = { ...light, ...lightByAttribute };
    const darkMore = { ...dark, ...darkByAttribute };
    for (const t of [lightMore, darkMore]) {
      for (const fg of ["--label-2", "--label-3"]) {
        for (const s of ["--bg", "--card"]) {
          const bg = parse(t[s]!);
          expect(ratio(over(parse(t[fg]!), bg), bg), `${fg} on ${s}`).toBeGreaterThanOrEqual(7);
        }
      }
    }
  });
});
